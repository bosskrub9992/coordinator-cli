# The Coordinator is a Harness session launched by `coord` in the Captain's current folder

The Coordinator is not a custom agent or a background service: `coord` starts the Captain's own Claude Code session in whatever folder the Captain is in, so it reads that folder's instructions (for example a team `workspace`'s SOPs), and injects the Coordinator role at system-prompt level so "delegate, never implement" outranks folder instructions that say "implement". Folder workflows are treated as plans the Coordinator walks by delegating each working step to Workers. Mechanics live in the deterministic `coord` command, which the Coordinator calls from its shell, so moving to another Harness later means a new launcher and Worker adapter, not a new design.

## Considered Options

- **Custom agent on the Claude Agent SDK**: full control of context, but Claude-only and it loses the Harness's own skills and setup.
- **Own model-agnostic agent loop over model APIs**: vendor-neutral, but needs per-token API keys instead of subscriptions and rebuilds what Harnesses already do.
- **Launch the Coordinator in the Home, ignoring the current folder**: same behaviour everywhere, but the Coordinator would not know the workflow of the folder the Captain works from.
- **A `/coordinator` skill inside an already-open session**: least to build, but the role ranks no higher than folder instructions, and launch-time settings (own memory, model and effort, background supervision, single-live-Coordinator lock) are unavailable.
