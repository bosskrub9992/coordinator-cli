package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/bosskrub9992/coordinator-cli/internal/claude/worker"
	"github.com/bosskrub9992/coordinator-cli/internal/harness"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/project"
	"github.com/bosskrub9992/coordinator-cli/internal/supervise"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
	"github.com/bosskrub9992/coordinator-cli/internal/treehouse"
	"github.com/spf13/cobra"
)

func ticketFolderName(t task.Task) string {
	slug := task.Slug(t.Title)
	prefix := strings.ToLower(t.Ticket) + "-"
	slug = strings.TrimPrefix(slug, prefix)
	if slug == strings.ToLower(t.Ticket) || slug == "" {
		return t.Ticket
	}
	return t.Ticket + "-" + slug
}

func ensureTaskFolder(s *task.Store, t task.Task) (task.Task, error) {
	folder := t.Folder
	if t.Ticket != "" && folder == s.WorkDir(t.ID) {
		f, err := task.ResolveFolder(t.LaunchFolder, t.Ticket, "", s.WorkDir(t.ID))
		if err != nil {
			return t, err
		}
		if f == s.WorkDir(t.ID) {
			f = filepath.Join(t.LaunchFolder, ticketFolderName(t))
		}
		folder = f
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return t, fmt.Errorf("create Task folder: %w", err)
	}
	if folder == t.Folder {
		return t, nil
	}
	return s.Update(t.ID, func(x *task.Task) error {
		x.Folder = folder
		return nil
	})
}

func newSpawnCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "spawn <task>",
		Short: "Start a Worker for a queued Task in leased treehouse worktrees, one per Project",
		Long: "Lease a treehouse worktree of each of the Task's Projects (branch coord/<task>, holder <task>), write the Worker's\n" +
			"system prompt, settings and MCP config into the Task, and start a detached supervisor. Returns at once;\n" +
			"follow the Worker with coord watch <task> and coord wait.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := a.tasks()
			t, err := s.Find(args[0])
			if err != nil {
				return err
			}
			if t.State != task.Queued {
				return fmt.Errorf("Task %s is %s; only a queued Task can be spawned (use coord steer to resume a Worker)", t.ID, t.State)
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			sel, err := cfg.Resolve(t.Class, t.PrimaryProject(), t.Overrides)
			if err != nil {
				return err
			}
			if err := harness.Check(sel.Harness); err != nil {
				return err
			}
			bin, err := coordBinary()
			if err != nil {
				return err
			}
			release, err := s.ClaimSpawn(t.ID)
			if err != nil {
				return err
			}
			defer release()
			if t, err = s.Get(t.ID); err != nil {
				return err
			}
			ps, err := taskProjects(a, t)
			if err != nil {
				return err
			}
			for _, p := range ps {
				if _, err := leaseWorktree(s, t.ID, p); err != nil {
					return err
				}
			}
			if t, err = ensureTaskFolder(s, t); err != nil {
				return err
			}
			rec, err := s.UpdateWorker(t.ID, func(w *task.WorkerRecord) error {
				w.Harness = string(sel.Harness)
				w.Model = sel.Model
				w.Effort = sel.Effort
				w.PlanApproval = string(sel.PlanApproval)
				w.PlanFrom = string(sel.Sources.PlanApproval)
				if w.SessionID == "" {
					id, err := newUUID()
					if err != nil {
						return err
					}
					w.SessionID = id
				}
				return nil
			})
			if err != nil {
				return err
			}
			if err := supervise.WriteFiles(s, t, rec, bin); err != nil {
				return err
			}
			if _, err := s.TransitionFrom(t.ID, task.Queued, task.Running, "Worker spawning"); err != nil {
				return err
			}
			if err := startSupervisor(a, bin, t.ID); err != nil {
				if _, terr := s.Transition(t.ID, task.Failed, "the supervisor could not start: "+err.Error()); terr != nil {
					return errors.Join(err, fmt.Errorf("record the failure: %w", terr))
				}
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Spawned a Worker for %s (%s %s/%s)\n", t.ID, sel.Harness, sel.Model, sel.Effort)
			for _, wt := range rec.ActiveWorktrees() {
				fmt.Fprintf(out, "  worktree:    %s: %s (branch %s)\n", wt.Project, wt.Path, wt.Branch)
			}
			fmt.Fprintf(out, "  Task folder: %s\n", t.Folder)
			fmt.Fprintf(out, "Follow it with: coord watch %s -f\n", t.ID)
			return nil
		},
	}
}

