# agentawake

Keeps a Windows PC awake **only while an AI coding agent is actually working**, then hands sleep back to Windows. Optionally keeps running with the lid closed, and puts the laptop to sleep the moment the agent finishes.

Free, MIT licensed, one small `.exe`, no runtime, no telemetry, works offline.

> **Status: v0.1, not yet verified on real Windows hardware.** The decision logic is unit-tested and the Windows build compiles and vets clean, but the Windows power calls (wake request, lid event, `powercfg`, sleep) need a run on a physical laptop. See [docs/WINDOWS-TEST-CHECKLIST.md](docs/WINDOWS-TEST-CHECKLIST.md). Please run it before you rely on it.

## The problem

- Windows sleeps on **idle input**. A coding agent working for an hour looks idle, the laptop sleeps, the run dies.
- "Keep awake" toggles stay on after the agent finishes, so the laptop never sleeps again.
- Closing the lid sleeps the laptop no matter what is running.

## What it does

| Situation | Behaviour |
|---|---|
| Agent working | PC stays awake (a normal Windows power request, visible in `powercfg /requests`) |
| Agent finished or idle at its prompt | Request released. Windows' own sleep timer applies, counted from the finish |
| Lid guard on, agent working | Lid close does nothing, so the run continues in a closed laptop |
| Lid guard on, agent finishes with lid closed | Original lid setting restored, laptop sleeps after a short grace period |
| Lid guard on, lid open at finish | Setting restored, Windows' normal timer applies |

## Install

1. Download `agentawake_<version>_windows_amd64.zip` from Releases and put `agentawake.exe` somewhere permanent, e.g. `C:\Tools\agentawake\`. (ARM64 build also provided.)
2. Run in a terminal:

```
agentawake install-hooks      # Claude Code: accurate "working / idle" signal
agentawake install-startup    # start at login
agentawake start              # start now
agentawake doctor             # check everything
```

Restart any open Claude Code session so it picks up the hooks.

## Commands

```
agentawake status             what it is doing right now, and why
agentawake hold 2h            force awake for a while (max 24h); "hold off" cancels
agentawake pause / resume     turn automatic holding off / on
agentawake stop               stop cleanly (restores the lid setting)
agentawake restore-lid        repair the lid setting after a crash
agentawake uninstall-hooks    remove only the hooks agentawake added
agentawake config init        write an editable config file
agentawake run --dry-run -v   simulate everything on any OS, no power changes
```

## How "working" is detected

| Agent | Signal | Accuracy |
|---|---|---|
| Claude Code | Hooks: busy on prompt and tool calls, idle on stop | High |
| Codex, Gemini CLI, OpenCode, Aider, cursor-agent | Process name + CPU time of the process tree | Medium |
| Cursor IDE agent mode | The editor is always open, so CPU only | Low, best effort |
| Anything else | Add a signature in the config, or use `hold` | You decide |

An agent sitting open at an empty prompt does **not** count as working. Claude Code is detected through hooks only by default, because the Claude desktop app has the same process name and would cause false positives.

## Lid guard (opt-in)

Off by default. Turn it on with `"lid_guard": true` in the config (`agentawake config init`), or run `agentawake run --lid-guard`.

While an agent works it sets the lid-close action to *Do nothing* using `powercfg`, saving your original value to disk first. When the agent finishes it restores your value, and if the lid is closed at that moment it sleeps the PC itself (Windows only reacts to the lid at the instant it closes, so restoring the setting alone would not).

- A closed laptop running a heavy agent has no airflow. Use it on a desk, not in a bag.
- If your original lid action was *Do nothing*, agentawake never sleeps the PC for you.
- If `powercfg` is blocked (managed work PCs), it falls back to the wake request only and says so in `status`.

## Safety rules

- Hooks always exit 0 and never slow or fail the agent.
- A killed agent cannot hold the PC awake: every session has a heartbeat expiry (default 10 min, or 60 min while one tool call is running, so long builds are not mistaken for idle) and an "is the agent process still alive" check.
- The original lid value is written to disk before it is changed. A crash or reboot is repaired the next time the daemon starts, or with `agentawake restore-lid`. If the daemon is killed while lid guard is active, the lid stays on *Do nothing* until it next starts, and `status` warns about it.
- Hard cap on one continuous hold (default 8 h). Below the battery floor (default 20%, unplugged) it stops holding, and sleeps right away if the lid is closed.
- `install-hooks` merges into `~/.claude/settings.json`, backs it up first, refuses to touch invalid JSON, and never overwrites your own hooks. (It rewrites the file with keys in alphabetical order.)

## Config

`agentawake config path` shows the location (`%APPDATA%\agentawake\config.json`). Every field is optional.

```json
{
  "poll_seconds": 3,
  "grace_seconds": 30,
  "max_hold_hours": 8,
  "battery_floor_percent": 20,
  "lid_guard": false,
  "session_ttl_seconds": 600,
  "tool_ttl_seconds": 3600,
  "process_detection": true,
  "cpu_threshold_percent": 3,
  "cpu_linger_seconds": 20,
  "agents": [
    { "name": "my-agent", "process_names": ["myagent"], "cmd_contains": ["my-agent-cli"] }
  ]
}
```

## Known limits

- Windows only in v0.1. WSL support is planned (the Windows exe can already be called from WSL through interop, but process detection inside WSL is not done).
- If Claude Code is waiting for you to approve a permission prompt, it still counts as working until the heartbeat expires.
- Cursor's IDE agent cannot be detected reliably.
- Hook support for non-Claude agents has not been checked, so those use CPU detection.
- Modern Standby laptops may throttle with the screen off even with the lid guard on.
- Lid guard changes the active power plan. That may need admin rights on locked-down machines.
- Hibernate: the sleep call can hibernate on some setups. `doctor` reports whether hibernate is enabled.

## Alternatives

Other tools exist in this space: Insomnia, nosleep-cli, PowerLockGuard, AgentsRoom. agentawake's angle is accurate idle-versus-working detection, post-finish sleep handling, and a lid mode that restores itself.

## Development

```
go test ./...                       # logic tests run on any OS
GOOS=windows go vet ./...           # compile check for Windows
go build -o agentawake.exe ./cmd/agentawake
agentawake run --dry-run -v         # simulate; "touch <state dir>/dry-lid-closed" fakes a closed lid
```

Zero third-party dependencies: standard library only. All OS calls live in `internal/platform` and `internal/detect`; the decision loop in `internal/engine` has no OS calls and is tested with fakes.

## License

MIT
