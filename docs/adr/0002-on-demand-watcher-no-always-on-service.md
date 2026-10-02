# Only an on-demand watcher, no always-on service

coordinator-cli has no daemon that runs all the time. Each Worker is owned by its own small detached supervisor process, and a single watcher process starts only when something needs watching (an open MR, a pending notification) and exits once nothing does. This keeps Workers and MR watching alive after the Captain closes the Coordinator's terminal, without asking the Captain to install, start, or upgrade a service on macOS and Windows, and without one process whose crash would take every Worker down with it.

## Considered Options

- **Always-on local daemon** (launchd / Windows service): one owner for everything and easy to add a web UI to, but it is a service to install, keep running, and upgrade on two operating systems, and a single point of failure for every Worker.
- **Everything inside the Coordinator's session**: no background processes at all, but closing the terminal would stop MR watching and notifications, so work would silently stall while the Captain is away.

## Consequences

The later web dashboard reads the Home's files directly rather than talking to a service, so it can be started on demand too.
