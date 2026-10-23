// Package platform holds the only code that touches the operating system's power features.
// Windows is the real implementation. A dry-run implementation (used for development and
// tests on any OS) logs what it would do and takes the lid state from a file.
package platform

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"agentawake/internal/state"
)

// Closer releases OS resources (window, handles).
type Closer func()

// System is the full machine interface the daemon needs (satisfies engine.Power and engine.Lid).
type System interface {
	SetAwake(on bool) error
	Sleep(hibernate bool) error
	Battery() (onBattery bool, percent int, ok bool)
	Available() bool
	Closed() bool
	Get() (state.LidOriginal, error)
	Set(ac, dc uint32) error
}

// NewSystem returns the real platform for this OS.
func NewSystem() (System, Closer, error) { return New() }

var ErrUnsupported = errors.New("this OS is not supported yet; use --dry-run to simulate")

// ---- dry run -------------------------------------------------------------

// Dry simulates the machine. The lid is "closed" while <dir>/dry-lid-closed exists.
// Lid action values live in memory, starting as sleep-on-close (1).
type Dry struct {
	Dir string
	Log *slog.Logger

	mu     sync.Mutex
	awake  bool
	ac, dc uint32
}

func NewDry(dir string, log *slog.Logger) *Dry { return &Dry{Dir: dir, Log: log, ac: 1, dc: 1} }

func (d *Dry) SetAwake(on bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.awake = on
	d.Log.Info("[dry] wake request", "on", on)
	return nil
}
func (d *Dry) Sleep(hibernate bool) error {
	d.Log.Info("[dry] SLEEP the machine", "hibernate", hibernate)
	return nil
}
func (d *Dry) Battery() (bool, int, bool) { return false, 100, true }
func (d *Dry) Available() bool            { return true }
func (d *Dry) Closed() bool {
	_, err := os.Stat(filepath.Join(d.Dir, "dry-lid-closed"))
	return err == nil
}
func (d *Dry) Get() (state.LidOriginal, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return state.LidOriginal{AC: d.ac, DC: d.dc}, nil
}
func (d *Dry) Set(ac, dc uint32) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ac, d.dc = ac, dc
	d.Log.Info("[dry] lid action set", "ac", ac, "dc", dc)
	return nil
}
