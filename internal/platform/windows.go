//go:build windows

package platform

import (
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"agentawake/internal/state"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")
	powrprof = syscall.NewLazyDLL("powrprof.dll")

	pPowerCreateRequest      = kernel32.NewProc("PowerCreateRequest")
	pPowerSetRequest         = kernel32.NewProc("PowerSetRequest")
	pPowerClearRequest       = kernel32.NewProc("PowerClearRequest")
	pGetSystemPowerStatus    = kernel32.NewProc("GetSystemPowerStatus")
	pSetSuspendState         = powrprof.NewProc("SetSuspendState")
	pRegisterClassExW        = user32.NewProc("RegisterClassExW")
	pCreateWindowExW         = user32.NewProc("CreateWindowExW")
	pDefWindowProcW          = user32.NewProc("DefWindowProcW")
	pGetMessageW             = user32.NewProc("GetMessageW")
	pTranslateMessage        = user32.NewProc("TranslateMessage")
	pDispatchMessageW        = user32.NewProc("DispatchMessageW")
	pRegisterPowerSettingNtf = user32.NewProc("RegisterPowerSettingNotification")
	pGetModuleHandleW        = kernel32.NewProc("GetModuleHandleW")
	pOpenProcess             = kernel32.NewProc("OpenProcess")
	pGetExitCodeProcess      = kernel32.NewProc("GetExitCodeProcess")
)

const (
	powerRequestSystemRequired    = 1
	powerRequestExecutionRequired = 3
	powerRequestContextSimple     = 0x1

	wmPowerBroadcast      = 0x0218
	pbtPowerSettingChange = 0x8013
	hwndMessage           = ^uintptr(2) // (HWND)-3, message-only window
)

// GUID_LIDSWITCH_STATE_CHANGE {BA3E0F4D-B817-4094-A2D1-D56379E6A0F3}
var guidLid = guid{0xBA3E0F4D, 0xB817, 0x4094, [8]byte{0xA2, 0xD1, 0xD5, 0x63, 0x79, 0xE6, 0xA0, 0xF3}}

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// Win32 is the real platform: wake request, powercfg, sleep, battery, lid switch.
type Win32 struct {
	mu  sync.Mutex
	req syscall.Handle

	lidClosed atomic.Bool
	lidOK     atomic.Bool // lid-close setting readable (laptop with a lid)
}

// New builds the Windows platform and starts the lid watcher.
func New() (*Win32, Closer, error) {
	w := &Win32{}
	if _, err := w.Get(); err == nil {
		w.lidOK.Store(true)
		go w.watchLid()
	}
	return w, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.req != 0 {
			pPowerClearRequest.Call(uintptr(w.req), powerRequestSystemRequired)
			pPowerClearRequest.Call(uintptr(w.req), powerRequestExecutionRequired)
			syscall.CloseHandle(w.req)
			w.req = 0
		}
	}, nil
}

// ---- wake request --------------------------------------------------------

type reasonContext struct {
	Version uint32
	Flags   uint32
	Simple  *uint16
	_       [3]uintptr // rest of the REASON_CONTEXT union
}

func (w *Win32) SetAwake(on bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if on {
		if w.req == 0 {
			reason, _ := syscall.UTF16PtrFromString("agentawake: AI coding agent is working")
			rc := reasonContext{Version: 0, Flags: powerRequestContextSimple, Simple: reason}
			h, _, err := pPowerCreateRequest.Call(uintptr(unsafe.Pointer(&rc)))
			if h == 0 || syscall.Handle(h) == syscall.InvalidHandle {
				return fmt.Errorf("PowerCreateRequest: %w", err)
			}
			w.req = syscall.Handle(h)
		}
		if r, _, err := pPowerSetRequest.Call(uintptr(w.req), powerRequestSystemRequired); r == 0 {
			return fmt.Errorf("PowerSetRequest: %w", err)
		}
		pPowerSetRequest.Call(uintptr(w.req), powerRequestExecutionRequired) // best effort (Modern Standby)
		return nil
	}
	if w.req != 0 {
		pPowerClearRequest.Call(uintptr(w.req), powerRequestSystemRequired)
		pPowerClearRequest.Call(uintptr(w.req), powerRequestExecutionRequired)
	}
	return nil
}

// ---- sleep ---------------------------------------------------------------

func (w *Win32) Sleep(hibernate bool) error {
	h := uintptr(0)
	if hibernate {
		h = 1
	}
	if r, _, err := pSetSuspendState.Call(h, 0, 0); r == 0 {
		return fmt.Errorf("SetSuspendState: %w", err)
	}
	return nil
}

// ---- battery -------------------------------------------------------------

type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

func (w *Win32) Battery() (onBattery bool, percent int, ok bool) {
	var s systemPowerStatus
	if r, _, _ := pGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&s))); r == 0 {
		return false, 0, false
	}
	if s.BatteryLifePercent == 255 || s.ACLineStatus == 255 {
		return false, 0, false
	}
	return s.ACLineStatus == 0, int(s.BatteryLifePercent), true
}

// ---- lid action via powercfg --------------------------------------------

var hexRe = regexp.MustCompile(`0x[0-9a-fA-F]{8}`)

