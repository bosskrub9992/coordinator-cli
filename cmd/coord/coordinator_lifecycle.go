package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/mrwatch"
	"github.com/bosskrub9992/coordinator-cli/internal/supervise"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
	"github.com/bosskrub9992/coordinator-cli/internal/treehouse"
	"github.com/spf13/cobra"
)

var dropStopWait = 60 * time.Second

func newAckCmd(a *app) *cobra.Command {
	var note string
	cmd := &cobra.Command{
		Use:   "ack <task> [--note <text>]",
		Short: "Mark the MR facts that put a Task in needs-decision as handled; it goes back to waiting on its MRs",
		Long: "Acknowledge the watcher's MR facts once the Coordinator has handled them (by the SOP or with the Captain).\n" +
			"The Task goes to waiting-review while any MR is open, otherwise to merged; the watcher's question is cleared\n" +
			"and every MR's comments-since-ack count starts again. Refused while the Worker is live, for a Task with no MRs,\n" +
			"and when the pending question is the Worker's own (answer that with coord steer).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := a.tasks()
			t, err := s.Find(args[0])
			if err != nil {
				return err
			}
			if t, err = s.Ack(t.ID, note); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Acknowledged; Task %s is %s.\n", t.ID, t.State)
			watchAfter(a, cmd)
			return nil
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "what was done about the facts, recorded in the Task's events")
	return cmd
}

func newLandCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "land <task>",
		Short: "End a Task as landed once its SOP is finished: return its settled worktrees and mark it landed",
		Long: "Land a Task whose SOP is finished (for example: deployed to prod and post-checked). Refused while the Worker\n" +
			"is live or any MR is still open. Worktrees that are clean with nothing unpushed go back to treehouse; any\n" +
			"other worktree is kept and noted in the Task's events.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := a.tasks()
			t, err := s.Find(args[0])
			if err != nil {
				return err
			}
			w, err := s.Worker(t.ID)
			if err != nil {
				return err
			}
			if w.SupervisorLive() {
				return fmt.Errorf("Task %s's Worker is still running; land it once the Worker has exited", t.ID)
			}
			mrs, err := s.MRs(t.ID)
			if err != nil {
				return err
			}
			var open []string
			for _, m := range mrs {
				if m.Open() {
					open = append(open, m.Ref.URL)
				}
			}
			if len(open) > 0 {
				return fmt.Errorf("Task %s still has open MRs (%s); a Task lands only when none is open", t.ID, strings.Join(open, ", "))
			}
			if !t.Allows("land") {
				return fmt.Errorf("Task %s cannot land from here. %s", t.ID, t.NextHint())
			}
			kept, err := supervise.ReturnSettled(s, t.ID, treehouse.Client{})
			if err != nil {
				return err
			}
			if t, err = s.TransitionFrom(t.ID, t.State, task.Landed, "landed by the Coordinator"); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Task %s is landed.\n", t.ID)
			printKept(out, kept)
			return nil
		},
	}
}

func printKept(w io.Writer, kept []supervise.KeptWorktree) {
	for _, k := range kept {
		fmt.Fprintf(w, "  kept %s (%s): %s\n", k.Worktree.Path, k.Worktree.Project, k.Why)
	}
}

func newDropCmd(a *app) *cobra.Command {
	var closeMR bool
	cmd := &cobra.Command{
		Use:   "drop <task> [--close-mr]",
		Short: "End a Task without it landing: stop its Worker, record and return its worktrees, mark it dropped",
		Long: "Drop a Task on the Captain's word. A live Worker is stopped first (coord waits up to 60s for it to exit).\n" +
			"Each active worktree's uncommitted changes and commits on no remote are recorded in one dropped-work event,\n" +
			"then the worktree goes back to treehouse with --force (uncommitted changes are discarded); one treehouse refuses\n" +
			"is kept and reported. --close-mr first closes\n" +
			"every open MR with glab or gh (only on the Captain's \"and close the MR\"); branches are never deleted.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := a.tasks()
			t, err := s.Find(args[0])
			if err != nil {
				return err
			}
			if t.State.Terminal() {
				return fmt.Errorf("Task %s is already %s", t.ID, t.State)
			}
			out := cmd.OutOrStdout()
			if err := stopWorker(s, t.ID, dropStopWait); err != nil {
				return err
			}
			if closeMR {
				if err := closeOpenMRs(cmd, s, t.ID); err != nil {
					return err
				}
			}
			kept, err := dropWorktrees(s, t.ID)
			if err != nil {
				return err
			}
			if t, err = s.Transition(t.ID, task.Dropped, "dropped by the Coordinator"); err != nil {
				return err
			}
			fmt.Fprintf(out, "Task %s is dropped.\n", t.ID)
			printKept(out, kept)
			return nil
		},
	}
	cmd.Flags().BoolVar(&closeMR, "close-mr", false, "also close the Task's open MRs/PRs (never deletes a branch)")
	return cmd
}

