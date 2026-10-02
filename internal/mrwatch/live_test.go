package mrwatch

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveFetch(t *testing.T) {
	raw := os.Getenv("COORD_LIVE_MR_URL")
	if raw == "" {
		t.Skip("set COORD_LIVE_MR_URL to fetch a real MR or PR read-only")
	}
	ref, err := ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClients().For(ref.Kind)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s, err := c.Fetch(ctx, ref, NoteCursor{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: state=%s head=%s ci=%q approved=%v notes=%d", ref.URL, s.State, s.HeadSHA, s.CI, s.Approved, len(s.Notes))
	for _, n := range s.Notes {
		t.Logf("  note %d by %s at %s %s", n.ID, n.Author, n.CreatedAt.Format(time.RFC3339), n.URL)
	}
	facts, p := Diff(Position{}, s, ref, time.Time{}, time.Now())
	for _, f := range facts {
		t.Logf("  first-poll fact %s: %s", f.Kind, f.Text)
	}
	t.Logf("  position %+v", p)
}
