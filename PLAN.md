# coordinator-cli — Plan

Status: **approved 2026-10-01; M0 done ([findings](docs/m0-findings.md)); M1 built and tested (it also absorbed M2's steering and `coord wait`); first real use done; permissions handed to Claude Code's auto mode ([ADR-0004](docs/adr/0004-coord-is-the-coordinators-toolbox.md)); M3, M4 and M5 built and tested 2026-10-02 ([ADR-0005](docs/adr/0005-a-task-ends-when-its-sop-is-finished.md)); M6 done 2026-10-03 on Windows 11 ([docs/windows.md](docs/windows.md)); M7 built and tested 2026-10-03 ([ADR-0006](docs/adr/0006-the-coordinator-learns-coord-from-coord.md))**. Vocabulary: [CONTEXT.md](CONTEXT.md). Decisions: [docs/adr/](docs/adr/).

## Impact

What the Captain gets when this plan is done:

- Type `coord` in any folder (in practice the team's `workspace`) and talk to one agent, the Coordinator, in the normal Claude Code screen. It knows that folder's workflow and SOPs, but never does the work itself.
- Every change and every non-trivial investigation runs as a Worker in its own treehouse worktree, in parallel, with no cap. Main checkouts are never edited.
- The Coordinator stays available to the Captain most of the time; `coord` is its toolbox, never its supervisor ([ADR-0004](docs/adr/0004-coord-is-the-coordinators-toolbox.md)).
- The Captain is interrupted only for product decisions, calls a Worker was refused and needs, prod changes, failures, merges, and discards. Each one arrives as a desktop notification, even with the terminal closed.
- A `ship` Task carries itself from Brief to Plan, to MR with green CI, through review fixes, to Landed. Nothing is lost on a crash, restart, or a second terminal.
- The previous orchestrator skill is retired at the end.

Out of scope for this plan: Codex and Cursor Harnesses (the adapter seam is built, the adapters are not), the web dashboard, separate fleets (`--fleet`), and per-Project auto-merge.

## Settled product behaviour

| Area | Behaviour |
|---|---|
| Start | `coord` launches Claude Code in the current folder, with the Coordinator role at system-prompt level, its own memory in the Home, and a status line that shows the Captain's own status line with a Fleet line under it |
| One Coordinator | A second `coord` asks `[y/N]` to take over; the old session's `coord` calls are refused afterwards; `coord --continue` resumes the last Coordinator conversation |
| Restart | A new Coordinator rebuilds the Fleet from the Home and live processes; a SessionStart hook hands it the Fleet, and its first reply opens with a recap |
| Workers | Start in the Launch folder by default (overridable per Project or Task), with leased worktrees attached and their `CLAUDE.md` loaded; they write only to their worktrees and their ticket folder |
| Lookups | The Coordinator does one or two small, predictable calls itself; anything open-ended or large, or in doubt, goes to a `scout` |
| Autonomy | Free: Briefs, Workers, worktrees, pushing branches, opening MRs/PRs, read-only prod calls (queries, `get_*`/`list_*`). Captain's word: merge, discard or drop, closing an MR, anything destructive, deploys, and any call that changes prod (shown with the exact call) |
| Permissions | The Coordinator and Workers run in Claude Code's `auto` mode, as the Captain's own sessions do; `coord` makes no permission decisions. A Worker's refused call fails at once; it reports `blocked`, the Coordinator brings the Captain the exact call, and the go-ahead returns as a steer |
| Coordinator tools | Everything except NotebookEdit/EnterWorktree, Edit/Write outside its own memory folder, and git writes (deny list incl. `git -C`/`-c`/`--git-dir`/`--work-tree`, `sh -c`, `bash -c`, `zsh -c`, `eval`); git reads and `git fetch` work, so quick lookups need no Worker |
| MCP servers | Workers get every MCP server the Captain has installed, with the Launch folder's enable/disable state |
| Plans | Every `ship` Worker submits a Plan (`coord report --status plan`) before changing anything, unless the Coordinator passed `--skip-plan` for a trivial change (refused under `all`). `plan_approval` = `product-decisions` (default) / `fyi` / `all`, set globally, per Project, or per Task, decides who approves it |
| Model and effort | Per Task, then per Project, then class matrix. The Coordinator's own picks come only from `coordinator_may_choose` and never from `forbidden_models`. Every Report records what ran |
| Usage limits | The Worker stops and the Task is `blocked` with "usage limit, resets at HH:MM"; the Captain is told once; nothing resumes on its own, the Coordinator resumes it when the Captain is back |
| After the MR | The Worker exits when the MR is up and green. A 2-minute poll (`mr_poll_interval` in the config) reports facts only (merged, new comments, CI turned red or green, closed); the Coordinator acts on them by the Launch folder's SOP, and where the SOP is silent it brings them to the Captain. Review comments are fixed only when the Captain agrees, by resuming the original Worker; replies on the MR are posted only when the SOP says so. After the merge, the SOP's next steps (deploy) start only when the SOP says so, otherwise on the Captain's word |
| Landed | A `ship` Task ends when its Project's SOP is finished (for example: deployed to prod and post-checked), not at the merge; a `scout` or `review-code` Task ends once its Report has reached the Captain. The Coordinator lands it itself and mentions it in the recap. A `ship` worktree stays leased until then |
| Merge | On the Captain's word the Coordinator runs `coord merge <task>`, which merges the open MRs in the order they were linked and stops at the first failure; never a Worker |
| Drop | Stops a running Worker first, then cleans up the worktree and the Task; the MR and remote branch are untouched unless the Captain says "and close the MR" |
| Notify | Desktop notification only when the Captain is needed: the Coordinator sends `coord notify`; while no Coordinator is open, coord itself sends a plain notice when a Task is reported, waiting on its MRs, needs a decision, is blocked, or failed |
| Home | `~/.coordinator-cli/` (`COORD_HOME` overrides), one folder on both operating systems |

## Internal choices (adopted, listed for review)

- **Language:** Go, a single binary for macOS and Windows/Git Bash. JSON config. Task state is written atomically, and the event log only ever grows.
- **Coordinator launch:** `claude` in the current folder with
  - `--append-system-prompt-file` (the built-in role plus `~/.coordinator-cli/COORDINATOR.md`); role wording per [M0 item 6](spikes/m0/c-coordinator/FINDINGS.md): keep the folder's coordination gates, always background `coord wait`, re-arm before replying, never substitute subagents
  - `--settings`, setting `autoMemoryDirectory` to `~/.coordinator-cli/memory`, the status line, the `coord _stop-hook` Stop hook (reads `background_tasks`; one reminder per stop, pointing at unread events when there are any, then an `unsupervised` Fleet event), and the `coord _session-start` SessionStart hook (the Fleet and unread events as context, and a recap request except after compaction)
  - `--permission-mode auto`, `--allowedTools "Bash(coord:*)"`, `--disallowedTools NotebookEdit EnterWorktree` plus the git-write and shell-wrapper Bash denies; a `coord _coordinator-guard` PreToolUse hook lets Edit/Write touch only `~/.coordinator-cli/memory` (a plain Edit/Write deny also blocks auto memory, [spike](spikes/m1/b-compact/FINDINGS.md))
  - `--model` and `--effort` from config
- **Worker launch:** a detached `coord _supervise <task>` process runs, from the Task folder,
  `claude -p --input-format stream-json --output-format stream-json --verbose --replay-user-messages`, with:
  - `--session-id` pre-assigned, so the session can be resumed later
  - `--add-dir` for each worktree, plus `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1`
  - `--append-system-prompt-file` with the Worker role and coord's resolved instruction imports (worktree `CLAUDE.md`/`AGENTS.md` imports and the Launch folder's, which Claude Code drops as "external"); an `InstructionsLoaded` hook records what loaded
  - `--model` / `--effort`, `--permission-mode auto`; no `--permission-prompt-tool` and no `--mcp-config` (the Captain's MCP servers load as usual; never `--strict-mcp-config`)
  - main-checkout protection per [ADR-0003](docs/adr/0003-main-checkouts-protected-by-instruction.md): the Brief names where to work, and a `coord _guard` PreToolUse hook allows Edit/Write in the Task's worktrees and Task folder (never `.claude/`), denies main checkouts and the rest of the Launch folder, leaves temp to Claude's normal permission flow, and denies everything else. No sandbox; normal `git push` from the worktree
  - `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`
  - the parent session's Claude Code identity variables removed (`CLAUDECODE`, `CLAUDE_CODE_SESSION_ID`, `CLAUDE_CODE_CHILD_SESSION`, `CLAUDE_CODE_MESSAGING_*`, `CLAUDE_EFFORT`, `CLAUDE_CODE_ENTRYPOINT`, `CLAUDE_PID`); everything else, including other tools' variables, passes through

  The supervisor owns the Worker's input and output, writes `worker.log` and `events.jsonl`, and passes on steers from a per-Task inbox.
- **Supervision:** `coord wait` blocks until a new event arrives past the Coordinator's read position. The Coordinator runs it as a background shell task, so Claude Code re-invokes the Coordinator when an event lands, and nothing uses tokens while waiting.
- **Watcher:** see [ADR-0002](docs/adr/0002-on-demand-watcher-no-always-on-service.md). It polls MRs with `glab` and `gh` (state, head pipeline, new notes past a stored position) and sends notifications. It starts when needed and exits when idle.
- **Harness seam:** two Go interfaces, `CoordinatorLauncher` and `WorkerRunner`, with one Claude implementation each.
- **Worktrees:** treehouse leases each Project from its main checkout, with the Task id as the holder. A worktree is returned only after the Task Lands or is Dropped. A missing `treehouse.toml` is escalated, never improvised.
- **Tests:** Go unit tests, plus an integration suite driven by a fake `claude` that replays recorded stream-json, so the lifecycle can be tested without tokens. A small set of manual smoke tests runs against real Claude.

## Milestones

Each milestone ends with something the Captain can try.

### M0 — Verify the assumptions (spike, about 1 day) — DONE, see [docs/m0-findings.md](docs/m0-findings.md)
Prove the unverified facts the design rests on, against the real `claude` v2.1.285, before any building:
1. Long-lived stream-json stdin session: message framing, a follow-up message mid-run, interrupt.
2. `--permission-prompt-tool` wired to a local MCP server: request shape and how to allow or deny.
3. `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1` loads an added worktree's `CLAUDE.md`, including its `@AGENTS.md` import.
4. Deny rules plus the sandbox stop Edit, Write, *and* Bash writes into a main checkout on macOS. On Windows the sandbox needs WSL2, so measure what still holds natively.
5. A background `coord wait` re-invokes an idle interactive session, and the Stop hook can re-arm it.
6. `--append-system-prompt-file` outranks the `workspace` AGENTS.md "implement" routing in practice. Test: ask the Coordinator to "fix PROJ-XXXX" and it must delegate.
7. `autoMemoryDirectory` set through `--settings` gives one memory store shared across folders.
8. A headless Worker launched in the Launch folder sees the same MCP servers as an interactive session there: user-scope servers, claude.ai connectors, per-folder enable/disable state, plus the coord server. Check that several Workers can share the Playwright extension (one tab each).

The Captain sees a one-page findings note. Any failing item comes back as a decision before M1.

### M1 — Launch, Home, first Worker
- `coord` (launch, `[y/N]` takeover, `--continue`), `coord project add`, `coord status`, and the Home layout.
- `coord config import`, a one-time import of the previous orchestrator skill's config (later removed).
- The Coordinator writes a Brief, then `coord task new` / `coord spawn` starts a Worker in a leased worktree.
- `coord watch <task>` shows the live stream, read-only.

The Captain can type "fix X" from `workspace`, watch a Worker do it in a worktree, and get a Report.

### M2 — Supervision and steering
- `coord wait`, the Stop-hook reminder, and `coord steer`.
- Worker states `blocked` and `needs-decision`.
- ~~The permission prompt tool and grants~~: retired by [ADR-0004](docs/adr/0004-coord-is-the-coordinators-toolbox.md); Workers run in `auto`.
- Restart reconcile: running Workers are reattached, and dead ones are marked `needs-decision`.

The Captain can leave several Workers running, and the Coordinator speaks up on its own when one finishes, gets stuck, or is refused a call it needs. Killing the terminal loses nothing.

### M3 — Plans, models, multi-repo — DONE (multi-repo built in M4; Plan gate and `--skip-plan` added; precedence and model guards covered by tests)
- `plan_approval` with all three values at all three levels, and a Plan that holds a product decision reaches the Captain impact-first.
- Model precedence, `coordinator_may_choose`, and `forbidden_models`, with what ran recorded in the Report.
- Ticket-folder writes.
- One Task holding several worktrees, with linked MRs and the merge order in the Report.
- Lookup-versus-scout guidance in the role.

The Captain can run a `proto` plus `api` change as one Task, and choose how much to see of each Plan.

### M4 — MR lifecycle and watcher — DONE
The Captain can open an MR, close the terminal, and come back to a Coordinator that already knows what happened to it: merged and waiting for the deploy go-ahead, a short list of review comments to choose from, or red CI to look at. A Task stays open through the deploy and ends Landed only when the SOP is finished.

What the Captain sees:
- `coord status` and `coord show` list each MR with its state, CI and unread comments; the Fleet line adds "N merged".
- Every MR change other than a merge lands as "needs you" until the Coordinator handles it by the SOP or with the Captain.
- `drop` asks for the Captain's word, stops the Worker, and closes the MR only on "and close the MR".

Internal choices (adopted):
- **Watcher.** `coord _watch`, one detached process per Home (lock file with pid and start time). Any command that leaves a Task with an open MR starts it if it is not running; it exits when no Task has an open MR. It polls every 2 minutes by default (`mr_poll_interval`, at least 30s) with `glab api` / `gh api` and keeps a per-MR position (state, head pipeline status, last note id) in the Task folder, so a crash or reboot loses nothing and repeats nothing.
- **Facts, not policy.** The watcher appends `mr-merged`, `mr-closed`, `mr-ci` (red or back to green) and `mr-comments` (every new human note, with author, excerpt and link) to the Task's events, which wake `coord wait` as today. Facts that need the Captain (closed, red CI, new comments, ready to merge) are saved with the MR position in one write as pending asks, so a crash loses or repeats nothing, and one rule settles the Task whenever no Worker is busy with it: pending asks → `needs-decision`; else any MR open → `waiting-review`; else any merged → `merged`; else `reported`. Asks raised while a Worker runs wait until it reports. The Captain's own comments are ignored, GitHub review comments are tracked by seen ids, and GitLab counts as approved only with an approver. An API failure is retried; after an hour of failures one `mr-watch-failing` event says so.
- **Coordinator commands.** `coord ack <task> [--note]` puts a handled Task back to waiting on its MR. `coord land <task>` refuses while an MR is still open, returns settled worktrees and ends the Task `landed`. `coord drop <task> [--close-mr]` stops a live Worker and waits for it to exit, records any uncommitted or unpushed work in the event log, returns the worktrees and ends the Task `dropped`; `--close-mr` closes the MRs with `glab`/`gh`, never deletes a branch.
- **Fixes and follow-up MRs** use what exists: `coord steer` resumes the original Worker in its still-leased worktree, and its next `coord report --status done --mr` puts the Task back to waiting on its MRs (a deploy's deploy-config or Unleash MR is added the same way, see Multi-repo).
- **Usage limits.** When a Worker hits the limit its Task goes `blocked` with the reset time in local time, one event; nothing resumes on its own.
- **Role.** The Coordinator role gets an "After the MR" section with the rules in Settled product behaviour (SOP first, otherwise the Captain; comments as a list with a recommendation each; no MR replies unless the SOP says so; land when the SOP's last step is done).
- **Docs.** `CONTEXT.md` redefines Landed and adds Merged and Watcher; ADR-0005 records "a Task ends when its SOP is finished, and the watcher reports facts only".
- **Tests.** Fake `glab`/`gh` on PATH replaying recorded API JSON for the watcher; the fake `claude` for resume, land and drop; read-only smoke polls against one real merged GitLab MR and one GitHub PR. No MR is opened, closed or commented on by a test.
- **Checks carried over.** Whether the Coordinator can save its auto memory with Write denied, and the real `coord` lock and status line across `/clear`'s new session id.

- **Approvals.** An MR approved with green CI raises `mr-ready` ("ready to merge"), since merging is the Captain's word (the Captain, 2026-10-02).
- **Multi-repo, pulled forward from M3** (the Captain, 2026-10-02: more than one MR per Task is common). A Task holds several Projects: `coord task new --project` repeats, and `coord task add-project <task> <project>` adds one later (a deploy's Unleash or deploy-config edit). The Worker gets one leased worktree per Project; a Task may have any number of MRs, several in one repo included (stacked MRs, beta cherry-picks, follow-ups), and how many is the SOP's call, not coord's; each `--mr` is recorded against its repo; MRs the Task did not open (a deploy-config MR opened by CI) are linked with `coord task add-mr`. The watcher tracks them all, `land` waits for all, and the Report gives the merge order across all of them (proto → provider → consumer, base before stacked), which "ready to merge" follows. M3 keeps only the merge-order checks across Tasks.

### M5 — Notifications — DONE (macOS and Windows verified; Linux `notify-send` untested)
- `coord notify "<text>"`: the Coordinator decides the Captain is needed; `coord` only delivers the macOS Notification Center or Windows toast.
- While no Coordinator is open, `coord` itself sends a plain notice when a Task finishes, fails, or asks a question, so the Captain knows to reopen `coord`.

### M6 — Windows and retirement — DONE 2026-10-03, see [docs/windows.md](docs/windows.md)
- Full smoke run on Windows with Git Bash: launch, Worker, treehouse, MR watch, notifications.
- Document anything that behaves differently there.
- Retire the previous orchestrator skill.

### M7 — The Coordinator knows coord — BUILT 2026-10-03, see [ADR-0006](docs/adr/0006-the-coordinator-learns-coord-from-coord.md)
The Captain gets a Coordinator that offers only moves coord allows, knows the way out of every state, answers "can coord do X?" from coord rather than guesswork, opens each session with a recap, closes finished scouts, and merges on the Captain's word itself.
- **One source.** The state table holds each state's moves (command, when, Task class, whose question). The Coordinator role's states section is generated from it; `coord show` prints `Next:`, `coord status` a `NEXT` column, and refusals name the moves.
- **Role.** "Knowing coord" (check `--help` before claiming; never spend a Worker learning coord) and "Where Workers work" (worktrees, Task folder choice, multi-repo).
- **SessionStart hook.** `coord _session-start` hands the Coordinator the Fleet at start, resume, clear and compaction.
- **`coord merge <task> [--mr <url>]... [--method merge|squash|rebase]`.** `gh pr merge`/`glab mr merge` in linked order, the method from `--method`, else the only one a GitHub repo allows, else the GitLab project's own; no branch deletion, admin or auto-merge; reads each MR's state afterwards and settles the Task as the watcher would.
- **Landing scouts.** `coord land` accepts a `scout` or `review-code` Task in `reported`.
- **Left to try.** An e2e run with a live Coordinator in the e2e rig, and a real `glab mr merge`.

## Risks

- **Auto mode trusts the Brief.** Claude Code's classifier treats a Worker's Brief and steers as the user's authorisation, so a Coordinator that writes "delete these keys" gets them deleted. Prod writes rest on the Coordinator and Worker roles (Captain's word first), not on a mechanical gate ([spike](spikes/m1/a-auto-mode/FINDINGS.md)).
- **Shell wrappers beyond the deny list.** The Coordinator's git-write denies match how a command starts; `sh -c`, `bash -c`, `zsh -c` and `eval` are denied too, but a determined agent could still find another wrapper. The deny list guards against drift, not against an adversarial Coordinator.

- **Subscription terms.** Anthropic's terms explicitly allow an end user signing in to the unmodified Claude Code binary with their own subscription. coordinator-cli stays inside that case: it runs the published `claude` binary, never reads, stores, or injects credentials (no `setup-token`, no `CLAUDE_CODE_OAUTH_TOKEN`), and every install uses its own owner's login. The remaining judgment call is volume: plan limits "assume ordinary, individual usage", and many parallel Workers with no cap is heavy individual usage that may hit limits sooner. If the Captain's account is a company Team or Enterprise seat, the Commercial Terms apply instead.
- **Bash writes into main checkouts are not blocked.** By the Captain's choice ([ADR-0003](docs/adr/0003-main-checkouts-protected-by-instruction.md)) only instructions and the Edit/Write guard protect main checkouts. A Worker's Bash command (e.g. `git commit` in a main checkout, which the workspace's `settings.local.json` allows without a prompt) could change one unnoticed.
- **Claude-specific details.** Background re-invocation and the Stop hook are Claude-specific, and a Codex adapter would need its own equivalent. The seam exists, but nothing has been tried yet.
