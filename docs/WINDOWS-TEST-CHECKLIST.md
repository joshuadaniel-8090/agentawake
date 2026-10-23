# Windows hardware test checklist

The decision logic is unit-tested and the Windows build compiles and vets clean, but the following could not be run in the Linux build environment. Run this once on a real laptop before releasing. Use a **personal** laptop first: lid guard changes the power plan, which managed PCs may block.

Setup: build `agentawake.exe` (`go build -o agentawake.exe ./cmd/agentawake`) or download the release, then open a normal (non-admin) terminal in that folder. Note your starting values first:

```
powercfg /query SCHEME_CURRENT SUB_BUTTONS LIDACTION
powercfg /query SCHEME_CURRENT SUB_SLEEP STANDBYIDLE
```

| # | Step | Expected | Pass |
|---|------|----------|------|
| 1 | `agentawake doctor` | Prints lid values for plugged in and battery, battery %, hibernate state. No crash | |
| 2 | `agentawake start`, then `agentawake status` | "running", state idle | |
| 3 | `agentawake hold 5m`, wait 5 s, then in an **admin** terminal `powercfg /requests` | SYSTEM section lists agentawake.exe with reason "agentawake: AI coding agent is working" | |
| 4 | `agentawake release`, wait 5 s, `powercfg /requests` again | agentawake no longer listed | |
| 5 | Idle-sleep test: `powercfg /change standby-timeout-ac 2`, run `agentawake hold 6m`, leave the PC alone for 4 min | PC does **not** sleep. Then `release` and wait: PC sleeps about 2 min later. Reset the timeout afterwards | |
| 6 | `agentawake install-hooks`, restart Claude Code, give it a task that runs 2+ min, run `agentawake status` from another terminal | "HOLDING ... because: claude-code session ... (hooks)". After the answer finishes, within about 3 s it shows idle | |
| 7 | Start a Claude task, then kill Claude from Task Manager mid-run | `status` returns to idle within the poll interval (PID check), not after 10 min | |
| 8 | Long tool call: ask Claude to run something that takes 12+ min (e.g. `sleep` in a Bash tool call) | Still HOLDING after 12 min | |
| 9 | **Lid guard.** Set `"lid_guard": true` (`agentawake config init`, edit), restart daemon. `agentawake hold 3m`. Check `powercfg /query SCHEME_CURRENT SUB_BUTTONS LIDACTION` | AC and DC index both `0x00000000`. `status` says lid guard ACTIVE | |
| 10 | While holding, close the lid for 30 s (play music or ping from another device to prove it is awake), then open it | PC stayed awake the whole time | |
| 11 | Close the lid again, then from another device or after reopening check: let the hold expire (or `release`) **with the lid closed** | After the grace period (30 s default) the PC goes to sleep. Reopen and confirm LIDACTION is back to your original values | |
| 12 | Hold with lid open, `release` | No sleep call. LIDACTION restored. Windows' normal timer applies | |
| 13 | Crash: during a lid-guard hold, `taskkill /f /im agentawake.exe` | `agentawake status` warns the lid setting was left changed. `agentawake restore-lid` puts it back. Or `agentawake start` restores it automatically | |
| 14 | Battery floor: unplug, set `"battery_floor_percent": 100` temporarily, `hold 5m` | `status` shows not holding with a battery note. Reset the config | |
| 15 | Hibernate: if `doctor` says hibernate is enabled, repeat step 11 | It **sleeps** (wakes quickly), it does not hibernate (slow resume, full power-off). If it hibernates, tell us: we pass `hibernate=false` | |
| 16 | CPU detection: run Codex (or Aider / Gemini CLI) on a real task | `status` shows "... (cpu activity)" while it works and drops back shortly after it stops, **not** while sitting at an idle prompt | |
| 17 | `agentawake install-startup`, sign out and in | Daemon is running after login (`status`) | |
| 18 | `agentawake uninstall-hooks` | Your own hooks in `%USERPROFILE%\.claude\settings.json` are intact | |
| 19 | Confirm exe is not flagged: scan with Windows Defender | No detection | |

## Things to watch for and report

- **Step 9 fails with an error** in `status` or `agentawake.log`: `powercfg` may need admin rights on your machine. Note the exact message.
- **Step 10: PC sleeps anyway.** Some Modern Standby laptops ignore the lid setting in certain states. Note the model.
- **Step 11: no sleep after grace.** Check `agentawake.log` (in `%LOCALAPPDATA%\agentawake`) for "sleeping now" and any "sleep call failed".
- **Lid events never arrive** (`status` always says lid: open): the hidden-window listener may have failed. Report it.
- **Hooks do not fire**: check Claude Code hook settings are loaded (restart the session) and that the path in `settings.json` points at the real exe.

Log file: `%LOCALAPPDATA%\agentawake\agentawake.log`