func stopWorker(s *task.Store, id task.ID, wait time.Duration) error {
	w, err := s.Worker(id)
	if err != nil || !w.SupervisorLive() {
		return err
	}
	if _, err := supervise.Post(s, id, supervise.InboxStop, ""); err != nil {
		return err
	}
	deadline := time.Now().Add(wait)
	for {
		w, err := s.Worker(id)
		if err != nil {
			return err
		}
		if !w.SupervisorLive() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Task %s's Worker did not exit within %s of the stop; nothing was dropped (check coord watch %s)", id, wait, id)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func closeOpenMRs(cmd *cobra.Command, s *task.Store, id task.ID) error {
	mrs, err := s.MRs(id)
	if err != nil {
		return err
	}
	var failed []error
	for _, m := range mrs {
		if !m.Open() {
			continue
		}
		if err := (mrwatch.Closer{}).Close(cmd.Context(), m.Ref); err != nil {
			failed = append(failed, err)
			continue
		}
		key := m.Ref.URL
		if _, err := s.UpdateMRs(id, func(all *[]task.MR) error {
			for i := range *all {
				if (*all)[i].Ref.URL == key {
					(*all)[i].Watch.State = mrwatch.Closed
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if _, err := s.Append(id, task.Event{Type: task.EventNote, Text: "closed " + key + " (coord drop --close-mr)"}); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Closed %s\n", key)
	}
	if len(failed) > 0 {
		return fmt.Errorf("not dropped: some MRs did not close, so the worktrees are untouched; retry coord drop --close-mr once fixed: %w", errors.Join(failed...))
	}
	return nil
}

type droppedWorktree struct {
	Project  string   `json:"project"`
	Path     string   `json:"path"`
	Branch   string   `json:"branch"`
	Status   string   `json:"status"`
	Unpushed []string `json:"unpushed,omitempty"`
	Error    string   `json:"error,omitempty"`
}

func dropWorktrees(s *task.Store, id task.ID) ([]supervise.KeptWorktree, error) {
	rec, err := s.Worker(id)
	if err != nil {
		return nil, err
	}
	active := rec.ActiveWorktrees()
	if len(active) == 0 {
		return nil, nil
	}
	work := make([]droppedWorktree, 0, len(active))
	var summary []string
	for _, wt := range active {
		d := droppedWorktree{Project: wt.Project, Path: wt.Path, Branch: wt.Branch}
		status, unpushed, err := supervise.UnsavedWork(wt.Path)
		d.Status, d.Unpushed = status, unpushed
		if err != nil {
			d.Error = err.Error()
		}
		work = append(work, d)
		summary = append(summary, wt.Project+": "+unsavedSummary(d))
	}
	data, _ := json.Marshal(map[string]any{"worktrees": work})
	if _, err := s.Append(id, task.Event{Type: task.EventDroppedWork, Text: strings.Join(summary, "; "), Data: data}); err != nil {
		return nil, err
	}
	var kept []supervise.KeptWorktree
	for _, wt := range active {
		if err := supervise.ReturnWorktree(s, id, treehouse.Client{Force: true}, wt, "dropped; its unsaved work is in the dropped-work event"); err != nil {
			kept = append(kept, supervise.KeptWorktree{Worktree: wt, Why: err.Error()})
			if _, err := s.Append(id, task.Event{Type: task.EventNote, Text: "worktree " + wt.Path + " kept: " + err.Error()}); err != nil {
				return kept, err
			}
		}
	}
	return kept, nil
}

func unsavedSummary(d droppedWorktree) string {
	if d.Error != "" {
		return d.Error
	}
	var parts []string
	if d.Status != "" {
		parts = append(parts, plural(len(strings.Split(d.Status, "\n")), "uncommitted change"))
	}
	if len(d.Unpushed) > 0 {
		parts = append(parts, plural(len(d.Unpushed), "commit")+" on no remote")
	}
	if len(parts) == 0 {
		return "nothing uncommitted or unpushed"
	}
	return strings.Join(parts, ", ")
}
