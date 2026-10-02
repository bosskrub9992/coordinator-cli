# coordinator-cli on Windows

Verified 2026-10-03 on Windows 11 with Git Bash, Claude Code 2.1.288, treehouse 3.1.0, gh 2.x, Go 1.27.1, inside Orca terminals: `go test ./cmd/... ./internal/...` green, `scripts/smoke-worker.sh` 14/14, and a full interactive run against a real GitHub repo (launch, guards, takeover, delegation, Worker PR, review comment, steer, stop, merge, land, scout, drop with `--close-mr`, terminal closed mid-Task, notifications).

## What differs from macOS and Linux

| Area | Windows behaviour |
|---|---|
| Shell | Run `coord` from Git Bash. Claude Code's Bash tool is Git Bash too. |
| `coord` on the agents' PATH | Claude Code's Bash tool rebuilds PATH from a login-shell snapshot and drops what the parent added, so coord writes a `CLAUDE_ENV_FILE` (`run/coordinator/env.sh` for the Coordinator, `tasks/<id>/worker-env.sh` for a Worker) that puts coord's folder first. An env file the parent already set is sourced from it. |
| Detached processes | Terminals such as Orca put their processes in a kill-on-close job that forbids breakaway, so `DETACHED_PROCESS` is not enough. Supervisors and the watcher are created through WMI (`Win32_Process.Create`, hidden console) and set themselves up from a spec file (`coord _detached`). If WMI fails, coord falls back to `DETACHED_PROCESS` and says so in the process log; closing the terminal may then stop them. |
| Captain at a terminal | A console (`CONIN$`) or a mintty pty counts as the Captain's terminal. Detached processes carry `COORD_DETACHED`, so their hidden console never does. |
| Stopping a Worker | `taskkill /T /F` on the Worker's PID after the grace period; there is no process-group signal. |
| Notifications | Windows toast through PowerShell; it is attributed to "Windows PowerShell". The history is readable with `ToastNotificationManager.History`. |
| Local remotes | Windows git only takes `file:///C:/...`; coord reads an empty `file://` host as `localhost`. |
| Line endings | `.gitattributes` keeps LF in checkouts, so shell scripts run under Git Bash. |
| Tests | No `-race` without a C toolchain. Fake binaries are copied as `<name>.exe` instead of symlinked, so Developer Mode is not needed. The Coordinator launch tests that use a `#!/bin/sh` fake `claude` are skipped. |

## Known gaps

- Interrupting a Worker relies on the stream-json control message only.
- A Worker that writes its Report through PowerShell instead of Bash can leave a UTF-8 BOM in `report.md`.
