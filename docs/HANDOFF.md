# agentawake: project handoff

Written 2026-10-23. Everything needed to continue this project from zero context: what it is, why, every decision and who made it, exactly what exists, what was and was not verified, known bugs, and the ordered plan for what to do next.

Owner: Joshua Daniel. Built in one Claude session (2026-10-21 to 2026-10-23).

---

## 0. Read this first (60-second version)

- **What:** `agentawake` is a free, MIT-licensed, single-file Windows tool (Go, no dependencies). It keeps the PC awake only while an AI coding agent (Claude Code first, then Codex, Gemini CLI, etc.) is actively working. When the agent stops, Windows' normal sleep rules take over. Optional "lid guard" keeps a laptop running with the lid closed during a run, then restores the setting and puts the laptop to sleep when the run ends.
- **State:** v0.1.0 code complete, 41 unit tests pass, Windows x64 and ARM64 binaries build and `go vet` clean. A simulated end-to-end run (dry-run mode) works.
- **Not done:** it has **never run on real Windows**. Every Windows power call (wake request, lid events, `powercfg`, sleep) is unverified. No git repo exists yet. Nothing is published.
- **Biggest known risk:** after you press Esc to interrupt Claude Code, the `Stop` hook probably does not fire, so the PC may stay awake for up to 10 minutes (up to 60 if a tool call was running). Fix designed in section 9, not built.
- **Next three actions:** (1) `git init`, push to GitHub, get CI green; (2) run `docs/WINDOWS-TEST-CHECKLIST.md` on your own laptop; (3) fix the Esc/stuck-busy problem. Then release hardening (section 9).
- **The sandbox this was built in is temporary.** The only durable copy of the code is the source zip that was sent to you. Put it in git today.

---

## 1. Why this exists (history of the idea)

1. Joshua wanted Windows to not sleep **only while** `claude`, `codex`, `cursor` style agents run, and to follow normal sleep (e.g. after 5 min) once they stop. First answer was a PowerShell script polling for processes and calling `SetThreadExecutionState`.
2. He decided to ship it free as an add-on for agentic coding tools and asked if there was real demand.
3. Web search showed the space is already crowded (see section 12). Demand is proven, differentiation is not automatic. Decision: build anyway, but with a real angle: accurate working-vs-idle detection, correct post-finish sleep, and a lid mode that restores itself.
4. Product was specced in conversation. Key correction mid-way: an earlier claim that "no app can prevent lid sleep" was wrong. A wake request cannot, but changing the lid-close action with `powercfg` can. That became "lid guard".
5. Key design insight: Windows only reacts to the lid at the instant it closes. If the lid is closed when the agent finishes, restoring the setting does nothing, so the app must **sleep the PC itself**. Joshua proposed the same ("log the lid close, then after the agent is done sleep manually").
6. A team design brief with flowcharts was published as a private artifact (section 11).
7. Stack chosen: Go. WSL support explicitly deferred ("not for WSL now, we'll do that later").
8. v0.1 built and tested in a Linux sandbox with Windows cross-compilation.

---

## 2. Product definition

**Problem.** Windows sleeps on idle *input*. A coding agent working for an hour looks idle, so the laptop sleeps and the run dies. Existing keep-awake toggles stay on after the agent finishes. Closing the lid sleeps the laptop regardless of what runs.

**Goal.** Awake exactly while an agent works. Normal Windows sleep rules otherwise, with the idle timer counted from the moment the agent finished. Optional closed-lid operation with automatic, crash-safe return to normal.

**Behaviour table**

