package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Events we subscribe to. Short list on purpose: every event costs one process launch.
var Events = []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop", "SessionEnd"}

// SettingsPath is the user-level Claude Code settings file.
func SettingsPath() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "settings.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".claude", "settings.json")
	}
	return filepath.Join(home, ".claude", "settings.json")
}

// Command builds the hook command line. Forward slashes avoid backslash escaping problems in
// whichever shell Claude Code uses to run hooks.
func Command(exe, event string) string {
	return fmt.Sprintf(`"%s" hook %s`, strings.ReplaceAll(exe, `\`, "/"), event)
}

func isOurs(cmd string) bool {
	c := strings.ToLower(cmd)
	return strings.Contains(c, "agentawake") && strings.Contains(c, " hook ")
}

func load(path string) (map[string]any, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return map[string]any{}, true, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, true, fmt.Errorf("%s is not valid JSON, refusing to touch it: %w", path, err)
	}
	return m, true, nil
}

func hookEntries(m map[string]any, create bool) (map[string]any, error) {
	raw, ok := m["hooks"]
	if !ok {
		if !create {
			return nil, nil
		}
		h := map[string]any{}
		m["hooks"] = h
		return h, nil
	}
	h, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New(`"hooks" in settings.json has an unexpected shape, refusing to touch it`)
	}
	return h, nil
}

func entryHasOurs(entry any) bool {
	e, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	hs, _ := e["hooks"].([]any)
	for _, h := range hs {
		if hm, ok := h.(map[string]any); ok {
			if c, _ := hm["command"].(string); isOurs(c) {
				return true
			}
		}
	}
	return false
}

func save(path string, m map[string]any, existed bool) (backup string, err error) {
	if existed {
		backup = path + ".agentawake-backup-" + time.Now().Format("20060102-150405")
		old, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(backup, old, 0o600); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return backup, err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return backup, err
	}
	tmp := path + ".agentawake-tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return backup, err
	}
	return backup, os.Rename(tmp, path)
}

// Install adds our hooks next to whatever is already there. Idempotent. Backs up first.
// Note: JSON key order in settings.json is normalised (alphabetical) when we rewrite it.
func Install(path, exe string) (changed bool, backup string, err error) {
	m, existed, err := load(path)
	if err != nil {
		return false, "", err
	}
	hooks, err := hookEntries(m, true)
	if err != nil {
		return false, "", err
	}
	for _, ev := range Events {
		list, _ := hooks[ev].([]any)
		if raw, present := hooks[ev]; present && list == nil {
			if _, isList := raw.([]any); !isList {
				return false, "", fmt.Errorf("hooks.%s has an unexpected shape, refusing to touch it", ev)
			}
		}
		found := false
		for _, e := range list {
			if entryHasOurs(e) {
				found = true
				break
			}
		}
		if found {
			continue
		}
		entry := map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": Command(exe, ev), "timeout": 5}},
		}
		if ev == "PreToolUse" || ev == "PostToolUse" {
			entry["matcher"] = "*"
		}
		hooks[ev] = append(list, entry)
		changed = true
	}
	if !changed {
		return false, "", nil
	}
	backup, err = save(path, m, existed)
	return changed, backup, err
}

// Uninstall removes only entries we added.
func Uninstall(path string) (changed bool, backup string, err error) {
	m, existed, err := load(path)
	if err != nil || !existed {
		return false, "", err
	}
	hooks, err := hookEntries(m, false)
	if err != nil || hooks == nil {
		return false, "", err
	}
	for ev, raw := range hooks {
		list, ok := raw.([]any)
		if !ok {
			continue
		}
		var keep []any
		for _, e := range list {
			if entryHasOurs(e) {
				changed = true
				continue
			}
			keep = append(keep, e)
		}
		if len(keep) == 0 {
			delete(hooks, ev)
		} else {
			hooks[ev] = keep
		}
	}
	if len(hooks) == 0 {
		delete(m, "hooks")
	}
	if !changed {
		return false, "", nil
	}
	backup, err = save(path, m, existed)
	return changed, backup, err
}

// Installed reports which of our events are present.
func Installed(path string) (present, missing []string, err error) {
	m, _, err := load(path)
	if err != nil {
		return nil, nil, err
	}
	hooks, err := hookEntries(m, false)
	if err != nil {
		return nil, nil, err
	}
	for _, ev := range Events {
		found := false
		if hooks != nil {
			list, _ := hooks[ev].([]any)
			for _, e := range list {
				if entryHasOurs(e) {
					found = true
				}
			}
		}
		if found {
			present = append(present, ev)
		} else {
			missing = append(missing, ev)
		}
	}
	return present, missing, nil
}
