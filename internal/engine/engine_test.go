package engine

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"agentawake/internal/state"
)

type fakePower struct {
	awake    bool
	setCalls int
	sleeps   []bool // hibernate flag per Sleep call
	onBat    bool
	pct      int
	batOK    bool
}

func (p *fakePower) SetAwake(on bool) error { p.awake = on; p.setCalls++; return nil }
func (p *fakePower) Sleep(h bool) error     { p.sleeps = append(p.sleeps, h); return nil }
func (p *fakePower) Battery() (bool, int, bool) {
	return p.onBat, p.pct, p.batOK
}

type fakeLid struct {
	avail, closed bool
	ac, dc        uint32
	sets          [][2]uint32
	failZeroSet   bool
}

func (l *fakeLid) Available() bool { return l.avail }
func (l *fakeLid) Closed() bool    { return l.closed }
func (l *fakeLid) Get() (state.LidOriginal, error) {
	return state.LidOriginal{AC: l.ac, DC: l.dc}, nil
}
func (l *fakeLid) Set(ac, dc uint32) error {
	if l.failZeroSet && ac == 0 && dc == 0 {
		return errors.New("access denied")
	}
	l.ac, l.dc = ac, dc
	l.sets = append(l.sets, [2]uint32{ac, dc})
	return nil
}

type fakeSrc struct{ reasons []string }

func (s *fakeSrc) Active(time.Time) []string { return s.reasons }

type rig struct {
	t     *testing.T
	e     *Engine
	p     *fakePower
	l     *fakeLid
	src   *fakeSrc
	store *state.Store
	now   time.Time
}

func newRig(t *testing.T, mod func(*Options)) *rig {
	t.Helper()
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, p: &fakePower{batOK: true, pct: 100}, l: &fakeLid{avail: true, ac: 1, dc: 1},
		src: &fakeSrc{}, store: store, now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	o := Options{
		Power: r.p, Lid: r.l, Store: store, Sources: []Source{r.src},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:   func() time.Time { return r.now },
		Grace: 30 * time.Second, MaxHold: 8 * time.Hour, BatteryFloor: 20, LidGuard: true,
	}
	if mod != nil {
		mod(&o)
	}
	r.e = New(o)
	return r
}

func (r *rig) tick(advance time.Duration) state.Status {
	r.now = r.now.Add(advance)
	return r.e.Tick()
}
func (r *rig) work() { r.src.reasons = []string{"claude-code session abc (hooks)"} }
func (r *rig) idle() { r.src.reasons = nil }

func TestHoldAndRelease_NoLidGuard(t *testing.T) {
	r := newRig(t, func(o *Options) { o.LidGuard = false })
	r.work()
	if st := r.tick(time.Second); !st.Holding || !r.p.awake {
		t.Fatalf("should hold: %+v", st)
	}
	r.idle()
	if st := r.tick(time.Second); st.Holding || r.p.awake {
		t.Fatalf("should release: %+v", st)
	}
	if len(r.l.sets) != 0 {
		t.Fatalf("lid must not be touched when lid guard is off: %v", r.l.sets)
	}
}

func TestLidGuard_AppliesAndRestores(t *testing.T) {
	r := newRig(t, nil)
	r.l.ac, r.l.dc = 1, 2
	r.work()
	st := r.tick(time.Second)
	if !st.LidOverride || r.l.ac != 0 || r.l.dc != 0 {
		t.Fatalf("lid should be Do nothing while working: %+v lid=%d,%d", st, r.l.ac, r.l.dc)
	}
	if o, ok := r.store.LoadLid(); !ok || o.AC != 1 || o.DC != 2 {
		t.Fatalf("original must be saved to disk first: %+v %v", o, ok)
	}
	r.tick(time.Second) // still working: must not overwrite saved original with 0,0
	if o, _ := r.store.LoadLid(); o.AC != 1 || o.DC != 2 {
		t.Fatalf("original overwritten: %+v", o)
	}
	r.idle()
	st = r.tick(time.Second)
	if st.LidOverride || r.l.ac != 1 || r.l.dc != 2 {
		t.Fatalf("lid should be restored: %+v lid=%d,%d", st, r.l.ac, r.l.dc)
	}
	if _, ok := r.store.LoadLid(); ok {
		t.Fatal("saved original should be cleared after restore")
	}
}

func TestLidClosedAtFinish_SleepsAfterGrace(t *testing.T) {
	r := newRig(t, nil)
	r.work()
	r.tick(time.Second)
	r.l.closed = true // user closes the lid mid-run; nothing happens
	r.tick(time.Second)
	if len(r.p.sleeps) != 0 {
		t.Fatal("must not sleep while agent works")
	}
	r.idle()
	st := r.tick(time.Second)
	if st.SleepAt == 0 || len(r.p.sleeps) != 0 {
		t.Fatalf("sleep should be scheduled, not immediate: %+v", st)
	}
	r.tick(29 * time.Second)
	if len(r.p.sleeps) != 0 {
		t.Fatal("slept before grace period ended")
	}
	r.tick(2 * time.Second)
	if len(r.p.sleeps) != 1 || r.p.sleeps[0] {
		t.Fatalf("expected exactly one normal sleep, got %v", r.p.sleeps)
	}
	r.tick(time.Minute)
	if len(r.p.sleeps) != 1 {
		t.Fatal("must not sleep twice")
	}
}

