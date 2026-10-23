package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentawake/internal/state"
)

const userSettings = `{
  "model": "opus",
  "permissions": {"allow": ["Bash(ls)"]},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "my-linter"}]}
    ],
    "Notification": [
      {"hooks": [{"type": "command", "command": "notify-send hi"}]}
    ]
  }
}`

func writeSettings(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "settings.json")
	if body != "" {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func readMap(t *testing.T, p string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("result is not valid JSON: %v\n%s", err, b)
	}
	return m
}

func TestInstall_PreservesUserSettingsAndHooks(t *testing.T) {
	p := writeSettings(t, userSettings)
	changed, backup, err := Install(p, `C:\Tools\agentawake.exe`)
	if err != nil || !changed {
		t.Fatalf("install: changed=%v err=%v", changed, err)
	}
	if backup == "" {
		t.Fatal("must back up the existing file")
	}
	if b, _ := os.ReadFile(backup); string(b) != userSettings {
		t.Fatal("backup is not a byte-for-byte copy")
	}
	m := readMap(t, p)
	if m["model"] != "opus" || m["permissions"] == nil {
		t.Fatalf("user settings lost: %v", m)
	}
	hooks := m["hooks"].(map[string]any)
	pre := hooks["PreToolUse"].([]any)
	if len(pre) != 2 {
		t.Fatalf("user's PreToolUse hook must stay, ours appended: %v", pre)
	}
	if hooks["Notification"] == nil {
		t.Fatal("unrelated hook event lost")
	}
	for _, ev := range Events {
		if hooks[ev] == nil {
			t.Fatalf("missing event %s", ev)
		}
	}
	raw, _ := json.Marshal(hooks["Stop"])
	if !strings.Contains(string(raw), `C:/Tools/agentawake.exe\" hook Stop`) {
		t.Fatalf("command should use forward slashes: %s", raw)
	}
}

func TestInstall_Idempotent(t *testing.T) {
	p := writeSettings(t, userSettings)
	if _, _, err := Install(p, "agentawake.exe"); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(p)
	changed, backup, err := Install(p, "agentawake.exe")
	if err != nil || changed || backup != "" {
		t.Fatalf("second install must do nothing: changed=%v backup=%q err=%v", changed, backup, err)
	}
	second, _ := os.ReadFile(p)
	if string(first) != string(second) {
		t.Fatal("file changed on second install")
	}
}

func TestInstall_CreatesMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "newdir", "settings.json")
	changed, backup, err := Install(p, "agentawake.exe")
	if err != nil || !changed || backup != "" {
		t.Fatalf("changed=%v backup=%q err=%v", changed, backup, err)
	}
	present, missing, _ := Installed(p)
	if len(present) != len(Events) || len(missing) != 0 {
		t.Fatalf("present=%v missing=%v", present, missing)
	}
}

func TestInstall_RefusesBrokenJSON(t *testing.T) {
	p := writeSettings(t, `{ "hooks": {oops`)
	if _, _, err := Install(p, "agentawake.exe"); err == nil {
		t.Fatal("must refuse to touch invalid JSON")
	}
	if b, _ := os.ReadFile(p); string(b) != `{ "hooks": {oops` {
		t.Fatal("invalid file was modified")
	}
}

func TestInstall_RefusesUnexpectedHooksShape(t *testing.T) {
	p := writeSettings(t, `{"hooks": []}`)
	if _, _, err := Install(p, "agentawake.exe"); err == nil {
		t.Fatal("must refuse a hooks value it does not understand")
	}
}

func TestUninstall_RemovesOnlyOurs(t *testing.T) {
	p := writeSettings(t, userSettings)
	_, _, _ = Install(p, "agentawake.exe")
	changed, _, err := Uninstall(p)
	if err != nil || !changed {
		t.Fatalf("uninstall: %v %v", changed, err)
	}
	m := readMap(t, p)
	hooks := m["hooks"].(map[string]any)
	pre := hooks["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("user's hook must remain: %v", pre)
	}
	for _, ev := range []string{"UserPromptSubmit", "PostToolUse", "Stop", "SessionEnd"} {
		if _, ok := hooks[ev]; ok {
			t.Fatalf("%s should be gone", ev)
		}
	}
	if hooks["Notification"] == nil || m["model"] != "opus" {
		t.Fatal("user content lost")
	}
}

func TestUninstall_NoOpWhenAbsent(t *testing.T) {
	p := writeSettings(t, userSettings)
	changed, _, err := Uninstall(p)
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
}

// ---- hook handler --------------------------------------------------------

func TestHandle_BusyIdleEnd(t *testing.T) {
	store, _ := state.Open(t.TempDir())
	now := time.Now()
	pid := int32(0)
	find := func() int32 { pid++; return 4242 }
	in := func(id string) *strings.Reader {
		return strings.NewReader(`{"session_id":"` + id + `","hook_event_name":"X"}`)
	}

	if err := Handle(store, "UserPromptSubmit", in("s1"), now, find); err != nil {
		t.Fatal(err)
	}
	s, ok := store.GetSession("s1")
	if !ok || !s.Busy || s.PID != 4242 || s.Agent != "claude-code" {
		t.Fatalf("busy session wrong: %+v", s)
	}
	_ = Handle(store, "PreToolUse", in("s1"), now.Add(time.Second), find)
	if s, _ = store.GetSession("s1"); !s.Tool {
		t.Fatal("PreToolUse must mark a tool call in flight")
	}
	_ = Handle(store, "PostToolUse", in("s1"), now.Add(time.Second), find)
	if s, _ = store.GetSession("s1"); s.Tool || !s.Busy {
		t.Fatal("PostToolUse clears the tool flag but stays busy")
	}
	if pid != 1 {
		t.Fatalf("agent pid lookup is expensive: must happen once per session, ran %d times", pid)
	}
	_ = Handle(store, "Stop", in("s1"), now.Add(2*time.Second), find)
	if s, _ = store.GetSession("s1"); s.Busy {
		t.Fatal("Stop must mark idle")
	}
	_ = Handle(store, "SessionEnd", in("s1"), now, find)
	if _, ok := store.GetSession("s1"); ok {
		t.Fatal("SessionEnd must remove the session")
	}
}

func TestHandle_GarbageInputNeverBreaks(t *testing.T) {
	store, _ := state.Open(t.TempDir())
	for _, body := range []string{"", "not json", "{", `{"session_id": 5}`} {
		if err := Handle(store, "PreToolUse", strings.NewReader(body), time.Now(), nil); err != nil {
			// a session write may legitimately fail; the contract is "never panic"
			t.Logf("body %q: %v", body, err)
		}
	}
	if err := Handle(store, "PreToolUse", nil, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestHandle_StopWithoutSessionIsFine(t *testing.T) {
	store, _ := state.Open(t.TempDir())
	if err := Handle(store, "Stop", strings.NewReader(`{"session_id":"ghost"}`), time.Now(), nil); err != nil {
		t.Fatal(err)
	}
}
