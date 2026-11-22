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

// Benign SATA softreset handling. Spinning HDDs on resume from suspend log
// "ataN: softreset failed (device not ready)" and recover ~6 s later with
// "ataN: SATA link up 6.0 Gbps". A match whose only matching line is such
// a softreset failure is NOT alerted when it comes within ResumeQuiet of a
// resume marker, and is held back for LinkUpHold otherwise: a "SATA link
// up" for the same port inside that time drops it, otherwise it fires as
// a normal match (late by LinkUpHold, nothing is lost). Every other
// matching line is alerted at once, exactly as before.
const (
	ResumeQuiet = 60 * time.Second
	LinkUpHold  = 30 * time.Second
)

var (
	softresetRe = regexp.MustCompile(`\b(ata[0-9]+)(?:\.[0-9]+)?: softreset failed`)
	linkUpRe    = regexp.MustCompile(`\b(ata[0-9]+): SATA link up\b`)
	resumeRe    = regexp.MustCompile(`PM: suspend exit`)
	suspendRe   = regexp.MustCompile(`sys-netconsole: suspending`)
	// A stall is never benign: these strings must never be swallowed by
	// the softreset suppression, whatever else the text says.
	hardRe = regexp.MustCompile(`exception Emask|DID_TIME_OUT`)
)

// Resume markers, besides "PM: suspend exit" (which usually falls inside
// the sequence GAP of a real resume and so never arrives):
//   - a GAP in the sequence numbers (the engine calls NoteGap);
//   - a "sys-netconsole: suspending" message followed by SuspendSilence of
//     nothing at all: the first message after that silence is a resume.
//
// A suspend that is aborted leaves the flag set; the next message that
// comes after a long silence (e.g. a 10-min heartbeat) then opens one
// harmless 60-s window.
const SuspendSilence = 5 * time.Minute

// Marker names, as reported by Stats.
const (
	MarkerSuspendExit = "suspend_exit"
	MarkerGap         = "gap"
	MarkerSilence     = "suspend_silence"
)

// Stats counts how softreset-only matches were dropped, and which marker
// opened the most recent resume window.
type Stats struct {
	SuppressedResume int
	SuppressedLinkUp int
	LastMarker       string
}

// held is a softreset-only match waiting for a link-up on its port.
type held struct {
	rule     int
	port     string
	line     string
	at       time.Time
	deadline time.Time
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
	rules    []Rule
	win      []*window
	resumeAt time.Time // last resume marker seen; zero = none
	pending  []held
	marker   string    // kind of the marker that set resumeAt
	lastMsg  time.Time // time of the previous Observe
	suspend  bool      // "sys-netconsole: suspending" seen, no resume yet
	stats    Stats
}

// Stats returns the suppression counters.
func (t *Tracker) Stats() Stats {
	st := t.stats
	st.LastMarker = t.marker
	return st
}

// NoteGap tells the tracker that the sender's sequence numbers jumped
// forward (messages were lost), which on a real resume is how the resume
// shows. Call it before Observe of the message that revealed the gap.
func (t *Tracker) NoteGap(now time.Time) { t.setResume(MarkerGap, now) }

func (t *Tracker) setResume(kind string, now time.Time) {
	t.resumeAt, t.marker, t.suspend = now, kind, false
}

func NewTracker(rules []Rule) *Tracker {
	return &Tracker{rules: rules, win: make([]*window, len(rules))}
}

// Open reports whether any rule's cooldown window is open. A held-back
// softreset is not "open": it may yet turn out benign.
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
	t.noteMarkers(text, now)
	var out []Alert
	for i, r := range t.rules {
		loc := r.Re.FindStringIndex(text)
		if loc == nil {
			continue
		}
		line := matchedLine(text, loc)
		if sm := softresetRe.FindStringSubmatch(line); sm != nil && !hardRe.MatchString(text) && !t.otherMatch(r, text) {
			if !t.resumeAt.IsZero() && !now.Before(t.resumeAt) && now.Sub(t.resumeAt) <= ResumeQuiet {
				t.stats.SuppressedResume++
				continue // benign: HDDs spinning up after resume
			}
			t.pending = append(t.pending, held{rule: i, port: sm[1], line: line, at: now, deadline: now.Add(LinkUpHold)})
			continue
		}
		if a, ok := t.fire(i, line, now); ok {
			out = append(out, a)
		}
	}
	return out
}

// noteMarkers records resume markers and drops held softresets whose port
// came back up.
func (t *Tracker) noteMarkers(text string, now time.Time) {
	if t.suspend && !t.lastMsg.IsZero() && now.Sub(t.lastMsg) >= SuspendSilence {
		t.setResume(MarkerSilence, now)
	}
	t.lastMsg = now
	if resumeRe.MatchString(text) {
		t.setResume(MarkerSuspendExit, now)
	}
	if suspendRe.MatchString(text) {
		t.suspend = true
	}
	if len(t.pending) == 0 {
		return
	}
	for _, m := range linkUpRe.FindAllStringSubmatch(text, -1) {
		keep := t.pending[:0]
		for _, h := range t.pending {
			if h.port != m[1] {
				keep = append(keep, h)
			} else {
				t.stats.SuppressedLinkUp++
			}
		}
		t.pending = keep
	}
}

// otherMatch reports whether rule r also matches text on a line that is
// not a softreset failure.
func (t *Tracker) otherMatch(r Rule, text string) bool {
	for _, l := range strings.Split(text, "\n") {
		if r.Re.MatchString(l) && !softresetRe.MatchString(l) {
			return true
		}
	}
	return false
}

// fire counts a match in rule i's window, opening it (and returning the
// first-match alert) when it was closed.
func (t *Tracker) fire(i int, line string, now time.Time) (Alert, bool) {
	r := t.rules[i]
	if w := t.win[i]; w != nil {
		w.count++
		w.last, w.lastT = line, now
		return Alert{}, false
	}
	t.win[i] = &window{end: now.Add(r.Cooldown), count: 1, first: line, last: line, lastT: now}
	return Alert{Rule: r.Name, Kind: KindFirst, Count: 1, First: line, Last: line, Time: now}, true
}

// Tick closes windows that have expired and returns one summary for each
// whose count rose above 1.
func (t *Tracker) Tick(now time.Time) []Alert {
	var out []Alert
	// Held softresets whose link-up never came are real: alert as usual.
	keep := t.pending[:0]
	for _, h := range t.pending {
		if now.Before(h.deadline) {
			keep = append(keep, h)
			continue
		}
		if a, ok := t.fire(h.rule, h.line, h.at); ok {
			out = append(out, a)
		}
	}
	t.pending = keep
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
