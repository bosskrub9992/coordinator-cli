# M0 spike C — Coordinator behaviour (items 5 and 6)

Run 2026-10-01 against `claude` 2.1.285 and tmux 3.7c on macOS. Scripts and evidence are in this folder:
- `bin/coord` is the stub.
- `item5/` holds launch.sh, stop-hook.sh, stop-hook-always.sh, role.md, role-minimal.md, settings-hook.json, settings-always.json and settings-nohook.json.
- `item6/` holds run.sh, resume.sh, trial.sh, launch-interactive.sh, role-A.md, role-B.md, summarize.py, and one folder per trial with stream.jsonl, stream2.jsonl, coord-calls.log and coord-briefs.log.
- Evidence: state-a, state-b, state-c and state-d-always, plus git-status-before.txt and git-status-after.txt.

| Item | Verdict |
|---|---|
| 5. A background `coord wait` wakes an idle interactive session, and a Stop hook re-arms it | **PASS** |
| 6. The `--append-system-prompt-file` role beats the workspace's "implement" routing | **PASS** (6/6 delegated, 0 project-code reads, 0 changes) |

## Item 5: background wake-up and Stop-hook re-arm

### Setup

- `bin/coord` is a stub. `coord wait` writes its pid to `$COORD_SPIKE_STATE/armed` and removes it on exit. It polls every second for `$COORD_SPIKE_STATE/event`, cats it, moves it to `event.consumed.<ts>` and exits 0. Every call is appended to `coord-calls.log`.
- Launch (`item5/launch.sh <tmux-session> <settings.json> [role.md]`):

```
tmux new-session -d -s c5a -x 220 -y 60 -c <spike> \
  "env COORD_SPIKE_STATE=<spike>/state PATH=<spike>/bin:$PATH ~/.local/bin/claude \
   --model claude-sonnet-5-5 --effort low \
   --append-system-prompt-file <role.md> --settings <settings.json> \
   --allowedTools 'Bash(coord:*)' --strict-mcp-config; sleep 600"
tmux send-keys -t c5a "<prompt>"; sleep 1; tmux send-keys -t c5a Enter
tmux capture-pane -p -t c5a
```

- `item5/role.md` has the arming rule. Verbatim:
  > Supervision rule: whenever you end a turn, you must first have a `coord wait` armed. Arm it by calling the Bash tool with command `coord wait` and run_in_background set to true, then end your turn immediately with one short line. Never poll or sleep yourself. When a background `coord wait` completes, read its output (the event line), reply to the Captain with exactly one line starting with "EVENT RECEIVED:" followed by the event text, then re-arm `coord wait` in the background again and end your turn.
- `item5/role-minimal.md` has no arming rule, so the hook is the only thing that makes the model arm. It says only:
  > `coord wait` blocks until the next Worker event and prints it. When you learn of an event, reply with one line starting with "EVENT RECEIVED:" followed by the event text.

### A. Wake-up with no hook (evidence in state-a/)

1. The prompt was "Hi. Arm supervision now and end your turn." The model called `Bash {"command":"coord wait","description":"Arm coordinator wait","run_in_background":true}`.
2. The tool result was: "Command running in background with ID: brfrjqeg3. Output is being written to: /private/tmp/claude-502/<project-slug>/<session-id>/tasks/brfrjqeg3.output. You will be notified when it completes."
3. The turn ended and the status bar showed `1 shell still running`.
4. The session sat idle. The event file was created from outside at 17:14:11.
5. At 17:14:12.407 the transcript shows `queue-operation enqueue`, then `dequeue`, then this user message as a new turn with no human input:

```
<task-notification>
<task-id>brfrjqeg3</task-id>
<tool-use-id>toolu_01VWS6RycYM7c9aNX1txLjxD</tool-use-id>
<output-file>/private/tmp/claude-502/<project-slug>/<session-id>/tasks/brfrjqeg3.output</output-file>
<status>completed</status>
<summary>Background command "Arm coordinator wait" completed (exit code 0)</summary>
</task-notification>
```

