package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/claude/worker"
	"github.com/spf13/cobra"
)

var watchPoll = 300 * time.Millisecond

func newWatchCmd(a *app) *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "watch <task>",
		Short: "Show the Worker's stream readably: text, tool calls, results, turn ends (read-only)",
		Long:  "Render the Task's worker.log. With -f, keep following until the Worker exits (Ctrl-C to stop watching).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := a.tasks()
			t, err := s.Find(args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			path := s.WorkerLogPath(t.ID)
			var f *os.File
			for {
				f, err = os.Open(path)
				if err == nil {
					break
				}
				if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				if !follow {
					fmt.Fprintf(out, "Task %s has no Worker output yet (state %s).\n", t.ID, t.State)
					return nil
				}
				w, _ := s.Worker(t.ID)
				if !w.SupervisorLive() {
					fmt.Fprintf(out, "Task %s has no live Worker and no output (state %s).\n", t.ID, t.State)
					return nil
				}
				time.Sleep(watchPoll)
			}
			defer f.Close()
			r := bufio.NewReaderSize(f, 1<<20)
			var partial []byte
			idle := 0
			for {
				line, err := r.ReadBytes('\n')
				if len(line) > 0 {
					partial = append(partial, line...)
				}
				if err == nil {
					worker.RenderLog(out, bytes.TrimSpace(partial))
					partial = partial[:0]
					idle = 0
					continue
				}
				if !errors.Is(err, io.EOF) {
					return err
				}
				if !follow {
					return nil
				}
				w, _ := s.Worker(t.ID)
				if !w.SupervisorLive() {
					idle++
					if idle > 3 {
						cur, _ := s.Get(t.ID)
						fmt.Fprintf(out, "-- the Worker is not running; Task %s is %s\n", t.ID, cur.State)
						return nil
					}
				}
				time.Sleep(watchPoll)
			}
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep following the live stream")
	return cmd
}
