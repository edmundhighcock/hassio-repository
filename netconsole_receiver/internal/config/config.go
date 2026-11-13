// Package config reads /data/options.json and applies defaults. Every key
// except the two lists is optional, so a schema change in a later version
// can never stop an installed add-on from starting: an absent or
// out-of-range key silently takes its default (out-of-range is logged).
package config

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"time"

	"netconsole_receiver/internal/alert"
)

// Defaults.
const (
	// ListenPort is fixed: the add-on publishes container port 26666/udp and
	// users change the HOST port in the Network tab. An old options.json that
	// still carries listen_port is ignored (unknown keys are skipped).
	ListenPort          = 26666
	DefaultLogDir       = "/config" // the add-on's own addon_config mount
	DefaultLogMaxMB     = 5
	DefaultLogFiles     = 5
	DefaultSilenceMin   = 15
	DefaultMemLimitMiB  = 24
	DefaultCooldownMin  = 15
	DefaultMQTTPort     = 1883
	DefaultLogLevel     = "info"
	FragmentTimeout     = 5 * time.Second
	CounterPublishEvery = 30 * time.Second
)

type SourceOpt struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type RuleOpt struct {
	Name        string `json:"name"`
	Regex       string `json:"regex"`
	CooldownMin int    `json:"cooldown_min"`
}

// Options mirrors config.yaml. Scalars use zero for "absent".
type Options struct {
	Sources       []SourceOpt `json:"sources"`
	LogDir        string      `json:"log_dir"`
	LogMaxMB      int         `json:"log_max_mb"`
	LogFiles      int         `json:"log_files"`
	AlertRules    []RuleOpt   `json:"alert_rules"` // nil = absent = defaults; [] = no rules
	SilenceMin    int         `json:"silence_after_min"`
	LogLevel      string      `json:"log_level"`
	GoMemLimitMiB int         `json:"go_memlimit_mib"`
	MQTTHost      string      `json:"mqtt_host"`
	MQTTPort      int         `json:"mqtt_port"`
	MQTTUsername  string      `json:"mqtt_username"`
	MQTTPassword  string      `json:"mqtt_password"`
}

// Source is a validated allow-list entry. Port 0 matches any source port.
type Source struct {
	Name string
	Addr netip.Addr
	Port uint16
}

// Resolved is the validated configuration. Warnings are problems that were
// worked around (bad entries skipped, values defaulted); log them loudly.
type Resolved struct {
	Options
	Srcs     []Source
	Rules    []alert.Rule
	Warnings []string
}

// DefaultRules are the generic kernel defaults used when alert_rules is absent.
func DefaultRules() []RuleOpt {
	return []RuleOpt{
		{Name: "hard_lockup", Regex: `hard LOCKUP`, CooldownMin: 1},
		{Name: "kernel_panic", Regex: `Kernel panic`, CooldownMin: 1},
		{Name: "ata_error", Regex: `ata[0-9.]+: .*(timeout|reset|failed command)`, CooldownMin: 15},
		{Name: "btrfs_error", Regex: `BTRFS (error|warning|critical)`, CooldownMin: 15},
	}
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,40}$`)

// Load reads path; a missing or unparsable file yields all defaults (and a
// warning), never an error that would stop the add-on.
func Load(path string) Resolved {
	var o Options
	var warn []string
	b, err := os.ReadFile(path)
	if err != nil {
		warn = append(warn, fmt.Sprintf("could not read %s (%v); using defaults", path, err))
	} else if err := json.Unmarshal(b, &o); err != nil {
		warn = append(warn, fmt.Sprintf("could not parse %s (%v); using defaults", path, err))
		o = Options{}
	}
	r := Resolve(o)
	r.Warnings = append(warn, r.Warnings...)
	return r
}

// Resolve applies defaults and validates o.
func Resolve(o Options) Resolved {
	r := Resolved{Options: o}
	warn := func(f string, a ...any) { r.Warnings = append(r.Warnings, fmt.Sprintf(f, a...)) }

	if r.LogDir == "" {
		r.LogDir = DefaultLogDir
	}
	if r.LogMaxMB < 1 {
		r.LogMaxMB = DefaultLogMaxMB
	}
	if r.LogFiles < 1 {
		r.LogFiles = DefaultLogFiles
	}
	if r.SilenceMin < 1 {
		r.SilenceMin = DefaultSilenceMin
	}
	if r.GoMemLimitMiB < 8 {
		r.GoMemLimitMiB = DefaultMemLimitMiB
	}
	if r.MQTTPort == 0 {
		r.MQTTPort = DefaultMQTTPort
	}
	switch r.LogLevel {
	case "debug", "info", "warning", "error":
	default:
		r.LogLevel = DefaultLogLevel
	}

	seen := map[string]bool{}
	for _, s := range o.Sources {
		if !slugRe.MatchString(s.Name) {
			warn("source %q skipped: name must be a lowercase slug (a-z, 0-9, _)", s.Name)
			continue
		}
		if seen[s.Name] {
			warn("source %q skipped: duplicate name", s.Name)
			continue
		}
		a, err := netip.ParseAddr(s.Address)
		if err != nil {
			warn("source %q skipped: bad address %q", s.Name, s.Address)
			continue
		}
		if s.Port < 0 || s.Port > 65535 {
			warn("source %q skipped: bad port %d", s.Name, s.Port)
			continue
		}
		seen[s.Name] = true
		r.Srcs = append(r.Srcs, Source{Name: s.Name, Addr: a.Unmap(), Port: uint16(s.Port)})
	}

	rules := o.AlertRules
	if rules == nil {
		rules = DefaultRules()
	}
	rseen := map[string]bool{}
	for _, ru := range rules {
		if !slugRe.MatchString(ru.Name) {
			warn("alert rule %q skipped: name must be a lowercase slug", ru.Name)
			continue
		}
		if rseen[ru.Name] {
			warn("alert rule %q skipped: duplicate name", ru.Name)
			continue
		}
		re, err := regexp.Compile(ru.Regex)
		if err != nil || ru.Regex == "" {
			warn("alert rule %q skipped: bad regex %q (%v)", ru.Name, ru.Regex, err)
			continue
		}
		cd := ru.CooldownMin
		if cd < 1 {
			cd = DefaultCooldownMin
		}
		rseen[ru.Name] = true
		r.Rules = append(r.Rules, alert.Rule{Name: ru.Name, Re: re, Cooldown: time.Duration(cd) * time.Minute})
	}
	return r
}
