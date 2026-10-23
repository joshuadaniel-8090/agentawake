// agentawake keeps a Windows laptop awake only while an AI coding agent is working.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"agentawake/internal/claude"
	"agentawake/internal/config"
	"agentawake/internal/detect"
	"agentawake/internal/engine"
	"agentawake/internal/platform"
	"agentawake/internal/state"
)

var version = "0.1.0-dev"

const usage = `agentawake - keep the PC awake only while an AI coding agent is working

Usage: agentawake <command>

Daemon
  start                 start the background daemon (hidden, no console window)
  stop                  stop it cleanly (restores the lid setting)
  run [--dry-run] [--lid-guard] [-v]
                        run in the foreground. --dry-run simulates the machine (any OS)
  status [--json]       what it is doing right now, and why

Control
  hold <duration>       force awake, e.g. 90m or 2h (max 24h). "hold off" cancels
  pause / resume        turn automatic holding off / on
  release               cancel a manual hold

Claude Code
  install-hooks         add hooks to ~/.claude/settings.json (backs it up first)
  uninstall-hooks       remove only the hooks agentawake added
  hook <Event>          used by Claude Code, not by you

Setup and repair
  install-startup       start the daemon at login
  uninstall-startup
  restore-lid           put the lid-close setting back if a crash left it changed
  doctor                check the setup and explain any problem
  config init|path      create or locate the optional config file
  version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	code := 0
	switch cmd {
	case "hook":
		cmdHook(args) // always exits 0
	case "run":
		code = cmdRun(args)
	case "start":
		code = cmdStart()
	case "stop":
		code = cmdStop()
	case "status":
		code = cmdStatus(args)
	case "hold":
		code = cmdHold(args)
	case "release":
		code = withStore(func(s *state.Store) int { s.ClearHold(); fmt.Println("manual hold cancelled"); return 0 })
	case "pause":
		code = withStore(func(s *state.Store) int {
			_ = s.SetPaused(true)
			fmt.Println("paused: not holding the PC awake")
			return 0
		})
	case "resume":
		code = withStore(func(s *state.Store) int { _ = s.SetPaused(false); fmt.Println("resumed"); return 0 })
	case "install-hooks":
		code = cmdInstallHooks()
	case "uninstall-hooks":
		code = cmdUninstallHooks()
	case "install-startup":
		code = cmdStartup(true)
	case "uninstall-startup":
		code = cmdStartup(false)
	case "restore-lid":
		code = cmdRestoreLid()
	case "doctor":
		code = cmdDoctor()
	case "config":
		code = cmdConfig(args)
	case "version", "-v", "--version":
		fmt.Println("agentawake", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		code = 2
	}
	os.Exit(code)
}

func withStore(f func(*state.Store) int) int {
	s, err := state.Open(state.DefaultDir())
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot open state folder:", err)
		return 1
	}
	return f(s)
}

// ---- hook ----------------------------------------------------------------

// cmdHook is run by Claude Code on every event. It must never block or fail the agent:
// any error is swallowed and the exit code is always 0.
func cmdHook(args []string) {
	defer func() { _ = recover(); os.Exit(0) }()
	ev := ""
	if len(args) > 0 {
		ev = args[0]
	}
	store, err := state.Open(state.DefaultDir())
	if err != nil {
		return
	}
	_ = claude.Handle(store, ev, os.Stdin, time.Now(), func() int32 {
		return claude.FindAgentPID(detect.NewLister(), int32(os.Getpid()))
	})
}

// ---- daemon --------------------------------------------------------------

func cmdRun(args []string) int {
	dry, forceLid, verbose := false, false, false
	for _, a := range args {
		switch a {
		case "--dry-run":
			dry = true
		case "--lid-guard":
			forceLid = true
		case "-v", "--verbose":
			verbose = true
		default:
			fmt.Fprintln(os.Stderr, "unknown flag:", a)
			return 2
		}
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error in %s: %v\n", config.Path(), err)
		return 1
	}
	if forceLid {
		cfg.LidGuard = true
	}
	store, err := state.Open(state.DefaultDir())
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot open state folder:", err)
		return 1
	}
	if pid := store.ReadPID(); pid != 0 && pid != os.Getpid() && platform.PidAlive(int32(pid)) {
		fmt.Fprintf(os.Stderr, "already running (pid %d)\n", pid)
		return 1
	}
	store.ClearStop()
	_ = store.WritePID(os.Getpid())
	defer store.ClearPID()

	logw := io.Writer(openLog(store.Dir))
	if verbose || dry {
		logw = io.MultiWriter(logw, os.Stderr)
	}
	log := slog.New(slog.NewTextHandler(logw, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var sys platform.System
	closer := platform.Closer(func() {})
	if dry {
		sys = platform.NewDry(store.Dir, log)
	} else if sys, closer, err = platform.NewSystem(); err != nil {
		fmt.Fprintln(os.Stderr, "cannot start:", err)
		return 1
	}
	defer closer()

	lister := detect.NewLister()
	sources := []engine.Source{
		engine.HookSource{Store: store, TTL: time.Duration(cfg.SessionTTLSeconds) * time.Second,
			ToolTTL: time.Duration(cfg.ToolTTLSeconds) * time.Second, PidAlive: platform.PidAlive},
		engine.ManualSource{Store: store},
	}
	if cfg.ProcessDetection {
		sc := detect.NewScanner(lister, detect.Merge(detect.DefaultSignatures(), cfg.Agents),
			cfg.CPUThresholdPercent, time.Duration(cfg.CPULingerSeconds)*time.Second, int32(os.Getpid()))
		sources = append(sources, engine.ScanSource{Scan: sc.Active})
	}
	eng := engine.New(engine.Options{
		Power: sys, Lid: sys, Store: store, Sources: sources, Log: log,
		Grace:        time.Duration(cfg.GraceSeconds) * time.Second,
		MaxHold:      time.Duration(cfg.MaxHoldHours * float64(time.Hour)),
		BatteryFloor: cfg.BatteryFloorPercent,
		LidGuard:     cfg.LidGuard,
		Version:      version,
	})
	log.Info("agentawake started", "version", version, "pid", os.Getpid(), "lid_guard", cfg.LidGuard, "dry_run", dry)
	eng.Recover()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	tick := time.NewTicker(time.Duration(cfg.PollSeconds) * time.Second)
	defer tick.Stop()

	var lastStatus []byte
	var lastWrite, lastPrune time.Time
	for {
		st := eng.Tick()
		cmp := st
		cmp.Updated = 0
		b, _ := json.Marshal(cmp)
		if !bytes.Equal(b, lastStatus) || time.Since(lastWrite) > 10*time.Second {
			_ = store.WriteStatus(st)
			lastStatus, lastWrite = b, time.Now()
		}
		if time.Since(lastPrune) > time.Minute {
			store.PruneSessions(time.Now(), 24*time.Hour)
			lastPrune = time.Now()
		}
		select {
		case <-sig:
			log.Info("signal received, shutting down")
			return shutdown(eng, store, log)
		case <-tick.C:
			if store.StopRequested() {
				log.Info("stop requested, shutting down")
				return shutdown(eng, store, log)
			}
		}
	}
}

func shutdown(eng *engine.Engine, store *state.Store, log *slog.Logger) int {
	eng.Shutdown()
	_ = store.WriteStatus(state.Status{Updated: time.Now().Unix(), Version: version, Note: "daemon stopped"})
	store.ClearStop()
	log.Info("stopped")
	return 0
}

// openLog returns a log file, rotating once at 2 MB (one backup).
func openLog(dir string) io.Writer {
	p := filepath.Join(dir, "agentawake.log")
	if fi, err := os.Stat(p); err == nil && fi.Size() > 2<<20 {
		_ = os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return io.Discard
	}
	return f
}

func cmdStart() int {
	return withStore(func(s *state.Store) int {
		if pid := s.ReadPID(); pid != 0 && platform.PidAlive(int32(pid)) {
			fmt.Printf("already running (pid %d)\n", pid)
			return 0
		}
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		cmd := exec.Command(exe, "run")
		platform.Detach(cmd)
		if err := cmd.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "cannot start daemon:", err)
			return 1
		}
		_ = cmd.Process.Release()
		for i := 0; i < 30; i++ {
			time.Sleep(100 * time.Millisecond)
			if pid := s.ReadPID(); pid != 0 && platform.PidAlive(int32(pid)) {
				fmt.Printf("started (pid %d). Log: %s\n", pid, filepath.Join(s.Dir, "agentawake.log"))
				return 0
			}
		}
		fmt.Fprintln(os.Stderr, "daemon did not come up; run `agentawake run -v` to see why")
		return 1
	})
}

func cmdStop() int {
	return withStore(func(s *state.Store) int {
		pid := s.ReadPID()
		if pid == 0 || !platform.PidAlive(int32(pid)) {
			fmt.Println("not running")
			return cmdRestoreLid() // repair anything a dead daemon left behind
		}
		_ = s.RequestStop()
		for i := 0; i < 100; i++ {
			time.Sleep(100 * time.Millisecond)
			if !platform.PidAlive(int32(pid)) {
				fmt.Println("stopped")
				return 0
			}
		}
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
		s.ClearPID()
		fmt.Println("daemon did not stop in time and was killed; restoring lid setting")
		return cmdRestoreLid()
	})
}

// ---- status --------------------------------------------------------------

func cmdStatus(args []string) int {
	return withStore(func(s *state.Store) int {
		pid := s.ReadPID()
		alive := pid != 0 && platform.PidAlive(int32(pid))
		st, ok := s.ReadStatus()
		if len(args) > 0 && args[0] == "--json" {
			out := map[string]any{"daemon_running": alive, "pid": pid, "status": st}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
			return 0
		}
		if !alive {
			fmt.Println("daemon:    NOT running (start it with `agentawake start`)")
			if _, saved := s.LoadLid(); saved {
				fmt.Println("WARNING:   lid setting was left changed by a crash. Run `agentawake restore-lid`")
			}
			return 1
		}
		fmt.Printf("daemon:    running (pid %d, v%s)\n", pid, st.Version)
		if !ok {
			fmt.Println("status:    no status yet")
			return 0
		}
		if age := time.Since(time.Unix(st.Updated, 0)); age > 30*time.Second {
			fmt.Printf("WARNING:   status is %s old, daemon may be stuck\n", age.Round(time.Second))
		}
		switch {
		case st.Paused:
			fmt.Println("state:     PAUSED (not holding)")
		case st.Holding:
			fmt.Println("state:     HOLDING the PC awake")
		default:
			fmt.Println("state:     idle (normal Windows sleep rules)")
		}
		for _, r := range st.Reasons {
			fmt.Println("  because: ", r)
		}
		lid := "open"
		if st.LidClosed {
			lid = "closed"
		}
		guard := "off"
		if st.LidGuard {
			guard = "on"
			if st.LidOverride {
				guard = "on, ACTIVE (lid close does nothing right now)"
			}
		}
		fmt.Printf("lid:       %s   lid guard: %s\n", lid, guard)
		if st.SleepAt != 0 {
			fmt.Println("sleep:     scheduled at", time.Unix(st.SleepAt, 0).Local().Format("15:04:05"))
		}
		if st.Note != "" {
			fmt.Println("note:     ", st.Note)
		}
		return 0
	})
}

// ---- hold ----------------------------------------------------------------

func cmdHold(args []string) int {
	return withStore(func(s *state.Store) int {
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "usage: agentawake hold <duration>   e.g. 90m, 2h, 1h30m   (or: hold off)")
			return 2
		}
		if args[0] == "off" || args[0] == "0" {
			s.ClearHold()
			fmt.Println("manual hold cancelled")
			return 0
		}
		d, err := time.ParseDuration(args[0])
		if err != nil || d <= 0 {
			fmt.Fprintln(os.Stderr, "bad duration:", args[0])
			return 2
		}
		if d > 24*time.Hour {
			fmt.Fprintln(os.Stderr, "max manual hold is 24h")
			return 2
		}
		until := time.Now().Add(d)
		if err := s.SetHold(until); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("holding awake until", until.Format("15:04"))
		if pid := s.ReadPID(); pid == 0 || !platform.PidAlive(int32(pid)) {
			fmt.Println("note: daemon is not running. Start it with `agentawake start` or this does nothing.")
		}
		return 0
	})
}

// ---- hooks install -------------------------------------------------------

func cmdInstallHooks() int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if strings.Contains(strings.ToLower(exe), "go-build") {
		fmt.Fprintln(os.Stderr, "refusing to install hooks that point at a temporary `go run` binary; build with `go build` first")
		return 1
	}
	path := claude.SettingsPath()
	changed, backup, err := claude.Install(path, exe)
	if err != nil {
		fmt.Fprintln(os.Stderr, "install-hooks failed:", err)
		return 1
	}
	if !changed {
		fmt.Println("hooks already installed in", path)
		return 0
	}
	fmt.Println("hooks installed in", path)
	if backup != "" {
		fmt.Println("backup of your old settings:", backup)
	}
	fmt.Println("restart running Claude Code sessions to pick them up")
	return 0
}

func cmdUninstallHooks() int {
	path := claude.SettingsPath()
	changed, backup, err := claude.Uninstall(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "uninstall-hooks failed:", err)
		return 1
	}
	if !changed {
		fmt.Println("no agentawake hooks found in", path)
		return 0
	}
	fmt.Println("agentawake hooks removed from", path)
	if backup != "" {
		fmt.Println("backup:", backup)
	}
	return 0
}

// ---- startup -------------------------------------------------------------

const runKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`