func TestLidOpenAtFinish_DoesNotSleep(t *testing.T) {
	r := newRig(t, nil)
	r.work()
	r.tick(time.Second)
	r.l.closed = true
	r.tick(time.Second)
	r.l.closed = false // reopened before finishing: user is at the laptop
	r.idle()
	r.tick(time.Second)
	r.tick(time.Minute)
	if len(r.p.sleeps) != 0 {
		t.Fatalf("slept with the lid open: %v", r.p.sleeps)
	}
}

func TestLidReopenedDuringGrace_CancelsSleep(t *testing.T) {
	r := newRig(t, nil)
	r.work()
	r.tick(time.Second)
	r.l.closed = true
	r.idle()
	r.tick(time.Second)
	r.l.closed = false
	r.tick(10 * time.Second)
	r.l.closed = true
	r.tick(time.Minute)
	if len(r.p.sleeps) != 0 {
		t.Fatalf("sleep should have been cancelled: %v", r.p.sleeps)
	}
}

func TestAgentResumesDuringGrace_CancelsSleep(t *testing.T) {
	r := newRig(t, nil)
	r.work()
	r.tick(time.Second)
	r.l.closed = true
	r.idle()
	r.tick(time.Second)
	r.work() // next prompt arrives during the grace window
	r.tick(5 * time.Second)
	r.tick(time.Minute)
	if len(r.p.sleeps) != 0 || !r.p.awake {
		t.Fatalf("agent resumed: should stay awake, sleeps=%v awake=%v", r.p.sleeps, r.p.awake)
	}
}

func TestUserOriginallyDoNothing_NeverSleeps(t *testing.T) {
	r := newRig(t, nil)
	r.l.ac, r.l.dc = 0, 0
	r.work()
	r.tick(time.Second)
	r.l.closed = true
	r.idle()
	r.tick(time.Second)
	r.tick(time.Minute)
	if len(r.p.sleeps) != 0 {
		t.Fatalf("user chose lid=do nothing; we must not sleep: %v", r.p.sleeps)
	}
}

func TestOriginalHibernate_Hibernates(t *testing.T) {
	r := newRig(t, nil)
	r.l.ac, r.l.dc = 2, 2
	r.work()
	r.tick(time.Second)
	r.l.closed = true
	r.idle()
	r.tick(time.Second)
	r.tick(time.Minute)
	if len(r.p.sleeps) != 1 || !r.p.sleeps[0] {
		t.Fatalf("expected one hibernate: %v", r.p.sleeps)
	}
}

func TestOnBattery_UsesBatteryAction(t *testing.T) {
	r := newRig(t, nil)
	r.l.ac, r.l.dc = 0, 1 // plugged in: do nothing, battery: sleep
	r.p.onBat, r.p.pct = true, 80
	r.work()
	r.tick(time.Second)
	r.l.closed = true
	r.idle()
	r.tick(time.Second)
	r.tick(time.Minute)
	if len(r.p.sleeps) != 1 {
		t.Fatalf("on battery the DC action (sleep) applies: %v", r.p.sleeps)
	}
}

func TestMaxHold_Caps(t *testing.T) {
	r := newRig(t, func(o *Options) { o.MaxHold = time.Hour; o.LidGuard = false })
	r.work()
	r.tick(time.Second)
	st := r.tick(61 * time.Minute)
	if st.Holding || !st.Capped {
		t.Fatalf("should be capped after max hold: %+v", st)
	}
	st = r.tick(time.Minute)
	if st.Holding {
		t.Fatal("must stay released while the same run continues")
	}
	r.idle()
	r.tick(time.Second)
	r.work()
	if st = r.tick(time.Second); !st.Holding {
		t.Fatalf("a new run after idle should hold again: %+v", st)
	}
}

func TestLowBattery_DoesNotHold(t *testing.T) {
	r := newRig(t, nil)
	r.p.onBat, r.p.pct = true, 10
	r.work()
	st := r.tick(time.Second)
	if st.Holding || r.p.awake {
		t.Fatalf("must not hold below battery floor: %+v", st)
	}
}

func TestLowBatteryWhileHolding_SleepsImmediatelyIfLidClosed(t *testing.T) {
	r := newRig(t, nil)
	r.work()
	r.tick(time.Second)
	r.l.closed = true
	r.p.onBat, r.p.pct = true, 15
	r.tick(time.Second)
	if len(r.p.sleeps) != 1 {
		t.Fatalf("low battery with lid closed should sleep right away: %v", r.p.sleeps)
	}
}

