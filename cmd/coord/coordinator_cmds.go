package main

import "github.com/spf13/cobra"

func addLaunchFlags(root *cobra.Command) {
	f := root.Flags()
	f.Bool("continue", false, "resume the last Coordinator conversation instead of starting a new one")
	f.Bool("takeover", false, "take over from a live Coordinator without asking")
}

func runLaunch(a *app, cmd *cobra.Command, args []string) error {
	cont, _ := cmd.Flags().GetBool("continue")
	takeover, _ := cmd.Flags().GetBool("takeover")
	return launch(a, cmd, launchOptions{resume: cont, takeover: takeover})
}

func coordinatorCommands(a *app) []*cobra.Command {
	return []*cobra.Command{newWaitCmd(a), newStopHookCmd(a), newSessionStartCmd(a), newStatusLineCmd(a), newCoordinatorGuardCmd(a), newAckCmd(a), newLandCmd(a), newDropCmd(a), newNotifyCmd(a)}
}