func powercfg(args ...string) (string, error) {
	cmd := exec.Command("powercfg", args...)
	Detach(cmd)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (w *Win32) Available() bool { return w.lidOK.Load() }
func (w *Win32) Closed() bool    { return w.lidClosed.Load() }

// Get reads the current lid-close action for AC and battery (0 nothing, 1 sleep, 2 hibernate, 3 shut down).
// Output labels are localized, so we take the last two 0x........ values: AC first, then DC.
func (w *Win32) Get() (state.LidOriginal, error) {
	out, err := powercfg("/query", "SCHEME_CURRENT", "SUB_BUTTONS", "LIDACTION")
	if err != nil {
		return state.LidOriginal{}, fmt.Errorf("powercfg /query: %w", err)
	}
	m := hexRe.FindAllString(out, -1)
	if len(m) < 2 {
		return state.LidOriginal{}, fmt.Errorf("powercfg /query: unexpected output (no lid setting on this machine?)")
	}
	ac, e1 := strconv.ParseUint(m[len(m)-2][2:], 16, 32)
	dc, e2 := strconv.ParseUint(m[len(m)-1][2:], 16, 32)
	if e1 != nil || e2 != nil {
		return state.LidOriginal{}, fmt.Errorf("powercfg /query: cannot parse values")
	}
	return state.LidOriginal{AC: uint32(ac), DC: uint32(dc)}, nil
}

func (w *Win32) Set(ac, dc uint32) error {
	steps := [][]string{
		{"/setacvalueindex", "SCHEME_CURRENT", "SUB_BUTTONS", "LIDACTION", strconv.Itoa(int(ac))},
		{"/setdcvalueindex", "SCHEME_CURRENT", "SUB_BUTTONS", "LIDACTION", strconv.Itoa(int(dc))},
		{"/setactive", "SCHEME_CURRENT"},
	}
	for _, s := range steps {
		if out, err := powercfg(s...); err != nil {
			return fmt.Errorf("powercfg %s: %w (%s)", s[0], err, strings.TrimSpace(out))
		}
	}
	return nil
}

// ---- lid switch events ---------------------------------------------------

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type powerBroadcastSetting struct {
	PowerSetting guid
	DataLength   uint32
	Data         [1]byte
}

// watchLid owns a hidden message-only window on a locked OS thread and records lid open/closed.
// Registering delivers the current state immediately, then every change.
func (w *Win32) watchLid() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	proc := syscall.NewCallback(func(hwnd, m, wparam, lparam uintptr) uintptr {
		if m == wmPowerBroadcast && wparam == pbtPowerSettingChange {
			ps := (*powerBroadcastSetting)(*(*unsafe.Pointer)(unsafe.Pointer(&lparam)))
			if ps.PowerSetting == guidLid && ps.DataLength >= 1 {
				w.lidClosed.Store(ps.Data[0] == 0) // 0 = closed, 1 = open
			}
			return 1
		}
		r, _, _ := pDefWindowProcW.Call(hwnd, m, wparam, lparam)
		return r
	})

	hinst, _, _ := pGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("agentawake_lid_watcher")
	wc := wndClassEx{Size: uint32(unsafe.Sizeof(wndClassEx{})), WndProc: proc, Instance: hinst, ClassName: className}
	if r, _, _ := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return
	}
	hwnd, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(className)),
		0, 0, 0, 0, 0, hwndMessage, 0, hinst, 0)
	if hwnd == 0 {
		return
	}
	pRegisterPowerSettingNtf.Call(hwnd, uintptr(unsafe.Pointer(&guidLid)), 0) // DEVICE_NOTIFY_WINDOW_HANDLE

	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// ---- helpers used by the CLI --------------------------------------------

// PidAlive reports whether a process with this id is still running.
func PidAlive(pid int32) bool {
	if pid <= 0 {
		return false
	}
	h, _, _ := pOpenProcess.Call(0x1000, 0, uintptr(pid)) // PROCESS_QUERY_LIMITED_INFORMATION
	if h == 0 {
		return false
	}
	defer syscall.CloseHandle(syscall.Handle(h))
	var code uint32
	if r, _, _ := pGetExitCodeProcess.Call(h, uintptr(unsafe.Pointer(&code))); r == 0 {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

// Detach makes a child process run without a console window, independent of ours.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000008 | 0x00000200} // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
}

// Diagnostics returns human-readable facts about this machine for `doctor`.
func Diagnostics() []string {
	var out []string
	w := &Win32{}
	if l, err := w.Get(); err == nil {
		names := map[uint32]string{0: "do nothing", 1: "sleep", 2: "hibernate", 3: "shut down"}
		out = append(out, fmt.Sprintf("lid close action: plugged in = %s, on battery = %s", names[l.AC], names[l.DC]))
	} else {
		out = append(out, "lid close action: not readable ("+err.Error()+"); lid guard will be unavailable")
	}
	if on, pct, ok := w.Battery(); ok {
		src := "plugged in"
		if on {
			src = "on battery"
		}
		out = append(out, fmt.Sprintf("power: %s, %d%%", src, pct))
	} else {
		out = append(out, "power: no battery detected")
	}
	cmd := exec.Command("reg", "query", `HKLM\SYSTEM\CurrentControlSet\Control\Power`, "/v", "HibernateEnabled")
	Detach(cmd)
	if b, err := cmd.CombinedOutput(); err == nil {
		if strings.Contains(string(b), "0x1") {
			out = append(out, "hibernate: enabled (the sleep call should still sleep, but verify once with `agentawake hold 1m`)")
		} else {
			out = append(out, "hibernate: disabled")
		}
	}
	return out
}
