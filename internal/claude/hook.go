// Package claude connects agentawake to Claude Code: the hook handler Claude Code runs on
// every event, and the installer that adds those hooks to ~/.claude/settings.json.
package claude

import (
	"encoding/json"
	"io"
	"strings"
	"time"

	"agentawake/internal/detect"
	"agentawake/internal/state"
)

type hookInput struct {
	SessionID string `json:"session_id"`
	Event     string `json:"hook_event_name"`
}

// Handle records one hook event. It must be fast and must never fail the agent, so the caller
// ignores its error and always exits 0.
//
//	UserPromptSubmit, PreToolUse, PostToolUse -> session is busy (refreshes the heartbeat);
//	                                             PreToolUse also marks a tool call in flight
//	Stop                                      -> session is idle
//	SessionEnd                                -> session removed
func Handle(store *state.Store, eventArg string, stdin io.Reader, now time.Time, findPID func() int32) error {
	var in hookInput
	if stdin != nil {
		if b, err := io.ReadAll(io.LimitReader(stdin, 1<<20)); err == nil {
			_ = json.Unmarshal(b, &in) // bad or empty JSON is fine, fall back to defaults
		}
	}
	event := eventArg
	if event == "" {
		event = in.Event
	}
	id := in.SessionID
	if id == "" {
		id = "default"
	}

	switch event {
	case "UserPromptSubmit", "PreToolUse", "PostToolUse":
		s, ok := store.GetSession(id)
		if !ok || s.PID == 0 {
			if findPID != nil {
				s.PID = findPID()
			}
		}
		s.ID, s.Agent, s.Busy, s.Updated = id, "claude-code", true, now.Unix()
		s.Tool = event == "PreToolUse" // cleared by the next PostToolUse or prompt
		return store.PutSession(s)
	case "Stop":
		s, ok := store.GetSession(id)
		if !ok {
			return nil
		}
		s.Busy, s.Tool, s.Updated = false, false, now.Unix()
		return store.PutSession(s)
	case "SessionEnd":
		store.DeleteSession(id)
	}
	return nil
}

// FindAgentPID walks up from this process to the Claude Code process so the daemon can tell if
// it died without firing Stop. Returns 0 if it cannot tell, in which case only the TTL applies.
// (The direct parent is usually a short-lived shell, so it is deliberately not used as a fallback.)
func FindAgentPID(l detect.Lister, self int32) int32 {
	for _, p := range detect.Ancestors(l, self) {
		n := strings.TrimSuffix(strings.ToLower(p.Name), ".exe")
		if n == "claude" || n == "claude-code" {
			return p.PID
		}
		if n == "node" || n == "bun" {
			c := strings.ToLower(l.Cmdline(p.PID))
			if strings.Contains(c, "claude-code") || strings.Contains(c, "claude") {
				return p.PID
			}
		}
	}
	return 0
}
