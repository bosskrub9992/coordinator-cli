package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
)

type Task struct {
	ID           ID                   `json:"id"`
	Title        string               `json:"title"`
	Class        config.Class         `json:"class"`
	Projects     []string             `json:"projects"`
	Ticket       string               `json:"ticket,omitempty"`
	State        State                `json:"state"`
	QuestionFrom QuestionSource       `json:"question_from,omitempty"`
	LaunchFolder string               `json:"launch_folder"`
	Folder       string               `json:"task_folder"`
	Overrides    config.TaskOverrides `json:"overrides,omitzero"`
	SkipPlan     bool                 `json:"skip_plan,omitempty"`
	CreatedAt    time.Time            `json:"created_at"`
	UpdatedAt    time.Time            `json:"updated_at"`
	StateSince   time.Time            `json:"state_since"`
}

type NewTask struct {
	Title        string
	Class        config.Class
	Projects     []string
	Brief        string
	Ticket       string
	TicketFolder string
	LaunchFolder string
	Overrides    config.TaskOverrides
	SkipPlan     bool
}

var ErrNotFound = errors.New("no such Task")

type Store struct {
	home    home.Home
	now     func() time.Time
	onEvent func(Event)
}

func NewStore(h home.Home) *Store {
	return &Store{home: h, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Store) OnEvent(fn func(Event)) *Store {
	s.onEvent = fn
	return s
}

func (s *Store) Dir(id ID) string        { return filepath.Join(s.home.TasksDir(), string(id)) }
func (s *Store) BriefPath(id ID) string  { return filepath.Join(s.Dir(id), "brief.md") }
func (s *Store) ReportPath(id ID) string { return filepath.Join(s.Dir(id), "report.md") }
func (s *Store) StatePath(id ID) string  { return filepath.Join(s.Dir(id), "state.json") }
func (s *Store) EventsPath(id ID) string { return filepath.Join(s.Dir(id), "events.jsonl") }
func (s *Store) WorkDir(id ID) string    { return filepath.Join(s.Dir(id), "work") }
func (s *Store) lockPath(id ID) string   { return filepath.Join(s.Dir(id), "state.json.lck") }
func (s *Store) log(id ID) home.Log      { return home.Log{Path: s.EventsPath(id)} }

func (s *Store) Create(n NewTask) (Task, error) {
	n.Title = strings.TrimSpace(n.Title)
	if n.Title == "" {
		return Task{}, errors.New("a Task needs a title")
	}
	if _, err := config.ParseClass(string(n.Class)); err != nil {
		return Task{}, err
	}
	if err := checkProjects(n.Projects); err != nil {
		return Task{}, err
	}
	if strings.TrimSpace(n.Brief) == "" {
		return Task{}, errors.New("a Task needs a Brief")
	}
	if !filepath.IsAbs(n.LaunchFolder) {
		return Task{}, fmt.Errorf("Launch folder %q is not absolute", n.LaunchFolder)
	}
	if err := os.MkdirAll(s.home.TasksDir(), 0o755); err != nil {
		return Task{}, err
	}
	var t Task
	err := home.WithFileLock(filepath.Join(s.home.TasksDir(), ".lck"), func() error {
		next, err := s.nextNumber()
		if err != nil {
			return err
		}
		id := FormatID(next, n.Title)
		if err := os.Mkdir(s.Dir(id), 0o755); err != nil {
			return fmt.Errorf("create Task folder: %w", err)
		}
		folder, err := ResolveFolder(n.LaunchFolder, n.Ticket, n.TicketFolder, s.WorkDir(id))
		if err != nil {
			os.RemoveAll(s.Dir(id))
			return err
		}
		now := s.now()
		t = Task{
			ID:           id,
			Title:        n.Title,
			Class:        n.Class,
			Projects:     slices.Clone(n.Projects),
			Ticket:       n.Ticket,
			State:        Queued,
			LaunchFolder: n.LaunchFolder,
			Folder:       folder,
			Overrides:    n.Overrides,
			SkipPlan:     n.SkipPlan,
			CreatedAt:    now,
			UpdatedAt:    now,
			StateSince:   now,
		}
		if folder == s.WorkDir(id) {
			if err := os.MkdirAll(folder, 0o755); err != nil {
				return err
			}
		}
		if err := home.WriteFileAtomic(s.BriefPath(id), []byte(ensureNewline(n.Brief)), 0o644); err != nil {
			return err
		}
		if err := home.WriteJSONAtomic(s.StatePath(id), t); err != nil {
			return err
		}
		_, err = s.Append(id, Event{Type: EventCreated, To: Queued, Text: t.Title})
		return err
	})
	return t, err
}

func checkProjects(projects []string) error {
	if len(projects) == 0 {
		return errors.New("a Task needs a Project")
	}
	for i, p := range projects {
		if strings.TrimSpace(p) == "" {
			return errors.New("a Project name is empty")
		}
		if slices.Contains(projects[:i], p) {
			return fmt.Errorf("Project %s is named twice", p)
		}
	}
	return nil
}

func (t Task) PrimaryProject() string {
	if len(t.Projects) == 0 {
		return ""
	}
	return t.Projects[0]
}

func ensureNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

func (s *Store) nextNumber() (int, error) {
	ids, err := s.ids()
	if err != nil {
		return 0, err
	}
	max := 0
	for _, id := range ids {
		if n := id.Number(); n > max {
			max = n
		}
	}
	return max + 1, nil
}

func (s *Store) ids() ([]ID, error) {
	entries, err := os.ReadDir(s.home.TasksDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []ID
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if id, err := ParseID(e.Name()); err == nil {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].Number() != ids[j].Number() {
			return ids[i].Number() < ids[j].Number()
		}
		return ids[i] < ids[j]
	})
	return ids, nil
}

