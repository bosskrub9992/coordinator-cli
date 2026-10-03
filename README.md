# coordinator-cli

`coord` lets one person direct many coding agents by talking to a single agent, the Coordinator.

Type `coord` in any folder and you get a normal Claude Code session with the Coordinator role. It turns what you ask for into Tasks and runs each one as a Worker: a headless Claude Code session in its own git worktree, in parallel. Workers report back, the Coordinator brings you only the decisions that need you, and a watcher follows each MR through review, CI and merge, even while the terminal is closed.

Vocabulary: [CONTEXT.md](CONTEXT.md). Design decisions: [docs/adr/](docs/adr/). Plan and status: [PLAN.md](PLAN.md).

## Requirements

- Go 1.27 or later
- [Claude Code](https://docs.claude.com/en/docs/claude-code) (`claude` on PATH)
- [treehouse](https://github.com/kunchenguid/treehouse) for worktrees, with a `treehouse.toml` in each Project's main checkout
- `glab` and/or `gh`, logged in, for MR watching
- macOS, Linux, or Windows with Git Bash ([Windows notes](docs/windows.md))

## Install

    go install github.com/bosskrub9992/coordinator-cli/cmd/coord@latest

## Use

    coord project add ~/src/api     # register a repo as a Project
    coord                           # start the Coordinator in the current folder
    coord --continue                # resume the last Coordinator conversation
    coord status                    # the Fleet: every Task, its state and MRs

Everything else (`coord task new`, `coord spawn`, `coord wait`, `coord steer`, `coord land`, ...) is run by the Coordinator itself; `coord --help` lists them.

State lives in `~/.coordinator-cli/` (`COORD_HOME` overrides): `config.json` for models, efforts and plan approval, `COORDINATOR.md` for your own additions to the Coordinator role, such as when a Task counts as finished in your workflow.

## Develop

    go vet ./...
    go test -race ./cmd/... ./internal/...
    scripts/smoke-worker.sh          # real-Claude smoke test, uses tokens

## License

[MIT](LICENSE)
