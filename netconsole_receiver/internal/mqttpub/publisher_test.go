package mqttpub

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"netconsole_receiver/internal/alert"
	"netconsole_receiver/internal/recv"
)

type fakeClient struct {
	mu   sync.Mutex
	up   bool
	msgs []Msg
}

func (f *fakeClient) Publish(t string, p []byte, r bool) {
	f.mu.Lock()
	f.msgs = append(f.msgs, Msg{t, p, r})
	f.mu.Unlock()
}
func (f *fakeClient) Connected() bool { return f.up }
func (f *fakeClient) topic(t string) []Msg {
	var out []Msg
	for _, m := range f.msgs {
		if m.Topic == t {
			out = append(out, m)
		}
	}
	return out
}

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func newPub(f *fakeClient) *Publisher {
	p := New([]SourceMeta{{Name: "box", Address: "10.0.0.2", SilenceMin: 15}}, []string{"hard_lockup", "kernel_panic"},
		30*time.Second, func() recv.Snapshot { return recv.Snapshot{} })
	p.SetClient(f)
	return p
}

func TestDiscoveryEntities(t *testing.T) {
	p := newPub(&fakeClient{})
	got := map[string]map[string]any{}
	for _, m := range p.Discovery() {
		var cfg map[string]any
		if err := json.Unmarshal(m.Payload, &cfg); err != nil {
			t.Fatal(err)
		}
		if !m.Retain || cfg["availability_topic"] != AvailabilityTopic {
			t.Errorf("%s: retain=%v avail=%v", m.Topic, m.Retain, cfg["availability_topic"])
		}
		got[m.Topic] = cfg
	}
	chk := func(topic string, kv map[string]any) {
		cfg, ok := got["homeassistant/"+topic+"/config"]
		if !ok {
			t.Fatalf("missing %s; have %v", topic, keys(got))
		}
		for k, v := range kv {
			if cfg[k] != v {
				t.Errorf("%s: %s = %v, want %v", topic, k, cfg[k], v)
			}
		}
	}
	chk("sensor/netconsole_receiver_box/last_message", map[string]any{"device_class": "timestamp", "default_entity_id": "sensor.netconsole_box_last_message"})
	chk("sensor/netconsole_receiver_box/messages", map[string]any{"state_class": "total_increasing"})
	chk("sensor/netconsole_receiver_box/sequence_gaps", map[string]any{"state_class": "total_increasing"})
	chk("sensor/netconsole_receiver_box/last_alert", map[string]any{"json_attributes_topic": "netconsole_receiver/box/last_alert_attrs"})
	chk("binary_sensor/netconsole_receiver_box/problem", map[string]any{"device_class": "problem"})
	chk("event/netconsole_receiver_box/alert", map[string]any{"state_topic": "netconsole_receiver/box/alert_event"})
	chk("sensor/netconsole_receiver/dropped_datagrams", map[string]any{"state_class": "total_increasing"})
	ev := got["homeassistant/event/netconsole_receiver_box/alert/config"]
	if et := ev["event_types"].([]any); len(et) != 2 || et[0] != "hard_lockup" {
		t.Errorf("event_types %v", et)
	}
	dev := got["homeassistant/sensor/netconsole_receiver_box/messages/config"]["device"].(map[string]any)
	if dev["name"] != "Netconsole box" {
		t.Errorf("device %v", dev)
	}
}

func keys(m map[string]map[string]any) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	return k
}

func TestNoEventEntityWithoutRules(t *testing.T) {
	p := New([]SourceMeta{{Name: "box"}}, nil, time.Second, nil)
	for _, m := range p.Discovery() {
		if strings.Contains(m.Topic, "/event/") {
			t.Fatal("event entity without event_types")
		}
	}
}

func TestOnConnectPublishesAvailabilityAndDiscovery(t *testing.T) {
	f := &fakeClient{up: true}
	p := newPub(f)
	p.OnConnect()
	if m := f.topic(AvailabilityTopic); len(m) != 1 || string(m[0].Payload) != "online" || !m[0].Retain {
		t.Fatalf("%+v", m)
	}
	if len(f.topic("homeassistant/binary_sensor/netconsole_receiver_box/problem/config")) != 1 {
		t.Fatal("no discovery")
	}
	if m := f.topic("netconsole_receiver/box/problem"); len(m) != 1 || string(m[0].Payload) != "OFF" {
		t.Fatalf("%+v", m)
	}
}

