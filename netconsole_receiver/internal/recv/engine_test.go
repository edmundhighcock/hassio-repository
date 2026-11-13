package recv

import (
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"netconsole_receiver/internal/alert"
	"netconsole_receiver/internal/config"
)

type memSink struct {
	mu    sync.Mutex
	lines []string
}

func (m *memSink) WriteLine(l string) error {
	m.mu.Lock()
	m.lines = append(m.lines, l)
	m.mu.Unlock()
	return nil
}
func (m *memSink) joined() string { return strings.Join(m.lines, "\n") }

type note struct {
	alerts   []alert.Alert
	problems []bool
}

func (n *note) Alert(_ string, a alert.Alert) { n.alerts = append(n.alerts, a) }
func (n *note) Problem(_ string, on bool)     { n.problems = append(n.problems, on) }

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 123_000_000, time.UTC)

func ap(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }

func newEngine(t *testing.T, srcs []config.Source) (*Engine, map[string]*memSink, *note) {
	t.Helper()
	sinks := map[string]*memSink{}
	n := &note{}
	c := config.Resolved{Srcs: srcs}
	c.Rules = []alert.Rule{{Name: "panic", Re: regexp.MustCompile(`Kernel panic`), Cooldown: time.Minute}}
	e := New(c, func(name string) Sink { s := &memSink{}; sinks[name] = s; return s }, n, func(string, ...any) {})
	return e, sinks, n
}

func one() []config.Source {
	return []config.Source{{Name: "box", Addr: netip.MustParseAddr("10.0.0.2")}}
}

func TestAllowListDropsAndCounts(t *testing.T) {
	e, sinks, _ := newEngine(t, []config.Source{
		{Name: "any_port", Addr: netip.MustParseAddr("10.0.0.2")},
		{Name: "pinned", Addr: netip.MustParseAddr("10.0.0.3"), Port: 6665},
	})
	e.Handle(ap("10.0.0.2:1111"), []byte("hello"), t0)
	e.Handle(ap("10.0.0.2:2222"), []byte("hello2"), t0)
	e.Handle(ap("10.0.0.3:6665"), []byte("pinned ok"), t0)
	e.Handle(ap("10.0.0.3:6666"), []byte("wrong port"), t0)
	e.Handle(ap("10.0.0.9:1"), []byte("stranger"), t0)
	e.Handle(ap("[::ffff:10.0.0.2]:1"), []byte("v4-mapped"), t0)
	if e.Dropped() != 2 {
		t.Fatalf("dropped %d, want 2", e.Dropped())
	}
	if len(sinks["any_port"].lines) != 3 || len(sinks["pinned"].lines) != 1 {
		t.Fatalf("%v %v", sinks["any_port"].lines, sinks["pinned"].lines)
	}
	for _, s := range sinks {
		if strings.Contains(s.joined(), "stranger") || strings.Contains(s.joined(), "wrong port") {
			t.Fatal("dropped datagram was logged")
		}
	}
	snap := e.Snapshot()
	if snap.Dropped != 2 {
		t.Fatal("snapshot dropped")
	}
}

func TestEmptySourceListAcceptsNothing(t *testing.T) {
	e, _, _ := newEngine(t, nil)
	e.Handle(ap("10.0.0.2:1"), []byte("x"), t0)
	if e.Dropped() != 1 || len(e.Snapshot().Sources) != 0 {
		t.Fatal("empty list must drop everything")
	}
}

func TestLogLineFormatAndEscaping(t *testing.T) {
	e, sinks, _ := newEngine(t, one())
	e.Handle(ap("10.0.0.2:6665"), []byte("6,1,2,-;line one\n foo=bar\x01"), t0)
	got := sinks["box"].lines[0]
	want := `2026-11-13T12:00:00.123Z 10.0.0.2:6665 6,1,2,-;line one\n foo=bar\x01`
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
	if len(sinks["box"].lines) != 1 {
		t.Fatal("one datagram must be exactly one line")
	}
}

func TestPlainLineStoredAsIsNoSequenceTracking(t *testing.T) {
	e, sinks, _ := newEngine(t, one())
	e.Handle(ap("10.0.0.2:1"), []byte("plain one\n"), t0)
	e.Handle(ap("10.0.0.2:1"), []byte("plain two\n"), t0)
	if len(sinks["box"].lines) != 2 || !strings.HasSuffix(sinks["box"].lines[0], " 10.0.0.2:1 plain one") {
		t.Fatalf("%v", sinks["box"].lines)
	}
	if e.Snapshot().Sources[0].Gaps != 0 {
		t.Fatal("plain lines have no sequence")
	}
}

func TestGapAndResetDetection(t *testing.T) {
	e, sinks, _ := newEngine(t, one())
	h := func(seq string) { e.Handle(ap("10.0.0.2:1"), []byte("6,"+seq+",5,-;m"), t0) }
	h("10")
	h("11") // contiguous
	h("11") // same seq (fragment/duplicate): nothing
	h("15") // GAP 12..14
	h("3")  // decrease: reset
	h("4")
	j := sinks["box"].joined()
	if !strings.Contains(j, " GAP 12..14") {
		t.Fatalf("no GAP 12..14:\n%s", j)
	}
	if !strings.Contains(j, " SEQ_RESET 15->3") {
		t.Fatalf("no SEQ_RESET:\n%s", j)
	}
	if strings.Count(j, "GAP") != 1 || strings.Count(j, "SEQ_RESET") != 1 {
		t.Fatalf("spurious markers:\n%s", j)
	}
	if g := e.Snapshot().Sources[0].Gaps; g != 1 {
		t.Fatalf("gaps %d", g)
	}
}

