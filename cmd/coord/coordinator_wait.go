package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
	"github.com/spf13/cobra"
)

var (
	waitPoll   = 500 * time.Millisecond
	waitBatch  = 300 * time.Millisecond
	waitSettle = time.Second
)

const summaryLimit = 240

func newWaitCmd(a *app) *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "wait [<task>]",
		Short: "Block until the next Fleet event the Coordinator has not read, print it, and exit",
		Long: "Block until there are unread events (for one Task when given), then print the batch that arrived within ~300ms,\n" +
			"one \"<task> <type> <summary>\" line per event,\n" +
			"mark them read and exit 0. On timeout or shutdown print \"no-event: <reason>\" and exit 0.\n" +
			"Run it as a background shell task (run_in_background: true); never in the foreground.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var id task.ID
			if len(args) == 1 {
				t, err := a.tasks().Find(args[0])
				if err != nil {
					return err
				}
				id = t.ID
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
			defer stop()
			return waitEvents(ctx, a.home, a.tasks(), waitOptions{task: id, timeout: timeout, poll: waitPoll, token: os.Getenv(home.EnvToken)}, cmd.OutOrStdout())
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "give up after this long (0 waits until an event or shutdown)")
	return cmd
}

type waitOptions struct {
	task    task.ID
	timeout time.Duration
	poll    time.Duration
	token   string
	batch   time.Duration
	settle  time.Duration
	now     func() time.Time
}

func (o waitOptions) withDefaults() waitOptions {
	if o.batch == 0 {
		o.batch = waitBatch
	}
	if o.settle == 0 {
		o.settle = waitSettle
	}
	if o.now == nil {
		o.now = time.Now
	}
	return o
}

func waitEvents(ctx context.Context, h home.Home, s *task.Store, o waitOptions, w io.Writer) error {
	o = o.withDefaults()
	var deadline <-chan time.Time
	if o.timeout > 0 {
		t := time.NewTimer(o.timeout)
		defer t.Stop()
		deadline = t.C
	}
	tick := time.NewTicker(o.poll)
	defer tick.Stop()
	for {
		if _, err := s.ReapLostSupervisors(); err != nil {
			return err
		}
		woke, err := deliver(ctx, h, s, o, w)
		if err != nil || woke {
			return err
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(w, "no-event: shutdown")
			return nil
		case <-deadline:
			fmt.Fprintf(w, "no-event: timeout after %s\n", o.timeout)
			return nil
		case <-tick.C:
		}
	}
}

type wakeClass int

const (
	silent wakeClass = iota
	companion
	pending
	waking
)

func unreadFor(s *task.Store, id task.ID) ([]task.Event, error) {
	all, err := s.Unread()
	if err != nil || id == "" {
		return all, err
	}
	var out []task.Event
	for _, e := range all {
		if e.Task == id {
			out = append(out, e)
		}
	}
	return out, nil
}

func classify(s *task.Store, events []task.Event, o waitOptions) ([]wakeClass, error) {
	out := make([]wakeClass, len(events))
	now := o.now()
	for i, e := range events {
		switch {
		case quiet(e):
			out[i] = silent
		case e.Type == task.EventWorkerExited && e.Task != "":
			after, err := followsReport(s, e)
			if err != nil {
				return nil, err
			}
			out[i] = waking
			if after {
				out[i] = companion
			}
		case e.Type == task.EventStateChanged && e.Task != "":
			acc, err := accompanies(s, e, o.settle)
			if err != nil {
				return nil, err
			}
			switch {
			case acc:
				out[i] = companion
			case now.Sub(e.Time) < o.settle:
				out[i] = pending
			default:
				out[i] = waking
			}
		default:
			out[i] = waking
		}
	}
	return out, nil
}

func followsReport(s *task.Store, exit task.Event) (bool, error) {
	found := false
	err := s.Backward(exit.Task, func(e task.Event) bool {
		if e.Seq >= exit.Seq {
			return true
		}
		switch e.Type {
		case task.EventReport:
			found = true
			return false
		case task.EventWorkerStarted:
			return false
		}
		return true
	})
	return found, err
}