func cmdStartup(install bool) int {
	if runtime.GOOS != "windows" {
		fmt.Fprintln(os.Stderr, "start-at-login is Windows only")
		return 1
	}
	var cmd *exec.Cmd
	if install {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		cmd = exec.Command("reg", "add", runKey, "/v", "agentawake", "/t", "REG_SZ", "/d", fmt.Sprintf(`"%s" start`, exe), "/f")
	} else {
		cmd = exec.Command("reg", "delete", runKey, "/v", "agentawake", "/f")
	}
	platform.Detach(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "failed: %v: %s\n", err, strings.TrimSpace(string(out)))
		return 1
	}
	if install {
		fmt.Println("agentawake will start at login")
	} else {
		fmt.Println("start-at-login removed")
	}
	return 0
}

// ---- restore-lid ---------------------------------------------------------

func cmdRestoreLid() int {
	return withStore(func(s *state.Store) int {
		orig, ok := s.LoadLid()
		if !ok {
			fmt.Println("lid setting is not changed by agentawake, nothing to restore")
			return 0
		}
		sys, closer, err := platform.NewSystem()
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot restore:", err)
			return 1
		}
		defer closer()
		if err := sys.Set(orig.AC, orig.DC); err != nil {
			fmt.Fprintln(os.Stderr, "restore failed:", err)
			fmt.Fprintf(os.Stderr, "set it by hand: Power Options > lid close action. Your original values: plugged in=%d, battery=%d (0 nothing, 1 sleep, 2 hibernate, 3 shut down)\n", orig.AC, orig.DC)
			return 1
		}
		s.ClearLid()
		fmt.Printf("lid action restored (plugged in=%d, battery=%d)\n", orig.AC, orig.DC)
		return 0
	})
}

