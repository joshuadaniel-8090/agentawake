package detect

import (
	"testing"
	"time"
)

type fakeLister struct {
	procs []Proc
	cpu   map[int32]float64
	cmd   map[int32]string
}

func (f *fakeLister) List() ([]Proc, error)        { return f.procs, nil }
func (f *fakeLister) CPU(p int32) (float64, error) { return f.cpu[p], nil }
func (f *fakeLister) Cmdline(p int32) string       { return f.cmd[p] }

func newFake() *fakeLister {
	return &fakeLister{
		procs: []Proc{
			{PID: 1, PPID: 0, Name: "explorer.exe"},
			{PID: 10, PPID: 1, Name: "node.exe"}, // codex via npm
			{PID: 11, PPID: 10, Name: "node.exe"},
			{PID: 20, PPID: 1, Name: "node.exe"}, // unrelated node
			{PID: 30, PPID: 1, Name: "agentawake.exe"},
		},
		cpu: map[int32]float64{10: 5, 11: 5, 20: 100},
		cmd: map[int32]string{
			10: `node C:\Users\x\AppData\npm\node_modules\@openai\codex\bin\codex.js`,
			20: `node server.js`,
		},
	}
}

func TestScanner_DetectsCPUActivityNotJustPresence(t *testing.T) {
	f := newFake()
	sc := NewScanner(f, DefaultSignatures(), 3, 20*time.Second, 30)
	t0 := time.Now()
	f.cmd[10] = `node /usr/lib/node_modules/@openai/codex/bin/codex.js` // slash form too

	if got := sc.Active(t0); len(got) != 0 {
		t.Fatalf("first scan has no baseline: %v", got)
	}
	// open at an idle prompt: process exists, CPU flat
	if got := sc.Active(t0.Add(3 * time.Second)); len(got) != 0 {
		t.Fatalf("idle agent must not count: %v", got)
	}
	// a child process burns 1.0 s of CPU in 3 s (33%)
	f.cpu[11] += 1.0
	if got := sc.Active(t0.Add(6 * time.Second)); len(got) != 1 || got[0] != "codex" {
		t.Fatalf("busy agent should be detected: %v", got)
	}
	// quiet again but inside the linger window
	if got := sc.Active(t0.Add(12 * time.Second)); len(got) != 1 {
		t.Fatalf("should linger through short gaps: %v", got)
	}
	// quiet past the linger window
	if got := sc.Active(t0.Add(40 * time.Second)); len(got) != 0 {
		t.Fatalf("should go idle after linger: %v", got)
	}
}

func TestScanner_IgnoresUnrelatedNodeAndSelf(t *testing.T) {
	f := newFake()
	sc := NewScanner(f, DefaultSignatures(), 3, 20*time.Second, 30)
	t0 := time.Now()
	sc.Active(t0)
	f.cpu[20] += 10 // unrelated node is very busy
	f.cpu[30] += 10 // so is agentawake itself
	if got := sc.Active(t0.Add(3 * time.Second)); len(got) != 0 {
		t.Fatalf("unrelated processes must not count: %v", got)
	}
}

func TestScanner_BelowThresholdIsIdle(t *testing.T) {
	f := newFake()
	f.cmd[10] = "node codex @openai/codex"
	sc := NewScanner(f, DefaultSignatures(), 3, 5*time.Second, 30)
	t0 := time.Now()
	sc.Active(t0)
	f.cpu[10] += 0.03 // 1% of a core over 3 s
	if got := sc.Active(t0.Add(3 * time.Second)); len(got) != 0 {
		t.Fatalf("1%% cpu is below threshold: %v", got)
	}
}

func TestScanner_ProcessNameMatch(t *testing.T) {
	f := &fakeLister{
		procs: []Proc{{PID: 1, PPID: 0, Name: "System"}, {PID: 50, PPID: 1, Name: "Aider.EXE"}},
		cpu:   map[int32]float64{50: 0}, cmd: map[int32]string{},
	}
	sc := NewScanner(f, DefaultSignatures(), 3, 5*time.Second, 99)
	t0 := time.Now()
	sc.Active(t0)
	f.cpu[50] = 2
	if got := sc.Active(t0.Add(2 * time.Second)); len(got) != 1 || got[0] != "aider" {
		t.Fatalf("name match is case/.exe insensitive: %v", got)
	}
}

func TestMerge_OverridesAndAdds(t *testing.T) {
	m := Merge(DefaultSignatures(), []Signature{
		{Name: "codex", ProcessNames: []string{"mycodex"}},
		{Name: "custom", ProcessNames: []string{"foo"}},
	})
	var codex, custom bool
	for _, s := range m {
		if s.Name == "codex" && len(s.ProcessNames) == 1 && s.ProcessNames[0] == "mycodex" {
			codex = true
		}
		if s.Name == "custom" {
			custom = true
		}
	}
	if !codex || !custom {
		t.Fatalf("merge wrong: %+v", m)
	}
}

func TestAncestors(t *testing.T) {
	f := newFake()
	chain := Ancestors(f, 11)
	if len(chain) < 2 || chain[0].PID != 10 || chain[1].PID != 1 {
		t.Fatalf("bad chain: %+v", chain)
	}
}
