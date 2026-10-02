package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/notify"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
	"github.com/spf13/cobra"
)

var notifier notify.Sender = notify.Desktop{}

var noticeStates = []task.State{task.Reported, task.WaitingReview, task.Merged, task.NeedsDecision, task.Blocked, task.Failed}

func newStore(h home.Home) *task.Store {
	return task.NewStore(h).OnEvent(func(e task.Event) { noticeIfUnattended(h, e) })
}

func noticeIfUnattended(h home.Home, e task.Event) {
	if e.Type != task.EventStateChanged || !slices.Contains(noticeStates, e.To) {
		return
	}
	if l, err := h.ReadLock(); err != nil || (l != nil && l.Live()) {
		return
	}
	text := fmt.Sprintf("%s is %s", e.Task, e.To)
	if e.Text != "" {
		text += ": " + e.Text
	}
	notifier.Fire(notify.Title, oneLine(text+". Open coord to handle it.", summaryLimit))
}

func newNotifyCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "notify <text>",
		Short: "Send the Captain a desktop notification (the Coordinator decides when the Captain is needed)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := strings.Join(args, " ")
			if err := notifier.Send(notify.Title, text); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Notified the Captain.")
			return nil
		},
	}
}
