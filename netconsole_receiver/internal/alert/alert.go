// Package alert matches log text against regex rules and applies a
// per-rule cooldown window: the first match fires at once, further
// matches inside the window are only counted, and when the window closes
// ONE summary fires if the count rose.
package alert

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"netconsole_receiver/internal/logsink"
)

// Rule is a compiled alert rule.
type Rule struct {
	Name     string
	Re       *regexp.Regexp
	Cooldown time.Duration
}

// Kind of an Alert.
const (
	KindFirst   = "first"
	KindSummary = "summary"
)

// Alert is what a Tracker reports. For KindFirst Count is 1; for
// KindSummary it is the total number of matches in the closed window.
type Alert struct {
	Rule  string
	Kind  string
	Count int
	First string // first matching line of the window
	Last  string // most recent matching line
	Time  time.Time
}

type window struct {
	end   time.Time
	count int
	first string
	last  string
	lastT time.Time
}

// Tracker holds the cooldown state of every rule for one source. Not safe
// for concurrent use; the caller serialises.
type Tracker struct {
	rules []Rule
	win   []*window
}

func NewTracker(rules []Rule) *Tracker {
	return &Tracker{rules: rules, win: make([]*window, len(rules))}
}

// Open reports whether any rule's cooldown window is open.
func (t *Tracker) Open() bool {
	for _, w := range t.win {
		if w != nil {
			return true
		}
	}
	return false
}

// matchedLine returns the line of text containing the match.
func matchedLine(text string, loc []int) string {
	s := strings.LastIndexByte(text[:loc[0]], '\n') + 1
	e := strings.IndexByte(text[loc[0]:], '\n')
	if e < 0 {
		return text[s:]
	}
	return text[s : loc[0]+e]
}

// Observe tests text against every rule and returns the alerts that fire
// now (only first matches; later ones are counted silently).
func (t *Tracker) Observe(text string, now time.Time) []Alert {
	var out []Alert
	for i, r := range t.rules {
		loc := r.Re.FindStringIndex(text)
		if loc == nil {
			continue
		}
		line := matchedLine(text, loc)
		w := t.win[i]
		if w != nil {
			w.count++
			w.last, w.lastT = line, now
			continue
		}
		t.win[i] = &window{end: now.Add(r.Cooldown), count: 1, first: line, last: line, lastT: now}
		out = append(out, Alert{Rule: r.Name, Kind: KindFirst, Count: 1, First: line, Last: line, Time: now})
	}
	return out
}

// Tick closes windows that have expired and returns one summary for each
// whose count rose above 1.
func (t *Tracker) Tick(now time.Time) []Alert {
	var out []Alert
	for i, w := range t.win {
		if w == nil || now.Before(w.end) {
			continue
		}
		if w.count > 1 {
			out = append(out, Alert{Rule: t.rules[i].Name, Kind: KindSummary, Count: w.count,
				First: w.first, Last: w.last, Time: w.lastT})
		}
		t.win[i] = nil
	}
	return out
}

// MaxState is HA's limit for an entity state (255 characters); we stay
// safely under it, counting bytes, which is never fewer than characters.
const MaxState = 250

// Truncate shortens s to at most max bytes on a rune boundary, ending in
// "..." when it had to cut.
func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max - 3
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// StateText is the last_alert sensor state: the rule name and a short
// excerpt of the last matching line, always under MaxState.
func StateText(a Alert) string {
	excerpt := logsink.Escape(strings.TrimSpace(a.Last))
	if a.Kind == KindSummary {
		return Truncate(a.Rule+" x"+strconv.Itoa(a.Count)+": "+excerpt, MaxState)
	}
	return Truncate(a.Rule+": "+excerpt, MaxState)
}

// AttrLine is the longest first/last line kept in attributes.
const AttrLine = 500

// AttrText prepares a matching line for an attribute value.
func AttrText(s string) string { return Truncate(logsink.Escape(s), AttrLine) }
