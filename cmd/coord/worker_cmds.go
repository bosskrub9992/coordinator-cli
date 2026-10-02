package main

import (
	"fmt"
	"os"
	"slices"

	"github.com/bosskrub9992/coordinator-cli/internal/supervise"
	"github.com/spf13/cobra"
)

var workerAllowed = []string{"report", "_guard", "_hook", "_mcp", "help"}

func topLevel(cmd *cobra.Command) *cobra.Command {
	for cmd.HasParent() && cmd.Parent().HasParent() {
		cmd = cmd.Parent()
	}
	return cmd
}

func isWorker() bool {
	if os.Getenv(supervise.EnvRole) == supervise.RoleWorker {
		return true
	}
	return os.Getenv(supervise.EnvTask) != "" && !captainTerminal()
}

func authorize(a *app, cmd *cobra.Command) error {
	if !isWorker() {
		return nil
	}
	top := topLevel(cmd)
	if top.HasParent() && slices.Contains(workerAllowed, top.Name()) {
		return nil
	}
	name := "coord"
	if top.HasParent() {
		name += " " + top.Name()
	}
	return fmt.Errorf("%s is not available to a Worker; a Worker finishes or escalates only with `coord report --status done|blocked|needs-decision|failed --file -`", name)
}

func workerCommands(a *app) []*cobra.Command {
	return []*cobra.Command{
		newSpawnCmd(a),
		newSuperviseCmd(a),
		newSteerCmd(a),
		newInterruptCmd(a),
		newStopCmd(a),
		newWatchCmd(a),
		newReportCmd(a),
		newGuardCmd(a),
		newHookCmd(a),
	}
}
