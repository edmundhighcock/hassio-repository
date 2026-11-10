// Package haws is a minimal Home Assistant WebSocket API client: just
// enough to authenticate, subscribe to state_changed for a fixed set of
// entities, fetch their recent history for startup warm-up, read their
// current states at connect, and (0.2.0) call services with id-matched
// results. Not a general-purpose HA client. See DOCS.md "Live inputs".
//
// Concurrency: one goroutine reads (ReadMessage); CallService may be
// called from any other goroutine (writes are serialised by a mutex).
package haws

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// StateEvent is one state_changed event, already filtered to entities the
// caller cares about and reduced to what the modules.StateUpdate needs.
type StateEvent struct {
	EntityID string
	Time     time.Time
	State    string
	// Attrs (0.2.0) holds only the whitelisted attributes (see
	// Client.AttrWhitelist), as raw JSON.
	Attrs map[string]json.RawMessage
}

// Result (0.2.0) is one WS "result" message: the reply to a command we
// sent with the given id.
type Result struct {
	ID      int
	Success bool
	Err     string
	// Response is the service response data, if the call asked for it.
	Response json.RawMessage
}

// Message is one inbound WS message of interest: either a state event or
// a command result (exactly one is non-nil).
type Message struct {
	Event  *StateEvent
	Result *Result
}

// Client is a connected (or about-to-reconnect) HA WebSocket client.
type Client struct {
	wsURL   string
	httpURL string
	token   string
	conn    *websocket.Conn
	wmu     sync.Mutex
	nextID  int

	// AttrWhitelist (0.2.0) returns the attribute names to keep for a full
	// entity id (nil: keep none). Attributes are only decoded for entities
	// it names, so large blobs on unrelated entities stay undecoded.
	AttrWhitelist func(entityID string) []string
}

// New returns a Client for the given WebSocket and HTTP base URLs (e.g.
// "ws://supervisor/core/websocket" and "http://supervisor/core/api"),
// authenticated with the Supervisor long-lived token.
func New(wsURL, httpURL, token string) *Client {
	return &Client{wsURL: wsURL, httpURL: httpURL, token: token}
}

// Connect dials and authenticates once. Callers reconnect with their own
// backoff loop (see main.go) rather than this package looping internally,
// so a connection failure is always visible to the caller's logging.
func (c *Client) Connect(ctx context.Context) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", c.wsURL, err)
	}
	c.conn = conn

	var hello struct {
		Type string `json:"type"`
	}
	if err := conn.ReadJSON(&hello); err != nil {
		return fmt.Errorf("read auth_required: %w", err)
	}
	if hello.Type != "auth_required" {
		return fmt.Errorf("unexpected first message type %q (want auth_required)", hello.Type)
	}
	if err := conn.WriteJSON(map[string]string{"type": "auth", "access_token": c.token}); err != nil {
		return fmt.Errorf("send auth: %w", err)
	}
	var authResp struct {
		Type string `json:"type"`
	}
	if err := conn.ReadJSON(&authResp); err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}
	if authResp.Type != "auth_ok" {
		return fmt.Errorf("authentication failed (got %q) - check SUPERVISOR_TOKEN and homeassistant_api: true in config.yaml", authResp.Type)
	}
	c.nextID = 1
	return nil
}

func (c *Client) send(msg map[string]any) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.conn == nil {
		return 0, fmt.Errorf("not connected")
	}
	id := c.nextID
	c.nextID++
	msg["id"] = id
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return id, c.conn.WriteJSON(msg)
}

// Subscribe sends subscribe_events for state_changed. The caller must
// then call ReadMessage in a loop.
func (c *Client) Subscribe() error {
	_, err := c.send(map[string]any{"type": "subscribe_events", "event_type": "state_changed"})
	return err
}

// CallService (0.2.0) sends a call_service command and returns its WS id;
// the outcome arrives later as a Result with that id from ReadMessage.
// target, if non-nil, is sent as the service call's target.
func (c *Client) CallService(domain, service string, data, target map[string]any) (int, error) {
	msg := map[string]any{"type": "call_service", "domain": domain, "service": service}
	if data != nil {
		msg["service_data"] = data
	}
	if target != nil {
		msg["target"] = target
	}
	return c.send(msg)
}

// CallServiceResponse is CallService with return_response: the service's
// response data arrives in Result.Response.
func (c *Client) CallServiceResponse(domain, service string, data, target map[string]any) (int, error) {
	msg := map[string]any{"type": "call_service", "domain": domain, "service": service, "return_response": true}
	if data != nil {
		msg["service_data"] = data
	}
	if target != nil {
		msg["target"] = target
	}
	return c.send(msg)
}

