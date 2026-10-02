package mrwatch

import (
	"errors"
	"slices"
	"testing"
	"time"
)

var (
	diffRef = Ref{URL: "https://git.example.com/g/r/-/merge_requests/7", Kind: GitLab, Host: "git.example.com", Repo: "g/r", Number: 7}
	t0      = time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
)

func note(id int64, author string) Note {
	return Note{ID: id, Author: author, Body: "b", URL: diffRef.URL + "#note_" + itoa(id), CreatedAt: t0}
}

func polled(p Position) Position {
	p.Polled = true
	if p.State == "" {
		p.State = Open
	}
	if p.HeadSHA == "" {
		p.HeadSHA = "a1"
	}
	return p
}

func snap(s Snapshot) Snapshot {
	if s.State == "" {
		s.State = Open
	}
	if s.HeadSHA == "" {
		s.HeadSHA = "a1"
	}
	return s
}

func kindsOf(facts []Fact) []FactKind {
	var out []FactKind
	for _, f := range facts {
		out = append(out, f.Kind)
	}
	return out
}

func TestDiff(t *testing.T) {
	cases := []struct {
		name  string
		prev  Position
		snap  Snapshot
		added time.Time
		kinds []FactKind
		check func(t *testing.T, facts []Fact, next Position)
	}{
		{
			name: "first poll baselines notes without a comments fact",
			snap: snap(Snapshot{CI: CIRunning, Notes: []Note{note(5, "alice"), note(9, "bob")}}),
			check: func(t *testing.T, _ []Fact, next Position) {
				if !next.Polled || next.LastNoteID != 9 || next.State != Open || next.CI != CIRunning || next.HeadSHA != "a1" {
					t.Fatalf("next %+v", next)
				}
			},
		},
		{name: "first poll merged", snap: snap(Snapshot{State: Merged, CI: CIPassed, Approved: true}), kinds: []FactKind{FactMerged}},
		{name: "first poll closed", snap: snap(Snapshot{State: Closed}), kinds: []FactKind{FactClosed}},
		{
			name:  "first poll red CI",
			snap:  snap(Snapshot{CI: CIFailed, Notes: []Note{note(3, "alice")}}),
			kinds: []FactKind{FactCIRed},
			check: func(t *testing.T, _ []Fact, next Position) {
				if !next.CIRed || next.LastNoteID != 3 {
					t.Fatalf("next %+v", next)
				}
			},
		},
		{
			name:  "first poll ready",
			snap:  snap(Snapshot{CI: CIPassed, Approved: true}),
			kinds: []FactKind{FactReady},
			check: func(t *testing.T, _ []Fact, next Position) {
				if !next.ReadyReported {
					t.Fatalf("next %+v", next)
				}
			},
		},
		{name: "open to merged", prev: polled(Position{CI: CIPassed}), snap: snap(Snapshot{State: Merged, CI: CIPassed}), kinds: []FactKind{FactMerged}},
		{name: "open to closed", prev: polled(Position{}), snap: snap(Snapshot{State: Closed}), kinds: []FactKind{FactClosed}},
		{name: "closed to reopened is silent", prev: polled(Position{State: Closed}), snap: snap(Snapshot{})},
		{name: "merged stays merged", prev: polled(Position{State: Merged}), snap: snap(Snapshot{State: Merged})},
		{name: "closed stays closed", prev: polled(Position{State: Closed}), snap: snap(Snapshot{State: Closed})},
		{name: "running to failed", prev: polled(Position{CI: CIRunning}), snap: snap(Snapshot{CI: CIFailed}), kinds: []FactKind{FactCIRed}},
		{name: "passed to failed", prev: polled(Position{CI: CIPassed}), snap: snap(Snapshot{CI: CIFailed}), kinds: []FactKind{FactCIRed}},
		{name: "failed stays failed on same head", prev: polled(Position{CI: CIFailed, CIRed: true}), snap: snap(Snapshot{CI: CIFailed})},
		{
			name:  "failed again on a new head",
			prev:  polled(Position{CI: CIFailed, CIRed: true}),
			snap:  snap(Snapshot{CI: CIFailed, HeadSHA: "b2"}),
			kinds: []FactKind{FactCIRed},
		},
		{
			name:  "failed to passed",
			prev:  polled(Position{CI: CIFailed, CIRed: true}),
			snap:  snap(Snapshot{CI: CIPassed}),
			kinds: []FactKind{FactCIGreen},
			check: func(t *testing.T, _ []Fact, next Position) {
				if next.CIRed {
					t.Fatalf("next %+v", next)
				}
			},
		},
		{
			name:  "green after a fix pipeline ran in between",
			prev:  polled(Position{CI: CIRunning, CIRed: true, HeadSHA: "b2"}),
			snap:  snap(Snapshot{CI: CIPassed, HeadSHA: "b2"}),
			kinds: []FactKind{FactCIGreen},
		},
		{name: "passed without a prior red is silent", prev: polled(Position{CI: CIRunning}), snap: snap(Snapshot{CI: CIPassed})},
		{
			name: "red CI stays red while running",
			prev: polled(Position{CI: CIFailed, CIRed: true}),
			snap: snap(Snapshot{CI: CIRunning, HeadSHA: "b2"}),
			check: func(t *testing.T, _ []Fact, next Position) {
				if !next.CIRed {
					t.Fatalf("next %+v", next)
				}
			},
		},
		{
			name:  "new notes past the position",
			prev:  polled(Position{LastNoteID: 10}),
			snap:  snap(Snapshot{Notes: []Note{note(8, "old"), note(11, "alice"), note(12, "bob"), note(14, "alice")}}),
			kinds: []FactKind{FactComments},
			check: func(t *testing.T, facts []Fact, next Position) {
				f := facts[0]
				if next.LastNoteID != 14 || len(f.Notes) != 3 || f.Notes[0].ID != 11 || f.Ref != diffRef {
					t.Fatalf("fact %+v next %+v", f, next)
				}
				if want := "3 new comments on " + diffRef.URL + " from alice, bob"; f.Text != want {
					t.Fatalf("text %q want %q", f.Text, want)
				}
			},
		},
		{
			name:  "one new note",
			prev:  polled(Position{LastNoteID: 10}),
			snap:  snap(Snapshot{Notes: []Note{note(11, "alice")}}),
			kinds: []FactKind{FactComments},
			check: func(t *testing.T, facts []Fact, _ Position) {
				if want := "1 new comment on " + diffRef.URL + " from alice"; facts[0].Text != want {
					t.Fatalf("text %q", facts[0].Text)
				}
			},
		},
		{
			name: "review notes are fresh until seen, whatever their id",
			prev: polled(Position{LastNoteID: 100, SeenReviewCommentIDs: []int64{10, 20}, SeenReviewSummaryIDs: []int64{500}}),
			snap: snap(Snapshot{Notes: []Note{
				note(50, "old"),
				{ID: 10, Author: "old", Source: SourceReviewComment},
				{ID: 15, Author: "bob", Source: SourceReviewComment},
				{ID: 500, Author: "old", Source: SourceReviewSummary},
				{ID: 120, Author: "carol", Source: SourceReviewSummary},
				note(101, "alice"),
			}}),
			kinds: []FactKind{FactComments},
			check: func(t *testing.T, facts []Fact, next Position) {
				if ids := noteIDs(facts[0].Notes); !slices.Equal(ids, []int64{15, 120, 101}) {
					t.Fatalf("notes %v", ids)
				}
				if next.LastNoteID != 101 || !slices.Equal(next.SeenReviewCommentIDs, []int64{10, 15, 20}) || !slices.Equal(next.SeenReviewSummaryIDs, []int64{120, 500}) {
					t.Fatalf("next %+v", next)
				}
				if want := "3 new comments on " + diffRef.URL + " from bob, carol, alice"; facts[0].Text != want {
					t.Fatalf("text %q", facts[0].Text)
				}
			},
		},
		{
			name:  "review comment and summary ids are tracked apart",
			prev:  polled(Position{SeenReviewCommentIDs: []int64{7}}),
			snap:  snap(Snapshot{Notes: []Note{{ID: 7, Author: "carol", Source: SourceReviewSummary}}}),
			kinds: []FactKind{FactComments},
		},
		{
			name: "first poll baselines every source",
			snap: snap(Snapshot{Notes: []Note{note(500, "alice"), {ID: 40, Author: "bob", Source: SourceReviewComment}, {ID: 900, Author: "carol", Source: SourceReviewSummary}}}),
			check: func(t *testing.T, _ []Fact, next Position) {
				if next.LastNoteID != 500 || !slices.Equal(next.SeenReviewCommentIDs, []int64{40}) || !slices.Equal(next.SeenReviewSummaryIDs, []int64{900}) {
					t.Fatalf("next %+v", next)
				}
			},
		},
		{
			name:  "first poll reports notes posted after the MR was added",
			added: t0.Add(-time.Hour),
			snap: snap(Snapshot{Notes: []Note{
				{ID: 3, Author: "old", CreatedAt: t0.Add(-2 * time.Hour)},
				{ID: 4, Author: "alice", CreatedAt: t0.Add(-30 * time.Minute)},
				{ID: 40, Author: "old", Source: SourceReviewComment, CreatedAt: t0.Add(-time.Hour)},
				{ID: 41, Author: "bob", Source: SourceReviewComment, CreatedAt: t0.Add(-time.Minute)},
				{ID: 90, Author: "carol", Source: SourceReviewSummary, CreatedAt: t0.Add(-time.Minute)},
			}}),
			kinds: []FactKind{FactComments},
			check: func(t *testing.T, facts []Fact, next Position) {
				if ids := noteIDs(facts[0].Notes); !slices.Equal(ids, []int64{4, 41, 90}) {
					t.Fatalf("notes %v", ids)
				}
				if next.LastNoteID != 4 || !slices.Equal(next.SeenReviewCommentIDs, []int64{40, 41}) || !slices.Equal(next.SeenReviewSummaryIDs, []int64{90}) {
					t.Fatalf("next %+v", next)
				}
			},
		},
		{
			name:  "added-at only matters on the first poll",
			prev:  polled(Position{LastNoteID: 4}),
			added: t0.Add(-time.Hour),
			snap:  snap(Snapshot{Notes: []Note{{ID: 4, Author: "alice", CreatedAt: t0.Add(-30 * time.Minute)}}}),
		},
		{
			name: "no new notes keeps the position",
			prev: polled(Position{LastNoteID: 10}),
			snap: snap(Snapshot{Notes: []Note{note(9, "old"), note(10, "old")}}),
			check: func(t *testing.T, _ []Fact, next Position) {
				if next.LastNoteID != 10 {
					t.Fatalf("next %+v", next)
				}
			},
		},
		{name: "becomes ready", prev: polled(Position{CI: CIPassed}), snap: snap(Snapshot{CI: CIPassed, Approved: true}), kinds: []FactKind{FactReady}},
		{name: "ready reported once", prev: polled(Position{CI: CIPassed, Approved: true, ReadyReported: true}), snap: snap(Snapshot{CI: CIPassed, Approved: true})},
		{name: "approved but CI running is not ready", prev: polled(Position{}), snap: snap(Snapshot{CI: CIRunning, Approved: true})},
		{name: "merged is never ready", prev: polled(Position{CI: CIPassed, Approved: true, ReadyReported: true}), snap: snap(Snapshot{State: Merged, CI: CIPassed, Approved: true}), kinds: []FactKind{FactMerged}},
		{
			name: "ready resets when approval is lost",
			prev: polled(Position{CI: CIPassed, Approved: true, ReadyReported: true}),
			snap: snap(Snapshot{CI: CIPassed}),
			check: func(t *testing.T, _ []Fact, next Position) {
				if next.ReadyReported {
					t.Fatalf("next %+v", next)
				}
			},
		},
		{
			name: "ready resets when CI stops passing",
			prev: polled(Position{CI: CIPassed, Approved: true, ReadyReported: true}),
			snap: snap(Snapshot{CI: CIRunning, Approved: true}),
			check: func(t *testing.T, _ []Fact, next Position) {
				if next.ReadyReported {
					t.Fatalf("next %+v", next)
				}
			},
		},
		{
			name:  "ready again on a new head",
			prev:  polled(Position{CI: CIPassed, Approved: true, ReadyReported: true}),
			snap:  snap(Snapshot{CI: CIPassed, Approved: true, HeadSHA: "b2"}),
			kinds: []FactKind{FactReady},
		},
		{
			name:  "green and ready together",
			prev:  polled(Position{CI: CIFailed, CIRed: true, Approved: true, LastNoteID: 1}),
			snap:  snap(Snapshot{CI: CIPassed, Approved: true, Notes: []Note{note(2, "alice")}}),
			kinds: []FactKind{FactCIGreen, FactComments, FactReady},
		},
		{
			name: "success clears the failing state",
			prev: polled(Position{FailingSince: t0.Add(-2 * time.Hour), FailReported: true, LastError: "boom"}),
			snap: snap(Snapshot{}),
			check: func(t *testing.T, _ []Fact, next Position) {
				if !next.FailingSince.IsZero() || next.FailReported || next.LastError != "" || !next.PolledAt.Equal(t0) {
					t.Fatalf("next %+v", next)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			facts, next := Diff(c.prev, c.snap, diffRef, c.added, t0)
			if got := kindsOf(facts); !slices.Equal(got, c.kinds) {
				t.Fatalf("facts %v, want %v (%+v)", got, c.kinds, facts)
			}
			for _, f := range facts {
				if f.Ref != diffRef || f.Text == "" {
					t.Fatalf("fact %+v", f)
				}
			}
			if !next.Polled || next.State != c.snap.State || next.HeadSHA != c.snap.HeadSHA || next.CI != c.snap.CI || next.Approved != c.snap.Approved || !next.PolledAt.Equal(t0) {
				t.Fatalf("position %+v", next)
			}
			if c.check != nil {
				c.check(t, facts, next)
			}
		})
	}
}

func noteIDs(notes []Note) []int64 {
	var ids []int64
	for _, n := range notes {
		ids = append(ids, n.ID)
	}
	return ids
}

func reviewNotes(ids ...int64) []Note {
	var out []Note
	for _, id := range ids {
		out = append(out, Note{ID: id, Author: "r", Source: SourceReviewComment})
	}
	return out
}

func TestSeenReviewLimit(t *testing.T) {
	var ids []int64
	for i := range SeenReviewLimit + 500 {
		ids = append(ids, int64(1000+i))
	}
	facts, p := Diff(Position{}, snap(Snapshot{Notes: reviewNotes(ids...)}), diffRef, time.Time{}, t0)
	if len(facts) != 0 || len(p.SeenReviewCommentIDs) != SeenReviewLimit || p.SeenReviewCommentIDs[0] != 1500 {
		t.Fatalf("baseline %v %d %v", facts, len(p.SeenReviewCommentIDs), p.SeenReviewCommentIDs[:1])
	}
	facts, p = Diff(p, snap(Snapshot{Notes: reviewNotes(append(ids, 900, 5000)...)}), diffRef, time.Time{}, t0)
	if len(facts) != 1 || !slices.Equal(noteIDs(facts[0].Notes), []int64{5000}) {
		t.Fatalf("after cap %+v", facts)
	}
	if len(p.SeenReviewCommentIDs) != SeenReviewLimit || p.SeenReviewCommentIDs[SeenReviewLimit-1] != 5000 || p.SeenReviewCommentIDs[0] != 1501 {
		t.Fatalf("trimmed %d", len(p.SeenReviewCommentIDs))
	}
	facts, _ = Diff(p, snap(Snapshot{Notes: reviewNotes(ids...)}), diffRef, time.Time{}, t0)
	if len(facts) != 0 {
		t.Fatalf("trimmed ids reported again %+v", facts)
	}
}

func TestDiffTexts(t *testing.T) {
	cases := []struct {
		prev Position
		snap Snapshot
		want string
	}{
		{polled(Position{}), snap(Snapshot{State: Merged}), "merged: " + diffRef.URL},
		{polled(Position{}), snap(Snapshot{State: Closed}), "closed without merging: " + diffRef.URL},
		{polled(Position{}), snap(Snapshot{CI: CIFailed}), "CI failed on " + diffRef.URL},
		{polled(Position{CI: CIFailed, CIRed: true}), snap(Snapshot{CI: CIPassed}), "CI green again on " + diffRef.URL},
		{polled(Position{}), snap(Snapshot{CI: CIPassed, Approved: true}), "approved with green CI, ready to merge: " + diffRef.URL},
	}
	for _, c := range cases {
		facts, _ := Diff(c.prev, c.snap, diffRef, time.Time{}, t0)
		if len(facts) != 1 || facts[0].Text != c.want {
			t.Errorf("facts %+v, want %q", facts, c.want)
		}
	}
}

func TestFailed(t *testing.T) {
	boom := errors.New("glab api: 502 Bad Gateway")
	base := polled(Position{CI: CIPassed, LastNoteID: 4})

	facts, p := Failed(base, diffRef, boom, t0)
	if len(facts) != 0 || !p.FailingSince.Equal(t0) || p.LastError != boom.Error() || p.FailReported || p.CI != CIPassed || p.LastNoteID != 4 {
		t.Fatalf("first failure %v %+v", facts, p)
	}
	facts, p = Failed(p, diffRef, errors.New("later"), t0.Add(FailingAfter-time.Second))
	if len(facts) != 0 || !p.FailingSince.Equal(t0) || p.LastError != "later" {
		t.Fatalf("before threshold %v %+v", facts, p)
	}
	facts, p = Failed(p, diffRef, boom, t0.Add(FailingAfter))
	if len(facts) != 1 || facts[0].Kind != FactFailing || facts[0].Ref != diffRef || !p.FailReported {
		t.Fatalf("at threshold %v %+v", facts, p)
	}
	if want := "cannot read " + diffRef.URL + " for 1h: " + boom.Error(); facts[0].Text != want {
		t.Fatalf("text %q want %q", facts[0].Text, want)
	}
	facts, p = Failed(p, diffRef, boom, t0.Add(3*FailingAfter))
	if len(facts) != 0 || !p.FailReported {
		t.Fatalf("reported twice %v %+v", facts, p)
	}
	_, p = Diff(p, snap(Snapshot{CI: CIPassed}), diffRef, time.Time{}, t0.Add(4*FailingAfter))
	facts, p = Failed(p, diffRef, boom, t0.Add(5*FailingAfter))
	if len(facts) != 0 || !p.FailingSince.Equal(t0.Add(5*FailingAfter)) || p.FailReported {
		t.Fatalf("after recovery %v %+v", facts, p)
	}
}

func TestHoursMinutes(t *testing.T) {
	for d, want := range map[time.Duration]string{time.Hour: "1h", 90 * time.Minute: "1h30m", 10 * time.Minute: "10m", 2*time.Hour + 59*time.Second: "2h"} {
		if got := hoursMinutes(d); got != want {
			t.Errorf("%v = %q want %q", d, got, want)
		}
	}
}
