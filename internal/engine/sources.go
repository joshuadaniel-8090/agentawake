package engine

import (
	"fmt"
	"time"

	"agentawake/internal/state"
)

// HookSource: sessions reported busy by agent hooks (Claude Code). Most accurate signal.
// A session counts only while its heartbeat is fresh AND (if known) its agent process is alive,
// so a killed agent whose Stop hook never fired cannot hold the machine awake.
type HookSource struct {
	Store    *state.Store
	TTL      time.Duration // heartbeat expiry while the model is thinking (events arrive often)
	ToolTTL  time.Duration // longer expiry while one tool call runs (a build or test run can take long)
	PidAlive func(pid int32) bool
}

func (h HookSource) Active(now time.Time) []string {
	var out []string
	for _, s := range h.Store.Sessions() {
		ttl := h.TTL
		if s.Tool && h.ToolTTL > ttl {
			ttl = h.ToolTTL
		}
		if !s.Busy || now.Sub(time.Unix(s.Updated, 0)) > ttl {
			continue
		}
		if s.PID != 0 && h.PidAlive != nil && !h.PidAlive(s.PID) {
			continue
		}
		out = append(out, fmt.Sprintf("%s session %s (hooks)", s.Agent, short(s.ID)))
	}
	return out
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// ManualSource: `agentawake hold 2h`.
type ManualSource struct{ Store *state.Store }

func (m ManualSource) Active(now time.Time) []string {
	until, ok := m.Store.Hold()
	if !ok {
		return nil
	}
	if !now.Before(until) {
		m.Store.ClearHold()
		return nil
	}
	return []string{"manual hold until " + until.Local().Format("15:04")}
}

// ScanSource adapts any "who is active" scanner (the CPU-based process scanner).
type ScanSource struct {
	Scan func(now time.Time) []string
}

func (p ScanSource) Active(now time.Time) []string {
	var out []string
	for _, n := range p.Scan(now) {
		out = append(out, n+" (cpu activity)")
	}
	return out
}
