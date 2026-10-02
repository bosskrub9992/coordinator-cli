package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
	"github.com/bosskrub9992/coordinator-cli/internal/watcher"
	"github.com/spf13/cobra"
)

type statusView struct {
	Home        string       `json:"home"`
	Coordinator *coordView   `json:"coordinator"`
	Watcher     *watcherView `json:"watcher"`
	Tasks       []taskView   `json:"tasks"`
}

type watcherView struct {
	PID   int       `json:"pid"`
	Since time.Time `json:"since"`
}

type taskView struct {
	task.Task
	MRs []task.MR `json:"mrs"`
}

func (t taskView) mrSummary() string {
	var open, merged, closed int
	for _, m := range t.MRs {
		switch {
		case m.Open():
			open++
		case m.Merged():
			merged++
		default:
			closed++
		}
	}
	var parts []string
	for _, c := range []struct {
		n     int
		label string
	}{{open, "open"}, {merged, "merged"}, {closed, "closed"}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.label))
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func (t taskView) display() string {
	return string(t.State)
}

type coordView struct {
	PID          int       `json:"pid"`
	LaunchFolder string    `json:"launch_folder"`
	SessionID    string    `json:"session_id"`
	Since        time.Time `json:"since"`
	Live         bool      `json:"live"`
}

func newStatusCmd(a *app) *cobra.Command {
	var asJSON, all bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the Fleet board: every Task with its state and age",
		Long:  "Show the Fleet board. Landed and Dropped Tasks are hidden unless --all is given.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := buildStatus(a.home, all)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), v)
			}
			return printStatus(cmd.OutOrStdout(), v, time.Now())
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.Flags().BoolVar(&all, "all", false, "include Landed and Dropped Tasks")
	return cmd
}

func buildStatus(h home.Home, all bool) (statusView, error) {
	v := statusView{Home: h.Root, Tasks: []taskView{}}
	s := newStore(h)
	if _, err := s.ReapLostSupervisors(); err != nil {
		return v, err
	}
	l, err := h.ReadLock()
	if err != nil {
		return v, err
	}
	if l != nil {
		v.Coordinator = &coordView{PID: l.PID, LaunchFolder: l.LaunchFolder, SessionID: l.SessionID, Since: l.AcquiredAt, Live: l.Live()}
	}
	if r, err := watcher.ReadRecord(h); err != nil {
		return v, err
	} else if r.Live() {
		v.Watcher = &watcherView{PID: r.PID, Since: r.Since}
	}
	tasks, err := s.List()
	if err != nil {
		return v, err
	}
	for _, t := range tasks {
		if !all && t.State.Terminal() {
			continue
		}
		mrs, err := s.MRs(t.ID)
		if err != nil {
			return v, err
		}
		if mrs == nil {
			mrs = []task.MR{}
		}
		v.Tasks = append(v.Tasks, taskView{Task: t, MRs: mrs})
	}
	return v, nil
}

func openMRs(v statusView) int {
	n := 0
	for _, t := range v.Tasks {
		if t.State.Terminal() {
			continue
		}
		for _, m := range t.MRs {
			if m.Open() {
				n++
			}
		}
	}
	return n
}

func printStatus(w io.Writer, v statusView, now time.Time) error {
	switch {
	case v.Coordinator == nil:
		fmt.Fprintln(w, "Coordinator: none")
	case v.Coordinator.Live:
		fmt.Fprintf(w, "Coordinator: live, pid %d, in %s, session %s, for %s\n", v.Coordinator.PID, v.Coordinator.LaunchFolder, v.Coordinator.SessionID, age(now.Sub(v.Coordinator.Since)))
	default:
		fmt.Fprintf(w, "Coordinator: none (stale lock from pid %d)\n", v.Coordinator.PID)
	}
	if v.Watcher != nil {
		fmt.Fprintf(w, "MR watcher: running, pid %d, for %s\n", v.Watcher.PID, age(now.Sub(v.Watcher.Since)))
	} else if openMRs(v) > 0 {
		fmt.Fprintln(w, "MR watcher: not running while MRs are open; the next coord launch, coord ack or MR report restarts it")
	}
	if len(v.Tasks) == 0 {
		fmt.Fprintln(w, "No Tasks in the Fleet.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TASK\tSTATE\tAGE\tIN STATE\tCLASS\tPROJECTS\tMRS\tTASK FOLDER")
	for _, t := range v.Tasks {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.display(), age(now.Sub(t.CreatedAt)), age(now.Sub(t.StateSince)), t.Class, strings.Join(t.Projects, ","), t.mrSummary(), shortFolder(t.Folder))
	}
	return tw.Flush()
}

func shortFolder(p string) string {
	return filepath.Base(filepath.Dir(p)) + string(filepath.Separator) + filepath.Base(p)
}

func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
