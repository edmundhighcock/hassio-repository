// Package mqttpub publishes per-source status as Home Assistant entities
// through MQTT discovery. The structure follows virtual_sensors/internal/mqttpub
// (retained discovery configs re-sent on every connect and on
// homeassistant/status "online"; availability topic with a retained LWT),
// adapted to one HA device per source plus one for the receiver itself.
package mqttpub

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"netconsole_receiver/internal/alert"
	"netconsole_receiver/internal/recv"
)

const (
	discoveryPrefix = "homeassistant"
	node            = "netconsole_receiver"
	// AvailabilityTopic carries the retained "online" and the LWT "offline".
	AvailabilityTopic = node + "/status"
	manufacturer      = "ham/hassio-repository"
	model             = "netconsole_receiver add-on"
)

// Credentials is where to find the broker.
type Credentials struct {
	Host     string
	Port     int
	Username string
	Password string
	Source   string // for logging only: "supervisor" | "options"
}

// SourceMeta describes one configured source for discovery.
type SourceMeta struct {
	Name       string
	Address    string
	Port       int
	SilenceMin int
}

// Client is the slice of an MQTT client the publisher needs. Publish must
// not block on the network.
type Client interface {
	Publish(topic string, payload []byte, retain bool)
	Connected() bool
}

// Msg is one MQTT message.
type Msg struct {
	Topic   string
	Payload []byte
	Retain  bool
}

// Publisher owns the entity model. Safe for concurrent use.
type Publisher struct {
	mu        sync.Mutex
	c         Client
	raw       mqtt.Client
	sources   []SourceMeta
	ruleNames []string
	interval  time.Duration

	lastAlert map[string]alert.Alert
	problem   map[string]bool
	sent      map[string]string // counter topic -> last payload
	lastPub   time.Time
	snapFn    func() recv.Snapshot
}

func New(sources []SourceMeta, ruleNames []string, interval time.Duration, snap func() recv.Snapshot) *Publisher {
	return &Publisher{
		sources: sources, ruleNames: ruleNames, interval: interval, snapFn: snap,
		lastAlert: map[string]alert.Alert{}, problem: map[string]bool{}, sent: map[string]string{},
	}
}

func srcTopic(name, leaf string) string { return node + "/" + name + "/" + leaf }

func device(name string) map[string]any {
	if name == "" {
		return map[string]any{"identifiers": []string{node}, "name": "Netconsole receiver",
			"manufacturer": manufacturer, "model": model}
	}
	return map[string]any{"identifiers": []string{node + "_" + name}, "name": "Netconsole " + name,
		"manufacturer": manufacturer, "model": model, "via_device": node}
}

// entity builds one discovery config.
func entity(src, component, key, title string, extra map[string]any) Msg {
	obj := "netconsole_" + key
	if src != "" {
		obj = "netconsole_" + src + "_" + key
	}
	cfg := map[string]any{
		"unique_id":          node + "_" + obj,
		"default_entity_id":  component + "." + obj,
		"name":               title,
		"has_entity_name":    true,
		"availability_topic": AvailabilityTopic,
		"device":             device(src),
	}
	for k, v := range extra {
		cfg[k] = v
	}
	b, _ := json.Marshal(cfg)
	dev := node
	if src != "" {
		dev = node + "_" + src
	}
	return Msg{Topic: fmt.Sprintf("%s/%s/%s/%s/config", discoveryPrefix, component, dev, key), Payload: b, Retain: true}
}

