package main

import (
	"context"
	"fmt"
	"slices"
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
		Short: "Merge a ship Task's open MRs in the order its Worker reported them (on the Captain's word)",
		Long: "Merge a ship Task's open MRs with gh or glab, only on the Captain's word. Allowed for a ship Task that is\n" +
			"waiting-review, or needs-decision holding the watcher's facts, once its Worker has exited.\n" +
			"\n" +
			"Without --mr, coord merges the open MRs the Worker reported, in the order of its latest report. MRs linked by\n" +
			"coord task add-mr are left open and listed; if the Worker has no open MR but linked ones are open, coord refuses\n" +
			"and names them. --mr merges exactly the given MRs, in the order given, and may name any MR linked to the Task.\n" +
			"MRs already merged or closed are skipped.\n" +
			"\n" +
			"coord stops at the first MR that fails to merge, reports what merged before it and the error, and leaves the\n" +
			"rest untouched. After each merge coord reads the MR's real state. If an MR is still open (a merge queue or\n" +
			"similar) or its state cannot be read, coord stops there with an error, leaves the rest untouched, and you run\n" +
			"coord merge <task> again once it has merged. The Task then settles as the watcher would (all MRs merged:\n" +
			"merged; otherwise waiting-review); the watcher's facts about the merged MRs are cleared, facts about other MRs\n" +
			"stay and keep the Task in needs-decision.\n" +
			"\n" +
			"The method is --method when given. On GitHub, without --method, coord reads the repository's allowed methods\n" +
			"and uses the only one it allows; if it allows several or none can be read, nothing is merged until you pass\n" +
			"--method as the SOP or the Captain says. On GitLab, without --method, the project's own merge method decides.\n" +
			"\n" +
			"Refused: a Task that is not a ship Task or whose state has no merge move; a Task whose Worker is still\n" +
			"running; a Task with no open MRs to merge; an --mr that is not linked to the Task (link it with coord task\n" +
			"add-mr). coord never asks for branch deletion, an admin override or auto-merge; the host's own settings may\n" +
			"still delete a branch or queue the merge.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := a.tasks()
			if _, err := s.ReapLostSupervisors(); err != nil {
				return err
			}
			t, err := s.Find(args[0])
			if err != nil {
				return err
			}
			want, err := mrwatch.ParseMethod(method)
			if err != nil {
				return err
			}
			todo, left, err := mergeTargets(s, t, urls)
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
			var merged []mrwatch.Ref
			var failure error
			for i, m := range todo {
				if failure = mergeOne(cmd, s, t.ID, m.Ref, methods[i]); failure != nil {
					break
				}
				merged = append(merged, m.Ref)
			}
			if len(merged) > 0 {
				if _, err := s.ResolveAsks(t.ID, merged, task.MergeNote); err != nil {
					return err
				}
			}
			watchAfter(a, cmd)
			out := cmd.OutOrStdout()
			for _, m := range left {
				fmt.Fprintf(out, "left open: %s (linked; pass --mr to merge it)\n", m.Ref.URL)
			}
			if failure != nil {
				return mergeFailure(t.ID, merged, todo[len(merged):], failure)
			}
			t, err = s.Get(t.ID)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Task %s is %s.\n", t.ID, t.State)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&urls, "mr", nil, "merge only this MR/PR (repeatable; merged in the order given; may be a linked MR)")
	cmd.Flags().StringVar(&method, "method", "", "merge method: merge, squash or rebase (default: the only method a GitHub repo allows, or the GitLab project's own)")
	return cmd
}

func mergeTargets(s *task.Store, t task.Task, urls []string) ([]task.MR, []task.MR, error) {
	if t.Class != config.Ship {
		return nil, nil, fmt.Errorf("Task %s is a %s Task; only ship Tasks have MRs to merge", t.ID, t.Class)
	}
	if t.State.Terminal() {
		return nil, nil, fmt.Errorf("Task %s is already %s", t.ID, t.State)
	}
	if !slices.ContainsFunc(t.Moves(), func(m task.Move) bool { return m.Verb() == "merge" }) {
		return nil, nil, fmt.Errorf("coord merge is for a ship Task waiting on its MRs or holding the watcher's facts. %s", t.NextHint())
	}
	w, err := s.Worker(t.ID)
	if err != nil {
		return nil, nil, err
	}
	if w.SupervisorLive() {
		return nil, nil, fmt.Errorf("Task %s's Worker is still running; wait for it to exit, then merge", t.ID)
	}
	mrs, err := s.MRs(t.ID)
	if err != nil {
		return nil, nil, err
	}
	var picked, left []task.MR
	if len(urls) == 0 {
		for _, m := range mrs {
			switch {
			case !m.Open():
			case m.Source == task.MRFromWorker:
				picked = append(picked, m)
			default:
				left = append(left, m)
			}
		}
		if len(picked) == 0 && len(left) > 0 {
			var names []string
			for _, m := range left {
				names = append(names, m.Ref.URL)
			}
			return nil, nil, fmt.Errorf("Task %s has no open MR from its Worker; its linked MRs are open: %s; pass --mr to merge them", t.ID, strings.Join(names, ", "))
		}
	}
	for _, u := range urls {
		ref, err := mrwatch.ParseURL(u)
		if err != nil {
			return nil, nil, err
		}
		found := false
		for _, m := range mrs {
			if strings.EqualFold(m.Ref.RepoKey(), ref.RepoKey()) && m.Ref.Number == ref.Number {
				found = true
				if m.Open() && !containsMR(picked, m) {
					picked = append(picked, m)
				}
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("%s is not linked to Task %s; link it first with coord task add-mr %s %s", u, t.ID, t.ID, u)
		}
	}
	if len(picked) == 0 {
		return nil, nil, fmt.Errorf("Task %s has no open MRs to merge", t.ID)
	}
	return picked, left, nil
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
	if err != nil || state != mrwatch.Merged {
		reason := fmt.Sprintf("it is still %s", state)
		if err != nil {
			reason = fmt.Sprintf("its state could not be read: %v", err)
		}
		if _, err := s.Append(id, task.Event{Type: task.EventMerged, Text: fmt.Sprintf("merge of %s requested (coord merge, %s); not confirmed, %s", ref.URL, method.Label(), reason)}); err != nil {
			return err
		}
		fmt.Fprintf(out, "Merge of %s was requested and is pending: %s.\n", ref.URL, reason)
		return fmt.Errorf("the merge was requested but is not confirmed (%s); run coord merge %s again once it has merged", reason, id)
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
	if _, err := s.Append(id, task.Event{Type: task.EventMerged, Text: fmt.Sprintf("merged %s (coord merge, %s)", ref.URL, method.Label())}); err != nil {
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

func mergeFailure(id task.ID, merged []mrwatch.Ref, rest []task.MR, cause error) error {
	before := "nothing was merged before it"
	if len(merged) > 0 {
		var done []string
		for _, r := range merged {
			done = append(done, r.URL)
		}
		before = "done before it: " + strings.Join(done, ", ")
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
