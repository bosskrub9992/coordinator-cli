# The Coordinator learns coord from coord, and merges with coord

The first real use showed a Coordinator that knew coord's commands but had no model of a Task's states, where Workers run, or what may happen next. It offered to land a scout that coord then refused. It had no way out of `failed` that it knew of. It told the Captain that Workers could not start in the Launch folder, then found `--ticket-folder` in `--help`. It offered to run a scout just to learn how coord picks the Task folder. It steered a Worker to merge instead of merging itself. And it opened a fresh session without a recap until the Stop hook flagged unread events. Each gap was knowledge coord already had and did not give it.

So coord teaches the Coordinator, from one source. The state table in `internal/task/state.go` holds, for each state, its meaning, the states it may go to, and its moves: the commands that move it on, filtered by Task class and by whether the pending question is the Worker's or the watcher's. From that table coord:
- generates the "Task states and their moves" section of the Coordinator role at launch;
- prints a Task's moves as `Next:` in `coord show` and as `NEXT` in `coord status`;
- names them when it refuses a command.

A test keeps the table and coord's own refusals in step: a state offers `land` only if it can go to `landed`. The hand-written role adds what a table cannot hold: where Workers work and how the Task folder is chosen, and the rule to check `coord <command> --help` before telling the Captain what coord can or cannot do, never spending a Worker to find out.

A SessionStart hook (`coord _session-start`) gives the Coordinator the Fleet, unread events included, whenever its session starts, resumes, clears or compacts, and asks it to open its first reply with a recap. The recap no longer depends on the Coordinator remembering to run `coord status`.

Merging is mechanics, like closing an MR on `coord drop --close-mr`, so it moves into coord: `coord merge <task>` merges a ship Task's open MRs in the order they were linked, stops at the first failure, never deletes a branch, never uses admin or auto-merge, reads each MR's real state afterwards, and settles the Task as the watcher would. Whether to merge stays the Captain's word, carried by the role. Workers name their MRs in merge order with `coord report --mr`.

A scout or review-code Task lands from `reported` once the Coordinator has given the Captain its Report, with no word needed, so finished scouts leave the board. Landed now means "finished": a ship Task's SOP is done, or a Report has reached the Captain.

## Considered Options

- **A skill with lifecycle playbooks**: loads only when the Coordinator thinks it needs it, and every gap above was something it did not know it lacked. Rejected.
- **The Launch folder's AGENTS.md or `COORDINATOR.md`**: per workspace or per Captain, and AGENTS.md also reaches Workers. Right for SOPs, wrong for how coord works. Rejected for this.
- **A longer hand-written role only**: drifts from the code the first time a state or refusal changes. Rejected in favour of generating the states section.
- **Let the Coordinator run `gh pr merge` itself**: works, but every Coordinator then rediscovers merge methods, ordering and state settling, and the git-write deny list would need an exception. Rejected.

## Consequences

The always-loaded role grows from about 1,000 to about 1,600 words; a test caps it at 1,650. Changing a state's moves is a code change that updates the role, `coord show`, `coord status` and the refusals together. A running Coordinator picks up the new role only when it is relaunched.
