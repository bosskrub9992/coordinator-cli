package mrwatch

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	githubPRURL   = "https://github.com/cli/cli/pull/14519"
	githubPRPath  = "repos/cli/cli/pulls/14519"
	githubHeadSHA = "97577e407ff3aee505aec675da081da75a24224a"
	githubReviews = githubPRPath + "/reviews?per_page=100"
	githubRuns    = "repos/cli/cli/commits/" + githubHeadSHA + "/check-runs?per_page=100"
	githubStatusP = "repos/cli/cli/commits/" + githubHeadSHA + "/status?per_page=100"
	githubIssueC  = "repos/cli/cli/issues/14519/comments?per_page=100"
	githubReviewC = githubPRPath + "/comments?per_page=100"
)

func githubRef(t *testing.T) Ref {
	t.Helper()
	r, err := ParseURL(githubPRURL)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var githubHumanNotes = []struct {
	id     int64
	author string
	anchor string
}{
	{5835081370, "waldyrious", "issuecomment-"},
	{5835204889, "williammartin", "issuecomment-"},
	{5835269986, "waldyrious", "issuecomment-"},
	{4106458120, "waldyrious", "discussion_r"},
	{4106460114, "waldyrious", "discussion_r"},
	{5836156576, "waldyrious", "issuecomment-"},
	{4107865905, "BagToad", "discussion_r"},
	{4107868980, "BagToad", "discussion_r"},
	{4107885267, "BagToad", "discussion_r"},
	{4107899463, "BagToad", "discussion_r"},
	{4108367439, "waldyrious", "discussion_r"},
	{4108408863, "waldyrious", "discussion_r"},
	{4108962035, "BagToad", "discussion_r"},
	{4108971098, "BagToad", "discussion_r"},
	{4110495971, "waldyrious", "discussion_r"},
	{4110500210, "waldyrious", "discussion_r"},
	{4110502951, "waldyrious", "discussion_r"},
	{5347711347, "chj2dctmcr-debug", "pullrequestreview-"},
}

var anchorSource = map[string]Source{"issuecomment-": SourceNote, "discussion_r": SourceReviewComment, "pullrequestreview-": SourceReviewSummary}

func checkGitHubRecorded(t *testing.T, s Snapshot) {
	t.Helper()
	if s.State != Merged || s.HeadSHA != githubHeadSHA || s.CI != CIPassed || !s.Approved {
		t.Fatalf("snapshot %+v", s)
	}
	if len(s.Notes) != len(githubHumanNotes) {
		t.Fatalf("notes %d %+v", len(s.Notes), s.Notes)
	}
	for i, w := range githubHumanNotes {
		n := s.Notes[i]
		if n.ID != w.id || n.Author != w.author || n.Body == "" || n.CreatedAt.IsZero() || n.URL != githubPRURL+"#"+w.anchor+itoa(w.id) || n.Source != anchorSource[w.anchor] {
			t.Fatalf("note %d %+v want %+v", i, n, w)
		}
	}
}

func TestGitHubFetchRecorded(t *testing.T) {
	t.Parallel()
	fake := newFake(t, "gh", "github_cli_14519.json")
	s, err := (&GitHubClient{Bin: fake.bin, Env: fake.env}).Fetch(context.Background(), githubRef(t), NoteCursor{})
	if err != nil {
		t.Fatal(err)
	}
	checkGitHubRecorded(t, s)
	if calls := fake.calls(); len(calls) != 7 {
		t.Fatalf("calls %q", calls)
	}
	fake.checkGETs("github.com", githubReviews, githubRuns, githubIssueC, githubReviewC)
}

func TestGitHubReviewNotesUseSeenSet(t *testing.T) {
	t.Parallel()
	fake := newFake(t, "gh", "github_cli_14519.json")
	c := &GitHubClient{Bin: fake.bin, Env: fake.env}
	fetch := func(after NoteCursor) Snapshot {
		t.Helper()
		s, err := c.Fetch(context.Background(), githubRef(t), after)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := fetch(NoteCursor{Note: 5836156576})
	if len(s.Notes) != 14 {
		t.Fatalf("notes %d %+v", len(s.Notes), s.Notes)
	}
	for _, n := range s.Notes {
		if n.Source == SourceNote {
			t.Fatalf("issue comment past its cursor %+v", n)
		}
	}

	_, p := Diff(Position{}, fetch(NoteCursor{}), githubRef(t), time.Time{}, t0)
	if p.LastNoteID != 5836156576 || len(p.SeenReviewCommentIDs) != 13 || !slices.Equal(p.SeenReviewSummaryIDs, []int64{5347711347}) {
		t.Fatalf("baseline %+v", p)
	}
	facts, p := Diff(p, fetch(p.Cursor()), githubRef(t), time.Time{}, t0)
	if len(facts) != 0 {
		t.Fatalf("replayed notes %+v", facts)
	}

	fake.edit(func(f map[string]fakeEntry) {
		e := f[githubReviewC]
		e.Pages = append(e.Pages, json.RawMessage(`[{"id":4106000000,"body":"drafted early, submitted late","html_url":"u1","user":{"login":"alice","type":"User"},"created_at":"2026-09-25T15:00:00Z"}]`))
		f[githubReviewC] = e
		r := f[githubReviews]
		r.Pages = append(r.Pages, json.RawMessage(`[{"id":5300000000,"state":"CHANGES_REQUESTED","body":"see inline","html_url":"u2","user":{"login":"alice","type":"User"},"submitted_at":"2026-09-30T01:00:00Z"}]`))
		f[githubReviews] = r
	})
	facts, _ = Diff(p, fetch(p.Cursor()), githubRef(t), time.Time{}, t0)
	if len(facts) != 1 || facts[0].Kind != FactComments || !slices.Equal(noteIDs(facts[0].Notes), []int64{4106000000, 5300000000}) {
		t.Fatalf("late-submitted review notes %+v", facts)
	}
}

func TestGitHubDropsOwnNotes(t *testing.T) {
	t.Parallel()
	fake := newFake(t, "gh", "github_cli_14519.json")
	fake.setUser(`{"login":"bagtoad","type":"User"}`)
	s, err := (&GitHubClient{Bin: fake.bin, Env: fake.env}).Fetch(context.Background(), githubRef(t), NoteCursor{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range s.Notes {
		if n.Author == "BagToad" {
			t.Fatalf("own note kept %+v", n)
		}
	}
	if len(s.Notes) != 12 || !s.Approved {
		t.Fatalf("notes %d approved %v", len(s.Notes), s.Approved)
	}
}

func TestGitHubReviewSummaries(t *testing.T) {
	t.Parallel()
	fake := newFake(t, "gh", "github_cli_14519.json")
	fake.edit(func(f map[string]fakeEntry) {
		f[githubReviewC] = fakeEntry{Pages: raws(`[]`)}
		f[githubReviews] = fakeEntry{Pages: raws(`[
			{"id":10,"state":"CHANGES_REQUESTED","body":"please split this","html_url":"https://github.com/cli/cli/pull/14519#pullrequestreview-10","user":{"login":"alice","type":"User"},"submitted_at":"2026-09-30T01:00:00Z"},
			{"id":11,"state":"PENDING","body":"draft thoughts","html_url":"u11","user":{"login":"alice","type":"User"}},
			{"id":12,"state":"APPROVED","body":"  ","html_url":"u12","user":{"login":"bob","type":"User"},"submitted_at":"2026-09-30T02:00:00Z"},
			{"id":13,"state":"COMMENTED","body":"bot summary","html_url":"u13","user":{"login":"bot[bot]","type":"Bot"},"submitted_at":"2026-09-30T03:00:00Z"}
		]`)}
	})
	s, err := (&GitHubClient{Bin: fake.bin, Env: fake.env}).Fetch(context.Background(), githubRef(t), NoteCursor{Note: 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	if s.Approved {
		t.Fatal("changes requested counted as approved")
	}
	if len(s.Notes) != 1 {
		t.Fatalf("notes %+v", s.Notes)
	}
	n := s.Notes[0]
	if n.ID != 10 || n.Author != "alice" || n.Body != "please split this" || n.Source != SourceReviewSummary || n.URL != githubPRURL+"#pullrequestreview-10" || n.CreatedAt.IsZero() {
		t.Fatalf("note %+v", n)
	}
}

func TestGitHubFetchJoinedPages(t *testing.T) {
	t.Parallel()
	fake := newFake(t, "gh", "github_cli_14519.json")
	fake.edit(func(f map[string]fakeEntry) {
		for _, p := range []string{githubReviews, githubIssueC, githubReviewC} {
			e := f[p]
			e.Joined = true
			f[p] = e
		}
	})
	s, err := (&GitHubClient{Bin: fake.bin, Env: fake.env}).Fetch(context.Background(), githubRef(t), NoteCursor{})
	if err != nil {
		t.Fatal(err)
	}
	checkGitHubRecorded(t, s)
}

func TestGitHubFetchOpenAndClosed(t *testing.T) {
	t.Parallel()
	fake := newFake(t, "gh", "github_cli_14519.json")
	for _, c := range []struct {
		pr   string
		want State
	}{
		{`{"state":"open","merged":false,"head":{"sha":"` + githubHeadSHA + `"}}`, Open},
		{`{"state":"closed","merged":false,"head":{"sha":"` + githubHeadSHA + `"}}`, Closed},
	} {
		fake.edit(func(f map[string]fakeEntry) { f[githubPRPath] = fakeEntry{Pages: raws(c.pr)} })
		s, err := (&GitHubClient{Bin: fake.bin, Env: fake.env}).Fetch(context.Background(), githubRef(t), NoteCursor{})
		if err != nil || s.State != c.want {
			t.Fatalf("state %q err %v, want %q", s.State, err, c.want)
		}
	}
}

func TestGitHubFetchFailure(t *testing.T) {
	t.Parallel()
	fake := newFake(t, "gh", "github_cli_14519.json")
	fake.edit(func(f map[string]fakeEntry) { delete(f, githubRuns) })
	_, err := (&GitHubClient{Bin: fake.bin, Env: fake.env}).Fetch(context.Background(), githubRef(t), NoteCursor{})
	if err == nil || !strings.Contains(err.Error(), "gh api --hostname github.com --method GET --paginate "+githubRuns) || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("err = %v", err)
	}
	fake.edit(func(f map[string]fakeEntry) { f[githubIssueC] = fakeEntry{Pages: raws(`[{"id":1`)} })
	if _, err := (&GitHubClient{Bin: fake.bin, Env: fake.env}).Fetch(context.Background(), githubRef(t), NoteCursor{}); err == nil {
		t.Fatal("truncated JSON accepted")
	}
}

func TestGitHubApproved(t *testing.T) {
	rv := func(login, state string) githubReview {
		return githubReview{State: state, User: githubUser{Login: login}}
	}
	cases := []struct {
		name    string
		reviews []githubReview
		want    bool
	}{
		{"none", nil, false},
		{"one approval", []githubReview{rv("a", "APPROVED")}, true},
		{"comment only", []githubReview{rv("a", "COMMENTED")}, false},
		{"comment after approval keeps it", []githubReview{rv("a", "APPROVED"), rv("a", "COMMENTED")}, true},
		{"changes requested blocks", []githubReview{rv("a", "APPROVED"), rv("b", "CHANGES_REQUESTED")}, false},
		{"approval after changes requested", []githubReview{rv("a", "CHANGES_REQUESTED"), rv("a", "APPROVED")}, true},
		{"dismissed approval", []githubReview{rv("a", "APPROVED"), rv("a", "DISMISSED")}, false},
		{"dismissed change request", []githubReview{rv("a", "CHANGES_REQUESTED"), rv("a", "DISMISSED"), rv("b", "APPROVED")}, true},
	}
	for _, c := range cases {
		if got := githubApproved(c.reviews); got != c.want {
			t.Errorf("%s: approved %v, want %v", c.name, got, c.want)
		}
	}
}

func TestGitHubCIState(t *testing.T) {
	runs := func(spec ...string) []githubCheckRuns {
		var page githubCheckRuns
		for _, s := range spec {
			status, conclusion, _ := strings.Cut(s, ":")
			page.CheckRuns = append(page.CheckRuns, githubCheckRun{Status: status, Conclusion: conclusion})
		}
		return []githubCheckRuns{page}
	}
	statuses := func(states ...string) githubStatus {
		var st githubStatus
		for _, s := range states {
			st.Statuses = append(st.Statuses, githubCommitStatus{State: s})
		}
		return st
	}
	cases := []struct {
		name   string
		runs   []githubCheckRuns
		status githubStatus
		want   CI
	}{
		{"nothing", nil, githubStatus{}, CINone},
		{"all success", runs("completed:success", "completed:skipped"), githubStatus{}, CIPassed},
		{"neutral only", runs("completed:neutral"), githubStatus{}, CIPassed},
		{"one failure wins", runs("completed:success", "in_progress:", "completed:failure"), githubStatus{}, CIFailed},
		{"timed out", runs("completed:timed_out"), githubStatus{}, CIFailed},
		{"action required", runs("completed:action_required"), githubStatus{}, CIFailed},
		{"in progress", runs("completed:success", "in_progress:"), githubStatus{}, CIRunning},
		{"queued", runs("completed:success", "queued:"), githubStatus{}, CIPending},
		{"running beats queued", runs("queued:", "in_progress:"), githubStatus{}, CIRunning},
		{"cancelled", runs("completed:success", "completed:cancelled"), githubStatus{}, CIOther},
		{"status failure", runs("completed:success"), statuses("failure"), CIFailed},
		{"status error", nil, statuses("error"), CIFailed},
		{"status pending", runs("completed:success"), statuses("pending"), CIPending},
		{"status success only", nil, statuses("success"), CIPassed},
	}
	for _, c := range cases {
		if got := githubCIState(c.runs, c.status); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