func TestPaused(t *testing.T) {
	r := newRig(t, nil)
	r.work()
	_ = r.store.SetPaused(true)
	if st := r.tick(time.Second); st.Holding || !st.Paused {
		t.Fatalf("paused must not hold: %+v", st)
	}
}

func TestRecover_RestoresLeftoverLid(t *testing.T) {
	r := newRig(t, nil)
	_ = r.store.SaveLid(state.LidOriginal{AC: 1, DC: 1})
	r.l.ac, r.l.dc = 0, 0 // a previous run crashed with the override on
	r.e.Recover()
	if r.l.ac != 1 || r.l.dc != 1 {
		t.Fatalf("crash leftover not repaired: %d,%d", r.l.ac, r.l.dc)
	}
	if _, ok := r.store.LoadLid(); ok {
		t.Fatal("saved copy should be cleared")
	}
}

func TestLidGuardFails_FallsBackToWakeRequestOnly(t *testing.T) {
	r := newRig(t, nil)
	r.l.failZeroSet = true
	r.work()
	st := r.tick(time.Second)
	if !st.Holding || st.LidOverride {
		t.Fatalf("wake request must still work: %+v", st)
	}
	if st.Note == "" {
		t.Fatal("must tell the user lid guard is not active")
	}
	if _, ok := r.store.LoadLid(); ok {
		t.Fatal("failed attempt must not leave a stale saved original")
	}
	// no retry storm: second tick makes no more attempts
	n := len(r.l.sets)
	r.tick(time.Second)
	if len(r.l.sets) != n {
		t.Fatal("should not retry lid guard every tick")
	}
}

func TestLidGuardUnavailable_StillHolds(t *testing.T) {
	r := newRig(t, nil)
	r.l.avail = false
	r.work()
	st := r.tick(time.Second)
	if !st.Holding || st.Note == "" {
		t.Fatalf("desktop PC without lid: hold normally and say why lid guard is off: %+v", st)
	}
}

func TestShutdown_ReleasesAndNeverSleeps(t *testing.T) {
	r := newRig(t, nil)
	r.work()
	r.tick(time.Second)
	r.l.closed = true
	r.e.Shutdown()
	if r.p.awake || r.l.ac != 1 || len(r.p.sleeps) != 0 {
		t.Fatalf("shutdown must release and restore without sleeping: awake=%v lid=%d sleeps=%v", r.p.awake, r.l.ac, r.p.sleeps)
	}
}

func TestHookSource(t *testing.T) {
	store, _ := state.Open(t.TempDir())
	now := time.Now()
	_ = store.PutSession(state.Session{ID: "fresh", Agent: "claude-code", Busy: true, Updated: now.Unix(), PID: 10})
	_ = store.PutSession(state.Session{ID: "stale", Agent: "claude-code", Busy: true, Updated: now.Add(-time.Hour).Unix()})
	_ = store.PutSession(state.Session{ID: "idle", Agent: "claude-code", Busy: false, Updated: now.Unix()})
	_ = store.PutSession(state.Session{ID: "dead", Agent: "claude-code", Busy: true, Updated: now.Unix(), PID: 99})
	h := HookSource{Store: store, TTL: 10 * time.Minute, PidAlive: func(p int32) bool { return p == 10 }}
	got := h.Active(now)
	if len(got) != 1 {
		t.Fatalf("only the fresh live busy session counts, got %v", got)
	}
}

func TestManualSource_Expires(t *testing.T) {
	store, _ := state.Open(t.TempDir())
	now := time.Now()
	_ = store.SetHold(now.Add(time.Minute))
	m := ManualSource{Store: store}
	if len(m.Active(now)) != 1 {
		t.Fatal("hold should be active")
	}
	if len(m.Active(now.Add(2*time.Minute))) != 0 {
		t.Fatal("hold should expire")
	}
	if _, ok := store.Hold(); ok {
		t.Fatal("expired hold file should be removed")
	}
}

func TestHookSource_LongToolCallOutlivesNormalTTL(t *testing.T) {
	store, _ := state.Open(t.TempDir())
	now := time.Now()
	old := now.Add(-30 * time.Minute).Unix() // 30 min since the last event
	_ = store.PutSession(state.Session{ID: "build", Agent: "claude-code", Busy: true, Tool: true, Updated: old})
	_ = store.PutSession(state.Session{ID: "thinking", Agent: "claude-code", Busy: true, Tool: false, Updated: old})
	h := HookSource{Store: store, TTL: 10 * time.Minute, ToolTTL: time.Hour}
	got := h.Active(now)
	if len(got) != 1 {
		t.Fatalf("a 30 min build counts as working, a 30 min silent 'thinking' does not: %v", got)
	}
	if got := h.Active(now.Add(40 * time.Minute)); len(got) != 0 {
		t.Fatalf("even tool calls expire eventually: %v", got)
	}
}
