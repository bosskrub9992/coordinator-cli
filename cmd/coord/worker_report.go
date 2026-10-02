package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/mrwatch"
	"github.com/bosskrub9992/coordinator-cli/internal/supervise"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
	"github.com/spf13/cobra"
)

func checkMRs(class config.Class, status string, mrs []string) error {
	if len(mrs) == 0 {
		return nil
	}
	if status != "done" {
		return errors.New("--mr goes with --status done")
	}
	if class != config.Ship {
		return fmt.Errorf("--mr is for ship Tasks; this Task is %s", class)
	}
	_, err := parseMRs(mrs)
	return err
}

func parseMRs(urls []string) ([]mrwatch.Ref, error) {
	refs := make([]mrwatch.Ref, 0, len(urls))
	for _, u := range urls {
		r, err := mrwatch.ParseURL(u)
		if err != nil {
			return nil, err
		}
		refs = append(refs, r)
	}
	return refs, nil
}

func reportTarget(class config.Class, status string, mrs []task.MR) (task.State, error) {
	switch status {
	case "done":
		if class != config.Ship {
			return task.Reported, nil
		}
		return task.MRSettledState(task.MRFile{MRs: mrs}), nil
	case "failed":
		return task.Failed, nil
	case "blocked":
		return task.Blocked, nil
	case "needs-decision":
		return task.NeedsDecision, nil
	case "plan":
		if class != config.Ship {
			return "", fmt.Errorf("--status plan is for ship Tasks; this Task is %s", class)
		}
		return task.NeedsDecision, nil
	}
	return "", fmt.Errorf("--status must be done, plan, blocked, needs-decision or failed, not %q", status)
}

func moveTo(s *task.Store, id task.ID, to task.State, note string) error {
	t, err := s.Get(id)
	if err != nil {
		return err
	}
	if t.State == to {
		return nil
	}
	if !task.CanTransition(t.State, to) && task.CanTransition(t.State, task.Running) && task.CanTransition(task.Running, to) {
		if _, err := s.Transition(id, task.Running, "Worker reporting"); err != nil {
			return err
		}
	}
	_, err = s.Transition(id, to, note)
	return err
}

func newReportCmd(a *app) *cobra.Command {
	var status, file string
	var mrs []string
	cmd := &cobra.Command{
		Use:   "report --status done|plan|blocked|needs-decision|failed [--mr <url>] --file -",
		Short: "Worker only: submit the Report, or escalate a question to the Coordinator",
		Long: "Worker only. done and failed write the Report and end the Worker's session: done --mr <url> records each MR/PR\n" +
			"and leaves a ship Task waiting-review on them; a ship Task with MRs recorded goes back to waiting on them (or merged\n" +
			"when all are merged); done otherwise leaves the Task reported. plan (ship Tasks) submits the Plan for approval;\n" +
			"plan, blocked and needs-decision send it to the Coordinator; the Worker ends its turn and the answer arrives as\n" +
			"its next message.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isWorker() {
				return errors.New("coord report is for Workers only; the Coordinator reads Reports with coord show <task>")
			}
			ref := os.Getenv(supervise.EnvTask)
			if ref == "" {
				return fmt.Errorf("%s is not set; coord report runs only inside a Worker", supervise.EnvTask)
			}
			s := a.tasks()
			t, err := s.Find(ref)
			if err != nil {
				return err
			}
			text, err := readText(cmd.InOrStdin(), file)
			if err != nil {
				return fmt.Errorf("read the report: %w", err)
			}
			if err := checkMRs(t.Class, status, mrs); err != nil {
				return err
			}
			refs, err := parseMRs(mrs)
			if err != nil {
				return err
			}
			var records []task.MR
			if t.Class == config.Ship && status == "done" {
				if records, err = s.AddMR(t.ID, task.MRFromWorker, refs...); err != nil {
					return err
				}
			}
			to, err := reportTarget(t.Class, status, records)
			if err != nil {
				return err
			}
			rec, _ := s.Worker(t.ID)
			d := map[string]any{"status": status, "model": rec.Model, "effort": rec.Effort, "harness": rec.Harness, "session_id": rec.SessionID}
			if len(mrs) > 0 {
				d["mr_urls"] = mrs
			}
			data, _ := json.Marshal(d)
			first := firstLine(text)
			out := cmd.OutOrStdout()
			switch status {
			case "done", "failed":
				if err := home.WriteFileAtomic(s.ReportPath(t.ID), []byte(strings.TrimRight(text, "\n")+"\n"), 0o644); err != nil {
					return err
				}
				os.Remove(s.QuestionPath(t.ID))
				if _, err := s.Append(t.ID, task.Event{Type: task.EventReport, Text: status + ": " + first, Data: data}); err != nil {
					return err
				}
				if err := moveTo(s, t.ID, to, "Report submitted ("+status+")"); err != nil {
					return err
				}
				if len(records) > 0 {
					if _, err := s.Settle(t.ID, ""); err != nil {
						return err
					}
				}
				if _, err := supervise.Post(s, t.ID, supervise.InboxReported, status); err != nil {
					return fmt.Errorf("Report recorded and Task %s is %s, but the supervisor was not told to end the session: %w", t.ID, to, err)
				}
				fmt.Fprintf(out, "Report recorded; Task %s is now %s. Your session ends here: do nothing more.\n", t.ID, to)
				if len(records) > 0 {
					watchAfter(a, cmd)
				}
			default:
				if _, err := s.SetQuestion(t.ID, task.QuestionFromWorker, text); err != nil {
					return err
				}
				typ := task.EventQuestion
				if status == "plan" {
					typ = task.EventPlan
				}
				if _, err := s.Append(t.ID, task.Event{Type: typ, Text: text, Data: data}); err != nil {
					return err
				}
				if err := moveTo(s, t.ID, to, status+": "+first); err != nil {
					return err
				}
				fmt.Fprintf(out, "Sent to the Coordinator; Task %s is now %s. End your turn now; the answer arrives as your next message.\n", t.ID, to)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "done, plan, blocked, needs-decision or failed (required)")
	cmd.Flags().StringVar(&file, "file", "", "read the text from this file, or - for stdin (required)")
	cmd.Flags().StringArrayVar(&mrs, "mr", nil, "ship Tasks: an MR/PR URL this Report hands over for review (repeat for several)")
	cmd.MarkFlagRequired("status")
	cmd.MarkFlagRequired("file")
	return cmd
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(strings.TrimLeft(l, "#")); l != "" {
			if r := []rune(l); len(r) > 200 {
				return string(r[:199]) + "…"
			}
			return l
		}
	}
	return ""
}