// Discovery returns every retained discovery config.
func (p *Publisher) Discovery() []Msg {
	out := []Msg{entity("", "sensor", "dropped_datagrams", "Dropped datagrams", map[string]any{
		"state_topic": node + "/dropped_datagrams", "state_class": "total_increasing",
		"icon": "mdi:shield-alert-outline"})}
	for _, s := range p.sources {
		n := s.Name
		out = append(out,
			entity(n, "sensor", "last_message", "Last message", map[string]any{
				"state_topic": srcTopic(n, "last_message"), "device_class": "timestamp",
				"json_attributes_topic": srcTopic(n, "last_message_attrs")}),
			entity(n, "sensor", "messages", "Messages", map[string]any{
				"state_topic": srcTopic(n, "messages"), "state_class": "total_increasing"}),
			entity(n, "sensor", "sequence_gaps", "Sequence gaps", map[string]any{
				"state_topic": srcTopic(n, "sequence_gaps"), "state_class": "total_increasing"}),
			entity(n, "sensor", "last_alert", "Last alert", map[string]any{
				"state_topic": srcTopic(n, "last_alert"), "json_attributes_topic": srcTopic(n, "last_alert_attrs"),
				"icon": "mdi:alert-circle-outline"}),
			entity(n, "binary_sensor", "problem", "Problem", map[string]any{
				"state_topic": srcTopic(n, "problem"), "device_class": "problem"}),
		)
		if len(p.ruleNames) > 0 { // HA requires a non-empty event_types
			out = append(out, entity(n, "event", "alert", "Alert", map[string]any{
				"state_topic": srcTopic(n, "alert_event"), "event_types": p.ruleNames}))
		}
	}
	return out
}

func (p *Publisher) send(topic string, payload []byte, retain bool) {
	if p.c != nil && p.c.Connected() {
		p.c.Publish(topic, payload, retain)
	}
}

func (p *Publisher) metaAttrs(s SourceMeta) []byte {
	b, _ := json.Marshal(map[string]any{"silence_after_min": s.SilenceMin, "source_address": s.Address, "source_port": s.Port})
	return b
}

// OnConnect (re)publishes availability, discovery and the full current
// state. Call it whenever the client (re)connects or HA says it is online.
func (p *Publisher) OnConnect() {
	// Take the snapshot BEFORE p.mu: the engine holds its own lock while
	// calling Alert/Problem (which take p.mu), so the reverse order deadlocks.
	var snap recv.Snapshot
	if p.snapFn != nil {
		snap = p.snapFn()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.send(AvailabilityTopic, []byte("online"), true)
	for _, m := range p.Discovery() {
		p.send(m.Topic, m.Payload, m.Retain)
	}
	for _, s := range p.sources {
		p.send(srcTopic(s.Name, "last_message_attrs"), p.metaAttrs(s), true)
		p.send(srcTopic(s.Name, "problem"), []byte(onOff(p.problem[s.Name])), true)
		if a, ok := p.lastAlert[s.Name]; ok {
			p.publishLastAlert(s.Name, a)
		}
	}
	p.sent = map[string]string{}
	p.publishCounters(snap, time.Now())
}

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

func (p *Publisher) publishLastAlert(name string, a alert.Alert) {
	p.send(srcTopic(name, "last_alert"), []byte(alert.StateText(a)), true)
	attrs, _ := json.Marshal(map[string]any{
		"rule":       a.Rule,
		"kind":       a.Kind,
		"count":      a.Count,
		"first_line": alert.AttrText(a.First),
		"last_line":  alert.AttrText(a.Last),
		"time":       a.Time.UTC().Format(time.RFC3339),
	})
	p.send(srcTopic(name, "last_alert_attrs"), attrs, true)
}

// Alert implements recv.Notifier: updates last_alert and fires the event
// entity immediately (not subject to the counter throttle).
func (p *Publisher) Alert(name string, a alert.Alert) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastAlert[name] = a
	p.publishLastAlert(name, a)
	ev, _ := json.Marshal(map[string]any{
		"event_type": a.Rule, "kind": a.Kind, "count": a.Count, "excerpt": alert.AttrText(a.Last),
	})
	p.send(srcTopic(name, "alert_event"), ev, false) // events are never retained
}

// Problem implements recv.Notifier.
func (p *Publisher) Problem(name string, on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.problem[name] = on
	p.send(srcTopic(name, "problem"), []byte(onOff(on)), true)
}

