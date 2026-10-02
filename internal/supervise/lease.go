package supervise

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
	"github.com/bosskrub9992/coordinator-cli/internal/treehouse"
)

func (s *Supervisor) treehouse() Returner {
	if s.Treehouse != nil {
		return s.Treehouse
	}
	return treehouse.Client{}
}

func git(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

func WorktreeSettled(path string) (bool, string) {
	status, err := git(path, "status", "--porcelain")
	if err != nil {
		return false, "git status failed: " + err.Error()
	}
	if status != "" {
		return false, "it has uncommitted changes"
	}
	n, err := git(path, "rev-list", "--count", "HEAD", "--not", "--remotes")
	if err != nil {
		return false, "git rev-list failed: " + err.Error()
	}
	if c, _ := strconv.Atoi(n); c > 0 {
		return false, fmt.Sprintf("it has %d commits that are on no remote", c)
	}
	return true, ""
}

func UnsavedWork(path string) (status string, unpushed []string, err error) {
	out, err := exec.Command("git", "-C", path, "status", "--porcelain").Output()
	if err != nil {
		return "", nil, fmt.Errorf("git status failed: %w", err)
	}
	status = strings.TrimRight(string(out), "\n")
	log, err := git(path, "log", "--format=%h %s", "HEAD", "--not", "--remotes")
	if err != nil {
		return status, nil, fmt.Errorf("git log failed: %w", err)
	}
	if log != "" {
		unpushed = strings.Split(log, "\n")
	}
	return status, unpushed, nil
}

func (s *Supervisor) returnLease() {
	t, err := s.Store.Get(s.Task)
	if err != nil || t.State != task.Reported || t.Class == config.Ship {
		return
	}
	if _, err := ReturnSettled(s.Store, s.Task, s.treehouse()); err != nil {
		s.logf("return worktrees: %v", err)
	}
}

type KeptWorktree struct {
	Worktree task.Worktree
	Why      string
}

func ReturnSettled(s *task.Store, id task.ID, th Returner) ([]KeptWorktree, error) {
	rec, err := s.Worker(id)
	if err != nil {
		return nil, err
	}
	var kept []KeptWorktree
	for _, wt := range rec.ActiveWorktrees() {
		var why string
		if ok, unsettled := WorktreeSettled(wt.Path); !ok {
			why = unsettled
		} else if err := th.Return(wt.Path, wt.LeaseID); err != nil {
			why = err.Error()
		} else {
			if err := recordReturned(s, id, wt, "clean, nothing unpushed"); err != nil {
				return kept, err
			}
			continue
		}
		kept = append(kept, KeptWorktree{Worktree: wt, Why: why})
		if _, err := s.Append(id, task.Event{Type: task.EventNote, Text: "worktree " + wt.Path + " kept: " + why}); err != nil {
			return kept, err
		}
	}
	return kept, nil
}

func ReturnWorktree(s *task.Store, id task.ID, th Returner, wt task.Worktree, why string) error {
	if err := th.Return(wt.Path, wt.LeaseID); err != nil {
		return err
	}
	return recordReturned(s, id, wt, why)
}

func recordReturned(s *task.Store, id task.ID, wt task.Worktree, why string) error {
	now := time.Now().UTC()
	if _, err := s.UpdateWorker(id, func(r *task.WorkerRecord) error {
		for i := range r.Worktrees {
			if r.Worktrees[i].Path == wt.Path && !r.Worktrees[i].Returned() {
				r.Worktrees[i].ReturnedAt = now
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("returned %s to treehouse, but recording it failed: %w", wt.Path, err)
	}
	data, _ := json.Marshal(map[string]any{"path": wt.Path, "lease_id": wt.LeaseID, "project": wt.Project})
	_, err := s.Append(id, task.Event{Type: task.EventWorktreeReturned, Text: "returned " + wt.Path + " to the treehouse pool (" + why + ")", Data: data})
	return err
}