func TestAlertEventAndState(t *testing.T) {
	f := &fakeClient{up: true}
	p := newPub(f)
	a := alert.Alert{Rule: "hard_lockup", Kind: alert.KindFirst, Count: 1, First: "x hard LOCKUP", Last: strings.Repeat("hard LOCKUP ", 100), Time: t0}
	p.Alert("box", a)
	st := f.topic("netconsole_receiver/box/last_alert")
	if len(st) != 1 || len(st[0].Payload) > 255 || !strings.HasPrefix(string(st[0].Payload), "hard_lockup: ") || !st[0].Retain {
		t.Fatalf("%+v", st)
	}
	var attrs map[string]any
	json.Unmarshal(f.topic("netconsole_receiver/box/last_alert_attrs")[0].Payload, &attrs)
	if attrs["rule"] != "hard_lockup" || attrs["count"] != float64(1) || attrs["first_line"] != "x hard LOCKUP" || attrs["time"] != "2026-11-13T12:00:00Z" {
		t.Fatalf("%v", attrs)
	}
	if len(attrs["last_line"].(string)) > alert.AttrLine {
		t.Fatal("last_line not truncated")
	}
	ev := f.topic("netconsole_receiver/box/alert_event")
	if len(ev) != 1 || ev[0].Retain {
		t.Fatalf("event must be published once, not retained: %+v", ev)
	}
	var e map[string]any
	json.Unmarshal(ev[0].Payload, &e)
	if e["event_type"] != "hard_lockup" || e["kind"] != "first" {
		t.Fatalf("%v", e)
	}
}

func TestProblemPublishedImmediately(t *testing.T) {
	f := &fakeClient{up: true}
	p := newPub(f)
	p.Problem("box", true)
	p.Problem("box", false)
	m := f.topic("netconsole_receiver/box/problem")
	if len(m) != 2 || string(m[0].Payload) != "ON" || string(m[1].Payload) != "OFF" {
		t.Fatalf("%+v", m)
	}
}

func TestCountersThrottled(t *testing.T) {
	f := &fakeClient{up: true}
	p := newPub(f)
	snap := func(n uint64) recv.Snapshot {
		return recv.Snapshot{Dropped: 1, Sources: []recv.SourceStats{{Name: "box", Messages: n, LastMessage: t0}}}
	}
	if !p.MaybePublishCounters(snap(1), t0) {
		t.Fatal("first publish should happen")
	}
	for i := 1; i < 30; i++ { // a burst: counters change every second
		if p.MaybePublishCounters(snap(uint64(100*i)), t0.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("published inside the 30 s interval at +%ds", i)
		}
	}
	if !p.MaybePublishCounters(snap(5000), t0.Add(30*time.Second)) {
		t.Fatal("should publish after the interval")
	}
	m := f.topic("netconsole_receiver/box/messages")
	if len(m) != 2 || string(m[1].Payload) != "5000" {
		t.Fatalf("%+v", m)
	}
	// unchanged values are not republished
	p.MaybePublishCounters(snap(5000), t0.Add(61*time.Second))
	if len(f.topic("netconsole_receiver/box/messages")) != 2 || len(f.topic("netconsole_receiver/dropped_datagrams")) != 1 {
		t.Fatal("unchanged counters republished")
	}
}

func TestNothingPublishedWhileDisconnected(t *testing.T) {
	f := &fakeClient{up: false}
	p := newPub(f)
	p.Alert("box", alert.Alert{Rule: "hard_lockup", Last: "x", Time: t0})
	p.Problem("box", true)
	if p.MaybePublishCounters(recv.Snapshot{}, t0); len(f.msgs) != 0 {
		t.Fatalf("%+v", f.msgs)
	}
	// on connect the latest state is replayed
	f.up = true
	p.OnConnect()
	if m := f.topic("netconsole_receiver/box/problem"); len(m) != 1 || string(m[0].Payload) != "ON" {
		t.Fatalf("%+v", m)
	}
	if len(f.topic("netconsole_receiver/box/last_alert")) != 1 {
		t.Fatal("last alert not replayed")
	}
}
