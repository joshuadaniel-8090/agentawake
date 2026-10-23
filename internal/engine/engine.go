// Package engine is the decision loop. It contains no OS calls: Power, Lid and Source
// are interfaces, so every rule here is unit-tested with fakes on any OS.
package engine

import (
	"fmt"
	"log/slog"
	"time"

	"agentawake/internal/state"
)

// Power is everything the engine does to the machine.
type Power interface {
	SetAwake(on bool) error
	Sleep(hibernate bool) error
	Battery() (onBattery bool, percent int, ok bool)
}

// Lid reads the lid switch and the lid-close power setting.
type Lid interface {
	Available() bool
	Closed() bool
	Get() (state.LidOriginal, error)
	Set(ac, dc uint32) error
}

// Source reports why the machine should stay awake right now. Empty = nothing working.
type Source interface {
	Active(now time.Time) []string
}

type Options struct {
	Power        Power
	Lid          Lid
	Store        *state.Store
	Sources      []Source
	Log          *slog.Logger
	Now          func() time.Time
	Grace        time.Duration
	MaxHold      time.Duration
	BatteryFloor int
	LidGuard     bool
	Version      string
}

type Engine struct {
	o Options

	holding    bool
	holdStart  time.Time
	lidApplied bool
	lidBroken  bool // lid guard failed this hold; do not retry every tick
	capped     bool
	sleepAt    time.Time
	sleepHib   bool
}

func New(o Options) *Engine {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Engine{o: o}
}

// Recover repairs a lid setting left changed by a crash or reboot. Call once at startup.
func (e *Engine) Recover() {
	if orig, ok := e.o.Store.LoadLid(); ok && e.o.Lid != nil {
		e.o.Log.Warn("restoring lid action left over from a previous run", "ac", orig.AC, "dc", orig.DC)
		e.restoreLid()
	}
}

// Shutdown releases everything. It never sleeps the machine.
func (e *Engine) Shutdown() {
	if e.holding {
		_ = e.o.Power.SetAwake(false)
		e.holding = false
	}
	e.restoreLid()
}

func (e *Engine) restoreLid() {
	orig, ok := e.o.Store.LoadLid()
	if !ok {
		e.lidApplied = false
		return
	}
	if err := e.o.Lid.Set(orig.AC, orig.DC); err != nil {
		e.o.Log.Error("could not restore lid action (kept saved copy, will retry)", "err", err)
		return
	}
	e.o.Store.ClearLid()
	e.lidApplied = false
	e.o.Log.Info("lid action restored", "ac", orig.AC, "dc", orig.DC)
}

func (e *Engine) applyLid() string {
	if !e.o.LidGuard || e.lidApplied || e.lidBroken {
		return ""
	}
	if e.o.Lid == nil || !e.o.Lid.Available() {
		e.lidBroken = true
		return "lid guard unavailable on this machine"
	}
	if _, saved := e.o.Store.LoadLid(); !saved { // keep the earliest copy: that is the user's real setting
		orig, err := e.o.Lid.Get()
		if err != nil {
			e.lidBroken = true
			e.o.Log.Error("lid guard: cannot read lid action", "err", err)
			return "lid guard failed: " + err.Error()
		}
		orig.SavedAt = e.o.Now().Unix()
		if err := e.o.Store.SaveLid(orig); err != nil {
			e.lidBroken = true
			return "lid guard failed: cannot save original setting"
		}
	}
	if err := e.o.Lid.Set(0, 0); err != nil {
		e.lidBroken = true
		e.restoreLid()
		e.o.Log.Error("lid guard: cannot set lid action", "err", err)
		return "lid guard failed: " + err.Error() + " (wake request only)"
	}
	e.lidApplied = true
	e.o.Log.Info("lid guard on: lid close does nothing while agent works")
	return ""
}

func (e *Engine) lidClosed() bool { return e.o.Lid != nil && e.o.Lid.Closed() }