| Situation | Behaviour |
|---|---|
| Agent working | Wake request held (visible in `powercfg /requests`) |
| Agent finished / idle at prompt | Request released; Windows idle timer applies |
| Lid guard on, agent working | Lid-close action set to Do nothing (original saved to disk first) |
| Lid guard on, agent finishes, lid closed | Original restored; PC sleeps after grace (default 30 s) via `SetSuspendState` |
| Lid guard on, agent finishes, lid open | Original restored; Windows timer applies; no forced sleep |
| Lid closed then reopened mid-run | Lid state **at finish** decides; no sleep if open |
| Agent resumes during grace | Sleep cancelled |
| User's original lid action was Do nothing | Never force sleep |
| Original action hibernate (2) | Calls hibernate |
| Original action shut down (3) | Does nothing (never shuts the PC down) |
| Unplugged and battery below floor (20%) | Stop holding; if lid guard was active and lid closed, sleep immediately |
| Continuous hold longer than 8 h | Force release until the agent goes idle once |
| Paused (`agentawake pause`) | Never hold |

**Non-goals for v0.1:** tray UI, WSL, macOS, Linux, GUI settings, auto-update, telemetry, any network access, killing or controlling agents, preventing display sleep.

**Name:** `agentawake` (working name, confirmed by default, not strongly decided). **License:** MIT, copyright "Joshua Daniel" (taken from profile; confirm). **Price:** free.

---

## 3. Decisions log

| # | Decision | Who | Reason / note |
|---|---|---|---|
| D1 | Build it, free, open source | Joshua | Portfolio and audience, not revenue |
| D2 | Go, single static `.exe` | Joshua ("let's use go") | No runtime, easy cross-compile, small |
| D3 | Native Windows only; WSL later | Joshua | Scope |
| D4 | CLI only in v0.1; tray later | Claude default (Joshua did not answer) | Scope. **Confirm** |
| D5 | Name `agentawake` | Claude default. **Confirm** | |
| D6 | Lid guard included, **off by default** | Claude default. **Confirm** | Heat risk, admin risk |
| D7 | Zero third-party Go dependencies | Claude | `proxy.golang.org` returned 403 in the build sandbox (policy denial, not worked around). Bonus: smaller exe, fewer antivirus false positives, no supply chain |
| D8 | Claude Code detected by **hooks only**, not process name | Claude | Claude desktop app has the same process name, would cause false positives |
| D9 | Other agents detected by process tree CPU time, not mere presence | Claude | An agent idle at a prompt must not count |
| D10 | JSON config, not TOML | Claude | No dependency available; stdlib only |
| D11 | File-based IPC (no sockets/pipes) | Claude | Simple, crash-safe, hook is a separate short-lived process |
| D12 | Lid setting changed with `powercfg`, original saved to disk first | Claude | Crash repair |
| D13 | App sleeps the PC itself when finishing with lid closed | Joshua + Claude | Windows ignores a restored lid setting for an already-closed lid |
| D14 | Hooks installed by merging into `~/.claude/settings.json` with backup | Claude | Never clobber user hooks |
| D15 | Run-key (`HKCU\...\Run`) for start-at-login, via `reg.exe` | Claude | No registry-write API in stdlib `syscall` |

---

## 4. Where everything is

### 4.1 Files delivered to Joshua (durable)

| File | SHA-256 |
|---|---|
| `agentawake_0.1.0_windows_amd64.zip` (exe, LICENSE, README, test checklist) | `c051b98e2cc6657fb0ef604ee3935d17c86b3e682be92a3953bd7355bf7061d1` |
| `agentawake_0.1.0_windows_arm64.zip` | `46c41bb9c446f9979bc7dd224835023fef3a7a20ef1d04ab5085892997015957` |
| `agentawake_0.1.0_source.zip` (all source, docs, CI, plugin; no binaries) | changes whenever regenerated |
| `HANDOFF.md` (this file; also inside the newest source zip at `docs/HANDOFF.md`) | |

The ARM64 build was never run anywhere. The amd64 build was never run on Windows.

### 4.2 Sandbox (temporary, will be lost)

Project root was `/home/claude/agentawake`. Build outputs and smoke-test material were under the session scratchpad. **Do not rely on any of it.** Unzip the source zip.

### 4.3 Published artifact

Team design brief (private, share via its Share menu): `https://claude.ai/artifact/EYRKgCyLAMJyh9kdW92Cf6`. Three diagrams (components, decision loop, lid timelines) plus reasoning. **Two inaccuracies to fix in it:** it says the loop runs every 2 s (default is 3 s) and lists tray icon and "lid guard in v0.1?" as open questions (defaults chosen, see D4/D6).

