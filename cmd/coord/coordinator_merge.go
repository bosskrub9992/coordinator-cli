package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/mrwatch"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
	"github.com/bosskrub9992/coordinator-cli/internal/watcher"
	"github.com/spf13/cobra"
)

var (
	mrMerger  = mrwatch.Merger{}
	mrClients = func(k mrwatch.Kind) (mrwatch.Client, error) { return mrwatch.NewClients().For(k) }
)

func newMergeCmd(a *app) *cobra.Command {
	var urls []string
	var method string
	cmd := &cobra.Command{
		Use:   "merge <task> [--mr <url>]... [--method merge|squash|rebase]",
		Short: "Merge a ship Task's open MRs in the order they were linked (on the Captain's word)",
		Long: "Merge a ship Task's open MRs with gh or glab, only on the Captain's word. The MRs go in the order they were\n" +
			"linked (the order of coord report --mr and coord task add-mr); --mr limits the merge to the given MRs, in the\n" +
			"order given. MRs already merged or closed are skipped. coord stops at the first MR that fails to merge, reports\n" +
			"what merged before it and the error, and leaves the rest untouched.\n" +
			"\n" +
			"The method is --method when given. On GitHub, without --method, coord reads the repository's allowed methods\n" +
			"and uses the only one it allows; if it allows several, nothing is merged until you pass --method as the SOP or\n" +
			"the Captain says. On GitLab, without --method, the project's own merge method decides.\n" +
			"\n" +
			"Refused: a Task that is not a ship Task, is queued or is terminal; a Task whose Worker is still running (stop\n" +
			"it or wait for it to exit); a Task with no open MRs; an --mr that is not linked to the Task (link it with\n" +
			"coord task add-mr). Branches are never deleted and nothing is force-merged: no admin override, no auto-merge.\n" +
			"After each merge coord reads the MR's real state; the Task then settles as the watcher would (all MRs merged:\n" +
			"merged; otherwise waiting-review), and the watcher's question, if the Task held one, is cleared as coord ack does.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := a.tasks()
			t, err := s.Find(args[0])
			if err != nil {
				return err
			}
			want, err := mrwatch.ParseMethod(method)
			if err != nil {
				return err
			}
			todo, err := mergeTargets(s, t, urls)
			if err != nil {
				return err
			}
			refs := make([]mrwatch.Ref, len(todo))
			for i, m := range todo {
				refs[i] = m.Ref
			}
			methods, err := mrMerger.Resolve(cmd.Context(), refs, want)
			if err != nil {
				return fmt.Errorf("Task %s: %w", t.ID, err)
			}
			var merged []string
			var failure error
			for i, m := range todo {
				if failure = mergeOne(cmd, s, t.ID, m.Ref, methods[i]); failure != nil {
					break
				}
				merged = append(merged, m.Ref.URL)
			}
			if len(merged) > 0 {
				if err := settleAfterMerge(s, t.ID); err != nil {
					return err
				}
			}
			watchAfter(a, cmd)
			if failure != nil {
				return mergeFailure(t.ID, merged, todo[len(merged):], failure)
			}
			t, err = s.Get(t.ID)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Task %s is %s.\n", t.ID, t.State)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&urls, "mr", nil, "merge only this MR/PR (repeatable; merged in the order given)")
	cmd.Flags().StringVar(&method, "method", "", "merge method: merge, squash or rebase (default: the only method a GitHub repo allows, or the GitLab project's own)")
	return cmd
}

func mergeTargets(s *task.Store, t task.Task, urls []string) ([]task.MR, error) {
	if t.Class != config.Ship {
		return nil, fmt.Errorf("Task %s is a %s Task; only ship Tasks have MRs to merge", t.ID, t.Class)
	}
	if t.State.Terminal() {
		return nil, fmt.Errorf("Task %s is already %s", t.ID, t.State)
	}
	if t.State == task.Queued {
		return nil, fmt.Errorf("Task %s is queued; no Worker has started on it, so it has no MRs", t.ID)
	}
	w, err := s.Worker(t.ID)
	if err != nil {
		return nil, err
	}
	if w.SupervisorLive() {
		return nil, fmt.Errorf("Task %s's Worker is still running; stop it (coord stop %s) or wait for it to exit, then merge", t.ID, t.ID)
	}
	mrs, err := s.MRs(t.ID)
	if err != nil {
		return nil, err
	}
	var picked []task.MR
	if len(urls) == 0 {
		picked = mrs
	}
	for _, u := range urls {
		ref, err := mrwatch.ParseURL(u)
		if err != nil {
			return nil, err
		}
		found := false
		for _, m := range mrs {
			if strings.EqualFold(m.Ref.RepoKey(), ref.RepoKey()) && m.Ref.Number == ref.Number {
				found = true
				if !containsMR(picked, m) {
					picked = append(picked, m)
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("%s is not linked to Task %s; link it first with coord task add-mr %s %s", u, t.ID, t.ID, u)
		}
	}
	var open []task.MR
	for _, m := range picked {
		if m.Open() {
			open = append(open, m)
		}
	}
	if len(open) == 0 {
		return nil, fmt.Errorf("Task %s has no open MRs to merge", t.ID)
	}
	return open, nil
}

func containsMR(mrs []task.MR, m task.MR) bool {
	for _, x := range mrs {
		if x.Ref.URL == m.Ref.URL {
			return true
		}
	}
	return false
}

func mergeOne(cmd *cobra.Command, s *task.Store, id task.ID, ref mrwatch.Ref, method mrwatch.Method) error {
	if err := mrMerger.Merge(cmd.Context(), ref, method); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	state, err := fetchMRState(cmd.Context(), ref)
	if err != nil {
		if _, err := s.Append(id, task.Event{Type: task.EventNote, Text: fmt.Sprintf("merge of %s requested (coord merge, %s); its state could not be read, the watcher will report it", ref.URL, method.Label())}); err != nil {
			return err
		}
		fmt.Fprintf(out, "Merge of %s was requested but its state could not be read (%v); the watcher will report it.\n", ref.URL, err)
		return nil
	}
	if state != mrwatch.Merged {
		if _, err := s.Append(id, task.Event{Type: task.EventNote, Text: fmt.Sprintf("merge of %s requested (coord merge, %s); it is still %s", ref.URL, method.Label(), state)}); err != nil {
			return err
		}
		fmt.Fprintf(out, "Merge of %s was requested; it is still %s (a merge queue or similar), and the watcher will report when it merges.\n", ref.URL, state)
		return nil
	}
	if _, err := s.UpdateMRs(id, func(all *[]task.MR) error {
		for i := range *all {
			if (*all)[i].Ref.URL == ref.URL {
				(*all)[i].Watch.State = mrwatch.Merged
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if _, err := s.Append(id, task.Event{Type: task.EventNote, Text: fmt.Sprintf("merged %s (coord merge, %s)", ref.URL, method.Label())}); err != nil {
		return err
	}
	fmt.Fprintf(out, "Merged %s\n", ref.URL)
	return nil
}

func fetchMRState(ctx context.Context, ref mrwatch.Ref) (mrwatch.State, error) {
	c, err := mrClients(ref.Kind)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, watcher.FetchTimeout)
	defer cancel()
	snap, err := c.Fetch(ctx, ref, mrwatch.NoteCursor{})
	if err != nil {
		return "", err
	}
	return snap.State, nil
}

func settleAfterMerge(s *task.Store, id task.ID) error {
	t, err := s.Get(id)
	if err != nil {
		return err
	}
	if t.State == task.NeedsDecision && t.QuestionFrom == task.QuestionFromWatcher {
		_, err = s.Ack(id, "MR facts handled by the Captain's merge word (coord merge)")
		return err
	}
	_, err = s.Settle(id, "")
	return err
}

func mergeFailure(id task.ID, merged []string, rest []task.MR, cause error) error {
	before := "nothing was merged before it"
	if len(merged) > 0 {
		before = "done before it: " + strings.Join(merged, ", ")
	}
	msg := fmt.Sprintf("Task %s: stopped at %s (%s)", id, rest[0].Ref.URL, before)
	if len(rest) > 1 {
		var left []string
		for _, m := range rest[1:] {
			left = append(left, m.Ref.URL)
		}
		msg += "; left untouched: " + strings.Join(left, ", ")
	}
	return fmt.Errorf("%s: %w", msg, cause)
}