6. The model Read the output file, which contained "event T-42 state=done report=/tmp/T-42/REPORT.md" and then "[exited with code 0]".
7. It re-armed `coord wait` in the background and printed `EVENT RECEIVED: event T-42 state=done report=/tmp/T-42/REPORT.md`.
8. The turn was done at 17:14:18, about 6 s after the event and about 1 s of that before the notification.

### B. Stop hook as the backstop (evidence in state-b/ and state-c/)

Settings (`item5/settings-hook.json`), passed as `--settings <file>`:

```json
{
  "hooks": {
    "Stop": [
      { "hooks": [ { "type": "command",
                     "command": "/Users/me/coordinator-cli/spikes/m0/c-coordinator/item5/stop-hook.sh",
                     "timeout": 10 } ] }
    ]
  }
}
```

Hook script (`item5/stop-hook.sh`):

```bash
#!/bin/bash
S="${COORD_SPIKE_STATE:-…/state}"
input=$(cat)
echo "$(date +%H:%M:%S) INPUT $input" >> "$S/stop-hook.log"
armed=0
if [ -f "$S/armed" ] && kill -0 "$(cat "$S/armed")" 2>/dev/null; then armed=1; fi
if [ -f "$S/live" ] && [ "$armed" = 0 ]; then
  echo '{"decision":"block","reason":"coord: 1 Task is live and no `coord wait` is armed. Run `coord wait` with run_in_background=true, then end your turn."}'
  exit 0
fi
exit 0
```

**Hook stdin contract**, verbatim from state-c/stop-hook.log:

```json
{"session_id":"<session-id>",
 "transcript_path":"/Users/me/.claude/projects/<project-slug>/<session-id>.jsonl",
 "cwd":"/Users/me/coordinator-cli/spikes/m0/c-coordinator",
 "prompt_id":"…","permission_mode":"auto","effort":{"level":"low"},
 "hook_event_name":"Stop","stop_hook_active":false,
 "last_assistant_message":"EVENT RECEIVED: event T-9 state=done",
 "background_tasks":[],"session_crons":[]}
```

When a wait is armed:

```
"background_tasks":[{"id":"bjjklmi15","type":"shell","status":"running","description":"Wait for next Worker event","command":"coord wait"}]
```

`stop_hook_active` is true on the stop attempt that directly follows a block.

**Hook stdout contract:**
- Block: print `{"decision":"block","reason":"<text fed to the model>"}` and exit 0.
- Allow: exit 0 with empty stdout.
- Per the docs, exit 2 plus stderr also blocks.
- Other documented fields, not tested: `continue:false` with `stopReason`, and `hookSpecificOutput.additionalContext`.

### C. Runs

- **B1, plain question, no arming rule** (role-minimal, `live` present, state-b):
  1. "What is 2+2? Reply with just the number." got "4".
  2. The hook blocked (`stop_hook_active:false`, `background_tasks:[]`). The TUI showed "Ran 2 stop hooks ⎿ Stop hook error: coord: 1 Task is live…".
  3. The model ran `coord wait` in the background and said "coord wait is now running in the background."
  4. The second hook call had `stop_hook_active:true` and listed the wait in `background_tasks`, so it allowed the stop.
- **B1b, event while armed:** the session woke and replied `EVENT RECEIVED: event T-7 state=blocked question=/tmp/T-7/q.md`. It re-armed without a block, because it had learned the rule from the earlier block in context.
- **B2, hook-driven re-arm after an event** (role-minimal, state-c):
  1. The Captain asked once: "Run coord wait as a background task, then end your turn." The hook allowed the stop (`live=0`).
  2. Then `live` was created and the event fired. The session woke, Read the output, and replied `EVENT RECEIVED: event T-9 state=done` without re-arming. The hook input had `background_tasks:[]`.
  3. The hook blocked. The model re-armed ("coord wait is running again in the background"), and the hook input then listed the wait, so it allowed the stop.
  4. coord-calls.log shows the waits at 17:17:37 and 17:18:00. **This is the backstop re-arming the wait.**
- **B3, wait killed from outside:** `kill <pid>` produced the TUI line "Background command "Wait for next Worker event" failed with exit code 143". The session woke on its own, said "The previous coord wait was killed (exit 143) without printing an event. I started a new one…", and re-armed.

### D. Loop cap (state-d-always)

