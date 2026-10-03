package task

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
)

type Worktree struct {
	Project      string    `json:"project"`
	MainCheckout string    `json:"main_checkout"`
	Path         string    `json:"path"`
	Branch       string    `json:"branch"`
	LeaseID      string    `json:"lease_id,omitempty"`
	LeasedAt     time.Time `json:"leased_at,omitzero"`
	ReturnedAt   time.Time `json:"returned_at,omitzero"`
}

func (w Worktree) Returned() bool { return !w.ReturnedAt.IsZero() }

type SpawnClaim struct {
	Token string    `json:"token"`
	PID   int       `json:"pid"`
	Start time.Time `json:"process_start,omitzero"`
	At    time.Time `json:"at"`
}

func (c SpawnClaim) live() bool { return home.ProcessLive(c.PID, c.Start) }

type WorkerRecord struct {
	Worktrees       []Worktree  `json:"worktrees"`
	Harness         string      `json:"harness,omitempty"`
	Model           string      `json:"model,omitempty"`
	Effort          string      `json:"effort,omitempty"`
	PlanApproval    string      `json:"plan_approval,omitempty"`
	PlanFrom        string      `json:"plan_approval_from,omitempty"`
	SessionID       string      `json:"session_id,omitempty"`
	SessionStarted  bool        `json:"session_started,omitempty"`
	SupervisorPID   int         `json:"supervisor_pid,omitempty"`
	SupervisorStart time.Time   `json:"supervisor_start,omitzero"`
	WorkerPID       int         `json:"worker_pid,omitempty"`
	StartedAt       time.Time   `json:"started_at,omitzero"`
	ExitedAt        time.Time   `json:"exited_at,omitzero"`
	ExitCode        *int        `json:"exit_code,omitempty"`
	CostUSD         float64     `json:"total_cost_usd,omitempty"`
	Spawning        *SpawnClaim `json:"spawning,omitempty"`
}

func (w WorkerRecord) WorktreePaths() []string {
	out := make([]string, 0, len(w.Worktrees))
	for _, wt := range w.ActiveWorktrees() {
		out = append(out, wt.Path)
	}
	return out
}

func (w WorkerRecord) ActiveWorktrees() []Worktree {
	var out []Worktree
	for _, wt := range w.Worktrees {
		if !wt.Returned() {
			out = append(out, wt)
		}
	}
	return out
}

func (w WorkerRecord) SupervisorLive() bool {
	return home.ProcessLive(w.SupervisorPID, w.SupervisorStart)
}

func (s *Store) WorkerPath(id ID) string       { return filepath.Join(s.Dir(id), "worker.json") }
func (s *Store) WorkerLogPath(id ID) string    { return filepath.Join(s.Dir(id), "worker.log") }
func (s *Store) InboxDir(id ID) string         { return filepath.Join(s.Dir(id), "inbox") }
func (s *Store) QuestionPath(id ID) string     { return filepath.Join(s.Dir(id), "question.md") }
func (s *Store) SystemPromptPath(id ID) string { return filepath.Join(s.Dir(id), "system-prompt.md") }
func (s *Store) SettingsPath(id ID) string     { return filepath.Join(s.Dir(id), "settings.json") }
func (s *Store) EnvFilePath(id ID) string      { return filepath.Join(s.Dir(id), "worker-env.sh") }
func (s *Store) InstructionsPath(id ID) string { return filepath.Join(s.Dir(id), "instructions.jsonl") }
func (s *Store) SupervisorLogPath(id ID) string {
	return filepath.Join(s.Dir(id), "supervisor.log")
}

func (s *Store) Worker(id ID) (WorkerRecord, error) {
	var w WorkerRecord
	if _, err := s.Get(id); err != nil {
		return w, err
	}
	_, err := home.ReadJSON(s.WorkerPath(id), &w)
	return w, err
}

func (s *Store) UpdateWorker(id ID, fn func(*WorkerRecord) error) (WorkerRecord, error) {
	var out WorkerRecord
	err := home.WithFileLock(s.WorkerPath(id)+".lck", func() error {
		w, err := s.Worker(id)
		if err != nil {
			return err
		}
		if err := fn(&w); err != nil {
			return err
		}
		out = w
		return home.WriteJSONAtomic(s.WorkerPath(id), w)
	})
	return out, err
}

var ErrSpawning = errors.New("another coord spawn is already starting this Task")

func (s *Store) ClaimSpawn(id ID) (release func(), err error) {
	b := make([]byte, 8)
	rand.Read(b)
	token := hex.EncodeToString(b)
	pid := os.Getpid()
	err = home.WithFileLock(s.lockPath(id), func() error {
		_, err := s.UpdateWorker(id, func(w *WorkerRecord) error {
			t, err := s.Get(id)
			if err != nil {
				return err
			}
			if t.State != Queued {
				return fmt.Errorf("Task %s is %s; only a queued Task can be spawned (use coord steer to resume a Worker)", id, t.State)
			}
			if c := w.Spawning; c != nil && c.live() {
				return fmt.Errorf("%w (pid %d, since %s)", ErrSpawning, c.PID, c.At.Local().Format(time.DateTime))
			}
			w.Spawning = &SpawnClaim{Token: token, PID: pid, Start: home.ProcessStart(pid), At: s.now()}
			return nil
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return func() {
		s.UpdateWorker(id, func(w *WorkerRecord) error {
			if w.Spawning != nil && w.Spawning.Token == token {
				w.Spawning = nil
			}
			return nil
		})
	}, nil
}
