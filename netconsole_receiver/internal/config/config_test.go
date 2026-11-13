package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultsWhenOptionsMissing(t *testing.T) {
	r := Load(filepath.Join(t.TempDir(), "nope.json"))
	if r.ListenPort != 26666 || r.LogDir != "/config" || r.LogMaxMB != 5 || r.LogFiles != 5 ||
		r.GoMemLimitMiB != 24 || r.MQTTPort != 1883 || r.SilenceMin != 15 || r.LogLevel != "info" {
		t.Fatalf("%+v", r.Options)
	}
	if len(r.Rules) != 4 || len(r.Srcs) != 0 || len(r.Warnings) == 0 {
		t.Fatalf("rules=%d srcs=%d warn=%v", len(r.Rules), len(r.Srcs), r.Warnings)
	}
}

func TestDefaultRulesMatchWhatTheyShould(t *testing.T) {
	r := Resolve(Options{})
	want := map[string]struct {
		hit, miss string
		cd        time.Duration
	}{
		"hard_lockup":  {"Watchdog detected hard LOCKUP on cpu 2", "soft lockup", time.Minute},
		"kernel_panic": {"Kernel panic - not syncing: Fatal exception", "no panic here", time.Minute},
		"ata_error":    {"ata1.00: failed command: READ DMA", "ata1: link up", 15 * time.Minute},
		"btrfs_error":  {"BTRFS error (device sda1): bad tree block", "BTRFS info (device sda1): using crc32c", 15 * time.Minute},
	}
	for _, ru := range r.Rules {
		w, ok := want[ru.Name]
		if !ok {
			t.Fatalf("unexpected default rule %s", ru.Name)
		}
		if !ru.Re.MatchString(w.hit) || ru.Re.MatchString(w.miss) || ru.Cooldown != w.cd {
			t.Errorf("%s: hit=%v miss=%v cd=%v", ru.Name, ru.Re.MatchString(w.hit), ru.Re.MatchString(w.miss), ru.Cooldown)
		}
	}
}

func TestRegexOverrideAndEmptyList(t *testing.T) {
	r := Resolve(Options{AlertRules: []RuleOpt{{Name: "oom", Regex: `Out of memory`}}})
	if len(r.Rules) != 1 || r.Rules[0].Name != "oom" || r.Rules[0].Cooldown != 15*time.Minute {
		t.Fatalf("%+v", r.Rules)
	}
	if r := Resolve(Options{AlertRules: []RuleOpt{}}); len(r.Rules) != 0 {
		t.Fatal("explicit empty list means no rules")
	}
}

func TestBadEntriesSkippedWithWarnings(t *testing.T) {
	r := Resolve(Options{
		Sources: []SourceOpt{
			{Name: "good", Address: "192.168.1.5"},
			{Name: "Bad Name", Address: "192.168.1.6"},
			{Name: "badip", Address: "not-an-ip"},
			{Name: "good", Address: "192.168.1.7"},
			{Name: "pinned", Address: "::ffff:192.168.1.8", Port: 6665},
		},
		AlertRules: []RuleOpt{{Name: "ok", Regex: "x"}, {Name: "broken", Regex: "("}, {Name: "ok", Regex: "y"}},
	})
	if len(r.Srcs) != 2 || r.Srcs[0].Name != "good" || r.Srcs[1].Addr.String() != "192.168.1.8" || r.Srcs[1].Port != 6665 {
		t.Fatalf("%+v", r.Srcs)
	}
	if len(r.Rules) != 1 || len(r.Warnings) != 5 {
		t.Fatalf("rules=%d warnings=%v", len(r.Rules), r.Warnings)
	}
}

func TestLoadFromFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "options.json")
	os.WriteFile(p, []byte(`{"listen_port": 4444, "sources":[{"name":"a","address":"10.1.1.1"}], "log_level":"debug", "unknown_future_key": 1}`), 0o600)
	r := Load(p)
	if r.ListenPort != 4444 || len(r.Srcs) != 1 || r.LogLevel != "debug" || len(r.Warnings) != 0 {
		t.Fatalf("%+v %v", r.Options, r.Warnings)
	}
	os.WriteFile(p, []byte(`{not json`), 0o600)
	r = Load(p)
	if r.ListenPort != 26666 || !strings.Contains(strings.Join(r.Warnings, ""), "parse") {
		t.Fatalf("%v", r.Warnings)
	}
}