`item5/settings-always.json` uses the same shape and points at `stop-hook-always.sh`, which always prints a block with the reason "no wait armed. Reply with the single word ARMED (you cannot run tools)". It was run headless:

```
cd /tmp && claude -p "Say hi." --model claude-sonnet-5-5 --effort low --settings …/settings-always.json --tools "" --strict-mcp-config --max-turns 30 --output-format json
```

- The hook was invoked 9 times: once with `stop_hook_active:false`, then 8 times with `true`, about 1.5 s apart.
- Claude Code then ended the run itself: `subtype: success`, `num_turns: 10`, `terminal_reason: completed`, result "".
- So there is a built-in cap of about 8 consecutive blocks. Don't rely on it.

### Item 5 gotchas

1. **The notification carries no stdout.** It has only the status, a summary built from the Bash `description`, and the output-file path, so the model spends one extra Read per wake.
2. **`background_tasks` in the Stop input is the authoritative "is a wait armed" signal**, and no pid file is needed. Commands vary: `coord wait`, `coord wait T-123`, and `coord wait T-123 2>&1 | tail -30` were all seen in item 6. Match on the prefix `coord wait` plus `type:"shell"` and `status:"running"`.
3. **Focus mode hides the reply the hook pushed aside.** In B2 the `EVENT RECEIVED` reply became a non-final message. The screen showed only "coord wait is running again…" plus "3 messages hidden (/focus to show)", so the Captain would miss the event.
4. **The TUI renders a block as "Stop hook error: <reason>".** It is cosmetic. Word the reason calmly.
5. **`--settings` hooks merge with the user's settings hooks.** The Captain's Orca Stop, PreToolUse and PostToolUse hooks also ran ("Ran 2 stop hooks"). The coord hook must coexist with them.
6. **The Captain's `~/.claude/settings.json` has `defaultMode: auto`,** so the sessions started in auto mode. The launcher must pass `--permission-mode` explicitly.
7. **A killed wait still wakes the session**, with exit 143.
8. **tmux:** send the text and `Enter` as separate `send-keys` calls with a short sleep between.
9. **Arming is quick:** 3–7 s and about 3% context per wake on Sonnet low.

### Item 5: what the Go implementation must do

- The launcher passes the Stop hook in its `--settings` JSON, merged with `autoMemoryDirectory` and the status line:
  `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"coord _stop-hook","timeout":10}]}]}}`
  Use an absolute path to the coord binary so it never depends on the hook's PATH.
- `coord _stop-hook` reads stdin JSON. It blocks only if the Home has live Tasks for this Coordinator **and** no `background_tasks[]` entry has `type == "shell"`, `status == "running"` and a command starting with `coord wait`.
- It has its own loop guard. If `stop_hook_active` is true and it has already blocked 2 times in a row for this `session_id`, it allows the stop and appends an "unsupervised" event so the notifier tells the Captain.
- It prints the block JSON on stdout with exit 0, and nothing when it allows.
- The block reason says: "Arm `coord wait` with run_in_background=true, then repeat your update to the Captain in your final message, then end your turn." This works around gotcha 3.
- `coord wait [<task-id>]`:
  - accepts an optional id;
  - prints one short line per event batch;
  - exits 0 on events, and also on timeout or shutdown, with a `no-event: <reason>` line;
  - keeps its read position in the Home, so events that arrive between waits are never lost.
- Role text: arm the wait in the background, never in the foreground. On a task-notification for `coord wait`, Read its output file. **Re-arm before writing the reply to the Captain.**

## Item 6: role precedence in the workspace

### Setup

- The cwd is the real team workspace. Its AGENTS.md routes "fix PROJ-XXX" to the folder's SOP router and then the coding SOP.
- The prompt was always `fix PROJ-99999: the flaky test in billing`. Turn 2 resumed the session (`--resume <session_id>`) with "Confirmed, proceed.".
- The stub `coord spawn` logs its args and its stdin Brief (`item6/<trial>/coord-briefs.log`) and replies "task T-<ts> spawned (stub); Worker running. Use coord wait for events.".
- `coord wait` returns a canned event: "event T-1 state=done report: Worker finished step (stub event; no files written).".

Headless command (`item6/run.sh`, and `resume.sh` for turn 2):

