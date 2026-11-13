package alert

import (
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func rules() []Rule {
	return []Rule{
		{Name: "hard_lockup", Re: regexp.MustCompile(`hard LOCKUP`), Cooldown: time.Minute},
		{Name: "ata_error", Re: regexp.MustCompile(`ata[0-9.]+: .*(timeout|reset|failed command)`), Cooldown: 15 * time.Minute},
	}
}

func TestFirstMatchFiresAtOnce(t *testing.T) {
	tr := NewTracker(rules())
	if tr.Open() {
		t.Fatal("open before any match")
	}
	got := tr.Observe("0,1,2,-;NMI watchdog: Watchdog detected hard LOCKUP on cpu 3", t0)
	if len(got) != 1 || got[0].Rule != "hard_lockup" || got[0].Kind != KindFirst || got[0].Count != 1 {
		t.Fatalf("%+v", got)
	}
	if !tr.Open() {
		t.Fatal("window should be open")
	}
	if len(tr.Observe("nothing to see", t0)) != 0 {
		t.Fatal("non-matching line fired")
	}
}

func TestCooldownCountsThenOneSummary(t *testing.T) {
	tr := NewTracker(rules())
	tr.Observe("hard LOCKUP one", t0)
	for i := 0; i < 4; i++ {
		if a := tr.Observe("hard LOCKUP again", t0.Add(time.Duration(i+1)*time.Second)); len(a) != 0 {
			t.Fatalf("fired inside the window: %+v", a)
		}
	}
	if a := tr.Tick(t0.Add(59 * time.Second)); len(a) != 0 || !tr.Open() {
		t.Fatalf("closed early: %+v", a)
	}
	a := tr.Tick(t0.Add(time.Minute))
	if len(a) != 1 || a[0].Kind != KindSummary || a[0].Count != 5 || a[0].First != "hard LOCKUP one" || a[0].Last != "hard LOCKUP again" {
		t.Fatalf("%+v", a)
	}
	if tr.Open() {
		t.Fatal("still open after window closed")
	}
	if a := tr.Tick(t0.Add(time.Hour)); len(a) != 0 {
		t.Fatalf("second summary: %+v", a)
	}
}

func TestNoSummaryWhenCountDidNotRise(t *testing.T) {
	tr := NewTracker(rules())
	tr.Observe("hard LOCKUP", t0)
	if a := tr.Tick(t0.Add(2 * time.Minute)); len(a) != 0 {
		t.Fatalf("summary without extra matches: %+v", a)
	}
	// and the next match opens a fresh window and fires again
	if a := tr.Observe("hard LOCKUP", t0.Add(3*time.Minute)); len(a) != 1 || a[0].Kind != KindFirst {
		t.Fatalf("%+v", a)
	}
}

func TestRulesAreIndependent(t *testing.T) {
	tr := NewTracker(rules())
	tr.Observe("hard LOCKUP", t0)
	a := tr.Observe("ata1.00: exception, status: timeout", t0.Add(time.Second))
	if len(a) != 1 || a[0].Rule != "ata_error" {
		t.Fatalf("%+v", a)
	}
	// short window closes, long one keeps the problem state on
	tr.Observe("hard LOCKUP", t0.Add(2*time.Second))
	tr.Tick(t0.Add(time.Minute + 5*time.Second))
	if !tr.Open() {
		t.Fatal("ata window still open, problem must stay on")
	}
	tr.Tick(t0.Add(16 * time.Minute))
	if tr.Open() {
		t.Fatal("all windows closed")
	}
}

func TestMatchedLineIsTheLineThatMatched(t *testing.T) {
	tr := NewTracker(rules())
	a := tr.Observe("first line\nBUG: hard LOCKUP here\n foo=bar", t0)
	if a[0].Last != "BUG: hard LOCKUP here" {
		t.Fatalf("%q", a[0].Last)
	}
}

func TestStateStaysUnder255(t *testing.T) {
	long := strings.Repeat("hard LOCKUP é中\n\x00", 200)
	for _, a := range []Alert{
		{Rule: "hard_lockup", Kind: KindFirst, Count: 1, Last: long},
		{Rule: "hard_lockup", Kind: KindSummary, Count: 12345, Last: long},
		{Rule: strings.Repeat("r", 300), Kind: KindFirst, Last: "x"},
	} {
		s := StateText(a)
		if len(s) > 255 || utf8.RuneCountInString(s) > 255 || !utf8.ValidString(s) {
			t.Errorf("state is %d bytes, valid=%v: %q", len(s), utf8.ValidString(s), s)
		}
		if strings.ContainsAny(s, "\n\x00") {
			t.Errorf("state has control chars: %q", s)
		}
	}
	s := StateText(Alert{Rule: "ata_error", Kind: KindFirst, Last: "ata1: timeout"})
	if s != "ata_error: ata1: timeout" {
		t.Fatalf("short state mangled: %q", s)
	}
	if got := Truncate("中中中中", 8); !utf8.ValidString(got) || len(got) > 8 {
		t.Fatalf("%q", got)
	}
}
