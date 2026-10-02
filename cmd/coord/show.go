package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/task"
	"github.com/spf13/cobra"
)

func newShowCmd(a *app) *cobra.Command {
	var brief, report, question bool
	cmd := &cobra.Command{
		Use:   "show <task> [--brief|--report|--question]",
		Short: "Show one Task: state, class, Projects, worktrees, MRs, then its Report or pending question (read-only)",
		Long: "Show one Task: its state, class, Projects, Task folder, worktrees and branches, MRs (state, CI, approval,\n" +
			"comments since the last coord ack), and what ran, followed by\n" +
			"the pending question when the Worker is blocked or needs a decision, otherwise the Report once there is one.\n" +
			"--brief, --report or --question prints only that text.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n := 0
			for _, b := range []bool{brief, report, question} {
				if b {
					n++
				}
			}
			if n > 1 {
				return errors.New("pass at most one of --brief, --report, --question")
			}
			s := a.tasks()
			t, err := s.Find(args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			switch {
			case brief:
				text, err := s.Brief(t.ID)
				if err != nil {
					return fmt.Errorf("read the Brief of Task %s: %w", t.ID, err)
				}
				return printText(out, text)
			case report:
				text, ok, err := s.Report(t.ID)
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("Task %s has no Report yet (state %s)", t.ID, t.State)
				}
				return printText(out, text)
			case question:
				text, ok, err := pendingQuestion(s, t)
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("Task %s has no pending question (state %s)", t.ID, t.State)
				}
				return printText(out, text)
			}
			return printTask(out, s, t, time.Now())
		},
	}
	cmd.Flags().BoolVar(&brief, "brief", false, "print only the Brief")
	cmd.Flags().BoolVar(&report, "report", false, "print only the Report")
	cmd.Flags().BoolVar(&question, "question", false, "print only the pending question")
	return cmd
}

func printText(w io.Writer, text string) error {
	_, err := fmt.Fprintln(w, strings.TrimRight(text, "\n"))
	return err
}

func pendingQuestion(s *task.Store, t task.Task) (string, bool, error) {
	if t.State != task.Blocked && t.State != task.NeedsDecision {
		return "", false, nil
	}
	b, err := os.ReadFile(s.QuestionPath(t.ID))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	return string(b), err == nil, err
}

func mrLine(m task.MR) string {
	head := m.Ref.URL
	if m.Project != "" {
		head += " (" + m.Project + ")"
	}
	if m.Watch.State == "" {
		return fmt.Sprintf("%s: not polled yet, %d comments since ack", head, m.CommentsSinceAck)
	}
	ci := string(m.Watch.CI)
	if ci == "" {
		ci = "none"
	}
	approved := "not approved"
	if m.Watch.Approved {
		approved = "approved"
	}
	return fmt.Sprintf("%s: %s, CI %s, %s, %d comments since ack", head, m.Watch.State, ci, approved, m.CommentsSinceAck)
}

func printTask(w io.Writer, s *task.Store, t task.Task, now time.Time) error {
	rec, err := s.Worker(t.ID)
	if err != nil {
		return err
	}
	mrs, err := s.MRs(t.ID)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Task:        %s  %s\n", t.ID, t.Title)
	fmt.Fprintf(w, "State:       %s for %s\n", taskView{Task: t}.display(), age(now.Sub(t.StateSince)))
	fmt.Fprintf(w, "Class:       %s\n", t.Class)
	if len(t.Projects) == 1 {
		fmt.Fprintf(w, "Project:     %s\n", t.Projects[0])
	} else {
		fmt.Fprintf(w, "Projects:    %s\n", strings.Join(t.Projects, ", "))
	}
	if t.Ticket != "" {
		fmt.Fprintf(w, "Ticket:      %s\n", t.Ticket)
	}
	fmt.Fprintf(w, "Task folder: %s\n", t.Folder)
	for _, wt := range rec.Worktrees {
		line := wt.Project + ": " + wt.Path + " (branch " + wt.Branch + ")"
		if wt.Returned() {
			line += " returned"
		}
		fmt.Fprintf(w, "Worktree:    %s\n", line)
	}
	for _, m := range mrs {
		fmt.Fprintf(w, "MR:          %s\n", mrLine(m))
	}
	if rec.Model != "" {
		fmt.Fprintf(w, "Ran:         %s %s on %s\n", rec.Model, rec.Effort, rec.Harness)
	}
	if rec.PlanApproval != "" {
		fmt.Fprintf(w, "Plan:        %s (from the %s setting)\n", rec.PlanApproval, rec.PlanFrom)
	}
	if rec.SessionID != "" {
		fmt.Fprintf(w, "Session:     %s\n", rec.SessionID)
	}
	if q, ok, err := pendingQuestion(s, t); err != nil {
		return err
	} else if ok {
		fmt.Fprintf(w, "\nQuestion (%s):\n", t.State)
		return printText(w, q)
	}
	text, ok, err := s.Report(t.ID)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(w, "\nNo Report yet.")
		return nil
	}
	fmt.Fprintln(w, "\nReport:")
	return printText(w, text)
}
