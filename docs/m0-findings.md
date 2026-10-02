# M0 findings — assumption spike (2026-10-01)

Tested against real `claude` 2.1.285 on macOS. Detail and raw evidence: [spikes/m0/a-worker-protocol](../spikes/m0/a-worker-protocol/FINDINGS.md), [spikes/m0/b-isolation](../spikes/m0/b-isolation/FINDINGS.md), [spikes/m0/c-coordinator](../spikes/m0/c-coordinator/FINDINGS.md). No real repo or config was changed.

## Verdicts

| # | Assumption | Verdict | What changes |
|---|---|---|---|
| 1 | A Worker is one long-lived stream-json process: follow-ups, interrupt, resume | PASS | — |
| 2 | Permission requests reach coord through `--permission-prompt-tool` | PASS with changes | `--permission-mode default` (Captain's default is `auto`); coord's MCP server sends progress every ~10 s while holding; it owns the needs-decision state and logs rewrites itself |
| 3 | A worktree's `CLAUDE.md`, and its `@AGENTS.md` import, reach the Worker | PARTIAL → fixed | Imports outside the cwd are silently dropped and nothing per-run can approve them. coord resolves the imports itself and passes them with `--append-system-prompt-file` (proven); an `InstructionsLoaded` hook records what loaded |
| 4 | Main checkouts can't be written, even from Bash | FAIL as planned → PASS with a new recipe (macOS) | Worker cwd must not contain main checkouts (ticket folder); sandbox with narrow `.git` allow paths; Go guard hook as an allow-list; pushes and fetches only through `coord push`/`coord fetch`; the Captain's user `additionalDirectories` must not cover `~/Desktop/works`. Native Windows has no sandbox |
| 5 | A background `coord wait` wakes an idle Coordinator; a Stop hook re-arms it | PASS | Stop hook reads `background_tasks`; its own 2-block loop guard; re-arm before replying so focus mode can't hide the update |
| 6 | The Coordinator role beats the `workspace`'s "implement" routing | PASS 6/6 | Milder wording that keeps the SOP's confirm gates; explicit "background wait", "never substitute subagents"; deny git writes and file tools at launch |
| 7 | One Coordinator memory across folders | PASS | — |
| 8 | Headless Workers see the Captain's MCP servers | PASS | Never `--strict-mcp-config`; Briefs open a fresh Playwright tab then navigate; close the extension's Welcome tab at Task end if it lingers |

Also required everywhere: build each Worker's environment without the parent session's `CLAUDE*`, `ORCA_*` and `HERDR_*` variables.

## Decisions for the Captain

1. **Workers start in their ticket folder**, not in the Launch folder. That keeps main checkouts out of the always-writable cwd; the Launch folder's instructions still load (as a parent folder, plus the resolved imports).
2. **User settings: S1 or S2.** S1: the Captain removes `~/Desktop/works` from user `permissions.additionalDirectories`; Workers keep the whole setup natively and coord refuses to launch if it comes back. S2: Workers skip user settings and coord rebuilds them (filtered settings copy, MCP list, skills via a plugin dir, which renames them `user-skills:<name>`).
3. **Workers push and fetch only through `coord push` / `coord fetch`.** Letting `git push` bypass the sandbox let a test create branches in the main repo and run arbitrary commands.

## Still unproven (moved into later milestones)

- A real SSH push to `gitlab.example.com` from a Worker worktree (M1 smoke test).
- The role under sustained pressure with real Workers (M1 smoke test).
- Whether the Playwright extension's Welcome tabs close on their own (M1).
- Everything on Windows (M6).

## Decisions taken (2026-10-01)

1. Workers start in their Task folder, and the Brief names where they work.
2. The Captain's settings stay as they are; Workers keep the whole setup natively.
3. No sandbox and no push wrapper: main checkouts are protected by instructions plus the Edit/Write guard ([ADR-0003](adr/0003-main-checkouts-protected-by-instruction.md)). Workers push with plain `git push`.
4. coordinator-cli has nothing Orca-specific; only the parent session's Claude Code identity variables are removed from Worker environments.