// ---- config --------------------------------------------------------------

func cmdConfig(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: agentawake config init|path")
		return 2
	}
	switch args[0] {
	case "path":
		fmt.Println(config.Path())
	case "init":
		p, err := config.WriteDefault()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("wrote", p)
	default:
		fmt.Fprintln(os.Stderr, "usage: agentawake config init|path")
		return 2
	}
	return 0
}

// ---- doctor --------------------------------------------------------------

func cmdDoctor() int {
	bad := 0
	ok := func(f string, a ...any) { fmt.Printf("  ok    %s\n", fmt.Sprintf(f, a...)) }
	warn := func(f string, a ...any) { bad++; fmt.Printf("  WARN  %s\n", fmt.Sprintf(f, a...)) }
	info := func(f string, a ...any) { fmt.Printf("  info  %s\n", fmt.Sprintf(f, a...)) }

	fmt.Println("agentawake", version, runtime.GOOS+"/"+runtime.GOARCH)
	if runtime.GOOS != "windows" {
		warn("not Windows: only --dry-run works here")
	}
	dir := state.DefaultDir()
	s, err := state.Open(dir)
	if err != nil {
		warn("state folder %s: %v", dir, err)
		return 1
	}
	ok("state folder %s", dir)

	cfg, err := config.Load()
	if err != nil {
		warn("config %s is broken: %v", config.Path(), err)
	} else if _, statErr := os.Stat(config.Path()); statErr == nil {
		ok("config loaded from %s", config.Path())
	} else {
		info("no config file (defaults in use). `agentawake config init` creates one")
	}

	if pid := s.ReadPID(); pid != 0 && platform.PidAlive(int32(pid)) {
		ok("daemon running (pid %d)", pid)
	} else {
		warn("daemon not running. `agentawake start`")
	}
	if _, saved := s.LoadLid(); saved {
		warn("lid setting is currently changed by agentawake. If no agent is running, run `agentawake restore-lid`")
	}

	present, missing, err := claude.Installed(claude.SettingsPath())
	switch {
	case err != nil:
		warn("Claude Code settings: %v", err)
	case len(missing) == 0:
		ok("Claude Code hooks installed (%s)", strings.Join(present, ", "))
	case len(present) == 0:
		warn("Claude Code hooks not installed. `agentawake install-hooks`")
	default:
		warn("Claude Code hooks incomplete, missing: %s. Run `agentawake install-hooks`", strings.Join(missing, ", "))
	}

	for _, l := range platform.Diagnostics() {
		info("%s", l)
	}
	if err == nil && cfg.LidGuard {
		info("lid guard is ON: a closed lid keeps running while an agent works. Use it on a desk, not in a bag (heat)")
	} else {
		info("lid guard is off (opt in with \"lid_guard\": true in config, or `run --lid-guard`)")
	}
	var names []string
	for _, sg := range detect.Merge(detect.DefaultSignatures(), cfg.Agents) {
		names = append(names, sg.Name)
	}
	info("CPU-detected agents: %s. Claude Code is detected through hooks", strings.Join(names, ", "))

	if bad == 0 {
		fmt.Println("all good")
		return 0
	}
	fmt.Printf("%d thing(s) to look at\n", bad)
	return 1
}