func taskProjects(a *app, t task.Task) ([]project.Project, error) {
	ps := make([]project.Project, 0, len(t.Projects))
	for _, name := range t.Projects {
		p, err := a.projects().Get(name)
		if err != nil {
			return nil, fmt.Errorf("%w; register it with: coord project add <path>", err)
		}
		ps = append(ps, p)
	}
	return ps, nil
}

func leaseWorktree(s *task.Store, id task.ID, p project.Project) (task.Worktree, error) {
	branch := "coord/" + string(id)
	l, err := treehouse.Client{}.Acquire(p.Path, branch, string(id))
	if err != nil {
		return task.Worktree{}, fmt.Errorf("lease a worktree for Project %s: %w", p.Name, err)
	}
	if l.Branch != "" {
		branch = l.Branch
	}
	lease := task.Worktree{
		Project: p.Name, MainCheckout: p.Path, Path: l.Path, Branch: branch,
		LeaseID: l.LeaseID, LeasedAt: l.LeasedAt,
	}
	_, err = s.UpdateWorker(id, func(w *task.WorkerRecord) error {
		if err := keepLease(w.Worktrees, lease); err != nil {
			return err
		}
		if !slices.ContainsFunc(w.Worktrees, func(wt task.Worktree) bool { return wt.Project == p.Name }) {
			w.Worktrees = append(w.Worktrees, lease)
		}
		return nil
	})
	return lease, err
}

func keepLease(recorded []task.Worktree, got task.Worktree) error {
	for _, wt := range recorded {
		if wt.Project != got.Project {
			continue
		}
		if wt.Path != got.Path || (wt.LeaseID != "" && got.LeaseID != "" && wt.LeaseID != got.LeaseID) {
			return fmt.Errorf("the Task already records a worktree lease for Project %s (%s, lease %s); treehouse now offers %s (lease %s), so coordinator-cli will not overwrite it. Check `treehouse status` in %s", got.Project, wt.Path, wt.LeaseID, got.Path, got.LeaseID, got.MainCheckout)
		}
	}
	return nil
}

func startSupervisor(a *app, bin string, id task.ID) error {
	s := a.tasks()
	logf, err := os.OpenFile(s.SupervisorLogPath(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	c := exec.Command(bin, "_supervise", string(id))
	c.Dir = a.home.Root
	c.Env = supervise.SupervisorEnv(os.Environ())
	c.Stdout = logf
	c.Stderr = logf
	detach(c)
	if err := c.Start(); err != nil {
		return fmt.Errorf("start the supervisor: %w", err)
	}
	pid := c.Process.Pid
	if _, err := s.UpdateWorker(id, func(w *task.WorkerRecord) error {
		if !w.SupervisorLive() {
			w.SupervisorPID = pid
			w.SupervisorStart = home.ProcessStart(pid)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("record the supervisor: %w", err)
	}
	return c.Process.Release()
}

func newSuperviseCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:    "_supervise <task>",
		Short:  "Run and supervise a Task's Worker (started by coord spawn)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := a.tasks()
			t, err := s.Find(args[0])
			if err != nil {
				return err
			}
			bin, err := coordBinary()
			if err != nil {
				return err
			}
			logger := log.New(cmd.ErrOrStderr(), string(t.ID)+" ", log.LstdFlags|log.Lmicroseconds)
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			sv := &supervise.Supervisor{
				Store:     s,
				Runner:    worker.Runner{},
				Treehouse: treehouse.Client{},
				Task:      t.ID,
				HomeRoot:  a.home.Root,
				CoordBin:  bin,
				Env:       os.Environ(),
				Logf:      logger.Printf,
			}
			err = sv.Run(ctx)
			if err != nil && !errors.Is(err, supervise.ErrSupervised) {
				logger.Printf("supervisor: %v", err)
			}
			return err
		},
	}
}

func readText(stdin io.Reader, src string) (string, error) {
	var data []byte
	var err error
	if src == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(src)
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", errors.New("the text is empty")
	}
	return string(data), nil
}
