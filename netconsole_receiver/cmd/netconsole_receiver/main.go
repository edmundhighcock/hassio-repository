// netconsole_receiver: receives Linux netconsole datagrams over UDP,
// writes them to rotated per-source log files and publishes per-source
// status to Home Assistant through MQTT discovery.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"netconsole_receiver/internal/config"
	"netconsole_receiver/internal/logsink"
	"netconsole_receiver/internal/mqttpub"
	"netconsole_receiver/internal/recv"
)

const rcvBufWanted = 4 << 20

var level = "info"

// logf logs at info level; messages starting with "ERROR" are always shown.
func logf(format string, args ...any) {
	if strings.HasPrefix(format, "ERROR") || level == "debug" || level == "info" {
		log.Printf(format, args...)
	}
}

func debugf(format string, args ...any) {
	if level == "debug" {
		log.Printf(format, args...)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func supervisorMQTTCredentials(token string) (mqttpub.Credentials, error) {
	if token == "" {
		return mqttpub.Credentials{}, fmt.Errorf("SUPERVISOR_TOKEN is not set")
	}
	req, err := http.NewRequest(http.MethodGet, "http://supervisor/services/mqtt", nil)
	if err != nil {
		return mqttpub.Credentials{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	cl := &http.Client{Timeout: 10 * time.Second}
	resp, err := cl.Do(req)
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
	return mqttpub.Credentials{Host: body.Data.Host, Port: body.Data.Port,
		Username: body.Data.Username, Password: body.Data.Password, Source: "supervisor"}, nil
}

// resolveMQTT tries the Supervisor services API, then the add-on's own
// mqtt_* options. Failure of both is an error the caller logs loudly and
// retries; it is never treated as "MQTT not needed".
func resolveMQTT(token string, c config.Resolved) (mqttpub.Credentials, error) {
	creds, err := supervisorMQTTCredentials(token)
	if err == nil {
		return creds, nil
	}
	logf("mqtt: supervisor services API did not yield credentials (%v); trying the mqtt_host option", err)
	if c.MQTTHost != "" {
		return mqttpub.Credentials{Host: c.MQTTHost, Port: c.MQTTPort,
			Username: c.MQTTUsername, Password: c.MQTTPassword, Source: "options"}, nil
	}
	return mqttpub.Credentials{}, fmt.Errorf("no MQTT credentials from the Supervisor AND no mqtt_host option set - see DOCS.md \"MQTT credentials\"")
}

func listen(port int) (*net.UDPConn, int, error) {
	var granted int
	lc := net.ListenConfig{Control: func(_, _ string, rc syscall.RawConn) error {
		var serr error
		if err := rc.Control(func(fd uintptr) {
			// The kernel caps this at net.core.rmem_max (unprivileged) and
			// reports double the value it actually uses.
			if err := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, rcvBufWanted); err != nil {
				logf("WARNING: setting SO_RCVBUF failed: %v", err)
			}
			granted, serr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
		}); err != nil {
			return err
		}
		return serr
	}}
	pc, err := lc.ListenPacket(context.Background(), "udp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, 0, err
	}
	return pc.(*net.UDPConn), granted, nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	c := config.Load(envOr("NETCONSOLE_OPTIONS", "/data/options.json"))
	level = c.LogLevel
	debug.SetMemoryLimit(int64(c.GoMemLimitMiB) << 20)
	log.Printf("netconsole_receiver: starting, listen_port=%d sources=%d rules=%d log_dir=%s log_max_mb=%d log_files=%d log_level=%s GOMEMLIMIT=%dMiB",
		c.ListenPort, len(c.Srcs), len(c.Rules), c.LogDir, c.LogMaxMB, c.LogFiles, c.LogLevel, c.GoMemLimitMiB)
	for _, w := range c.Warnings {
		log.Printf("ERROR: options: %s", w)
	}
	if len(c.Srcs) == 0 {
		log.Printf("ERROR: no valid `sources` configured - EVERY datagram will be dropped and nothing will be logged. Add at least one {name, address} under `sources` in the add-on configuration.")
	}

	token := os.Getenv("SUPERVISOR_TOKEN")
	open := func(name string) recv.Sink {
		return logsink.NewRotator(c.LogDir, name, int64(c.LogMaxMB)<<20, c.LogFiles)
	}
	var metas []mqttpub.SourceMeta
	for _, s := range c.Srcs {
		metas = append(metas, mqttpub.SourceMeta{Name: s.Name, Address: s.Addr.String(), Port: int(s.Port), SilenceMin: c.SilenceMin})
	}
	var ruleNames []string
	for _, r := range c.Rules {
		ruleNames = append(ruleNames, r.Name)
	}

	var eng *recv.Engine
	pub := mqttpub.New(metas, ruleNames, config.CounterPublishEvery, func() recv.Snapshot { return eng.Snapshot() })
	eng = recv.New(c, open, pub, logf)

	conn, granted, err := listen(c.ListenPort)
	if err != nil {
		log.Fatalf("ERROR: cannot listen on udp :%d: %v", c.ListenPort, err)
	}
	log.Printf("listening on udp %s; SO_RCVBUF requested %d bytes, kernel reports %d (Linux doubles the value; the cap is net.core.rmem_max)",
		conn.LocalAddr(), rcvBufWanted, granted)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// MQTT: retry loudly until credentials and broker are available. The
	// receiver keeps logging to files meanwhile.
	go func() {
		for ctx.Err() == nil {
			creds, err := resolveMQTT(token, c)
			if err == nil {
				err = pub.Connect(creds)
			}
			if err == nil {
				return
			}
			log.Printf("ERROR: mqtt not connected: %v - retrying in 15s (log files are still being written)", err)
			select {
			case <-ctx.Done():
			case <-time.After(15 * time.Second):
			}
		}
	}()

	go func() {
		tick := time.NewTicker(time.Second)
		cnt := time.NewTicker(5 * time.Second)
		warn := time.NewTicker(10 * time.Minute)
		defer tick.Stop()
		defer cnt.Stop()
		defer warn.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				eng.Tick(now)
			case now := <-cnt.C:
				pub.MaybePublishCounters(eng.Snapshot(), now)
			case <-warn.C:
				if len(c.Srcs) == 0 {
					log.Printf("ERROR: still no valid `sources` configured - all datagrams are being dropped (%d so far)", eng.Dropped())
				}
			}
		}
	}()

	go func() { <-ctx.Done(); conn.Close() }()

	buf := make([]byte, 65536)
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Printf("ERROR: recvfrom: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		debugf("datagram %d bytes from %s", n, from)
		eng.Handle(netip.AddrPortFrom(from.Addr().Unmap(), from.Port()), buf[:n], time.Now())
	}
	pub.Close()
	log.Printf("netconsole_receiver: stopped")
}
