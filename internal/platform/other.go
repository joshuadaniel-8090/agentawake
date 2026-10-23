//go:build !windows

package platform

import (
	"os"
	"os/exec"
	"syscall"

	"agentawake/internal/state"
)

type unsupported struct{}

func (unsupported) SetAwake(bool) error             { return ErrUnsupported }
func (unsupported) Sleep(bool) error                { return ErrUnsupported }
func (unsupported) Battery() (bool, int, bool)      { return false, 0, false }
func (unsupported) Available() bool                 { return false }
func (unsupported) Closed() bool                    { return false }
func (unsupported) Get() (state.LidOriginal, error) { return state.LidOriginal{}, ErrUnsupported }
func (unsupported) Set(uint32, uint32) error        { return ErrUnsupported }

// New returns the real platform. On non-Windows OSes there is none; callers should use Dry.
func New() (p unsupported, closer Closer, err error) { return unsupported{}, func() {}, ErrUnsupported }

// PidAlive reports whether a process exists.
func PidAlive(pid int32) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(int(pid))
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// Detach is only meaningful on Windows.
func Detach(cmd *exec.Cmd) {}

// Diagnostics is shown by `doctor`.
func Diagnostics() []string { return []string{"not Windows: power features unavailable"} }
