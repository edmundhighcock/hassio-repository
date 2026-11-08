// Package mqttpub publishes virtual-sensor output as retained MQTT
// discovery messages for Home Assistant's MQTT integration, and the
// resulting state updates. See DOCS.md "MQTT discovery" for the topic
// layout and why discovery is re-sent on every start and on
// homeassistant/status online.
package mqttpub

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"virtual_sensors/internal/modules"
)

const (
	discoveryPrefix   = "homeassistant"
	nodeID            = "virtual_sensors"
	availabilityTopic = "virtual_sensors/status"
)

// Device is the shared HA "device" block every discovery config points at,
// so every virtual sensor groups under one "Virtual sensors" device.
var Device = map[string]any{
	"identifiers":  []string{"virtual_sensors"},
	"name":         "Virtual sensors",
	"manufacturer": "ham/hassio-repository",
	"model":        "virtual_sensors add-on",
}

// Credentials is where to find the broker, from whichever source
// resolved it (Supervisor services API or the add-on's own options) -
// see cmd/virtual_sensors's mqttCredentials().
type Credentials struct {
	Host     string
	Port     int
	Username string
	Password string
	Source   string // for logging only - "supervisor" | "options"
}

// Publisher owns one MQTT connection, (re)sends discovery configs for a
// fixed set of outputs, and publishes their state.
type Publisher struct {
	client     mqtt.Client
	outputs    []modules.OutputSpec
	outputByID map[string]modules.OutputSpec
}

// perEntityAvailabilityTopic is the retained online/offline topic for one
// output that has OutputSpec.PerEntityAvailability set - see that field's
// doc comment. Distinct per ObjectID so losing one sensor's input doesn't
// mark unrelated sensors unavailable, unlike the shared availabilityTopic.
func perEntityAvailabilityTopic(objectID string) string {
	return fmt.Sprintf("%s/%s/availability", nodeID, objectID)
}

// attributesTopic is the retained topic for one output that has
// OutputSpec.HasAttributes set - see that field's doc comment.
func attributesTopic(objectID string) string {
	return fmt.Sprintf("%s/%s/attributes", nodeID, objectID)
}

// Connect blocks (with the caller's own retry loop expected around it -
// see main.go) until the broker accepts the connection, then publishes
// the LWT "online", discovery configs for every output, and subscribes to
// homeassistant/status to re-publish discovery whenever HA's MQTT
// integration reports itself online (e.g. after an HA restart that
// out-raced this add-on, or wiped retained discovery with "clean start").
func Connect(creds Credentials, outputs []modules.OutputSpec) (*Publisher, error) {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", creds.Host, creds.Port))
	opts.SetClientID("virtual_sensors")
	if creds.Username != "" {
		opts.SetUsername(creds.Username)
		opts.SetPassword(creds.Password)
	}
	opts.SetWill(availabilityTopic, "offline", 1, true)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(false) // main.go's own retry loop decides when to give up/log loudly
	opts.SetKeepAlive(30 * time.Second)

	outputByID := make(map[string]modules.OutputSpec, len(outputs))
	for _, o := range outputs {
		outputByID[o.ObjectID] = o
	}
	p := &Publisher{outputs: outputs, outputByID: outputByID}
	opts.OnConnect = func(c mqtt.Client) {
		log.Printf("mqttpub: connected to %s:%d (credentials from %s)", creds.Host, creds.Port, creds.Source)
		p.client = c
		if err := p.publishDiscovery(); err != nil {
			log.Printf("mqttpub: ERROR publishing discovery: %v", err)
		}
		if tok := c.Subscribe("homeassistant/status", 1, p.onHAStatus); tok.Wait() && tok.Error() != nil {
			log.Printf("mqttpub: ERROR subscribing to homeassistant/status: %v", tok.Error())
		}
	}
	opts.OnConnectionLost = func(_ mqtt.Client, err error) {
		log.Printf("mqttpub: ERROR connection to broker lost: %v - paho will keep retrying", err)
	}

	client := mqtt.NewClient(opts)
	token := client.Connect()
	token.Wait()
	if err := token.Error(); err != nil {
		return nil, fmt.Errorf("connect to mqtt broker %s:%d: %w", creds.Host, creds.Port, err)
	}
	p.client = client
	return p, nil
}

func (p *Publisher) onHAStatus(_ mqtt.Client, msg mqtt.Message) {
	if string(msg.Payload()) == "online" {
		log.Printf("mqttpub: homeassistant/status online - re-publishing discovery")
		if err := p.publishDiscovery(); err != nil {
			log.Printf("mqttpub: ERROR re-publishing discovery: %v", err)
		}
	}
}

