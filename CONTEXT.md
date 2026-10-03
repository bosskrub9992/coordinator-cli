# coordinator-cli

coordinator-cli lets one person direct many coding agents by talking to a single agent, the Coordinator, from any directory.

## Language

### People and agents

**Captain**:
The single human who owns a coordinator-cli install and is the only one who gives it intent and decisions.
_Avoid_: User, operator

**Coordinator**:
The one agent the Captain talks to, available to them most of the time; it turns intent into Tasks, supervises Workers, and reports outcomes, but never makes a change itself.
_Avoid_: Orchestrator, first mate, main agent, lead

**coordinator-cli**:
The Coordinator's toolbox: the `coord` command, the Worker processes it starts, and the Home it keeps; it carries out mechanics exactly (processes, state, token-free waiting, notifications, summaries) and never makes a judgment, permissions included.
_Avoid_: Coordinator (that is the agent), supervisor, daemon, server

**Worker**:
One supervised agent session that carries out exactly one Task and reports only to the Coordinator.
_Avoid_: Crewmate, subagent, child agent

**Harness**:
A coding-agent CLI that can host the Coordinator or a Worker, such as Claude Code, Codex, or Cursor Agent.
_Avoid_: Backend, provider, model

### Work

**Task**:
One unit of delegated work with an explicit contract, carried out by one or more Workers in sequence, possibly across several Projects.
_Avoid_: Job, ticket, issue

**Task class**:
The shape of a Task's deliverable: `ship` (a code change that lands), `scout` (a standalone Report, never a code change), or `review-code` (a review of a `ship` Task's change).
_Avoid_: Task type, mode

**Brief**:
The self-contained written contract a Worker receives for a Task.
_Avoid_: Spec, prompt, instructions

**MR**:
A GitLab merge request or GitHub pull request linked to a Task; a Task can have any number, in one or several Projects, opened by its Worker or by someone else such as CI.
_Avoid_: PR (as a separate term), change request

**Report**:
The durable written outcome a Worker leaves when its Task ends.
_Avoid_: Summary, result, output

### Places

**Home**:
The single global location where coordinator-cli keeps all durable state, independent of the directory the Captain is in.
_Avoid_: Workspace, FM_HOME, root

**Project**:
A git repository the Captain already has checked out on their machine and wants work done in; Workers never change it directly, only isolated copies of it.
_Avoid_: Repo, codebase, service

**Watcher**:
The background process coordinator-cli runs only while some Task has an open MR; it polls those MRs and records what changed as facts, never acting on them.
_Avoid_: Daemon, poller, bot

**Fleet**:
Every Task and Worker the Home knows about, no matter which directory or conversation started it.
_Avoid_: Crew, swarm, session

**Launch folder**:
The folder the Captain started the Coordinator in; its instructions shape the Coordinator and every Worker the Coordinator starts.
_Avoid_: Cwd, workspace, start dir

**Task folder**:
The folder a Worker starts in and keeps its plan and notes in: the Task's ticket folder inside the Launch folder when one applies, otherwise a folder for the Task in the Home.
_Avoid_: Ticket folder, work dir, scratch

### Decisions

**Plan**:
A `ship` Worker's proposed approach for a Task, written before it changes anything, impact first, that must be approved before it proceeds; the Coordinator may pre-approve one for a trivial change.
_Avoid_: Design, proposal

**Product decision**:
A choice that changes what the Captain, or the users of a Project, visibly get; every other choice is an internal choice.
_Avoid_: Business decision, big decision

**Merged**:
Every MR of a `ship` Task is merged, but the Task's SOP still has steps after the merge, such as the deploy.
_Avoid_: Landed, done

**Landed**:
A finished Task: a `ship` Task whose Project's SOP is finished (for example: deployed to prod and post-checked), not just merged, or a `scout` or `review-code` Task whose Report has reached the Captain; work that has not landed is never thrown away without the Captain's word.
_Avoid_: Done, shipped, released, merged

**Dropped**:
A Task the Captain explicitly ended without it landing; its worktree is cleaned up only on that word.
_Avoid_: Cancelled, abandoned, closed