### 4.4 Not done: version control

There is **no git repo**, no GitHub repo, no remote. The module path is plain `agentawake`. If the project is published, rename the module to `github.com/<owner>/agentawake` (a `sed` over imports in all `.go` files and `go.mod`).

---

## 5. Architecture

### 5.1 Data flow

```
 Claude Code ──hook(stdin JSON)──► agentawake hook <Event> ──► sessions/<id>.json ┐
 you ── agentawake hold 2h ─────► hold.json ──────────────────────────────────────┤
 you ── agentawake pause ───────► paused (flag file) ─────────────────────────────┤  files
                                                                                   ▼
 process list + CPU ◄── detect.Scanner ◄─────────────┐                     ┌───────────────┐
                                                      └─── Sources ──────► │ engine.Tick() │ every 3 s
 Windows lid event ──► platform.Win32 (hidden window) ─► Lid.Closed() ───►  │  (the daemon) │
                                                                             └──────┬────────┘
              ┌──────────────────────────────────────────────────────────────────────┤
              ▼                         ▼                          ▼                  ▼
   PowerCreateRequest/       powercfg SUB_BUTTONS        SetSuspendState      status.json, log
   PowerSetRequest           LIDACTION (AC+DC)           (sleep/hibernate)
```

One daemon makes every decision and is the only thing that touches Windows power settings. Inputs only write small files or get sampled.

### 5.2 Decision loop (`engine.Tick`, exact logic)

```
now = clock
paused = store.Paused()
reasons = [] ; if !paused: for each Source: reasons += source.Active(now)
raw = len(reasons) > 0
if !raw: capped = false
(onBat, pct, ok) = power.Battery() ; lowBat = ok && onBat && floor>0 && pct < floor
working = raw
  paused                         → working=false, note="paused"
  raw && lowBat                  → working=false, note="battery below floor"
  raw && capped                  → working=false, note="max hold reached..."
  raw && holding && now-holdStart > maxHold → capped=true, working=false
if working:
    sleepAt = none
    if !holding: power.SetAwake(true) → holding=true, holdStart=now, lidBroken=false
    if holding: applyLid()          # only if lid_guard on
else:
    release(now, lowBat)
write Status (reasons, holding, lid state, sleepAt, note)
```

