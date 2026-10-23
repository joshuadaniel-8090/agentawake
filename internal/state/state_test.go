package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessions_RoundTripAndSanitise(t *testing.T) {
	s, _ := Open(t.TempDir())
	if err := s.PutSession(Session{ID: "../../etc/passwd", Agent: "x", Busy: true, Updated: 1}); err != nil {
		t.Fatal(err)
	}
	// a hostile id must stay inside the sessions folder
	entries, _ := os.ReadDir(filepath.Join(s.Dir, "sessions"))
	if len(entries) != 1 || strings.Contains(entries[0].Name(), "/") || strings.Contains(entries[0].Name(), "..") {
		t.Fatalf("unsafe file name: %v", entries)
	}
	if got := s.Sessions(); len(got) != 1 || got[0].ID != "../../etc/passwd" {
		t.Fatalf("round trip: %+v", got)
	}
}

func TestSessions_SkipsCorruptFiles(t *testing.T) {
	s, _ := Open(t.TempDir())
	_ = s.PutSession(Session{ID: "ok", Busy: true, Updated: 1})
	_ = os.WriteFile(filepath.Join(s.Dir, "sessions", "bad.json"), []byte("{trunc"), 0o644)
	if got := s.Sessions(); len(got) != 1 {
		t.Fatalf("corrupt file should be skipped: %+v", got)
	}
}

func TestPrune(t *testing.T) {
	s, _ := Open(t.TempDir())
	now := time.Now()
	_ = s.PutSession(Session{ID: "old", Updated: now.Add(-48 * time.Hour).Unix()})
	_ = s.PutSession(Session{ID: "new", Updated: now.Unix()})
	s.PruneSessions(now, 24*time.Hour)
	if got := s.Sessions(); len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("prune: %+v", got)
	}
}

func TestFlagsHoldLidStatus(t *testing.T) {
	s, _ := Open(t.TempDir())
	if s.Paused() || s.StopRequested() {
		t.Fatal("flags start off")
	}
	_ = s.SetPaused(true)
	_ = s.RequestStop()
	if !s.Paused() || !s.StopRequested() {
		t.Fatal("flags should be on")
	}
	_ = s.SetPaused(false)
	s.ClearStop()
	if s.Paused() || s.StopRequested() {
		t.Fatal("flags should clear")
	}

	until := time.Now().Add(time.Hour).Truncate(time.Second)
	_ = s.SetHold(until)
	if got, ok := s.Hold(); !ok || !got.Equal(until) {
		t.Fatalf("hold: %v %v", got, ok)
	}
	s.ClearHold()
	if _, ok := s.Hold(); ok {
		t.Fatal("hold should clear")
	}

	_ = s.SaveLid(LidOriginal{AC: 1, DC: 2})
	if o, ok := s.LoadLid(); !ok || o.AC != 1 || o.DC != 2 {
		t.Fatalf("lid: %+v", o)
	}
	s.ClearLid()
	if _, ok := s.LoadLid(); ok {
		t.Fatal("lid should clear")
	}

	_ = s.WriteStatus(Status{Holding: true, Reasons: []string{"x"}})
	if st, ok := s.ReadStatus(); !ok || !st.Holding {
		t.Fatalf("status: %+v", st)
	}
}

func TestAtomicWriteReplacesExisting(t *testing.T) {
	s, _ := Open(t.TempDir())
	for i := 0; i < 20; i++ {
		if err := s.PutSession(Session{ID: "a", Busy: i%2 == 0, Updated: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	leftovers, _ := filepath.Glob(filepath.Join(s.Dir, "sessions", "*.tmp*"))
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}
