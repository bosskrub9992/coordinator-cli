# A Task ends when its SOP is finished, and the watcher reports facts only

A `ship` Task does not end at the merge. For the Captain a change is done when the Project's SOP is finished (for example: deployed to prod and post-checked), and the deploy often needs more MRs (a deploy-config `vars.json` bump, an Unleash flag). So a Task can hold several Projects and any number of MRs, passes through `merged` while the after-merge steps remain, and keeps its worktrees leased until the Coordinator lands it.

What happens to an MR after it is up follows the same split as ADR-0004. The watcher polls each open MR and records facts: merged, closed, CI red or green again, new comments, approved with green CI. A fixed rule turns facts into Task states (all MRs merged → `merged`; anything else that needs a reaction → `needs-decision`), so the Captain sees "needs you" without coord judging anything. What to do about a fact belongs to the Coordinator, which follows the Launch folder's SOP and, where the SOP is silent, asks the Captain: review comments are fixed only when the Captain agrees, replies on an MR are posted only when the SOP says so, and the deploy starts only when the SOP says so or on the Captain's word.

## Considered Options

- **Task ends at the merge, deploy as a new Task**: simpler states, but one ticket becomes several Tasks and nothing ties the deploy back to the change.
- **Built-in reactions in coord** (auto-fix red CI, auto-resume on comments, retry caps): fewer interruptions, but it puts policy in the toolbox, and the right reaction differs per Project and SOP.

## Consequences

`coord ack` exists so the Coordinator can put a Task back to waiting after handling a fact without resuming the Worker. Rules for red CI or closed MRs live in each Project's SOP; changing them never needs a coord release.