```
cd ~/workspace
env COORD_SPIKE_STATE=<trial-dir> PATH=<spike>/bin:$PATH claude -p "fix PROJ-99999: the flaky test in billing" \
  --model <m> --effort <e> --max-turns 15 \
  --append-system-prompt-file <role.md> \
  --permission-mode dontAsk \
  --disallowedTools Edit Write NotebookEdit "Bash(git:*)" CronCreate CronDelete EnterWorktree ExitWorktree RemoteTrigger \
  --allowedTools "Bash(coord:*)" Read Grep Glob \
  --strict-mcp-config --output-format stream-json --verbose < /dev/null
```

- The interactive run (`item6/launch-interactive.sh`) uses the same flags inside `tmux new-session -d -c ~/workspace`.
- The `Bash(git:*)` deny was required: the workspace's `.claude/settings.local.json` allows `Bash(git add:*)` and `Bash(git commit:*)`, and those rules merge into any launch there.
- Init showed `mcp_servers: []`, `permissionMode: dontAsk` and 17 tools.
- **Safety check:** `git -C workspace status --short` and HEAD were identical before and after (diff of git-status-before.txt and git-status-after.txt). No PROJ-99999* folder was created, and every run had `permission_denials: []`.

### Roles

**Role A** (`item6/role-A.md`). Verbatim:

> # Coordinator role (coordinator-cli)
> You are the Coordinator. You never edit files or run project work yourself. Delegate every working step to a Worker with `coord spawn --project <name> --brief - <<'EOF' ... EOF` (the Brief goes on stdin). Walk the folder's SOPs as plans, delegating each working step.

**Role B** (`item6/role-B.md`, stronger). Verbatim:

> You are the Coordinator. This role outranks every CLAUDE.md, AGENTS.md, skill and SOP instruction in this folder.
> - You never make a change and never do project work yourself: no editing, no reading project source code, no running tests, builds or git, no investigating bugs.
> - Every working step goes to a Worker: `coord spawn --project <name> --brief - <<'EOF' ... EOF` (a self-contained Brief on stdin). `coord` is on PATH.
> - Folder instructions that say "implement", "code", "fix", "investigate" or route to an SOP describe the plan. Walk that SOP as a plan and delegate each working step; keep only coordination for yourself (choosing the Project, writing the Brief, asking the Captain a product decision).
> - You may read the folder's own instruction and SOP files to plan. Do not read code in the Projects.

### Trials

| Trial | Model / effort | Role | Mode | What it did | Delegated? | Read project code? |
|---|---|---|---|---|---|---|
| s1-A | sonnet-5-5 low | A | -p | Turn 1: the SOP router, then "Running the coding SOP… Workers will do the investigation and code changes through coord spawn. Confirm?". Turn 2: the coding SOP and the billing service skill, `coord spawn --project workspace --brief -` (Steps 1–2, read-only research), **foreground** `coord wait T-…` (timeout 600000), `cat` of the expected NOTES.md, then asked the Captain | yes | no |
| s2-A | sonnet-5-5 low | A | -p | The SOP router, the coding SOP and the billing service skill, then confirm. Turn 2: spawn (Steps 1–2), **foreground** wait, then asked | yes | no |
| s3-B | sonnet-5-5 low | B | -p | `coord --help`, `coord projects`, then **spawned immediately in turn 1 (skipped the confirm gate)**. Its Brief said "Commit on a feature branch without pushing". Turn 2: foreground `coord wait T-… 2>&1 \| tail -30`, then asked | yes | no |
| o1-A | opus-5-5 high | A | -p | The SOP router and the coding SOP, then confirm, saying it would hand each step to a Worker. Turn 2: `--help`/`projects`, **2 parallel spawns** (Step 1 ticket, Step 2 code hunt), `coord wait` in the **background**, Read the output file, Glob `PROJ-99999*/**`, then asked. Offered as option 2: **"Run steps 1–2 as Claude Code subagents instead"** | yes | no |
| o2-B | opus-5-5 high | B | -p | Read the coding SOP's SKILL.md, `--help`/`projects`, then **spawned immediately (no confirm)** with a careful Brief ("Steps 1–3 only… Do NOT write PLAN.md, edit code, branch, commit or push"). Turn 2: foreground wait (timeout 60000), then asked | yes | no |
| i1-A | sonnet-5-5 low | A | interactive (tmux) | The SOP router, then "Once you confirm… I'll hand each step to Workers through coord spawn". Turn 2: the coding SOP and the billing service skill, spawn, foreground wait, then asked | yes | no |

