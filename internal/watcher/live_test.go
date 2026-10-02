package watcher

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/mrwatch"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

func TestLiveRunUntilMerged(t *testing.T) {
	raw := os.Getenv("COORD_LIVE_MR_URL")
	if raw == "" {
		t.Skip("set COORD_LIVE_MR_URL to a merged MR or PR to watch it read-only until the watcher exits")
	}
	r := newRig(t)
	id := r.shipTask(task.WaitingReview, raw)
	r.w.Client = mrwatch.NewClients().For
	r.w.Now = time.Now
	r.w.Interval = func() time.Duration { return time.Second }
	r.w.Sleep = SleepContext
	r.w.Logf = t.Logf
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := r.w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	evs, _ := r.s.Events(id, 0)
	for _, e := range evs {
		t.Logf("event %s %s", e.Type, e.Text)
	}
	if got := r.state(id); got != task.Merged {
		t.Fatalf("state %s", got)
	}
}