func anchors(e task.Event) bool {
	return !quiet(e) && e.Type != task.EventStateChanged
}

func accompanies(s *task.Store, st task.Event, window time.Duration) (bool, error) {
	found := false
	err := s.Backward(st.Task, func(e task.Event) bool {
		d := e.Time.Sub(st.Time)
		if d < -window {
			return false
		}
		if d <= window && anchors(e) {
			if e.Type == task.EventWorkerExited {
				if after, err := followsReport(s, e); err != nil || after {
					return true
				}
			}
			found = true
			return false
		}
		return true
	})
	return found, err
}

func has(cs []wakeClass, c wakeClass) bool {
	for _, x := range cs {
		if x == c {
			return true
		}
	}
	return false
}

func settled(events []task.Event, cs []wakeClass) []task.Event {
	hold := map[task.ID]int64{}
	for i, e := range events {
		if cs[i] == pending {
			if cur, ok := hold[e.Task]; !ok || e.Seq < cur {
				hold[e.Task] = e.Seq
			}
		}
	}
	var out []task.Event
	for _, e := range events {
		if lim, ok := hold[e.Task]; ok && e.Seq >= lim {
			continue
		}
		out = append(out, e)
	}
	return out
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func deliver(ctx context.Context, h home.Home, s *task.Store, o waitOptions, w io.Writer) (bool, error) {
	events, err := unreadFor(s, o.task)
	if err != nil || len(events) == 0 {
		return false, err
	}
	cs, err := classify(s, events, o)
	if err != nil {
		return false, err
	}
	if !has(cs, waking) {
		return false, s.MarkRead(settled(events, cs))
	}
	sleepCtx(ctx, o.batch)
	if events, err = unreadFor(s, o.task); err != nil {
		return false, err
	}
	if cs, err = classify(s, events, o); err != nil {
		return false, err
	}
	if err := h.CheckToken(o.token); err != nil {
		if errors.Is(err, home.ErrSuperseded) {
			fmt.Fprintln(w, "no-event: superseded (another Coordinator took over the Fleet)")
			return true, nil
		}
		return false, err
	}
	var b strings.Builder
	for i, e := range events {
		if cs[i] != silent {
			b.WriteString(eventLine(s, e))
			b.WriteByte('\n')
		}
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return false, err
	}
	return true, s.MarkRead(events)
}

func quiet(e task.Event) bool {
	switch e.Type {
	case task.EventCreated, task.EventSteer, task.EventWorkerStarted, task.EventInstructionsLoaded, task.EventWorktreeReturned,
		task.EventAck, task.EventMRLinked, task.EventDroppedWork, task.EventMerged:
		return true
	case task.EventStateChanged:
		return e.To == task.Running || e.To == task.Landed || e.To == task.Dropped || e.Text == task.AckNote || e.Text == task.MergeNote
	}
	return false
}

func eventTask(e task.Event) string {
	if e.Task == "" {
		return "fleet"
	}
	return string(e.Task)
}

func eventLine(s *task.Store, e task.Event) string {
	var parts []string
	switch e.Type {
	case task.EventStateChanged:
		parts = append(parts, fmt.Sprintf("%s -> %s", e.From, e.To))
	case task.EventReport:
		parts = append(parts, "Report at "+s.ReportPath(e.Task))
	}
	if e.Text != "" {
		parts = append(parts, e.Text)
	} else if len(e.Data) > 0 && e.Type != task.EventReport {
		var compact bytes.Buffer
		if err := json.Compact(&compact, e.Data); err == nil {
			parts = append(parts, compact.String())
		}
	}
	return fmt.Sprintf("%s %s %s", eventTask(e), e.Type, oneLine(strings.Join(parts, ": "), summaryLimit))
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > limit {
		return string(r[:limit-1]) + "…"
	}
	return s
}
