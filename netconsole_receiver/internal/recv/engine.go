// Package recv is the datagram-processing core: source allow-list, log
// lines, sequence tracking, fragment reassembly and alert matching. It
// does no I/O of its own beyond the Sink and Notifier it is given, and
// takes the current time as an argument, so it is fully unit-testable.
package recv

import (
	"fmt"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"netconsole_receiver/internal/alert"
	"netconsole_receiver/internal/config"
	"netconsole_receiver/internal/logsink"
	"netconsole_receiver/internal/nc"
)

// Sink receives finished one-line log records for one source.
type Sink interface{ WriteLine(line string) error }

// Notifier is told about alerts and problem-state changes.
type Notifier interface {
	Alert(source string, a alert.Alert)
	Problem(source string, on bool)
}

// SourceStats is a point-in-time copy of one source's counters.
type SourceStats struct {
	Name        string
	Addr        netip.Addr
	Port        uint16
	Messages    uint64
	Gaps        uint64
	LastMessage time.Time // zero until the first accepted datagram
}

type Snapshot struct {
	Sources []SourceStats
	Dropped uint64
}

type track struct {
	have bool
	last uint64
}

type source struct {
	cfg     config.Source
	sink    Sink
	tracker *alert.Tracker
	seq     track
	msgid   track
	stats   SourceStats
	problem bool
}

type Engine struct {
	mu      sync.Mutex
	srcs    []*source // sources with a pinned port first
	byName  map[string]*source
	frags   *nc.Reassembler
	notify  Notifier
	logf    func(format string, args ...any)
	dropped atomic.Uint64
	dropLog map[netip.Addr]bool
	errLog  map[string]time.Time
}

// New builds an Engine. open is called once per source to get its Sink.
func New(c config.Resolved, open func(name string) Sink, n Notifier, logf func(string, ...any)) *Engine {
	e := &Engine{
		byName: map[string]*source{}, notify: n, logf: logf,
		frags:   nc.NewReassembler(config.FragmentTimeout),
		dropLog: map[netip.Addr]bool{}, errLog: map[string]time.Time{},
	}
	mk := func(s config.Source) *source {
		src := &source{cfg: s, sink: open(s.Name), tracker: alert.NewTracker(c.Rules)}
		src.stats = SourceStats{Name: s.Name, Addr: s.Addr, Port: s.Port}
		e.byName[s.Name] = src
		return src
	}
	// A source pinned to a port is more specific than one for the address
	// alone, so it is tested first.
	for _, s := range c.Srcs {
		if s.Port != 0 {
			e.srcs = append(e.srcs, mk(s))
		}
	}
	for _, s := range c.Srcs {
		if s.Port == 0 {
			e.srcs = append(e.srcs, mk(s))
		}
	}
	return e
}

