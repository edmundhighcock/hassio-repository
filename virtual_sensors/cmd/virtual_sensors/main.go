// Command virtual_sensors hosts Home Assistant "virtual sensor" modules
// (see internal/modules) in one small Go process: live, driven over the
// Supervisor's WebSocket/REST APIs and published via MQTT discovery; or,
// with the `backfill` subcommand, re-run over InfluxDB history to
// reproduce the same figures for the past. See DOCS.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"virtual_sensors/internal/haws"
	"virtual_sensors/internal/modules"
	_ "virtual_sensors/internal/modules/example"  // side-effect: registers "example_passthrough"
	_ "virtual_sensors/internal/modules/hotwater" // side-effect: registers "hot_water"
	"virtual_sensors/internal/mqttpub"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "backfill" {
		if err := runBackfill(os.Args[2:]); err != nil {
			log.Fatalf("backfill: %v", err)
		}
		return
	}
	runLive()
}

// ---------------------------------------------------------------------------
// Options (add-on config.yaml options, read from /data/options.json).
// ---------------------------------------------------------------------------

type addonOptions struct {
	Modules       []string `json:"modules"`
	LogLevel      string   `json:"log_level"`
	MQTTHost      string   `json:"mqtt_host"`
	MQTTPort      int      `json:"mqtt_port"`
	MQTTUsername  string   `json:"mqtt_username"`
	MQTTPassword  string   `json:"mqtt_password"`
	GoMemLimitMiB int      `json:"go_memlimit_mib"`
}

func loadOptions(path string) addonOptions {
	opts := addonOptions{Modules: []string{"hot_water"}, LogLevel: "info", GoMemLimitMiB: 24}
	b, err := os.ReadFile(path)
	if err != nil {
		log.Printf("options: could not read %s (%v); using defaults %+v", path, err, opts)
		return opts
	}
	if err := json.Unmarshal(b, &opts); err != nil {
		log.Printf("options: could not parse %s (%v); using defaults", path, err)
	}
	if len(opts.Modules) == 0 {
		opts.Modules = []string{"hot_water"}
	}
	return opts
}

// ---------------------------------------------------------------------------
// MQTT credentials: Supervisor services API first, then add-on options as
// an explicit override/fallback - see DOCS.md "MQTT credentials" for why
// both paths exist (the Supervisor's service registry has been observed
// reporting mqtt unavailable while Mosquitto was in fact running).
// ---------------------------------------------------------------------------

