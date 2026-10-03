package main

import (
	"fmt"
	"strings"

	"github.com/bosskrub9992/coordinator-cli/internal/supervise"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
	"github.com/spf13/cobra"
)

func liveWorker(a *app, ref string) (task.Task, task.WorkerRecord, error) {
	s := a.tasks()
	t, err := s.Find(ref)
	if err != nil {
		return t, task.WorkerRecord{}, err
	}
	w, err := s.Worker(t.ID)
	if err != nil {
		return t, w, err
	}
	if !w.SupervisorLive() {
		return t, w, fmt.Errorf("Task %s has no live Worker. %s", t.ID, t.NextHint())
	}
	return t, w, nil
}

func newSteerCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "steer <task> <message>",
		Short: "Send the Worker a message; resumes its session if it has exited",
		Long: "Queue a message for the Task's Worker. A running Worker reads it at its next step (a steer event is written\n" +
			"when it is delivered). If the Worker has exited, its session is resumed with the message.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := a.tasks()
			t, err := s.Find(args[0])
			if err != nil {
				return err
			}
			text := strings.Join(args[1:], " ")
			if strings.TrimSpace(text) == "" {
				return fmt.Errorf("the steer message is empty")
			}
			if t.State == task.Queued {
				return fmt.Errorf("Task %s has no Worker yet; start it with coord spawn %s", t.ID, t.ID)
			}
			if t.State.Terminal() {
				return fmt.Errorf("Task %s is %s and final; it cannot be steered. Start a new Task for more work", t.ID, t.State)
			}
			w, err := s.Worker(t.ID)
			if err != nil {
				return err
			}
			if w.SessionID == "" {
				return fmt.Errorf("Task %s has no Worker session", t.ID)
			}
			m, err := supervise.Post(s, t.ID, supervise.InboxSteer, text)
			if err != nil {
				return err
			}
			if w.SupervisorLive() {
				fmt.Fprintf(cmd.OutOrStdout(), "Steer %s queued for %s; a steer event is written when the Worker reads it.\n", m.ID, t.ID)
				return nil
			}
			bin, err := coordBinary()
			if err != nil {
				return err
			}
			if err := startSupervisor(a, bin, t.ID); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Resuming the Worker of %s (session %s) with steer %s.\n", t.ID, w.SessionID, m.ID)
			return nil
		},
	}
}

func newInterruptCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "interrupt <task>",
		Short: "Interrupt the Worker's current turn; it stays alive and waits for a steer",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, _, err := liveWorker(a, args[0])
			if err != nil {
				return err
			}
			if _, err := supervise.Post(a.tasks(), t.ID, supervise.InboxInterrupt, ""); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Interrupt sent to %s.\n", t.ID)
			return nil
		},
	}
}

func newStopCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <task>",
		Short: "End the Worker: interrupt it, close its input, and kill it if it has not exited after 30s",
		Long: "End the Task's Worker. A Worker stopped before its final Report leaves the Task failed; its worktree and\n" +
			"session are kept, so coord steer can resume it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, _, err := liveWorker(a, args[0])
			if err != nil {
				return err
			}
			if _, err := supervise.Post(a.tasks(), t.ID, supervise.InboxStop, ""); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Stopping the Worker of %s.\n", t.ID)
			return nil
		},
	}
}