// MaybePublishCounters publishes the counters if at least the interval
// has passed since the last time, so a burst of datagrams cannot flood
// MQTT. Call it often (every few seconds); it rate-limits itself.
// Returns whether it published.
func (p *Publisher) MaybePublishCounters(snap recv.Snapshot, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.lastPub.IsZero() && now.Sub(p.lastPub) < p.interval {
		return false
	}
	p.publishCounters(snap, now)
	return true
}

func (p *Publisher) publishCounters(snap recv.Snapshot, now time.Time) {
	if p.c == nil || !p.c.Connected() {
		return // retried at the next call; lastPub is not advanced
	}
	p.lastPub = now
	put := func(topic, payload string) {
		if p.sent[topic] != payload {
			p.sent[topic] = payload
			p.send(topic, []byte(payload), true)
		}
	}
	put(node+"/dropped_datagrams", strconv.FormatUint(snap.Dropped, 10))
	for _, s := range snap.Sources {
		put(srcTopic(s.Name, "messages"), strconv.FormatUint(s.Messages, 10))
		put(srcTopic(s.Name, "sequence_gaps"), strconv.FormatUint(s.Gaps, 10))
		if !s.LastMessage.IsZero() {
			put(srcTopic(s.Name, "last_message"), s.LastMessage.UTC().Format(time.RFC3339))
		}
	}
}

// --- paho adapter ---------------------------------------------------------

type pahoClient struct{ c mqtt.Client }

func (p pahoClient) Publish(topic string, payload []byte, retain bool) {
	p.c.Publish(topic, 1, retain, payload) // fire and forget: never block the caller
}
func (p pahoClient) Connected() bool { return p.c.IsConnectionOpen() }

// Connect blocks until the broker accepts the connection (the caller
// retries and logs). Availability uses a retained last-will "offline" so
// every entity goes unavailable if the add-on dies.
func (p *Publisher) Connect(creds Credentials) error {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", creds.Host, creds.Port))
	opts.SetClientID(node)
	if creds.Username != "" {
		opts.SetUsername(creds.Username)
		opts.SetPassword(creds.Password)
	}
	opts.SetWill(AvailabilityTopic, "offline", 1, true)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(false)
	opts.SetKeepAlive(30 * time.Second)
	opts.OnConnect = func(c mqtt.Client) {
		log.Printf("mqttpub: connected to %s:%d (credentials from %s)", creds.Host, creds.Port, creds.Source)
		p.mu.Lock()
		p.c = pahoClient{c}
		p.raw = c
		p.mu.Unlock()
		p.OnConnect()
		if tok := c.Subscribe("homeassistant/status", 1, func(_ mqtt.Client, m mqtt.Message) {
			if string(m.Payload()) == "online" {
				log.Printf("mqttpub: homeassistant/status online - re-publishing discovery")
				p.OnConnect()
			}
		}); tok.Wait() && tok.Error() != nil {
			log.Printf("mqttpub: ERROR subscribing to homeassistant/status: %v", tok.Error())
		}
	}
	opts.OnConnectionLost = func(_ mqtt.Client, err error) {
		log.Printf("mqttpub: ERROR connection to broker lost: %v - paho will keep retrying", err)
	}
	client := mqtt.NewClient(opts)
	tok := client.Connect()
	tok.Wait()
	if err := tok.Error(); err != nil {
		return fmt.Errorf("connect to mqtt broker %s:%d: %w", creds.Host, creds.Port, err)
	}
	return nil
}

// SetClient is for tests.
func (p *Publisher) SetClient(c Client) {
	p.mu.Lock()
	p.c = c
	p.mu.Unlock()
}

// Close marks everything unavailable (best effort) and disconnects. A
// crash does the same through the last-will.
func (p *Publisher) Close() {
	p.mu.Lock()
	c := p.raw
	p.mu.Unlock()
	if c == nil {
		return
	}
	c.Publish(AvailabilityTopic, 1, true, "offline").WaitTimeout(2 * time.Second)
	c.Disconnect(250)
}
