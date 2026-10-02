# `coord` is the Coordinator's toolbox, not its supervisor, and permissions belong to the Harness

The goal that outranks every feature is that **the Coordinator is available to the Captain most of the time**. `coord` exists to make that cheap. It does the mechanics an agent can't do cheaply or reliably: keeping Worker processes alive, keeping crash-safe state, waiting without tokens, polling MRs, sending notifications, and summarising Worker output. Every judgement (what to approve, when the Captain is needed, what counts as risky) belongs to the Coordinator's role or to the Harness.

So `coord` no longer judges permissions. Workers and the Coordinator run in Claude Code's `auto` permission mode, the same mode the Captain uses in their own sessions. A Worker has no permission prompt tool and no coord MCP server. When `auto` would ask, the headless Worker gets an immediate denial, carries on, and reports `blocked` if it needs the call. The Coordinator relays that to the Captain, and the Captain's go-ahead reaches the Worker as an ordinary steer.

Two mechanisms stay, because they are coordination, not security:
- **The worktree guard** (ADR-0003): a PreToolUse hook that keeps a Worker's Edit/Write inside its own worktrees and Task folder, so parallel Workers never touch main checkouts or each other's work.
- **The Coordinator's lockdown**: no NotebookEdit/EnterWorktree, Edit/Write only into its own memory folder (a PreToolUse guard, because a plain deny also blocks the Coordinator's auto memory), and a git-write deny list (plus `sh -c`/`bash -c`/`zsh -c`/`eval`, which hide commands from deny rules). Everything else is allowed, git reads and `git fetch` included. The lockdown is what keeps the Coordinator from drifting into doing work and becoming unavailable, so it serves the goal above. It guards against drift, not against an adversarial agent.

Prod writes still need the Captain's word. That is a rule in the Coordinator and Worker roles, not something `coord` enforces mechanically.

Evidence: [spikes/m1/a-auto-mode/FINDINGS.md](../../spikes/m1/a-auto-mode/FINDINGS.md). The first real test woke the Coordinator 33 times in 6 minutes for permission requests, almost all safe reads, which `coord`'s own read-only and secret heuristics failed to recognise.

## Considered Options

- **coord's own permission policy** (the M1 design: `--permission-mode default`, a prompt tool, read-only and secret heuristics, standing grants): a hand-built classifier that churned on safe reads, raised false secret alarms and kept the Coordinator busy. Rejected.
- **`auto` plus coord hard gates for prod writes and secret reads** through the guard hook: works (a hook `ask` reaches the prompt tool in `auto`), but it keeps permission plumbing in `coord` for risks the roles and `auto` already cover. Rejected by the Captain.
- **`auto` plus a thin pass-through prompt tool** that turns each escalation into a notification: still permission plumbing; a blocked call surfaced through the Worker's report does the same job. Rejected.
- **`bypassPermissions`**: nothing ever asks, and deny rules become the only line. Rejected.
