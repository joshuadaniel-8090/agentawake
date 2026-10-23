//go:build linux

package detect

import (
	"os"
	"strconv"
	"strings"
)

// sysLister reads /proc. Used for development and tests on Linux; the shipped target is Windows.
type sysLister struct{}

func NewLister() Lister { return sysLister{} }

func parseStat(b []byte) (name string, ppid int32, cpu float64, ok bool) {
	s := string(b)
	l, r := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if l < 0 || r < l {
		return
	}
	name = s[l+1 : r]
	f := strings.Fields(s[r+1:])
	if len(f) < 13 {
		return
	}
	pp, _ := strconv.Atoi(f[1])
	ut, _ := strconv.ParseFloat(f[11], 64)
	st, _ := strconv.ParseFloat(f[12], 64)
	return name, int32(pp), (ut + st) / 100, true
}

func (sysLister) List() ([]Proc, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []Proc
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		if name, pp, _, ok := parseStat(b); ok {
			out = append(out, Proc{PID: int32(pid), PPID: pp, Name: name})
		}
	}
	return out, nil
}

func (sysLister) CPU(pid int32) (float64, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(int(pid)) + "/stat")
	if err != nil {
		return 0, err
	}
	_, _, cpu, ok := parseStat(b)
	if !ok {
		return 0, os.ErrInvalid
	}
	return cpu, nil
}

func (sysLister) Cmdline(pid int32) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(int(pid)) + "/cmdline")
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(string(b), "\x00", " ")
}
