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
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // 0.2.0: Europe/Stockholm for the controller, independent of the base image

	"virtual_sensors/internal/haws"
	"virtual_sensors/internal/host"
	"virtual_sensors/internal/logfile"
	"virtual_sensors/internal/modules"
	_ "virtual_sensors/internal/modules/evcontrol"   // side-effect: registers "ev_control"
	_ "virtual_sensors/internal/modules/example"     // side-effect: registers "example_passthrough"
	_ "virtual_sensors/internal/modules/hotwater"    // side-effect: registers "hot_water"
	_ "virtual_sensors/internal/modules/housebudget" // side-effect: registers "house_planner"
	_ "virtual_sensors/internal/modules/hpcontrol"   // side-effect: registers "hp_control"
	_ "virtual_sensors/internal/modules/hwcontrol"   // side-effect: registers "hot_water_control"
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

// loadOptions returns the parsed options and the raw JSON (handed to
// modules.Configurable modules, which read their own keys).
func loadOptions(path string) (addonOptions, []byte) {
	opts := addonOptions{Modules: []string{"hot_water"}, LogLevel: "info", GoMemLimitMiB: 24}
	b, err := os.ReadFile(path)
	if err != nil {
		log.Printf("options: could not read %s (%v); using defaults %+v", path, err, opts)
		return opts, nil
	}
	if err := json.Unmarshal(b, &opts); err != nil {
		log.Printf("options: could not parse %s (%v); using defaults", path, err)
	}
	if len(opts.Modules) == 0 {
		opts.Modules = []string{"hot_water"}
	}
	return opts, b
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
//
// 0.2.0: one serial event loop. Every call into a module happens on the
// loop goroutine, through internal/host: WS events, warm-up replay, the
// connect-time state snapshot, 5 s ticks, service-call results and link
// up/down all arrive as closures on one channel. The WS reader runs on its
// own goroutine and only enqueues; service calls are written from the
// loop (the WS client serialises writes).
// ---------------------------------------------------------------------------

type loopFn func(h *host.Host)

func runLive() {
	// 0.6.3: keep our own log history on disk (the HAOS journal is capped).
	if lw, err := logfile.NewDefault(); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: file logging disabled (%v); logging to stderr only\n", err)
	} else {
		log.SetOutput(io.MultiWriter(os.Stderr, lw))
	}
	optsPath := os.Getenv("VIRTUAL_SENSORS_OPTIONS")
	if optsPath == "" {
		optsPath = "/data/options.json"
	}
	opts, rawOpts := loadOptions(optsPath)
	debug.SetMemoryLimit(int64(opts.GoMemLimitMiB) * 1024 * 1024)
	log.Printf("virtual_sensors: starting, modules=%v log_level=%s GOMEMLIMIT=%dMiB",
		opts.Modules, opts.LogLevel, opts.GoMemLimitMiB)

	token := os.Getenv("SUPERVISOR_TOKEN")
	if token == "" {
		log.Printf("ERROR: SUPERVISOR_TOKEN is not set - homeassistant_api must be true in config.yaml. Cannot reach Home Assistant or the Supervisor services API; will keep retrying so this failure is visible in the log rather than exiting silently.")
	}

	stateDir := envOr("VIRTUAL_SENSORS_DATA_DIR", "/data")
	var active []modules.Module
	for _, name := range opts.Modules {
		ctor, ok := modules.Get(name)
		if !ok {
			log.Printf("ERROR: unknown module %q in the `modules` option (known: %v) - skipping it", name, modules.Names())
			continue
		}
		m := ctor()
		if c, ok := m.(modules.Configurable); ok {
			if err := c.Configure(rawOpts); err != nil {
				log.Printf("ERROR: %s: Configure: %v (defaults apply)", m.Name(), err)
			}
		}
		persisted, _ := os.ReadFile(host.StatePath(stateDir, m.Name()))
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
	var buttons []modules.ButtonSpec
	for _, m := range active {
		outputs = append(outputs, m.Outputs()...)
		if b, ok := m.(modules.Buttoned); ok {
			buttons = append(buttons, b.Buttons()...)
		}
	}
	// Button presses arrive on the MQTT client goroutine, possibly before
	// the host exists; they queue here and are moved onto the loop below.
	presses := make(chan string, 16)
	onPress := func(id string) {
		select {
		case presses <- id:
		default:
			log.Printf("mqttpub: button press %q dropped (queue full)", id)
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
			pub, err = mqttpub.Connect(creds, outputs, buttons, onPress)
		}
		if err != nil {
			log.Printf("ERROR: MQTT not available yet: %v - retrying in %s", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = nextBackoff(backoff)
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

	h := host.New(active, pub, host.SystemClock{}, stateDir)
	loop := make(chan loopFn, 256)
	enqueue := func(f loopFn) bool {
		select {
		case loop <- f:
			return true
		case <-ctx.Done():
			return false
		}
	}

	go func() {
		t := time.NewTicker(modules.TickInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !enqueue(func(h *host.Host) { h.Tick() }) {
					return
				}
			}
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case id := <-presses:
				if !enqueue(func(h *host.Host) { h.Press(id) }) {
					return
				}
			}
		}
	}()

	wsURL := envOr("VIRTUAL_SENSORS_HA_WS_URL", "ws://supervisor/core/websocket")
	httpURL := envOr("VIRTUAL_SENSORS_HA_HTTP_URL", "http://supervisor/core/api")
	go connectionLoop(ctx, h, enqueue, wsURL, httpURL, token)

	for {
		select {
		case <-ctx.Done():
			h.PersistAll()
			return
		case f := <-loop:
			f(h)
		}
	}
}

// ask runs f on the loop goroutine and waits for it.
func ask(enqueue func(loopFn) bool, f loopFn) bool {
	done := make(chan struct{})
	if !enqueue(func(h *host.Host) { f(h); close(done) }) {
		return false
	}
	<-done
	return true
}

// connectionLoop owns the HA connection: connect, warm-up replay, current
// state snapshot, subscribe, link up, then read until the connection
// drops (link down) and reconnect with backoff - see DOCS.md "Live inputs".
func connectionLoop(ctx context.Context, h *host.Host, enqueue func(loopFn) bool, wsURL, httpURL, token string) {
	var entityIDs []string
	var match func(string) bool
	var attrs func(string) []string
	ask(enqueue, func(h *host.Host) { entityIDs, match, attrs = h.ExactEntityIDs(), h.Match, h.AttrWhitelist })
	seeded := false
	backoff := time.Second
	for ctx.Err() == nil {
		client := haws.New(wsURL, httpURL, token)
		client.AttrWhitelist = attrs
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
			ask(enqueue, func(h *host.Host) {
				for _, ev := range hist {
					h.HandleState(ev, true)
				}
			})
		}
		if !seeded {
			var reqs []host.SeedReq
			ask(enqueue, func(h *host.Host) { reqs = h.SeedRequests() })
			for _, r := range reqs {
				evs, err := client.History(ctx, r.Since, r.EntityIDs)
				if err != nil {
					log.Printf("ERROR: seed history fetch failed: %v - clocks stay unknown (no OFF)", err)
					continue
				}
				sortEventsByTime(evs)
				r := r
				ask(enqueue, func(h *host.Host) { h.DeliverSeed(r, evs) })
			}
			seeded = true
		}

		if err := client.Subscribe(); err != nil {
			log.Printf("ERROR: subscribing to state_changed: %v", err)
			client.Close()
			sleepOrDone(ctx, backoff)
			backoff = nextBackoff(backoff)
			continue
		}
		// Current states AFTER subscribing, so nothing falls between the
		// snapshot and the stream. Held values for quiet entities come from
		// here (PLAN 2.1 "get_states at connect").
		states, err := client.States(ctx, match)
		if err != nil {
			log.Printf("ERROR: current-state fetch failed: %v - held values fill from events only", err)
		} else {
			sortEventsByTime(states)
			ask(enqueue, func(h *host.Host) {
				for _, ev := range states {
					h.HandleState(ev, false)
				}
			})
		}
		backoff = time.Second // reset after a successful connect+subscribe
		ask(enqueue, func(h *host.Host) { h.LinkUp(client) })

		for {
			msg, ok, err := client.ReadMessage()
			if err != nil {
				log.Printf("ERROR: Home Assistant websocket read failed: %v - reconnecting", err)
				break
			}
			if !ok {
				continue
			}
			if msg.Event != nil {
				ev := *msg.Event
				if !enqueue(func(h *host.Host) { h.HandleState(ev, false) }) {
					break
				}
			} else if msg.Result != nil {
				r := *msg.Result
				if !enqueue(func(h *host.Host) { h.HandleResult(r) }) {
					break
				}
			}
		}
		ask(enqueue, func(h *host.Host) { h.LinkDown() })
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
