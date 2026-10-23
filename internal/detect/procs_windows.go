//go:build windows

package detect

import (
	"syscall"
	"unsafe"
)

const (
	processQueryLimitedInformation = 0x1000
	processQueryInformation        = 0x0400
	processVMRead                  = 0x0010
)

var (
	modNtdll                   = syscall.NewLazyDLL("ntdll.dll")
	modKernel32                = syscall.NewLazyDLL("kernel32.dll")
	procNtQueryInformationProc = modNtdll.NewProc("NtQueryInformationProcess")
	procReadProcessMemory      = modKernel32.NewProc("ReadProcessMemory")
)

type sysLister struct{}

// NewLister lists processes through a Toolhelp snapshot (cheap: names and parent ids only).
func NewLister() Lister { return sysLister{} }

func (sysLister) List() ([]Proc, error) {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.CloseHandle(snap)
	var e syscall.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if err := syscall.Process32First(snap, &e); err != nil {
		return nil, err
	}
	var out []Proc
	for {
		out = append(out, Proc{
			PID:  int32(e.ProcessID),
			PPID: int32(e.ParentProcessID),
			Name: syscall.UTF16ToString(e.ExeFile[:]),
		})
		if err := syscall.Process32Next(snap, &e); err != nil {
			break
		}
	}
	return out, nil
}

func (sysLister) CPU(pid int32) (float64, error) {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return 0, err
	}
	defer syscall.CloseHandle(h)
	var create, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &create, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	return float64(kernel.Nanoseconds()+user.Nanoseconds()) / 1e9, nil
}

// Cmdline reads the command line from the target's PEB. Best effort: any failure returns "".
// Only called for generic runtimes (node, python...) when a signature needs a command-line match.
func (sysLister) Cmdline(pid int32) string {
	h, err := syscall.OpenProcess(processQueryInformation|processVMRead, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer syscall.CloseHandle(h)

	var pbi struct {
		ExitStatus, PebBaseAddress, AffinityMask, BasePriority, UniquePID, ParentPID uintptr
	}
	var retLen uint32
	if r, _, _ := procNtQueryInformationProc.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&pbi)),
		unsafe.Sizeof(pbi), uintptr(unsafe.Pointer(&retLen))); r != 0 || pbi.PebBaseAddress == 0 {
		return ""
	}

	ptr := unsafe.Sizeof(uintptr(0))
	pebParamsOff, cmdOff := uintptr(0x20), uintptr(0x70) // 64-bit layout
	if ptr == 4 {
		pebParamsOff, cmdOff = 0x10, 0x40
	}

	var params uintptr
	if !readMem(h, pbi.PebBaseAddress+pebParamsOff, unsafe.Pointer(&params), ptr) || params == 0 {
		return ""
	}
	// UNICODE_STRING { USHORT Length; USHORT MaximumLength; (pad on 64-bit); PWSTR Buffer }
	if ptr == 4 {
		var us32 struct {
			Length, MaxLength uint16
			Buffer            uint32
		}
		if !readMem(h, params+cmdOff, unsafe.Pointer(&us32), unsafe.Sizeof(us32)) || us32.Length == 0 || us32.Length > 32768 {
			return ""
		}
		return readWide(h, uintptr(us32.Buffer), us32.Length)
	}
	var us struct {
		Length, MaxLength uint16
		_                 [4]byte
		Buffer            uintptr
	}
	if !readMem(h, params+cmdOff, unsafe.Pointer(&us), unsafe.Sizeof(us)) || us.Length == 0 || us.Length > 32768 {
		return ""
	}
	return readWide(h, us.Buffer, us.Length)
}

func readWide(h syscall.Handle, addr uintptr, byteLen uint16) string {
	buf := make([]uint16, byteLen/2)
	if !readMem(h, addr, unsafe.Pointer(&buf[0]), uintptr(byteLen)) {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

func readMem(h syscall.Handle, addr uintptr, dst unsafe.Pointer, n uintptr) bool {
	var read uintptr
	r, _, _ := procReadProcessMemory.Call(uintptr(h), addr, uintptr(dst), n, uintptr(unsafe.Pointer(&read)))
	return r != 0 && read == n
}