type discoveryConfig struct {
	UniqueID string `json:"unique_id"`
	ObjectID string `json:"object_id"`
	// default_entity_id is what current Home Assistant uses to choose the
	// entity id; object_id alone was ignored on 2026.8 and the device name
	// was prefixed (sensor.virtual_sensors_...).
	DefaultEntityID   string         `json:"default_entity_id"`
	Name              string         `json:"name"`
	DeviceClass       string         `json:"device_class,omitempty"`
	StateClass        string         `json:"state_class,omitempty"`
	UnitOfMeasurement string         `json:"unit_of_measurement,omitempty"`
	StateTopic        string         `json:"state_topic"`
	AvailabilityTopic string         `json:"availability_topic"`
	// JSONAttributesTopic (0.1.7): set only when OutputSpec.HasAttributes
	// is true - see that field's doc comment.
	JSONAttributesTopic string         `json:"json_attributes_topic,omitempty"`
	Device              map[string]any `json:"device"`
}

func stateTopic(objectID string) string {
	return fmt.Sprintf("%s/%s/state", nodeID, objectID)
}

func discoveryTopic(objectID string) string {
	return fmt.Sprintf("%s/sensor/%s/%s/config", discoveryPrefix, nodeID, objectID)
}

func (p *Publisher) publishDiscovery() error {
	if tok := p.client.Publish(availabilityTopic, 1, true, "online"); tok.Wait() && tok.Error() != nil {
		return fmt.Errorf("publish availability: %w", tok.Error())
	}
	for _, o := range p.outputs {
		avTopic := availabilityTopic
		if o.PerEntityAvailability {
			avTopic = perEntityAvailabilityTopic(o.ObjectID)
			// Default this entity to available at discovery time (before
			// any state has been seen) - it is pulled back to "offline"
			// by PublishState as soon as (and for as long as) its module
			// reports Unavailable:true.
			if tok := p.client.Publish(avTopic, 1, true, "online"); tok.Wait() && tok.Error() != nil {
				return fmt.Errorf("publish initial availability for %s: %w", o.ObjectID, tok.Error())
			}
		}
		cfg := discoveryConfig{
			UniqueID: o.UniqueID, ObjectID: o.ObjectID, DefaultEntityID: "sensor." + o.ObjectID, Name: o.Name,
			DeviceClass: o.DeviceClass, StateClass: o.StateClass, UnitOfMeasurement: o.Unit,
			StateTopic: stateTopic(o.ObjectID), AvailabilityTopic: avTopic,
			Device: Device,
		}
		if o.HasAttributes {
			cfg.JSONAttributesTopic = attributesTopic(o.ObjectID)
		}
		b, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		if tok := p.client.Publish(discoveryTopic(o.ObjectID), 1, true, b); tok.Wait() && tok.Error() != nil {
			return fmt.Errorf("publish discovery for %s: %w", o.ObjectID, tok.Error())
		}
	}
	return nil
}

// PublishState publishes one output's current numeric value, retained,
// so a restarting Home Assistant immediately shows the last known value
// instead of "unknown" until the next update. For an output with
// OutputSpec.PerEntityAvailability set, u.Unavailable toggles that
// output's own availability topic instead of (Unavailable=true) or in
// addition to (Unavailable=false, i.e. back online) publishing a state
// value - see OutputSpec.PerEntityAvailability and OutputUpdate.Unavailable.
// For an output with OutputSpec.HasAttributes set, a non-empty
// u.AttributesJSON is published retained to its json_attributes_topic
// alongside whatever else this call does; u.AttributesOnly=true means
// THAT is all this call does (no state/availability topic is touched) -
// used when a module has a fresh attributes snapshot to publish (e.g. a
// known load newly lost/regained) without a fresh value of its own.
func (p *Publisher) PublishState(u modules.OutputUpdate) error {
	spec, known := p.outputByID[u.ObjectID]

	if u.AttributesOnly {
		if known && spec.HasAttributes && u.AttributesJSON != "" {
			if tok := p.client.Publish(attributesTopic(u.ObjectID), 1, true, u.AttributesJSON); tok.Wait() && tok.Error() != nil {
				return fmt.Errorf("publish attributes for %s: %w", u.ObjectID, tok.Error())
			}
		}
		return nil
	}

	if known && spec.PerEntityAvailability {
		payload := "online"
		if u.Unavailable {
			payload = "offline"
		}
		if tok := p.client.Publish(perEntityAvailabilityTopic(u.ObjectID), 1, true, payload); tok.Wait() && tok.Error() != nil {
			return fmt.Errorf("publish availability for %s: %w", u.ObjectID, tok.Error())
		}
	}

	if known && spec.HasAttributes && u.AttributesJSON != "" {
		if tok := p.client.Publish(attributesTopic(u.ObjectID), 1, true, u.AttributesJSON); tok.Wait() && tok.Error() != nil {
			return fmt.Errorf("publish attributes for %s: %w", u.ObjectID, tok.Error())
		}
	}

	if known && spec.PerEntityAvailability && u.Unavailable {
		return nil // no state payload while unavailable
	}
	tok := p.client.Publish(stateTopic(u.ObjectID), 1, true, fmt.Sprintf("%g", u.Value))
	tok.Wait()
	return tok.Error()
}

// Close publishes "offline" (best-effort) and disconnects.
func (p *Publisher) Close() {
	if p.client == nil {
		return
	}
	p.client.Publish(availabilityTopic, 1, true, "offline").WaitTimeout(2 * time.Second)
	p.client.Disconnect(250)
}
