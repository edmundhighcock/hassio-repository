// Package haws is a minimal Home Assistant WebSocket API client: just
// enough to authenticate, subscribe to state_changed for a fixed set of
// entities, and fetch their recent history for startup warm-up. Not a
// general-purpose HA client. See DOCS.md "Live inputs".
package haws

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

// StateEvent is one state_changed event, already filtered to entities the
// caller cares about and reduced to what the modules.StateUpdate needs.
type StateEvent struct {
	EntityID string
	Time     time.Time
	State    string
}

// Client is a connected (or about-to-reconnect) HA WebSocket client.
type Client struct {
	wsURL   string
	httpURL string
	token   string
	conn    *websocket.Conn
	nextID  int
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

// Subscribe sends subscribe_events for state_changed. The caller must
// then call ReadEvent in a loop.
func (c *Client) Subscribe() error {
	id := c.nextID
	c.nextID++
	return c.conn.WriteJSON(map[string]any{
		"id": id, "type": "subscribe_events", "event_type": "state_changed",
	})
}

type wsEventEnvelope struct {
	Type  string `json:"type"`
	Event struct {
		EventType string `json:"event_type"`
		Data      struct {
			EntityID string `json:"entity_id"`
			NewState *struct {
				State       string `json:"state"`
				LastChanged string `json:"last_changed"`
			} `json:"new_state"`
		} `json:"data"`
		TimeFired string `json:"time_fired"`
	} `json:"event"`
}

// ReadEvent blocks for the next state_changed message and returns it, or
// an error if the connection drops or a non-event control message type
// arrives that the caller should just ignore (ok=false, err=nil).
func (c *Client) ReadEvent() (StateEvent, bool, error) {
	var env wsEventEnvelope
	if err := c.conn.ReadJSON(&env); err != nil {
		return StateEvent{}, false, err
	}
	if env.Type != "event" || env.Event.EventType != "state_changed" || env.Event.Data.NewState == nil {
		return StateEvent{}, false, nil
	}
	t, err := time.Parse(time.RFC3339Nano, env.Event.TimeFired)
	if err != nil {
		t = time.Now().UTC()
	}
	return StateEvent{
		EntityID: env.Event.Data.EntityID,
		Time:     t,
		State:    env.Event.Data.NewState.State,
	}, true, nil
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// History fetches the HA REST history API for warm-up: the last `since`
// of history for entityIDs, oldest first, minimal_response + no_attributes
// to keep the payload small - see DOCS.md "Live inputs".
func (c *Client) History(ctx context.Context, since time.Time, entityIDs []string) ([]StateEvent, error) {
	u := fmt.Sprintf("%s/history/period/%s?minimal_response&no_attributes",
		c.httpURL, since.UTC().Format(time.RFC3339))
	for _, e := range entityIDs {
		u += "&filter_entity_id=" + e
	}
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
		for _, s := range series {
			t, err := time.Parse(time.RFC3339Nano, s.LastChanged)
			if err != nil {
				continue
			}
			out = append(out, StateEvent{EntityID: s.EntityID, Time: t, State: s.State})
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