func TestMsgIDTrackedLikeSeq(t *testing.T) {
	e, sinks, _ := newEngine(t, one())
	h := func(seq, id string) {
		e.Handle(ap("10.0.0.2:1"), []byte("6,"+seq+",5,-;m\n msgid="+id), t0)
	}
	h("1", "1")
	h("2", "2")
	h("3", "5") // msgid gap, seq contiguous
	h("4", "4294967295")
	h("5", "0") // 32-bit wrap is continuity
	h("6", "1")
	h("7", "0") // real decrease
	j := sinks["box"].joined()
	if !strings.Contains(j, "GAP msgid 3..4") && !strings.Contains(j, "GAP msgid 6..4294967294") {
		t.Fatalf("msgid gap missing:\n%s", j)
	}
	if !strings.Contains(j, "SEQ_RESET msgid 1->0") {
		t.Fatalf("msgid reset missing:\n%s", j)
	}
	if strings.Contains(j, "SEQ_RESET msgid 4294967295->0") {
		t.Fatalf("wrap treated as reset:\n%s", j)
	}
}

func TestFragmentsReassembledAndAlerted(t *testing.T) {
	e, sinks, n := newEngine(t, one())
	e.Handle(ap("10.0.0.2:1"), []byte("0,9,100,-,ncfrag=0/26;Kernel pa"), t0)
	if len(n.alerts) != 0 {
		t.Fatal("alert on half a message")
	}
	e.Handle(ap("10.0.0.2:1"), []byte("0,9,100,-,ncfrag=9/26;nic - not syncing"), t0)
	j := sinks["box"].joined()
	// raw fragments kept, plus the joined message
	if !strings.Contains(j, "ncfrag=0/26;Kernel pa") || !strings.Contains(j, "ncfrag=9/26;nic") {
		t.Fatalf("raw fragments missing:\n%s", j)
	}
	if !strings.Contains(j, "REASSEMBLED seq=9 Kernel panic - not syncing") {
		t.Fatalf("no reassembled line:\n%s", j)
	}
	if len(n.alerts) != 1 || n.alerts[0].Rule != "panic" {
		t.Fatalf("alerts %+v", n.alerts)
	}
}

func TestFragmentTimeoutLogsAndStillMatches(t *testing.T) {
	e, sinks, n := newEngine(t, one())
	e.Handle(ap("10.0.0.2:1"), []byte("0,9,100,-,ncfrag=0/40;Kernel panic start"), t0)
	e.Tick(t0.Add(config.FragmentTimeout - time.Second))
	if strings.Contains(sinks["box"].joined(), "FRAG_TIMEOUT") {
		t.Fatal("timed out early")
	}
	e.Tick(t0.Add(config.FragmentTimeout))
	j := sinks["box"].joined()
	if !strings.Contains(j, "FRAG_TIMEOUT seq=9 have=18/40 Kernel panic start") {
		t.Fatalf("no timeout line:\n%s", j)
	}
	if len(n.alerts) != 1 {
		t.Fatalf("a partial message should still be matched: %+v", n.alerts)
	}
	e.Tick(t0.Add(time.Hour))
	if strings.Count(sinks["box"].joined(), "FRAG_TIMEOUT") != 1 {
		t.Fatal("timeout reported twice")
	}
}

func TestProblemFollowsCooldownWindow(t *testing.T) {
	e, _, n := newEngine(t, one())
	e.Handle(ap("10.0.0.2:1"), []byte("0,1,1,-;Kernel panic"), t0)
	e.Handle(ap("10.0.0.2:1"), []byte("0,2,2,-;Kernel panic"), t0.Add(10*time.Second))
	if len(n.alerts) != 1 || len(n.problems) != 1 || !n.problems[0] {
		t.Fatalf("after first: %+v %+v", n.alerts, n.problems)
	}
	e.Tick(t0.Add(30 * time.Second))
	if len(n.alerts) != 1 {
		t.Fatal("summary before the window closed")
	}
	e.Tick(t0.Add(70 * time.Second))
	if len(n.alerts) != 2 || n.alerts[1].Kind != alert.KindSummary || n.alerts[1].Count != 2 {
		t.Fatalf("summary: %+v", n.alerts)
	}
	if len(n.problems) != 2 || n.problems[1] {
		t.Fatalf("problem should be off: %+v", n.problems)
	}
}

func TestCountersAndLastMessage(t *testing.T) {
	e, _, _ := newEngine(t, one())
	e.Handle(ap("10.0.0.2:1"), []byte("a"), t0)
	e.Handle(ap("10.0.0.2:1"), []byte("b"), t0.Add(time.Second))
	s := e.Snapshot().Sources[0]
	if s.Messages != 2 || !s.LastMessage.Equal(t0.Add(time.Second)) || s.Name != "box" {
		t.Fatalf("%+v", s)
	}
}
