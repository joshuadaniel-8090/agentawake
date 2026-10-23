// Package config loads the optional JSON config file. Every field has a safe default,
// so the tool works with no config at all.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"agentawake/internal/detect"
)

type Config struct {
	PollSeconds         int                `json:"poll_seconds"`          // decision loop interval
	GraceSeconds        int                `json:"grace_seconds"`         // wait before sleeping when lid is closed at finish
	MaxHoldHours        float64            `json:"max_hold_hours"`        // hard cap on one continuous hold
	BatteryFloorPercent int                `json:"battery_floor_percent"` // on battery below this: stop holding
	LidGuard            bool               `json:"lid_guard"`             // opt-in: keep running with lid closed
	SessionTTLSeconds   int                `json:"session_ttl_seconds"`   // hook heartbeat expiry while Claude is thinking
	ToolTTLSeconds      int                `json:"tool_ttl_seconds"`      // hook heartbeat expiry while one tool call runs
	ProcessDetection    bool               `json:"process_detection"`     // CPU-based detection for non-hook agents
	CPUThresholdPercent float64            `json:"cpu_threshold_percent"` // % of one core that counts as working
	CPULingerSeconds    int                `json:"cpu_linger_seconds"`    // keep "working" this long after last CPU burst
	Agents              []detect.Signature `json:"agents"`                // extra or overriding agent signatures
}

func Defaults() Config {
	return Config{
		PollSeconds:         3,
		GraceSeconds:        30,
		MaxHoldHours:        8,
		BatteryFloorPercent: 20,
		LidGuard:            false,
		SessionTTLSeconds:   600,
		ToolTTLSeconds:      3600,
		ProcessDetection:    true,
		CPUThresholdPercent: 3,
		CPULingerSeconds:    20,
	}
}

// Path returns the config file location (%APPDATA%\agentawake\config.json on Windows).
func Path() string {
	if p := os.Getenv("AGENTAWAKE_CONFIG"); p != "" {
		return p
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "agentawake", "config.json")
	}
	return "config.json"
}

// Load reads the config over the defaults. A missing file is fine. A broken file is an error.
func Load() (Config, error) {
	c := Defaults()
	b, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Defaults(), err
	}
	c.clamp()
	return c, nil
}

func (c *Config) clamp() {
	d := Defaults()
	if c.PollSeconds < 1 {
		c.PollSeconds = d.PollSeconds
	}
	if c.GraceSeconds < 0 {
		c.GraceSeconds = 0
	}
	if c.MaxHoldHours <= 0 {
		c.MaxHoldHours = d.MaxHoldHours
	}
	if c.BatteryFloorPercent < 0 || c.BatteryFloorPercent > 100 {
		c.BatteryFloorPercent = d.BatteryFloorPercent
	}
	if c.SessionTTLSeconds < 30 {
		c.SessionTTLSeconds = d.SessionTTLSeconds
	}
	if c.ToolTTLSeconds < c.SessionTTLSeconds {
		c.ToolTTLSeconds = c.SessionTTLSeconds
	}
	if c.CPUThresholdPercent <= 0 {
		c.CPUThresholdPercent = d.CPUThresholdPercent
	}
	if c.CPULingerSeconds < 0 {
		c.CPULingerSeconds = d.CPULingerSeconds
	}
}

// WriteDefault writes a commented-by-structure default config if none exists.
func WriteDefault() (string, error) {
	p := Path()
	if _, err := os.Stat(p); err == nil {
		return p, errors.New("config already exists: " + p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return p, err
	}
	d := Defaults()
	d.Agents = []detect.Signature{}
	b, _ := json.MarshalIndent(d, "", "  ")
	return p, os.WriteFile(p, append(b, '\n'), 0o644)
}