type wsEnvelope struct {
	ID      int    `json:"id"`
	Type    string `json:"type"`
	Success bool   `json:"success"`
	Result  *struct {
		Response json.RawMessage `json:"response"`
	} `json:"result"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Event struct {
		EventType string `json:"event_type"`
		Data      struct {
			EntityID string `json:"entity_id"`
			NewState *struct {
				State       string          `json:"state"`
				LastChanged string          `json:"last_changed"`
				Attributes  json.RawMessage `json:"attributes"`
			} `json:"new_state"`
		} `json:"data"`
		TimeFired string `json:"time_fired"`
	} `json:"event"`
}

// ReadMessage blocks for the next WS message. It returns ok=false, err=nil
// for messages the caller should ignore, and an error if the connection
// drops.
func (c *Client) ReadMessage() (Message, bool, error) {
	var env wsEnvelope
	if err := c.conn.ReadJSON(&env); err != nil {
		return Message{}, false, err
	}
	if env.Type == "result" {
		r := &Result{ID: env.ID, Success: env.Success}
		if env.Result != nil {
			r.Response = env.Result.Response
		}
		if env.Error != nil {
			r.Err = env.Error.Code + ": " + env.Error.Message
		}
		return Message{Result: r}, true, nil
	}
	if env.Type != "event" || env.Event.EventType != "state_changed" || env.Event.Data.NewState == nil {
		return Message{}, false, nil
	}
	t, err := time.Parse(time.RFC3339Nano, env.Event.TimeFired)
	if err != nil {
		t = time.Now().UTC()
	}
	ev := &StateEvent{
		EntityID: env.Event.Data.EntityID,
		Time:     t,
		State:    env.Event.Data.NewState.State,
	}
	ev.Attrs = c.filterAttrs(ev.EntityID, env.Event.Data.NewState.Attributes)
	return Message{Event: ev}, true, nil
}

// ReadEvent is the pre-0.2.0 API: the next state event, ignoring results.
func (c *Client) ReadEvent() (StateEvent, bool, error) {
	m, ok, err := c.ReadMessage()
	if err != nil || !ok || m.Event == nil {
		return StateEvent{}, false, err
	}
	return *m.Event, true, nil
}

func (c *Client) filterAttrs(entityID string, raw json.RawMessage) map[string]json.RawMessage {
	if c.AttrWhitelist == nil || len(raw) == 0 {
		return nil
	}
	keep := c.AttrWhitelist(entityID)
	if len(keep) == 0 {
		return nil
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(keep))
	for _, k := range keep {
		if v, ok := all[k]; ok {
			out[k] = append(json.RawMessage(nil), v...)
		}
	}
	return out
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

// States (0.2.0) fetches the current state of every entity for which
// match returns true, via REST GET /api/states, decoding the response as
// a stream so only matching entities are held in memory. Times are each
// state's last_updated (falling back to last_changed).
func (c *Client) States(ctx context.Context, match func(entityID string) bool) ([]StateEvent, error) {
	u := c.httpURL + "/states"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("GET %s: %s: %s", u, resp.Status, string(body))
	}
	return decodeStates(resp.Body, match, c.filterAttrs)
}

func decodeStates(r io.Reader, match func(string) bool, filter func(string, json.RawMessage) map[string]json.RawMessage) ([]StateEvent, error) {
	dec := json.NewDecoder(r)
	if _, err := dec.Token(); err != nil { // '['
		return nil, fmt.Errorf("decode states: %w", err)
	}
	var out []StateEvent
	for dec.More() {
		var s struct {
			EntityID    string          `json:"entity_id"`
			State       string          `json:"state"`
			LastChanged string          `json:"last_changed"`
			LastUpdated string          `json:"last_updated"`
			Attributes  json.RawMessage `json:"attributes"`
		}
		if err := dec.Decode(&s); err != nil {
			return nil, fmt.Errorf("decode states: %w", err)
		}
		if !match(s.EntityID) {
			continue
		}
		ts := s.LastUpdated
		if ts == "" {
			ts = s.LastChanged
		}
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			continue
		}
		out = append(out, StateEvent{EntityID: s.EntityID, Time: t, State: s.State, Attrs: filter(s.EntityID, s.Attributes)})
	}
	return out, nil
}

// History fetches the HA REST history API for warm-up: the last `since`
// of history for entityIDs, oldest first, minimal_response + no_attributes
// to keep the payload small - see DOCS.md "Live inputs".
func (c *Client) History(ctx context.Context, since time.Time, entityIDs []string) ([]StateEvent, error) {
	u := fmt.Sprintf("%s/history/period/%s?minimal_response&no_attributes",
		c.httpURL, since.UTC().Format(time.RFC3339))
	// One comma-separated filter_entity_id of full ids; module inputs
	// carry no domain, so sensor. is added here.
	full := make([]string, 0, len(entityIDs))
	for _, e := range entityIDs {
		if !strings.Contains(e, ".") {
			e = "sensor." + e
		}
		full = append(full, e)
	}
	u += "&filter_entity_id=" + strings.Join(full, ",")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("GET %s: %s: %s", u, resp.Status, string(body))
	}

	// Response is a list-of-lists: one list of state dicts per entity.
	var raw [][]struct {
		EntityID    string `json:"entity_id"`
		State       string `json:"state"`
		LastChanged string `json:"last_changed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode history response: %w", err)
	}
	var out []StateEvent
	for _, series := range raw {
		// minimal_response carries entity_id on the first entry only.
		id := ""
		for _, s := range series {
			if s.EntityID != "" {
				id = s.EntityID
			}
			t, err := time.Parse(time.RFC3339Nano, s.LastChanged)
			if err != nil {
				continue
			}
			out = append(out, StateEvent{EntityID: id, Time: t, State: s.State})
		}
	}
	return out, nil
}

// ParseFloat parses an HA state string to a float, reporting availability
// - HA uses "unavailable"/"unknown"/"" for no numeric value.
func ParseFloat(state string) (value float64, available bool) {
	switch state {
	case "unavailable", "unknown", "":
		return 0, false
	}
	v, err := strconv.ParseFloat(state, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
