package alert

import (
	"bufio"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func ataRules() []Rule {
	return []Rule{{Name: "ata_error", Re: regexp.MustCompile(`ata[0-9.]+: .*(timeout|reset|failed command)`), Cooldown: 15 * time.Minute}}
}

// obs feeds one kernel line at offset d from t0 and returns the alerts.
func obs(tr *Tracker, d time.Duration, line string) []Alert {
	return tr.Observe("6,1,1,-,caller=T1;"+line, t0.Add(d))
}

// tick runs Tick at offset d and returns the alerts.
func tick(tr *Tracker, d time.Duration) []Alert { return tr.Tick(t0.Add(d)) }

// The 16/11 sequence (shape taken from sda-health-2026-11-05.txt): resume
// marker, then both HDDs fail softreset and come back ~1-2 s later.
func TestResumeSoftresetDoesNotAlert(t *testing.T) {
	tr := NewTracker(ataRules())
	var all []Alert
	add := func(a []Alert) { all = append(all, a...) }
	add(obs(tr, 0, "PM: suspend exit"))
	add(obs(tr, 9*time.Second, "ata6: softreset failed (device not ready)"))
	add(obs(tr, 9*time.Second, "ata5: softreset failed (device not ready)"))
	add(obs(tr, 10*time.Second, "ata5: SATA link up 6.0 Gbps (SStatus 133 SControl 300)"))
	add(obs(tr, 11*time.Second, "ata6: SATA link up 6.0 Gbps (SStatus 133 SControl 300)"))
	for s := 0; s < 200; s++ {
		add(tick(tr, time.Duration(s)*time.Second))
	}
	if len(all) != 0 || tr.Open() {
		t.Fatalf("benign resume alerted: %+v", all)
	}
}

// Same, but the resume marker never reaches us (lost UDP): the link-up of
// the SAME port within 30 s still makes it benign.
func TestSoftresetFollowedByLinkUpDoesNotAlert(t *testing.T) {
	tr := NewTracker(ataRules())
	obs(tr, 0, "ata5: softreset failed (device not ready)")
	obs(tr, 6*time.Second, "ata5: SATA link up 6.0 Gbps (SStatus 133 SControl 300)")
	if a := tick(tr, 31*time.Second); len(a) != 0 || tr.Open() {
		t.Fatalf("alerted: %+v", a)
	}
}

// A link-up for a DIFFERENT port does not excuse the softreset.
func TestLinkUpOnOtherPortDoesNotExcuse(t *testing.T) {
	tr := NewTracker(ataRules())
	obs(tr, 0, "ata5: softreset failed (device not ready)")
	obs(tr, 6*time.Second, "ata6: SATA link up 6.0 Gbps")
	if a := tick(tr, 29*time.Second); len(a) != 0 {
		t.Fatalf("fired before the hold-back ended: %+v", a)
	}
	a := tick(tr, 30*time.Second)
	if len(a) != 1 || a[0].Rule != "ata_error" || a[0].Kind != KindFirst || !strings.Contains(a[0].Last, "ata5: softreset") {
		t.Fatalf("%+v", a)
	}
}

// Softreset with no link-up within 30 s and no resume marker must alert.
func TestSoftresetWithoutLinkUpOrResumeAlerts(t *testing.T) {
	tr := NewTracker(ataRules())
	if a := obs(tr, 0, "ata5: softreset failed (device not ready)"); len(a) != 0 {
		t.Fatalf("held-back match fired immediately: %+v", a)
	}
	if a := tick(tr, 29*time.Second); len(a) != 0 {
		t.Fatalf("early: %+v", a)
	}
	a := tick(tr, 31*time.Second)
	if len(a) != 1 || a[0].Kind != KindFirst || a[0].Count != 1 || !tr.Open() {
		t.Fatalf("%+v", a)
	}
	// a late link-up must not retract an alert already sent
	obs(tr, 40*time.Second, "ata5: SATA link up 6.0 Gbps")
}

// A softreset long after a resume (> 60 s) is not covered by the resume rule.
func TestSoftresetLongAfterResumeIsHeldThenAlerts(t *testing.T) {
	tr := NewTracker(ataRules())
	obs(tr, 0, "PM: suspend exit")
	obs(tr, 61*time.Second, "ata5: softreset failed (device not ready)")
	if a := tick(tr, 92*time.Second); len(a) != 1 {
		t.Fatalf("%+v", a)
	}
}

// Real ata errors (and a softreset line mixed with them) alert at once,
// never held back, even right after a resume.
func TestOtherAtaKindsAreNeverDelayed(t *testing.T) {
	for _, line := range []string{
		"ata10.00: failed command: READ FPDMA QUEUED",
		"ata10.00: cmd 60/10 tag 14 ncq dma 8192 in res 40/00 Emask 0x4 (timeout)",
		"ata10: hard resetting link",
		"ata5: softreset failed (device not ready)\nata5: hard resetting link", // not softreset-only
	} {
		tr := NewTracker(ataRules())
		obs(tr, 0, "PM: suspend exit") // even right after a resume
		a := obs(tr, time.Second, line)
		if len(a) != 1 || a[0].Kind != KindFirst {
			t.Errorf("%q: %+v", line, a)
		}
	}
}

// The real 14/11 WD stall on ata10 (fixture copied from Sys's corpus).
func TestWDStallAta10Alerts(t *testing.T) {
	f, err := os.Open("testdata/wd-stall-2026-11-14-ata10.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := NewTracker(ataRules())
	var first []Alert
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		_, payload, ok := strings.Cut(sc.Text(), ";")
		if !ok || strings.Contains(sc.Text(), " GAP ") || strings.Contains(sc.Text(), " SEQ_RESET ") {
			continue
		}
		n++
		first = append(first, tr.Observe(payload, t0.Add(time.Duration(n)*time.Millisecond))...)
	}
	if n < 100 {
		t.Fatalf("fixture too short: %d", n)
	}
	if len(first) != 1 || first[0].Rule != "ata_error" || !strings.Contains(first[0].First, "ata10.00") {
		t.Fatalf("%+v", first)
	}
	// the very first alert is immediate: no Tick needed
}