func (s *Store) Get(id ID) (Task, error) {
	var stored struct {
		Task
		Project string `json:"project,omitempty"`
	}
	found, err := home.ReadJSON(s.StatePath(id), &stored)
	if err != nil {
		return Task{}, err
	}
	if !found {
		return Task{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	t := stored.Task
	if len(t.Projects) == 0 && stored.Project != "" {
		t.Projects = []string{stored.Project}
	}
	return t, nil
}

func (s *Store) Find(ref string) (Task, error) {
	ids, err := s.ids()
	if err != nil {
		return Task{}, err
	}
	num, numErr := strconv.Atoi(ref)
	var matches []ID
	for _, id := range ids {
		if string(id) == ref {
			return s.Get(id)
		}
		if numErr == nil && id.Number() == num {
			matches = append(matches, id)
		}
	}
	if len(matches) == 1 {
		return s.Get(matches[0])
	}
	return Task{}, fmt.Errorf("%w: %s", ErrNotFound, ref)
}

func (s *Store) List() ([]Task, error) {
	ids, err := s.ids()
	if err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(ids))
	for _, id := range ids {
		t, err := s.Get(id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func (s *Store) Brief(id ID) (string, error) {
	b, err := os.ReadFile(s.BriefPath(id))
	return string(b), err
}

func (s *Store) Report(id ID) (string, bool, error) {
	b, err := os.ReadFile(s.ReportPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	return string(b), err == nil, err
}

func (s *Store) WriteReport(id ID, text string) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	if err := home.WriteFileAtomic(s.ReportPath(id), []byte(ensureNewline(text)), 0o644); err != nil {
		return err
	}
	_, err := s.Append(id, Event{Type: EventReport})
	return err
}

func (s *Store) Update(id ID, fn func(*Task) error) (Task, error) {
	return s.update(id, fn, nil)
}

func (s *Store) update(id ID, fn func(*Task) error, after func(Task) error) (Task, error) {
	var out Task
	err := home.WithFileLock(s.lockPath(id), func() error {
		t, err := s.Get(id)
		if err != nil {
			return err
		}
		if err := fn(&t); err != nil {
			return err
		}
		t.ID = id
		t.UpdatedAt = s.now()
		out = t
		if err := home.WriteJSONAtomic(s.StatePath(id), t); err != nil {
			return err
		}
		if after != nil {
			return after(t)
		}
		return nil
	})
	return out, err
}

func (s *Store) AddProject(id ID, name string) (Task, error) {
	return s.Update(id, func(t *Task) error {
		if t.State.Terminal() {
			return fmt.Errorf("Task %s is %s; a Project cannot be added", id, t.State)
		}
		if strings.TrimSpace(name) == "" {
			return errors.New("a Project name is empty")
		}
		w, err := s.Worker(id)
		if err != nil {
			return err
		}
		if w.SupervisorLive() {
			return fmt.Errorf("%w: wait for Task %s's turn to end and its Worker to exit, or stop it with coord stop %s, then add the Project", ErrWorkerLive, id, id)
		}
		if t.State == Queued && w.Spawning != nil && w.Spawning.live() {
			return fmt.Errorf("%w; add the Project once it has started", ErrSpawning)
		}
		if !slices.Contains(t.Projects, name) {
			t.Projects = append(t.Projects, name)
			return nil
		}
		if t.State == Queued || slices.ContainsFunc(w.Worktrees, func(wt Worktree) bool { return wt.Project == name }) {
			return fmt.Errorf("Task %s already has Project %s", id, name)
		}
		return nil
	})
}

var ErrStateChanged = errors.New("the Task's state changed")

func (s *Store) Transition(id ID, to State, note string) (Task, error) {
	return s.transition(id, "", to, note)
}

func (s *Store) TransitionFrom(id ID, from, to State, note string) (Task, error) {
	if _, err := ParseState(string(from)); err != nil {
		return Task{}, err
	}
	return s.transition(id, from, to, note)
}

func (s *Store) transition(id ID, from, to State, note string) (Task, error) {
	if _, err := ParseState(string(to)); err != nil {
		return Task{}, err
	}
	var ev *Event
	return s.update(id, func(t *Task) error {
		ev = nil
		if from != "" && t.State != from {
			return fmt.Errorf("%w: Task %s is %s, not %s", ErrStateChanged, id, t.State, from)
		}
		if t.State == to {
			return nil
		}
		if !CanTransition(t.State, to) {
			return fmt.Errorf("Task %s cannot go from %s to %s", id, t.State, to)
		}
		ev = &Event{Type: EventStateChanged, From: t.State, To: to, Text: note}
		t.State = to
		t.StateSince = s.now()
		return nil
	}, func(Task) error {
		if ev == nil {
			return nil
		}
		_, err := s.Append(id, *ev)
		return err
	})
}

func (s *Store) Append(id ID, e Event) (Event, error) {
	err := s.log(id).AppendWith(func(last []byte) (any, error) {
		var prev Event
		if len(last) > 0 {
			if err := json.Unmarshal(last, &prev); err != nil {
				return nil, fmt.Errorf("read last event of %s: %w", id, err)
			}
		}
		e.Seq = prev.Seq + 1
		e.Task = id
		if e.Time.IsZero() {
			e.Time = s.now()
		}
		return e, nil
	})
	if err == nil && s.onEvent != nil {
		s.onEvent(e)
	}
	return e, err
}

func (s *Store) Events(id ID, afterSeq int64) ([]Event, error) {
	var out []Event
	err := s.log(id).Each(func(line []byte) error {
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil
		}
		if e.Seq > afterSeq {
			out = append(out, e)
		}
		return nil
	})
	return out, err
}