// Tick runs one decision. Call it every poll interval.
func (e *Engine) Tick() state.Status {
	now := e.o.Now()
	o := e.o
	var note string

	paused := o.Store.Paused()
	var reasons []string
	if !paused {
		for _, s := range o.Sources {
			reasons = append(reasons, s.Active(now)...)
		}
	}
	raw := len(reasons) > 0
	if !raw {
		e.capped = false
	}

	onBat, pct, batOK := o.Power.Battery()
	lowBat := batOK && onBat && o.BatteryFloor > 0 && pct < o.BatteryFloor

	working := raw
	switch {
	case paused:
		note = "paused"
	case raw && lowBat:
		working, note = false, fmt.Sprintf("battery %d%% is below the %d%% floor, not holding", pct, o.BatteryFloor)
	case raw && e.capped:
		working, note = false, "max hold time reached, waiting for the agent to go idle"
	case raw && e.holding && now.Sub(e.holdStart) > o.MaxHold:
		e.capped = true
		working, note = false, "max hold time reached, releasing"
		o.Log.Warn("max hold time reached", "max", o.MaxHold)
	}

	if working {
		e.sleepAt = time.Time{}
		if !e.holding {
			if err := o.Power.SetAwake(true); err != nil {
				o.Log.Error("cannot set wake request", "err", err)
				note = "wake request failed: " + err.Error()
			} else {
				e.holding, e.holdStart, e.lidBroken = true, now, false
				o.Log.Info("hold", "reasons", reasons)
			}
		}
		if e.holding {
			if n := e.applyLid(); n != "" {
				note = n
			}
		}
	} else {
		e.release(now, lowBat, &note)
	}

	st := state.Status{
		Updated: now.Unix(), Holding: e.holding, Reasons: reasons, Paused: paused, Capped: e.capped,
		LidClosed: e.lidClosed(), LidGuard: o.LidGuard, LidOverride: e.lidApplied, Note: note, Version: o.Version,
	}
	if !e.sleepAt.IsZero() {
		st.SleepAt = e.sleepAt.Unix()
	}
	return st
}

func (e *Engine) release(now time.Time, lowBat bool, note *string) {
	o := e.o
	wasHolding := e.holding
	wasLid := e.lidApplied
	if e.holding {
		if err := o.Power.SetAwake(false); err != nil {
			o.Log.Error("cannot clear wake request", "err", err)
		}
		e.holding = false
		o.Log.Info("release")
	}
	var orig state.LidOriginal
	if wasLid {
		orig, _ = o.Store.LoadLid()
	}
	if e.lidApplied {
		e.restoreLid()
	}

	// Windows only reacts to the lid at the moment it closes, so restoring the setting is not
	// enough. If the lid is closed right now, put the machine to sleep ourselves.
	if wasHolding && wasLid && e.lidClosed() && e.sleepAt.IsZero() {
		action := orig.AC
		if onBat, _, ok := o.Power.Battery(); ok && onBat {
			action = orig.DC
		}
		switch action {
		case 1, 2:
			wait := o.Grace
			if lowBat {
				wait = 0
			}
			e.sleepAt = now.Add(wait)
			e.sleepHib = action == 2
			o.Log.Info("lid closed at finish, scheduling sleep", "in", wait)
		default:
			o.Log.Info("lid closed at finish, but your lid action is not sleep; leaving it", "action", action)
		}
	}
	if !e.sleepAt.IsZero() {
		switch {
		case !e.lidClosed():
			e.sleepAt = time.Time{}
			o.Log.Info("lid reopened, sleep cancelled")
		case !now.Before(e.sleepAt):
			o.Log.Info("sleeping now", "hibernate", e.sleepHib)
			if err := o.Power.Sleep(e.sleepHib); err != nil {
				o.Log.Error("sleep call failed", "err", err)
				*note = "sleep call failed: " + err.Error()
			}
			e.sleepAt = time.Time{}
		default:
			*note = "lid closed, sleeping in " + e.sleepAt.Sub(now).Round(time.Second).String()
		}
	}
}