func (e *Engine) match(ap netip.AddrPort) *source {
	a := ap.Addr().Unmap()
	for _, s := range e.srcs {
		if s.cfg.Addr == a && (s.cfg.Port == 0 || s.cfg.Port == ap.Port()) {
			return s
		}
	}
	return nil
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func (e *Engine) write(s *source, ap netip.AddrPort, now time.Time, text string) {
	line := stamp(now) + " " + ap.String() + " " + logsink.Escape(text)
	if err := s.sink.WriteLine(line); err != nil {
		if last, ok := e.errLog[s.cfg.Name]; !ok || now.Sub(last) > time.Minute {
			e.errLog[s.cfg.Name] = now
			e.logf("ERROR: writing log for source %s: %v (counters and alerts continue; this message is rate-limited)", s.cfg.Name, err)
		}
	}
}

// Handle processes one datagram. One datagram is one record.
func (e *Engine) Handle(from netip.AddrPort, payload []byte, now time.Time) {
	s := e.match(from)
	if s == nil {
		e.dropped.Add(1)
		e.mu.Lock()
		a := from.Addr().Unmap()
		if !e.dropLog[a] && len(e.dropLog) < 64 {
			e.dropLog[a] = true
			e.logf("dropping datagram from %s: not in `sources` (first drop from this address; further drops are only counted)", from)
		}
		e.mu.Unlock()
		return
	}
	rec := nc.Parse(payload)

	e.mu.Lock()
	defer e.mu.Unlock()
	from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
	s.stats.Messages++
	s.stats.LastMessage = now

	raw := string(payload)
	if !rec.Extended {
		raw = rec.Text // plain line: trimmed, stored as-is
	}
	e.write(s, from, now, raw)

	if !rec.Extended {
		e.alerts(s, rec.Text, now)
		e.syncProblem(s)
		return
	}
	e.trackSeq(s, from, now, "", &s.seq, rec.Seq)
	if rec.Fragmented {
		whole, done := e.frags.Add(from.String(), rec, now)
		if !done {
			e.flushExpired(now)
			return
		}
		e.write(s, from, now, "REASSEMBLED seq="+strconv.FormatUint(whole.Seq, 10)+" "+whole.Text)
		rec = whole
	}
	e.message(s, from, now, rec)
	e.flushExpired(now)
	e.syncProblem(s)
}

// message handles a complete (single or reassembled) extended message.
func (e *Engine) message(s *source, from netip.AddrPort, now time.Time, rec nc.Record) {
	if id, ok := rec.MsgID(); ok {
		e.trackMsgID(s, from, now, id)
	}
	e.alerts(s, rec.Text, now)
}

func (e *Engine) alerts(s *source, text string, now time.Time) {
	for _, a := range s.tracker.Observe(text, now) {
		e.logf("ALERT source=%s rule=%s: %s", s.cfg.Name, a.Rule, alert.AttrText(a.Last))
		e.notify.Alert(s.cfg.Name, a)
	}
}

func (e *Engine) syncProblem(s *source) {
	if on := s.tracker.Open(); on != s.problem {
		s.problem = on
		e.notify.Problem(s.cfg.Name, on)
	}
}

// trackSeq logs GAP / SEQ_RESET for a monotonically increasing counter.
// An equal value is fine (fragments of one message share a sequence
// number); a jump forward is a GAP; a decrease means the sender rebooted.
func (e *Engine) trackSeq(s *source, from netip.AddrPort, now time.Time, label string, t *track, v uint64) {
	defer func() { t.have, t.last = true, v }()
	if !t.have || v == t.last {
		return
	}
	switch {
	case v > t.last+1:
		s.stats.Gaps++
		e.write(s, from, now, fmt.Sprintf("GAP %s%d..%d", label, t.last+1, v-1))
	case v < t.last:
		e.write(s, from, now, fmt.Sprintf("SEQ_RESET %s%d->%d", label, t.last, v))
	}
}

func (e *Engine) trackMsgID(s *source, from netip.AddrPort, now time.Time, id uint64) {
	// msgid is a 32-bit counter that wraps: max -> 0 is continuity, not a reset.
	if s.msgid.have && s.msgid.last == 1<<32-1 && id == 0 {
		s.msgid.last = 0
		return
	}
	e.trackSeq(s, from, now, "msgid ", &s.msgid, id)
}

func (e *Engine) flushExpired(now time.Time) {
	for _, x := range e.frags.Expire(now) {
		// x.Src is "ip:port" of the sender; find the owning source again.
		ap, err := netip.ParseAddrPort(x.Src)
		if err != nil {
			continue
		}
		s := e.match(ap)
		if s == nil {
			continue
		}
		e.write(s, ap, now, fmt.Sprintf("FRAG_TIMEOUT seq=%d have=%d/%d %s", x.Rec.Seq, x.Have, x.Total, x.Rec.Text))
		e.message(s, ap, now, x.Rec)
		e.syncProblem(s)
	}
}

// Tick expires stale fragments and closes alert windows. Call about once a second.
func (e *Engine) Tick(now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.flushExpired(now)
	for _, s := range e.srcs {
		for _, a := range s.tracker.Tick(now) {
			e.logf("ALERT SUMMARY source=%s rule=%s count=%d", s.cfg.Name, a.Rule, a.Count)
			e.notify.Alert(s.cfg.Name, a)
		}
		e.syncProblem(s)
	}
}

// Snapshot copies the counters (sources in configuration order is not
// guaranteed; callers key by name).
func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := Snapshot{Dropped: e.dropped.Load()}
	for _, s := range e.srcs {
		out.Sources = append(out.Sources, s.stats)
	}
	return out
}

// Dropped is the global count of datagrams from unlisted sources.
func (e *Engine) Dropped() uint64 { return e.dropped.Load() }