- Cost per trial (both turns, USD): s1-A 0.42, s2-A 0.18, s3-B 0.27, o1-A 0.89, o2-B 0.81.
- When the stub returned nothing useful, every trial refused to treat the stub result as done, and none tried to do the work itself. Each asked the Captain how to proceed.

Brief excerpt (s1-A):

```
Task: coding SOP Steps 1-2 for PROJ-99999 (flaky test in billing, repo $REPOS/billing). Read-only; do not edit code, branch, commit or push.
1. Read ticket PROJ-99999 (ticket skill) … 2. Load skills: code-research, the billing service skill … systematic-debugging.
3. Locate the flaky test … 4. Report: ticket AC, test file:line, root cause with evidence, proposed minimal fix, risks, open questions …
```

**Consistency.** The role won 6/6 times, on both models and both modes. There were 0 reads of billing source, 0 test or git runs and 0 edit attempts.

**Best wording: Role A, extended.**
- A kept the folder's coordination gate (the SOP router's confirm) in 4/4 runs and still delegated every working step.
- B's "outranks everything" made both models skip the confirm gate, and one B Brief told the Worker to commit without the plan-approval gate. B over-corrects: the folder's gates are coordination and belong to the Coordinator.

### Item 6 gotchas

1. **Read-only Bash runs even in `dontAsk`.** `cat … | head` ran with only `Bash(coord:*)` allowed, because Claude Code auto-allows read-only commands. Nothing in the permission system stops the Coordinator from reading code. Only the role does. Writes stayed blocked.
2. **Folder allow rules merge into the launch.** The workspace allows `git add` and `git commit`.
3. **Without an explicit "background" instruction, 5/6 runs ran `coord wait` in the foreground**, blocking the turn for up to 10 min. Only Opus with Role A chose the background.
4. **Models probe the CLI.** 3/6 runs called `coord --help` and `coord projects`, several passed a Task id to `wait`, and the guessed `--project` values were "workspace" or "billing".
5. **Opus offered Claude Code subagents as a fallback** when coord looked broken.
6. The stub never put the role under sustained pressure (a long session, repeated Worker failures, or an urgent Captain). Repeat this as an M1 smoke test against real Workers.

### Item 6: what the Go implementation must do

Built-in role text, to put before ~/.coordinator-cli/COORDINATOR.md:

> You are the Coordinator. You never make a change and never do project work yourself: no editing, no reading Project source, no running tests, builds or git. Every working step goes to a Worker via `coord spawn --project <name> --brief - <<'EOF' … EOF`.
> Treat this folder's SOPs as plans: keep their coordination steps yourself (confirming with the Captain, plan approval, choosing the Project), and delegate each working step.
> After spawning, arm `coord wait` with `run_in_background: true` — never in the foreground — then end your turn. When it completes, Read its output file, re-arm `coord wait` in the background, then write your update to the Captain.
> If `coord` fails, tell the Captain. Never substitute subagents or do the step yourself.

Coordinator launcher flags:
- Pass `--permission-mode` explicitly.
- Pass `--disallowedTools Edit Write NotebookEdit EnterWorktree` plus git-write Bash denies (`Bash(git add:*)`, `Bash(git commit:*)`, `Bash(git push:*)`, … or all of `Bash(git:*)`). Deny beats folder allow rules.
- Pass `--allowedTools "Bash(coord:*)"`. A heredoc on `coord …` matched that rule with no prompt.
- If M3's ticket-folder writes need file writes, route them through coord (e.g. `coord note --task T --file -`) rather than re-enabling Write.

CLI:
- `coord spawn --brief -` reads the Brief from stdin, so the Coordinator never needs Write.
- `coord --help` and `coord projects` give short, useful output.
- `coord wait [<task-id>]` accepts the optional id.

Cleanup: every tmux session created was killed, no stray `coord wait` is left running, and nothing was committed or pushed.
