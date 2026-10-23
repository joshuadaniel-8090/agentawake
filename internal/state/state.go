// Package state is the file-based contract between the hook command, the CLI
// and the daemon. Everything is a small JSON file written atomically, so no
// sockets, no locks, and a crashed writer can never leave a half-written file.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// Store is a directory holding all agentawake runtime state.
type Store struct{ Dir string }

// DefaultDir returns the state directory (%LOCALAPPDATA%\agentawake on Windows).
func DefaultDir() string {
	if d := os.Getenv("AGENTAWAKE_HOME"); d != "" {
		return d
	}
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, "agentawake")
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "agentawake")
	}
	return filepath.Join(os.TempDir(), "agentawake")
}

// Open creates the directory layout if needed.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
}

func (s *Store) path(name string) string { return filepath.Join(s.Dir, name) }

// writeAtomic writes via temp file + rename. Windows can refuse a rename while
// a reader has the destination open, so retry briefly.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp" + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	var err error
	for i := 0; i < 5; i++ {
		if err = os.Rename(tmp, path); err == nil {
			return nil
		}
		time.Sleep(15 * time.Millisecond)
	}
	_ = os.Remove(tmp)
	return err
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(b, '\n'))
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// ---- hook sessions -------------------------------------------------------

// Session is one agent session reported by hooks.
type Session struct {
	ID      string `json:"id"`
	Agent   string `json:"agent"`
	PID     int32  `json:"pid"` // 0 = unknown, rely on TTL only
	Busy    bool   `json:"busy"`
	Tool    bool   `json:"tool"`    // a tool call is in flight (can legitimately run for a long time)
	Updated int64  `json:"updated"` // unix seconds, heartbeat
}

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func (s *Store) sessionPath(id string) string {
	clean := unsafeID.ReplaceAllString(id, "_")
	if len(clean) > 64 {
		clean = clean[:64]
	}
	if clean == "" {
		clean = "default"
	}
	return filepath.Join(s.Dir, "sessions", clean+".json")
}

func (s *Store) PutSession(x Session) error { return writeJSON(s.sessionPath(x.ID), x) }

func (s *Store) GetSession(id string) (Session, bool) {
	var x Session
	if err := readJSON(s.sessionPath(id), &x); err != nil {
		return Session{}, false
	}
	return x, true
}

func (s *Store) DeleteSession(id string) { _ = os.Remove(s.sessionPath(id)) }

// Sessions lists every readable session file. Unreadable or partial files are skipped.
func (s *Store) Sessions() []Session {
	entries, _ := os.ReadDir(filepath.Join(s.Dir, "sessions"))
	var out []Session
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		var x Session
		if readJSON(filepath.Join(s.Dir, "sessions", e.Name()), &x) == nil {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// PruneSessions deletes session files not updated for maxAge.
func (s *Store) PruneSessions(now time.Time, maxAge time.Duration) {
	for _, x := range s.Sessions() {
		if now.Sub(time.Unix(x.Updated, 0)) > maxAge {
			s.DeleteSession(x.ID)
		}
	}
}

// ---- manual hold / pause / stop -----------------------------------------

type holdFile struct {
	Until int64 `json:"until"`
}

func (s *Store) SetHold(until time.Time) error {
	return writeJSON(s.path("hold.json"), holdFile{Until: until.Unix()})
}
func (s *Store) ClearHold() { _ = os.Remove(s.path("hold.json")) }
func (s *Store) Hold() (time.Time, bool) {
	var h holdFile
	if readJSON(s.path("hold.json"), &h) != nil {
		return time.Time{}, false
	}
	return time.Unix(h.Until, 0), true
}

func (s *Store) flag(name string, on bool) error {
	if on {
		return os.WriteFile(s.path(name), []byte("1"), 0o644)
	}
	if err := os.Remove(s.path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
func (s *Store) has(name string) bool { _, err := os.Stat(s.path(name)); return err == nil }

func (s *Store) SetPaused(on bool) error { return s.flag("paused", on) }
func (s *Store) Paused() bool            { return s.has("paused") }
func (s *Store) RequestStop() error      { return s.flag("stop", true) }
func (s *Store) StopRequested() bool     { return s.has("stop") }
func (s *Store) ClearStop()              { _ = s.flag("stop", false) }

// ---- daemon pid ----------------------------------------------------------

func (s *Store) WritePID(pid int) error {
	return os.WriteFile(s.path("daemon.pid"), []byte(strconv.Itoa(pid)), 0o644)
}
func (s *Store) ReadPID() int {
	b, err := os.ReadFile(s.path("daemon.pid"))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(string(b))
	return n
}
func (s *Store) ClearPID() { _ = os.Remove(s.path("daemon.pid")) }

// ---- saved lid action ----------------------------------------------------

// LidOriginal is the user's own lid-close action (0 nothing, 1 sleep, 2 hibernate, 3 shutdown),
// saved to disk BEFORE we change it so a crash can always be repaired.
type LidOriginal struct {
	AC      uint32 `json:"ac"`
	DC      uint32 `json:"dc"`
	SavedAt int64  `json:"saved_at"`
}

func (s *Store) SaveLid(o LidOriginal) error { return writeJSON(s.path("lid-original.json"), o) }
func (s *Store) LoadLid() (LidOriginal, bool) {
	var o LidOriginal
	if readJSON(s.path("lid-original.json"), &o) != nil {
		return LidOriginal{}, false
	}
	return o, true
}
func (s *Store) ClearLid() { _ = os.Remove(s.path("lid-original.json")) }

// ---- status written by the daemon, read by `agentawake status` -----------

type Status struct {
	Updated     int64    `json:"updated"`
	Holding     bool     `json:"holding"`
	Reasons     []string `json:"reasons"`
	Paused      bool     `json:"paused"`
	Capped      bool     `json:"capped"`
	LidClosed   bool     `json:"lid_closed"`
	LidGuard    bool     `json:"lid_guard"`
	LidOverride bool     `json:"lid_override"`
	SleepAt     int64    `json:"sleep_at"`
	Note        string   `json:"note"`
	Version     string   `json:"version"`
}

func (s *Store) WriteStatus(st Status) error { return writeJSON(s.path("status.json"), st) }
func (s *Store) ReadStatus() (Status, bool) {
	var st Status
	if readJSON(s.path("status.json"), &st) != nil {
		return Status{}, false
	}
	return st, true
}