func supervisorMQTTCredentials(token string) (mqttpub.Credentials, error) {
	req, err := http.NewRequest(http.MethodGet, "http://supervisor/services/mqtt", nil)
	if err != nil {
		return mqttpub.Credentials{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return mqttpub.Credentials{}, fmt.Errorf("GET /services/mqtt: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return mqttpub.Credentials{}, fmt.Errorf("GET /services/mqtt: %s", resp.Status)
	}
	var body struct {
		Data struct {
			Host     string `json:"host"`
			Port     int    `json:"port"`
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return mqttpub.Credentials{}, fmt.Errorf("decode /services/mqtt: %w", err)
	}
	if body.Data.Host == "" {
		return mqttpub.Credentials{}, fmt.Errorf("supervisor returned no mqtt host (service not yet available)")
	}
	return mqttpub.Credentials{
		Host: body.Data.Host, Port: body.Data.Port,
		Username: body.Data.Username, Password: body.Data.Password, Source: "supervisor",
	}, nil
}

// resolveMQTTCredentials tries the Supervisor services API, then falls
// back to the add-on's own mqtt_* options. If NEITHER works, it returns
// an error - the caller logs this loudly and keeps retrying; it must
// never be treated as "mqtt not needed" and silently skipped.
func resolveMQTTCredentials(token string, opts addonOptions) (mqttpub.Credentials, error) {
	if creds, err := supervisorMQTTCredentials(token); err == nil {
		return creds, nil
	} else {
		log.Printf("mqtt: supervisor services API did not yield credentials (%v); trying add-on options override", err)
	}
	if opts.MQTTHost != "" {
		port := opts.MQTTPort
		if port == 0 {
			port = 1883
		}
		return mqttpub.Credentials{
			Host: opts.MQTTHost, Port: port,
			Username: opts.MQTTUsername, Password: opts.MQTTPassword, Source: "options",
		}, nil
	}
	return mqttpub.Credentials{}, fmt.Errorf("no MQTT credentials from the Supervisor services API AND no mqtt_host option set - see DOCS.md \"MQTT credentials\"")
}

// ---------------------------------------------------------------------------
// Live mode.
// ---------------------------------------------------------------------------

func runLive() {
	optsPath := os.Getenv("VIRTUAL_SENSORS_OPTIONS")
	if optsPath == "" {
		optsPath = "/data/options.json"
	}
	opts := loadOptions(optsPath)
	debug.SetMemoryLimit(int64(opts.GoMemLimitMiB) * 1024 * 1024)
	log.Printf("virtual_sensors: starting, modules=%v log_level=%s GOMEMLIMIT=%dMiB",
		opts.Modules, opts.LogLevel, opts.GoMemLimitMiB)

	token := os.Getenv("SUPERVISOR_TOKEN")
	if token == "" {
		log.Printf("ERROR: SUPERVISOR_TOKEN is not set - homeassistant_api must be true in config.yaml. Cannot reach Home Assistant or the Supervisor services API; will keep retrying so this failure is visible in the log rather than exiting silently.")
	}

	var active []modules.Module
	for _, name := range opts.Modules {
		ctor, ok := modules.Get(name)
		if !ok {
			log.Printf("ERROR: unknown module %q in the `modules` option (known: %v) - skipping it", name, modules.Names())
			continue
		}
		m := ctor()
		statePath := fmt.Sprintf("/data/%s-state.json", m.Name())
		persisted, _ := os.ReadFile(statePath)
		if err := m.Init(persisted); err != nil {
			log.Printf("ERROR: %s: Init failed: %v", m.Name(), err)
			continue
		}
		active = append(active, m)
		log.Printf("module %s: enabled, inputs=%d outputs=%d", m.Name(), len(m.Inputs()), len(m.Outputs()))
	}
	if len(active) == 0 {
		log.Fatalf("ERROR: no modules enabled - nothing to do (check the `modules` option)")
	}

	var outputs []modules.OutputSpec
	entityToModules := map[string][]modules.Module{}
	for _, m := range active {
		outputs = append(outputs, m.Outputs()...)
		for _, in := range m.Inputs() {
			entityToModules[in.EntityID] = append(entityToModules[in.EntityID], m)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// --- MQTT: connect with backoff; a failure here is loud and retried,
	// never silent (see resolveMQTTCredentials's doc comment). ---------
	var pub *mqttpub.Publisher
	backoff := time.Second
	for pub == nil {
		creds, err := resolveMQTTCredentials(token, opts)
		if err == nil {
			pub, err = mqttpub.Connect(creds, outputs)
		}
		if err != nil {
			log.Printf("ERROR: MQTT not available yet: %v - retrying in %s", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 60*time.Second {
				backoff *= 2
			}
			continue
		}
	}
	defer pub.Close()
	for _, m := range active {
		if is, ok := m.(modules.InitialStater); ok {
			for _, out := range is.InitialStates() {
				if err := pub.PublishState(out); err != nil {
					log.Printf("ERROR: publishing initial %s: %v", out.ObjectID, err)
				}
			}
		}
	}

	persistAll := func() {
		for _, m := range active {
			b, err := m.Persist()
			if err != nil {
				log.Printf("ERROR: %s: Persist failed: %v", m.Name(), err)
				continue
			}
			if err := os.WriteFile(fmt.Sprintf("/data/%s-state.json", m.Name()), b, 0o600); err != nil {
				log.Printf("ERROR: %s: could not write state file: %v", m.Name(), err)
			}
		}
	}
	defer persistAll()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				persistAll()
			}
		}
	}()

	// --- HA WebSocket: connect/subscribe with backoff; warm up from the
	// last ~4h of history every (re)connect so a restart mid-cycle isn't
	// lost - see DOCS.md "Live inputs". -------------------------------
	var entityIDs []string
	for e := range entityToModules {
		entityIDs = append(entityIDs, e)
	}

	dispatch := func(ev haws.StateEvent) {
		// Modules name inputs as the InfluxDB buckets do (no "sensor."
		// prefix), so live and backfill share one set of ids.
		id := strings.TrimPrefix(ev.EntityID, "sensor.")
		ms, ok := entityToModules[id]
		if !ok {
			return
		}
		value, available := haws.ParseFloat(ev.State)
		u := modules.StateUpdate{EntityID: id, Time: ev.Time, Value: value, Available: available}
		for _, m := range ms {
			for _, out := range m.OnStateUpdate(u) {
				if err := pub.PublishState(out); err != nil {
					log.Printf("ERROR: publishing %s: %v", out.ObjectID, err)
				}
			}
		}
	}

	wsURL := envOr("VIRTUAL_SENSORS_HA_WS_URL", "ws://supervisor/core/websocket")
	httpURL := envOr("VIRTUAL_SENSORS_HA_HTTP_URL", "http://supervisor/core/api")
	backoff = time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		client := haws.New(wsURL, httpURL, token)
		if err := client.Connect(ctx); err != nil {
			log.Printf("ERROR: connecting to Home Assistant at %s: %v - retrying in %s", wsURL, err, backoff)
			sleepOrDone(ctx, backoff)
			backoff = nextBackoff(backoff)
			continue
		}
		log.Printf("connected to Home Assistant at %s", wsURL)

		since := time.Now().Add(-4 * time.Hour)
		hist, err := client.History(ctx, since, entityIDs)
		if err != nil {
			log.Printf("ERROR: warm-up history fetch failed: %v - continuing without it", err)
		} else {
			log.Printf("warm-up: replaying %d history points since %s", len(hist), since.Format(time.RFC3339))
			sortEventsByTime(hist)
			for _, ev := range hist {
				dispatch(ev)
			}
		}

		if err := client.Subscribe(); err != nil {
			log.Printf("ERROR: subscribing to state_changed: %v", err)
			client.Close()
			sleepOrDone(ctx, backoff)
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = time.Second // reset after a successful connect+subscribe

		for {
			ev, ok, err := client.ReadEvent()
			if err != nil {
				log.Printf("ERROR: Home Assistant websocket read failed: %v - reconnecting", err)
				break
			}
			if ok {
				dispatch(ev)
			}
		}
		client.Close()
		sleepOrDone(ctx, time.Second)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func sleepOrDone(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func nextBackoff(d time.Duration) time.Duration {
	if d < 60*time.Second {
		return d * 2
	}
	return d
}

func sortEventsByTime(evs []haws.StateEvent) {
	// Insertion sort is fine: history responses are already close to
	// sorted (per-entity chunks concatenated), and warm-up windows are
	// small (~4h).
	for i := 1; i < len(evs); i++ {
		for j := i; j > 0 && evs[j].Time.Before(evs[j-1].Time); j-- {
			evs[j], evs[j-1] = evs[j-1], evs[j]
		}
	}
}

// ---------------------------------------------------------------------------
// Backfill mode.
// ---------------------------------------------------------------------------

func runBackfill(args []string) error {
	fs := flag.NewFlagSet("backfill", flag.ExitOnError)
	module := fs.String("module", "hot_water", "module to backfill")
	bucket := fs.String("bucket", "", "InfluxDB bucket to read (required)")
	from := fs.String("from", "", "RFC3339 start time (required)")
	to := fs.String("to", "", "RFC3339 end time (required)")
	influxURL := fs.String("influx-url", "http://192.168.0.36:8086", "InfluxDB base URL")
	tokenFile := fs.String("token-file", "", "path to a file containing the InfluxDB read token (required)")
	org := fs.String("org", "household", "InfluxDB org")
	outDir := fs.String("out-dir", ".", "directory to write the .lp and hourly .csv into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *bucket == "" || *from == "" || *to == "" || *tokenFile == "" {
		return fmt.Errorf("--bucket, --from, --to and --token-file are all required")
	}
	if *module != "hot_water" {
		return fmt.Errorf("unknown backfill module %q (only hot_water is implemented)", *module)
	}
	fromT, err := time.Parse(time.RFC3339, *from)
	if err != nil {
		return fmt.Errorf("--from: %w", err)
	}
	toT, err := time.Parse(time.RFC3339, *to)
	if err != nil {
		return fmt.Errorf("--to: %w", err)
	}
	tokenBytes, err := os.ReadFile(*tokenFile)
	if err != nil {
		return fmt.Errorf("--token-file: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	// Never log the token itself, and never echo it - this function's
	// own error returns above intentionally never include tokenBytes.

	return runHotWaterBackfill(*influxURL, *org, token, *bucket, fromT, toT, *outDir)
}
