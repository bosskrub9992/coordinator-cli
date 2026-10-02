package mrwatch

import (
	"context"
	"strings"
	"sync"
	"testing"
)

const (
	gitlabMRURL   = "https://gitlab.example.com/acme/backend/api/-/merge_requests/651"
	gitlabMRPath  = "projects/acme%2Fbackend%2Fapi/merge_requests/651"
	gitlabNotes   = gitlabMRPath + "/notes?sort=asc&order_by=created_at&per_page=100"
	gitlabHeadSHA = "0123456789abcdef0123456789abcdef01234567"
)

func gitlabRef(t *testing.T) Ref {
	t.Helper()
	r, err := ParseURL(gitlabMRURL)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGitLabFetchRecorded(t *testing.T) {
	t.Parallel()
	fake := newFake(t, "glab", "gitlab_api_651.json")
	s, err := (&GitLabClient{Bin: fake.bin, Env: fake.env}).Fetch(context.Background(), gitlabRef(t), NoteCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if s.State != Merged || s.HeadSHA != gitlabHeadSHA || s.CI != CIPassed || !s.Approved {
		t.Fatalf("snapshot %+v", s)
	}
	want := []struct {
		id     int64
		author string
	}{{6196438, "review-bot"}, {6196629, "alice"}, {6196775, "bob"}}
	if len(s.Notes) != len(want) {
		t.Fatalf("notes %+v", s.Notes)
	}
	for i, w := range want {
		n := s.Notes[i]
		if n.ID != w.id || n.Author != w.author || n.Body == "" || n.CreatedAt.IsZero() || n.URL != gitlabMRURL+"#note_"+itoa(w.id) {
			t.Fatalf("note %d %+v", i, n)
		}
	}
	calls := fake.calls()
	if len(calls) != 4 {
		t.Fatalf("calls %q", calls)
	}
	fake.checkGETs("gitlab.example.com", gitlabNotes)
}

func TestGitLabFetchAfterNote(t *testing.T) {
	t.Parallel()
	fake := newFake(t, "glab", "gitlab_api_651.json")
	s, err := (&GitLabClient{Bin: fake.bin, Env: fake.env}).Fetch(context.Background(), gitlabRef(t), NoteCursor{Note: 6196629})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Notes) != 1 || s.Notes[0].ID != 6196775 {
		t.Fatalf("notes %+v", s.Notes)
	}
}

func TestGitLabFetchStateAndCI(t *testing.T) {
	t.Parallel()
	fake := newFake(t, "glab", "gitlab_api_651.json")
	fake.edit(func(f map[string]fakeEntry) {
		f[gitlabMRPath] = fakeEntry{Pages: raws(`{"state":"opened","sha":"abc","web_url":"` + gitlabMRURL + `","head_pipeline":null}`)}
		f[gitlabMRPath+"/approvals"] = fakeEntry{Pages: raws(`{"approved":false}`)}
		f[gitlabNotes] = fakeEntry{Pages: raws(`[]`)}
	})
	s, err := (&GitLabClient{Bin: fake.bin, Env: fake.env}).Fetch(context.Background(), gitlabRef(t), NoteCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if s.State != Open || s.HeadSHA != "abc" || s.CI != CINone || s.Approved || len(s.Notes) != 0 {
		t.Fatalf("snapshot %+v", s)
	}
	fake.edit(func(f map[string]fakeEntry) {
		f[gitlabMRPath] = fakeEntry{Pages: raws(`{"state":"reverted","sha":"abc"}`)}
	})
	if _, err := (&GitLabClient{Bin: fake.bin, Env: fake.env}).Fetch(context.Background(), gitlabRef(t), NoteCursor{}); err == nil || !strings.Contains(err.Error(), `"reverted"`) {
		t.Fatalf("unknown state err = %v", err)
	}
}

func TestClients(t *testing.T) {
	cs := NewClients()
	a, _ := cs.For(GitLab)
	b, _ := cs.For(GitLab)
	if a != b || a != Client(cs.GitLab) {
		t.Fatal("clients not shared")
	}
	if _, err := cs.For("bitbucket"); err == nil {
		t.Fatal("unknown kind accepted by Clients")
	}
}

func TestGitLabApprovedNeedsAnApprover(t *testing.T) {
	t.Parallel()
	fake := newFake(t, "glab", "gitlab_api_651.json")
	fake.edit(func(f map[string]fakeEntry) {
		f[gitlabMRPath+"/approvals"] = fakeEntry{Pages: raws(`{"approved":true,"approvals_left":0,"approved_by":[]}`)}
	})
	s, err := (&GitLabClient{Bin: fake.bin, Env: fake.env}).Fetch(context.Background(), gitlabRef(t), NoteCursor{})
	if err != nil || s.Approved {
		t.Fatalf("approved %v err %v", s.Approved, err)
	}
}

func TestGitLabDropsOwnNotesAndCachesUser(t *testing.T) {
	t.Parallel()
	fake := newFake(t, "glab", "gitlab_api_651.json")
	fake.setUser(`{"id":580,"username":"Alice"}`)
	c := &GitLabClient{Bin: fake.bin, Env: fake.env}
	for range 2 {
		s, err := c.Fetch(context.Background(), gitlabRef(t), NoteCursor{})
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range s.Notes {
			if n.Author == "alice" {
				t.Fatalf("own note kept %+v", n)
			}
		}
		if len(s.Notes) != 2 {
			t.Fatalf("notes %+v", s.Notes)
		}
	}
	if n := fake.callsTo("user"); n != 1 {
		t.Fatalf("user looked up %d times", n)
	}
	shared := &GitLabClient{Bin: fake.bin, Env: fake.env}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := shared.Fetch(context.Background(), gitlabRef(t), NoteCursor{}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := fake.callsTo("user"); n != 2 {
		t.Fatalf("user looked up %d times across concurrent fetches", n)
	}
	other := gitlabRef(t)
	other.Host = "gitlab.example.org"
	if _, err := shared.Fetch(context.Background(), other, NoteCursor{}); err != nil {
		t.Fatal(err)
	}
	if n := fake.callsTo("user"); n != 3 {
		t.Fatalf("user per host looked up %d times", n)
	}
}

func TestGitLabUserLookupFails(t *testing.T) {
	t.Parallel()
	fake := newFake(t, "glab", "gitlab_api_651.json")
	fake.edit(func(f map[string]fakeEntry) {
		f["user"] = fakeEntry{Exit: 1, Stderr: "glab: 401 Unauthorized (HTTP 401)"}
	})
	c := &GitLabClient{Bin: fake.bin, Env: fake.env}
	_, err := c.Fetch(context.Background(), gitlabRef(t), NoteCursor{})
	if err == nil || !strings.Contains(err.Error(), "glab api --hostname gitlab.example.com --method GET user") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
	fake.setUser(`{"username":""}`)
	if _, err := c.Fetch(context.Background(), gitlabRef(t), NoteCursor{}); err == nil || !strings.Contains(err.Error(), "signed in") {
		t.Fatalf("empty user err = %v", err)
	}
	fake.setUser(`{"username":"someone"}`)
	if _, err := c.Fetch(context.Background(), gitlabRef(t), NoteCursor{}); err != nil {
		t.Fatalf("lookup not retried after failure: %v", err)
	}
}

func TestGitLabMappings(t *testing.T) {
	states := map[string]State{"opened": Open, "locked": Open, "merged": Merged, "closed": Closed}
	for in, want := range states {
		if got, err := gitlabState(in); err != nil || got != want {
			t.Errorf("state %q = %q, %v", in, got, err)
		}
	}
	cis := map[string]CI{
		"success": CIPassed, "failed": CIFailed, "running": CIRunning,
		"created": CIPending, "pending": CIPending, "preparing": CIPending, "waiting_for_resource": CIPending, "scheduled": CIPending,
		"canceled": CIOther, "skipped": CIOther, "manual": CIOther, "": CINone,
	}
	for in, want := range cis {
		if got := gitlabCI(in); got != want {
			t.Errorf("ci %q = %q, want %q", in, got, want)
		}
	}
}

func TestGitLabFetchFailure(t *testing.T) {
	t.Parallel()
	fake := newFake(t, "glab", "gitlab_api_651.json")
	fake.edit(func(f map[string]fakeEntry) { delete(f, gitlabMRPath+"/approvals") })
	_, err := (&GitLabClient{Bin: fake.bin, Env: fake.env}).Fetch(context.Background(), gitlabRef(t), NoteCursor{})
	if err == nil || !strings.Contains(err.Error(), "glab api --hostname gitlab.example.com --method GET "+gitlabMRPath+"/approvals") || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("err = %v", err)
	}
	fake.edit(func(f map[string]fakeEntry) {
		f[gitlabMRPath] = fakeEntry{Exit: 1, Stderr: "glab: 401 Unauthorized (HTTP 401)"}
	})
	_, err = (&GitLabClient{Bin: fake.bin, Env: fake.env}).Fetch(context.Background(), gitlabRef(t), NoteCursor{})
	if err == nil || !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Fatalf("auth err = %v", err)
	}
	_, err = (&GitLabClient{Bin: fake.bin + "-missing", Env: fake.env}).Fetch(context.Background(), gitlabRef(t), NoteCursor{})
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("missing bin err = %v", err)
	}
}
