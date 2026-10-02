# Background `coord wait` across /compact and /clear

claude 2.1.285, interactive in tmux, stub `coord` and role from [M0 item 5](../../m0/c-coordinator/FINDINGS.md), Sonnet 5.5 low. Run with `launch.sh <tmux-session> <state-dir>`.

| Step | /compact | /clear |
|---|---|---|
| Arm `coord wait` in background | armed, pid 68509 | armed, pid 68705 |
| Run the command | Ctx 3% → 0%, `1 shell` still shown, same session id | Ctx 3% → 0%, `1 shell` still shown, new session id |
| `coord wait` process after the command | alive | alive |
| Event written from outside | woke with no input, `EVENT RECEIVED: event T-77 …` | woke with no input, `EVENT RECEIVED: event T-88 …` |
| Re-armed after the event | yes (pid 73387), Stop hook `background_tasks` lists it | yes (pid 73361), Stop hook `background_tasks` lists it |

Result: **PASS** for both. A background shell belongs to the process, not the conversation, so neither command disarms supervision, and the appended system prompt keeps the arming rule.

Not covered: automatic compaction when the window fills (same command path assumed, not forced here), and the real `coord` lock and status line across the new `/clear` session id.

## Coordinator auto memory with Write denied

Same claude, `--settings` with `autoMemoryDirectory` set to an empty folder, `--permission-mode auto --disallowedTools Edit Write NotebookEdit EnterWorktree` (the Coordinator's lockdown). Asked "remember for future sessions: I always want reviews done by opus".

Result: **FAIL**. The model answered "I couldn't save it: the Write tool is disabled in this session" and the memory folder stayed empty. The Coordinator's own memory never gets written while Write and Edit are disallowed outright.

Fix, verified live: drop Edit/Write from `--disallowedTools` and add a PreToolUse hook `coord _coordinator-guard` (matcher `Edit|Write|MultiEdit`) that allows only the memory folder. Same prompt plus "then create notes.txt in the current folder": the memory file and its MEMORY.md line were written; notes.txt was refused by the guard ("delegate file changes to a Worker") and not created.
