package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/harness"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
	"github.com/spf13/cobra"
)

func newTaskCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Create Tasks, and add Projects or MRs to them",
	}
	cmd.AddCommand(newTaskNewCmd(a), newTaskAddProjectCmd(a), newTaskAddMRCmd(a))
	return cmd
}

func newTaskNewCmd(a *app) *cobra.Command {
	var (
		projects                         []string
		class, title, brief              string
		ticket, ticketFolder             string
		model, effort, harnessName, plan string
		skipPlan                         bool
	)
	cmd := &cobra.Command{
		Use:   "new --project <name> [--project <name>...] --class <ship|scout|review-code> --title <title> --brief -",
		Short: "Create a queued Task from a Brief and print its Task id",
		Long: "Create a queued Task. The Brief is read from stdin (--brief -) or from a file (--brief <path>).\n" +
			"Repeat --project for a Task across several Projects; the first is the primary one, whose config picks the model,\n" +
			"effort and plan approval. coord spawn leases one worktree per Project.\n" +
			"The Task folder is the ticket folder inside the Launch folder when --ticket matches one (or --ticket-folder names one),\n" +
			"otherwise <Home>/tasks/<id>/work. --model must be in coordinator_may_choose and not in forbidden_models.",
		Example: "  coord task new --project api --class ship --title \"Fix login timeout\" --ticket PROJ-1234 --brief - <<'EOF'\n  ...Brief...\n  EOF",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := config.ParseClass(class)
			if err != nil {
				return err
			}
			for i, name := range projects {
				if slices.Contains(projects[:i], name) {
					return fmt.Errorf("--project %s is given twice", name)
				}
				if _, err := a.projects().Get(name); err != nil {
					return fmt.Errorf("%w; register it with: coord project add <path>", err)
				}
			}
			text, err := readBrief(cmd.InOrStdin(), brief)
			if err != nil {
				return err
			}
			o := config.TaskOverrides{Harness: harness.Name(harnessName), Model: model, Effort: effort, PlanApproval: config.PlanApproval(plan)}
			if !o.Empty() {
				o.ChosenBy = config.ChosenByCoordinator
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			if err := cfg.CheckTaskOverrides(o); err != nil {
				return err
			}
			if skipPlan {
				if cl != config.Ship {
					return fmt.Errorf("--skip-plan is for ship Tasks; a %s Task has no Plan step", cl)
				}
				sel, err := cfg.Resolve(cl, projects[0], o)
				if err != nil {
					return err
				}
				if sel.PlanApproval == config.PlanAll {
					return fmt.Errorf("--skip-plan is refused: plan_approval is all (from the %s setting), so the Captain approves every Plan", sel.Sources.PlanApproval)
				}
			}
			launch, err := launchFolder(a.home)
			if err != nil {
				return err
			}
			t, err := a.tasks().Create(task.NewTask{
				Title: title, Class: cl, Projects: projects, Brief: text,
				Ticket: ticket, TicketFolder: ticketFolder, LaunchFolder: launch, Overrides: o, SkipPlan: skipPlan,
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), t.ID)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&projects, "project", nil, "registered Project name (required; repeat for several, the first is the primary)")
	f.StringVar(&class, "class", "", "Task class: ship, scout or review-code (required)")
	f.StringVar(&title, "title", "", "short Task title; becomes the id slug (required)")
	f.StringVar(&brief, "brief", "", "Brief source: - for stdin, or a file path (required)")
	f.StringVar(&ticket, "ticket", "", "ticket key such as PROJ-1234")
	f.StringVar(&ticketFolder, "ticket-folder", "", "ticket folder name inside the Launch folder")
	f.StringVar(&model, "model", "", "model override")
	f.StringVar(&effort, "effort", "", "effort override: low, medium, high, xhigh, max")
	f.StringVar(&harnessName, "harness", "", "harness override")
	f.StringVar(&plan, "plan-approval", "", "plan approval override: product-decisions, fyi, all")
	f.BoolVar(&skipPlan, "skip-plan", false, "ship Tasks: the Plan is pre-approved, for a trivial change (refused when plan_approval is all)")
	for _, n := range []string{"project", "class", "title", "brief"} {
		cmd.MarkFlagRequired(n)
	}
	return cmd
}

func readBrief(stdin io.Reader, src string) (string, error) {
	var data []byte
	var err error
	if src == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(src)
	}
	if err != nil {
		return "", fmt.Errorf("read Brief: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", errors.New("the Brief is empty")
	}
	return string(data), nil
}

func launchFolder(h home.Home) (string, error) {
	if tok := os.Getenv(home.EnvToken); tok != "" {
		l, err := h.ReadLock()
		if err != nil {
			return "", err
		}
		if l != nil && l.Token == tok && l.LaunchFolder != "" {
			return l.LaunchFolder, nil
		}
	}
	return cwd()
}

func newTaskAddProjectCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "add-project <task> <project>",
		Short: "Add a Project to a Task: a queued Task gets its worktree at coord spawn, any other at once",
		Long: "Add a registered Project to a Task, such as a deploy's feature-flag or deploy-config edit. Refused for Landed or Dropped\n" +
			"Tasks and while the Task's Worker is live. A Task that has had a Worker gets the Project's worktree leased now\n" +
			"(branch coord/<task>, holder <task>); the Worker sees it when coord steer resumes it.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := a.tasks()
			t, err := s.Find(args[0])
			if err != nil {
				return err
			}
			p, err := a.projects().Get(args[1])
			if err != nil {
				return fmt.Errorf("%w; register it with: coord project add <path>", err)
			}
			if t, err = s.AddProject(t.ID, p.Name); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if t.State == task.Queued {
				fmt.Fprintf(out, "Added Project %s to %s; coord spawn leases its worktree.\n", p.Name, t.ID)
				return nil
			}
			wt, err := leaseWorktree(s, t.ID, p)
			if err != nil {
				return fmt.Errorf("Project %s is added to %s, but its worktree is not leased (run this again to retry): %w", p.Name, t.ID, err)
			}
			fmt.Fprintf(out, "Added Project %s to %s: worktree %s (branch %s). The Worker gets it when coord steer resumes it.\n", p.Name, t.ID, wt.Path, wt.Branch)
			return nil
		},
	}
}

func newTaskAddMRCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "add-mr <task> <url>...",
		Short: "Link MRs/PRs a ship Task did not open itself (such as the deploy-config MR a release tool opens) so the watcher tracks them",
		Long: "Link GitLab merge requests or GitHub pull requests to a ship Task. Linking an MR the Task already has keeps\n" +
			"what the watcher knows about it. A merged Task that gains an open MR goes back to waiting-review.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := a.tasks()
			t, err := s.Find(args[0])
			if err != nil {
				return err
			}
			if t.Class != config.Ship {
				return fmt.Errorf("Task %s is %s; MRs belong to ship Tasks", t.ID, t.Class)
			}
			if t.State.Terminal() {
				return fmt.Errorf("Task %s is %s; MRs cannot be linked to it", t.ID, t.State)
			}
			refs, err := parseMRs(args[1:])
			if err != nil {
				return err
			}
			mrs, err := s.AddMR(t.ID, task.MRLinked, refs...)
			if err != nil {
				return err
			}
			urls := make([]string, len(refs))
			for i, r := range refs {
				urls[i] = r.URL
			}
			data, _ := json.Marshal(map[string]any{"mr_urls": urls})
			if _, err := s.Append(t.ID, task.Event{Type: task.EventMRLinked, Text: "linked " + strings.Join(urls, ", "), Data: data}); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if t, err = s.Settle(t.ID, "linked an MR"); err != nil {
				return err
			}
			fmt.Fprintf(out, "Task %s (%s) has %s:\n", t.ID, t.State, plural(len(mrs), "MR"))
			for _, m := range mrs {
				fmt.Fprintf(out, "  %s\n", mrLine(m))
			}
			watchAfter(a, cmd)
			return nil
		},
	}
}