`applyLid()`: no-op unless lid_guard and not already applied and not `lidBroken`. If lid unavailable → `lidBroken`, note "lid guard unavailable". If no saved original exists: read current values (`Get`), save to `lid-original.json` **before** changing anything (never overwrite an existing saved copy: it is the user's real value). Then `Set(0,0)`. On failure: restore, mark `lidBroken` (no retry storm), note "lid guard failed ... (wake request only)".

`release(now, lowBat)`:
```
wasHolding, wasLid = holding, lidApplied
if holding: power.SetAwake(false); holding=false
orig = (wasLid ? store.LoadLid() : none)
if lidApplied: restoreLid()           # Set(orig.AC, orig.DC); clear file only on success
if wasHolding && wasLid && lid.Closed() && sleepAt unset:
    action = orig.AC  (orig.DC if on battery)
    action 1 → sleep, 2 → hibernate : sleepAt = now + (lowBat ? 0 : grace)
    other (0 nothing, 3 shutdown) → leave alone
if sleepAt set:
    lid open now         → cancel
    now >= sleepAt       → power.Sleep(hibernate) ; clear
    else                 → note "lid closed, sleeping in Ns"
```

`Recover()` (startup): if `lid-original.json` exists, restore it. `Shutdown()` (stop/signal): release wake request and restore lid; **never sleeps the machine**.

### 5.3 Packages

| Path | Responsibility | OS calls? | Tests |
|---|---|---|---|
| `cmd/agentawake/main.go` (623 lines) | CLI, daemon loop, start/stop, status, hold, doctor, install-hooks, startup, restore-lid, config | via platform | none (manual dry-run only) |
| `internal/engine/engine.go` | Decision loop, lid guard, sleep logic, safety limits | none (interfaces `Power`, `Lid`, `Source`) | 20 tests with fakes |
| `internal/engine/sources.go` | `HookSource` (sessions, TTL, PID alive), `ManualSource` (hold file), `ScanSource` (adapter) | none | covered |
| `internal/detect/detect.go` | `Scanner`: signature matching, descendant CPU deltas, linger, `Merge`, `Ancestors` | none (interface `Lister`) | 6 tests |
| `internal/detect/procs_windows.go` | Toolhelp32 process list, `GetProcessTimes` CPU, PEB command-line read | yes | none (needs Windows) |
| `internal/detect/procs_linux.go` | `/proc` reader for dev/tests | yes | used by dry-run |
| `internal/detect/procs_other.go` | stub | | |
| `internal/platform/windows.go` (334 lines) | Win32: wake request, `SetSuspendState`, battery, `powercfg` lid get/set, hidden window + lid power-setting notification, `PidAlive`, `Detach`, `Diagnostics` | yes | none (needs Windows) |
| `internal/platform/platform.go` | `System` interface, `Dry` simulator (lid via `<dir>/dry-lid-closed` file) | no | exercised by smoke |
| `internal/platform/other.go` | non-Windows stub, `PidAlive`, `Detach` | | |
| `internal/state/state.go` | All state files, atomic writes with rename retry, session id sanitising | file I/O | 5 tests |
| `internal/config/config.go` | JSON config over defaults, clamping, `WriteDefault` | file I/O | none |
| `internal/claude/hook.go` | Hook event handler, agent PID discovery | no | 3 tests |
| `internal/claude/settings.go` | Safe install/uninstall/inspect of `~/.claude/settings.json` | file I/O | 7 tests |
| `plugin/` | Claude Code plugin manifest + `hooks.json` (**format unverified**) | | |
| `scripts/windows-smoke.ps1` | Real-Windows CI test of the wake request | | CI only |
| `.github/workflows/ci.yml`, `release.yml` | CI (ubuntu+windows, race), Windows smoke, tag-triggered release | | |
| `docs/WINDOWS-TEST-CHECKLIST.md` | 23-step hardware test with expected results | | |

Totals: 3,346 lines of Go (about 840 are tests), 41 tests, standard library only, Go 1.24.7 (`go.mod`).

### 5.4 State files (`%LOCALAPPDATA%\agentawake`, override `AGENTAWAKE_HOME`)

| File | Written by | Read by | Content |
|---|---|---|---|
| `sessions/<id>.json` | `hook` | daemon | `{id, agent, pid, busy, tool, updated}`; id sanitised to `[A-Za-z0-9_-]`, max 64 chars |
| `hold.json` | `hold` | daemon | `{until: unix}` |
| `paused` | `pause`/`resume` | daemon | empty flag file |
| `stop` | `stop` | daemon | empty flag file, checked every tick |
| `daemon.pid` | daemon | CLI | pid (single-instance check uses `PidAlive`) |
| `lid-original.json` | daemon | daemon, `restore-lid`, `status`, `doctor` | `{ac, dc, saved_at}`; exists **only while the lid override is active** |
| `status.json` | daemon | `status` | holding, reasons, paused, capped, lid_closed, lid_guard, lid_override, sleep_at, note, version. Written on change or every 10 s |
| `agentawake.log` (+`.1`) | daemon | humans | slog text; rotated once at 2 MB |
| `dry-lid-closed` | you (dry-run only) | `Dry` platform | presence = lid closed |

All JSON files are written to a temp name then renamed (5 retries, 15 ms) so readers never see partial files.

### 5.5 Hook mapping

| Claude Code event | Effect |
|---|---|
| `UserPromptSubmit` | session busy, `tool=false` |
| `PreToolUse` | session busy, `tool=true` (long-tool expiry applies) |
| `PostToolUse` | session busy, `tool=false` |
| `Stop` | session idle (`busy=false, tool=false`) |
| `SessionEnd` | session file deleted |

Installed entries: command `"<exe with forward slashes>" hook <Event>`, `timeout: 5`, `matcher: "*"` on the two tool events. The hook command **always exits 0**, recovers from panics, and the agent PID is looked up once per session (walk up to 10 ancestors for `claude`/`claude-code`, or `node`/`bun` whose command line contains `claude`; if none found `pid=0` and only the TTL applies). The direct parent is deliberately not used as a fallback because it is usually a short-lived shell.

Expiry: a hook session counts only while `now - updated <= session_ttl` (600 s) or `<= tool_ttl` (3600 s) when `tool=true`, **and** (if `pid != 0`) the process is alive.

### 5.6 CPU-based detection (fallback for other agents)

| Rail | Mechanism |
|---|---|
| Hook can't hurt the agent | Always exit 0, panic recover, 5 s timeout, 1 MB stdin cap |
| Killed agent can't hold PC | Heartbeat expiry + PID liveness |
| Lid never stays changed | Original saved to disk first; restored on release, shutdown, next start; `restore-lid`; `status`/`doctor` warn if stale |
| Failed `powercfg` | Falls back to wake-request-only, tells user in `status` |
| Runaway hold | 8 h cap per continuous hold |
| Battery | No hold below floor; immediate sleep if lid closed |
| Never sleep when user chose Do nothing | Checks saved original |
| Never shut down | Action 3 ignored |
| Settings file safety | Backup, refuse invalid JSON / unknown shapes, idempotent, uninstall removes only own entries |
| Single instance | pid file + liveness |

---

## 6. What is done (with evidence)

| Item | Evidence |
|---|---|
| All v0.1 code in section 5 | Builds for linux/amd64, windows/amd64, windows/arm64, windows/386 (`go vet` clean for all) |
| 41 unit tests | Pass with `-race -count=1`. Lid/sleep scenarios, battery, cap, pause, crash recovery, lid-guard failure fallback, hook TTL incl. long tool call, scanner CPU logic, settings install/uninstall/idempotent/refusal cases, state atomicity and id sanitising |
| End-to-end simulation | Dry-run daemon + real CLI + simulated hooks: hold → lid override applied → fake lid closed → Stop hook → restored → scheduled sleep → "SLEEP" after grace → clean stop → saved lid file gone |
| Crash recovery simulation | `kill -9` with override active: `lid-original.json` left, `status` warns, next `run` logs "restoring lid action left over" and clears it |
| Hook install on a settings file with existing hooks | User's own hook and `model` key preserved, backup created, second install a no-op, uninstall removes only ours |
| Docs | `README.md`, `docs/WINDOWS-TEST-CHECKLIST.md`, this handoff |
| CI/release workflows written | **Never executed** (no repo) |
| Team design brief artifact | Published, private |

Bugs found and fixed during the build (so nobody re-introduces them):
1. `filepath.ToSlash` is a no-op on Linux, so the hook command path differed by host OS. Replaced with an explicit backslash→slash replace.
2. A single tool call longer than the 10 min heartbeat would look idle mid-run. Added `tool` flag and `tool_ttl_seconds` (60 min).
3. Hook handler compared the CLI argument instead of the resolved event name when setting `tool`. Fixed.
4. Windows PEB code had leftover dead variables. Cleaned.

---

## 7. What is NOT verified (be honest about this)

| Area | Why unverified | How to verify |
|---|---|---|
| Wake request actually blocks idle sleep | No Windows in sandbox | Checklist 3-5; CI `windows-smoke.ps1` for the `powercfg /requests` part |
| `powercfg /query` parsing (last two hex values = AC, DC) | Never saw real output | Checklist 1, 9 |
| `powercfg /set*valueindex` works without admin | Unknown | Checklist 9 |
| Lid power-setting notification arrives, incl. initial state | Never ran | Checklist 10-11; `status` lid field |
| `SetSuspendState` sleeps (not hibernates) when hibernate enabled | Known quirk reports | Checklist 15 |
| Modern Standby laptops honour the settings | Hardware dependent | Checklist 10 |
| Hook command executes under Claude Code's Windows shell | Quoting differs in Git Bash, cmd, PowerShell. **PowerShell would treat a leading quoted string as a string, not a command** | Checklist 6. Fallback: write `agentawake hook X` unquoted when path has no spaces, or prefix `& ` |
| `Stop` hook behaviour on Esc interrupt | Believed not to fire (from Claude Code hook docs as recalled) | New checklist step 20 |
| PEB command-line reading, Toolhelp enumeration | Never ran | Checklist 16 |
| ARM64 binary | Never ran | Any Windows-on-ARM device |
| Defender / SmartScreen reaction | Unsigned binary that reads other processes' memory, changes power settings, adds Run key: heuristic risk | Checklist 19; upload to VirusTotal after first build |
| Claude Code plugin manifest format | From memory | Install via Claude Code plugin flow; otherwise delete `plugin/` and rely on `install-hooks` |
| CI workflows | Never ran | Push to GitHub |
| `cmd/agentawake/main.go` | No automated tests; only manual dry-run | Add integration test (section 9) |

---

## 8. Known issues and limitations

| ID | Issue | Severity | Fix |
|---|---|---|---|
| K1 | Esc interrupt: `Stop` likely doesn't fire. Session stays busy up to 10 min (60 min if a tool call was running) while Claude is actually idle | **High** (defeats the product promise) | Section 9, P0-C |
| K2 | Claude waiting on a permission prompt counts as working until expiry | Medium | Same fix (CPU/child liveness) or `Notification` hook |
| K3 | If the daemon is killed or the PC loses power with lid guard active, lid stays Do nothing until the daemon next starts | Medium | Start-at-login restores it; add a logon scheduled task as belt-and-braces; `status`/`doctor` warn |
| K4 | Closed lid + heavy agent = no airflow, heat | Medium | Documented; `doctor` warns; lid guard off by default |
| K5 | `status` shows reasons but not per-session detail; no `logs` command; config needs daemon restart | Low | Backlog |
| K6 | Settings rewrite sorts JSON keys alphabetically | Low | Documented |
| K7 | Module path `agentawake` not a repo path | Low | Rename on publish |
| K8 | Cursor IDE agent mode undetectable (editor always open) | Low | Documented best effort |
| K9 | No tests for `main.go`, `platform/windows.go`, `procs_windows.go` | Medium | Integration tests + hardware checklist |
| K10 | Subagent/background tasks in Claude Code produce no hook events of their own | Low | Verify behaviour on Windows |
| K11 | Hook processes spawn per tool call (one process each). Cost unmeasured on Windows | Low-Med | Measure; if heavy, batch via a persistent named pipe later |

---

## 9. Next plan (ordered, with acceptance criteria)

### Phase 0: today (about 1 hour)
- **P0-A** Unzip source; `git init`; decide GitHub owner; rename module path; first commit; push public repo
- Confirm D4 (CLI only), D5 (name), D6 (lid guard default off), license holder
- Check name availability
- Watch CI (`ci.yml`) run on ubuntu + windows, plus `windows-smoke`

### Phase 1: hardware validation (about 2-3 hours)
- **P0-B** Run all 23 steps of `docs/WINDOWS-TEST-CHECKLIST.md` on Joshua's personal laptop
- Diagnose and fix failures (powercfg parse, hook quoting, etc.)
- Capture real test fixtures

### Phase 2: fix stuck-busy (P0-C, about half a day)
- Liveness cross-check
- Shorter thinking TTL (3 min)
- Hook `Notification`

### Phase 3: v0.1.0 release hardening
- Tags, CI release builds, Defender/VirusTotal scan, Code signing, README, SECURITY.md, Winget/Scoop manifests

---

## 10. Conventions

- Go 1.24+ standard library only (zero external dependencies).
- No OS calls outside `internal/platform` and `internal/detect/procs_*.go`.
- Every safety behaviour gets a unit test with fakes.
- Never modify a user config without creating a `.bak` backup first.
