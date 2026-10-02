# Main checkouts are protected by instruction, not by a sandbox

Workers are told exactly where to work (their leased worktrees and their Task folder), and a `coord _guard` hook stops Claude Code's Edit and Write tools from touching anything else; Bash is not sandboxed. M0 proved that a sandbox can fully protect main checkouts on macOS, but only by starting Workers outside the Captain's user settings (or removing `~/Desktop/works` from `additionalDirectories`), routing every push and fetch through a `coord` wrapper because the sandbox blocks SSH, and accepting no equivalent on native Windows. The Captain chose the simpler model: one behaviour on macOS and Windows, the Captain's settings untouched, and plain `git push`, at the cost that a Worker's Bash command could still change a main checkout unnoticed.

## Considered Options

- **Sandbox with narrow `.git` allow paths and a push wrapper**: hard guarantee on macOS; costs the Captain's settings or a rebuilt copy of them, a push wrapper, and a different story on Windows.
- **Instructions plus a per-command tripwire** that pauses a Worker whose Bash command changed a main checkout: detects instead of preventing; rejected as more machinery than the Captain wants.
