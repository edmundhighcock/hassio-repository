package mqttpub

import (
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"virtual_sensors/internal/modules"
	"virtual_sensors/internal/modules/priceshed"
)

type fakeToken struct{}

func (fakeToken) Wait() bool                     { return true }
func (fakeToken) WaitTimeout(time.Duration) bool { return true }
func (fakeToken) Done() <-chan struct{}          { c := make(chan struct{}); close(c); return c }
func (fakeToken) Error() error                   { return nil }

type fakeMsg struct{ payload string }

func (fakeMsg) Duplicate() bool   { return false }
func (fakeMsg) Qos() byte         { return 1 }
func (fakeMsg) Retained() bool    { return false }
func (fakeMsg) Topic() string     { return "" }
func (fakeMsg) MessageID() uint16 { return 0 }
func (m fakeMsg) Payload() []byte { return []byte(m.payload) }
func (fakeMsg) Ack()              {}

// fakeClient records publishes and subscriptions (a minimal broker).
type fakeClient struct {
	mqtt.Client // unimplemented methods panic if used
	mu          sync.Mutex
	pub         map[string]string
	subs        map[string]mqtt.MessageHandler
}

func (f *fakeClient) Publish(topic string, _ byte, _ bool, payload any) mqtt.Token {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pub == nil {
		f.pub = map[string]string{}
	}
	switch v := payload.(type) {
	case string:
		f.pub[topic] = v
	case []byte:
		f.pub[topic] = string(v)
	}
	return fakeToken{}
}

func (f *fakeClient) Subscribe(topic string, _ byte, h mqtt.MessageHandler) mqtt.Token {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = map[string]mqtt.MessageHandler{}
	}
	f.subs[topic] = h
	return fakeToken{}
}

// TestPriceShedSensorAndButtonReachMQTT proves the discovery configs and,
// crucially, the button's command-topic subscription (a past release shipped
// a button whose subscription was never wired): a PRESS delivered on the
// subscribed topic reaches onPress with the module's own object id.
func TestPriceShedSensorAndButtonReachMQTT(t *testing.T) {
	m := priceshed.New()
	var pressed []string
	p := &Publisher{outputs: m.Outputs(), outputByID: map[string]modules.OutputSpec{}, buttons: m.Buttons(),
		onPress: func(id string) { pressed = append(pressed, id) }}
	for _, o := range m.Outputs() {
		p.outputByID[o.ObjectID] = o
	}
	fc := &fakeClient{}
	p.setup(fc)

	sensor := fc.pub["homeassistant/sensor/virtual_sensors/price_shed_tool_shed_lights/config"]
	if !strings.Contains(sensor, `"default_entity_id":"sensor.price_shed_tool_shed_lights"`) || !strings.Contains(sensor, "json_attributes_topic") {
		t.Fatalf("sensor discovery: %s", sensor)
	}
	button := fc.pub["homeassistant/button/virtual_sensors/replan_price_shed/config"]
	if !strings.Contains(button, `"default_entity_id":"button.replan_price_shed"`) || !strings.Contains(button, `"command_topic":"virtual_sensors/replan_price_shed/press"`) {
		t.Fatalf("button discovery: %s", button)
	}
	h, ok := fc.subs["virtual_sensors/replan_price_shed/press"]
	if !ok {
		t.Fatalf("no subscription for the button's command topic; subs=%v", fc.subs)
	}
	h(fc, fakeMsg{"NOPE"})
	h(fc, fakeMsg{"PRESS"})
	if len(pressed) != 1 || pressed[0] != "replan_price_shed" {
		t.Fatalf("pressed = %v", pressed)
	}
	// a state publish lands on the state and attributes topics
	if err := p.PublishState(modules.OutputUpdate{ObjectID: "price_shed_tool_shed_lights", StateString: "on", AttributesJSON: `{"a":1}`}); err != nil {
		t.Fatal(err)
	}
	if fc.pub["virtual_sensors/price_shed_tool_shed_lights/state"] != "on" || fc.pub["virtual_sensors/price_shed_tool_shed_lights/attributes"] != `{"a":1}` {
		t.Fatalf("%v", fc.pub)
	}
}
