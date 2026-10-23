// Package detect decides whether a non-hook agent (Codex, Gemini CLI, Aider...) is
// actively working. "Open at an idle prompt" must NOT count, so it measures CPU time
// of the agent process tree between scans instead of just checking the process exists.
package detect

import (
	"sort"
	"strings"
	"time"
)

type Proc struct {
	PID, PPID int32
	Name      string
}

// Lister is the OS boundary. Tests use a fake.
type Lister interface {
	List() ([]Proc, error)
	CPU(pid int32) (seconds float64, err error) // cumulative user+kernel CPU time
	Cmdline(pid int32) string                   // best effort, "" on failure
}

// Signature identifies one agent. A process matches if its name is in ProcessNames,
// or it is a generic runtime (node, python...) whose command line contains any CmdContains.
type Signature struct {
	Name         string   `json:"name"`
	ProcessNames []string `json:"process_names"`
	CmdContains  []string `json:"cmd_contains"`
}

// DefaultSignatures deliberately excludes Claude Code: it is detected through hooks, and
// the Claude desktop app has the same process name, which would cause false positives.
// Users can add a "claude" signature in config if they run without hooks.
func DefaultSignatures() []Signature {
	return []Signature{
		{Name: "codex", ProcessNames: []string{"codex"}, CmdContains: []string{"@openai/codex"}},
		{Name: "gemini-cli", ProcessNames: []string{"gemini"}, CmdContains: []string{"@google/gemini-cli"}},
		{Name: "opencode", ProcessNames: []string{"opencode"}, CmdContains: []string{"opencode-ai"}},
		{Name: "aider", ProcessNames: []string{"aider"}, CmdContains: []string{"aider"}},
		{Name: "cursor-agent", ProcessNames: []string{"cursor-agent"}, CmdContains: []string{"cursor-agent"}},
	}
}

var runtimes = map[string]bool{
	"node": true, "bun": true, "deno": true, "npx": true,
	"python": true, "python3": true, "pythonw": true, "uv": true,
}

func norm(n string) string {
	n = strings.ToLower(n)
	return strings.TrimSuffix(n, ".exe")
}

// Merge returns base with overrides replacing same-named signatures and extras appended.
func Merge(base, extra []Signature) []Signature {
	out := append([]Signature{}, base...)
	for _, e := range extra {
		replaced := false
		for i := range out {
			if out[i].Name == e.Name {
				out[i] = e
				replaced = true
			}
		}
		if !replaced {
			out = append(out, e)
		}
	}
	return out
}

type Scanner struct {
	L            Lister
	Sigs         []Signature
	ThresholdPct float64       // % of one core
	Linger       time.Duration // keep "working" after the last burst, bridges gaps between bursts
	Self         int32         // never match ourselves

	prevCPU  map[int32]float64
	prevAt   time.Time
	lastBusy map[string]time.Time
}

func NewScanner(l Lister, sigs []Signature, thresholdPct float64, linger time.Duration, self int32) *Scanner {
	return &Scanner{L: l, Sigs: sigs, ThresholdPct: thresholdPct, Linger: linger, Self: self,
		prevCPU: map[int32]float64{}, lastBusy: map[string]time.Time{}}
}

// Active returns the names of agents currently working. Call once per tick.
func (s *Scanner) Active(now time.Time) []string {
	procs, err := s.L.List()
	if err != nil {
		return s.lingering(now)
	}
	children := map[int32][]int32{}
	names := map[int32]string{}
	for _, p := range procs {
		children[p.PPID] = append(children[p.PPID], p.PID)
		names[p.PID] = p.Name
	}

	cmdCache := map[int32]string{}
	cmd := func(pid int32) string {
		c, ok := cmdCache[pid]
		if !ok {
			c = strings.ToLower(s.L.Cmdline(pid))
			cmdCache[pid] = c
		}
		return c
	}

	// roots per signature
	roots := map[string][]int32{}
	for _, p := range procs {
		if p.PID == s.Self || p.PID == 0 {
			continue
		}
		n := norm(p.Name)
		for _, sig := range s.Sigs {
			match := false
			for _, pn := range sig.ProcessNames {
				if n == norm(pn) {
					match = true
					break
				}
			}
			if !match && runtimes[n] && len(sig.CmdContains) > 0 {
				c := cmd(p.PID)
				for _, sub := range sig.CmdContains {
					if sub != "" && strings.Contains(c, strings.ToLower(sub)) {
						match = true
						break
					}
				}
			}
			if match {
				roots[sig.Name] = append(roots[sig.Name], p.PID)
			}
		}
	}

	// cumulative CPU for root + all descendants
	nowCPU := map[int32]float64{}
	sigPIDs := map[string][]int32{}
	for name, rs := range roots {
		seen := map[int32]bool{}
		var walk func(pid int32, depth int)
		walk = func(pid int32, depth int) {
			if seen[pid] || depth > 12 {
				return
			}
			seen[pid] = true
			if _, ok := nowCPU[pid]; !ok {
				if c, err := s.L.CPU(pid); err == nil {
					nowCPU[pid] = c
				}
			}
			for _, ch := range children[pid] {
				walk(ch, depth+1)
			}
		}
		for _, r := range rs {
			walk(r, 0)
		}
		for pid := range seen {
			sigPIDs[name] = append(sigPIDs[name], pid)
		}
	}

	if !s.prevAt.IsZero() {
		dt := now.Sub(s.prevAt).Seconds()
		if dt > 0 {
			for name, pids := range sigPIDs {
				var delta float64
				for _, pid := range pids {
					cur, ok := nowCPU[pid]
					prev, had := s.prevCPU[pid]
					if ok && had && cur > prev {
						delta += cur - prev
					}
				}
				if delta/dt*100 >= s.ThresholdPct {
					s.lastBusy[name] = now
				}
			}
		}
	}
	s.prevCPU = nowCPU
	s.prevAt = now
	return s.lingering(now)
}

func (s *Scanner) lingering(now time.Time) []string {
	var out []string
	for name, t := range s.lastBusy {
		if now.Sub(t) <= s.Linger {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Ancestors returns the chain of parent processes of pid, nearest first.
func Ancestors(l Lister, pid int32) []Proc {
	procs, err := l.List()
	if err != nil {
		return nil
	}
	by := map[int32]Proc{}
	for _, p := range procs {
		by[p.PID] = p
	}
	var chain []Proc
	cur := pid
	for i := 0; i < 10; i++ {
		p, ok := by[cur]
		if !ok || p.PPID == 0 || p.PPID == cur {
			break
		}
		parent, ok := by[p.PPID]
		if !ok {
			break
		}
		chain = append(chain, parent)
		cur = parent.PID
	}
	return chain
}
