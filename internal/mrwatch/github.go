package mrwatch

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"time"
)

type GitHubClient struct {
	Bin  string
	Env  []string
	self selfUser
}

type githubUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type githubPR struct {
	State  string `json:"state"`
	Merged bool   `json:"merged"`
	Head   struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

type githubReview struct {
	ID          int64      `json:"id"`
	State       string     `json:"state"`
	Body        string     `json:"body"`
	HTMLURL     string     `json:"html_url"`
	User        githubUser `json:"user"`
	SubmittedAt time.Time  `json:"submitted_at"`
}

type githubCheckRun struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type githubCheckRuns struct {
	CheckRuns []githubCheckRun `json:"check_runs"`
}

type githubCommitStatus struct {
	State string `json:"state"`
}

type githubStatus struct {
	Statuses []githubCommitStatus `json:"statuses"`
}

type githubComment struct {
	ID        int64      `json:"id"`
	Body      string     `json:"body"`
	HTMLURL   string     `json:"html_url"`
	User      githubUser `json:"user"`
	CreatedAt time.Time  `json:"created_at"`
}

func (c *GitHubClient) Fetch(ctx context.Context, r Ref, after NoteCursor) (Snapshot, error) {
	a := api{name: "gh", bin: c.Bin, env: c.Env}
	me, err := c.self.name(r.Host, func() (string, error) {
		var u githubUser
		err := a.object(ctx, r.Host, "user", &u)
		return u.Login, err
	})
	if err != nil {
		return Snapshot{}, err
	}
	base := "repos/" + r.Repo
	pr := base + "/pulls/" + strconv.Itoa(r.Number)
	var p githubPR
	if err := a.object(ctx, r.Host, pr, &p); err != nil {
		return Snapshot{}, err
	}
	s := Snapshot{State: Open, HeadSHA: p.Head.SHA}
	switch {
	case p.Merged:
		s.State = Merged
	case p.State == "closed":
		s.State = Closed
	}
	reviews, err := list[githubReview](ctx, a, r.Host, pr+"/reviews?per_page=100")
	if err != nil {
		return Snapshot{}, err
	}
	s.Approved = githubApproved(reviews)
	if s.CI, err = githubCI(ctx, a, r, p.Head.SHA); err != nil {
		return Snapshot{}, err
	}
	issue, err := list[githubComment](ctx, a, r.Host, base+"/issues/"+strconv.Itoa(r.Number)+"/comments?per_page=100")
	if err != nil {
		return Snapshot{}, err
	}
	review, err := list[githubComment](ctx, a, r.Host, pr+"/comments?per_page=100")
	if err != nil {
		return Snapshot{}, err
	}
	add := func(user githubUser, note Note) {
		if user.Type != "Bot" && !strings.EqualFold(user.Login, me) {
			s.Notes = append(s.Notes, note)
		}
	}
	for _, cm := range issue {
		if cm.ID <= after.Note {
			continue
		}
		add(cm.User, Note{ID: cm.ID, Author: cm.User.Login, Body: cm.Body, URL: cm.HTMLURL, CreatedAt: cm.CreatedAt, Source: SourceNote})
	}
	for _, cm := range review {
		add(cm.User, Note{ID: cm.ID, Author: cm.User.Login, Body: cm.Body, URL: cm.HTMLURL, CreatedAt: cm.CreatedAt, Source: SourceReviewComment})
	}
	for _, rv := range reviews {
		if rv.State != "PENDING" && strings.TrimSpace(rv.Body) != "" {
			add(rv.User, Note{ID: rv.ID, Author: rv.User.Login, Body: rv.Body, URL: rv.HTMLURL, CreatedAt: rv.SubmittedAt, Source: SourceReviewSummary})
		}
	}
	slices.SortStableFunc(s.Notes, func(x, y Note) int {
		return cmp.Or(x.CreatedAt.Compare(y.CreatedAt), cmp.Compare(x.ID, y.ID))
	})
	return s, nil
}

func githubApproved(reviews []githubReview) bool {
	latest := map[string]string{}
	for _, rv := range reviews {
		switch rv.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			latest[rv.User.Login] = rv.State
		}
	}
	approved := false
	for _, state := range latest {
		switch state {
		case "CHANGES_REQUESTED":
			return false
		case "APPROVED":
			approved = true
		}
	}
	return approved
}

func githubCI(ctx context.Context, a api, r Ref, sha string) (CI, error) {
	if sha == "" {
		return CINone, nil
	}
	commit := "repos/" + r.Repo + "/commits/" + sha
	pages, err := list[githubCheckRuns](ctx, a, r.Host, commit+"/check-runs?per_page=100")
	if err != nil {
		return CINone, err
	}
	var status githubStatus
	if err := a.object(ctx, r.Host, commit+"/status?per_page=100", &status); err != nil {
		return CINone, err
	}
	return githubCIState(pages, status), nil
}

func githubCIState(pages []githubCheckRuns, status githubStatus) CI {
	var failed, running, pending, cancelled, passed bool
	for _, page := range pages {
		for _, run := range page.CheckRuns {
			switch run.Status {
			case "in_progress":
				running = true
				continue
			case "completed":
			default:
				pending = true
				continue
			}
			switch run.Conclusion {
			case "failure", "timed_out", "action_required", "startup_failure":
				failed = true
			case "cancelled":
				cancelled = true
			case "success", "neutral", "skipped":
				passed = true
			}
		}
	}
	for _, st := range status.Statuses {
		switch st.State {
		case "failure", "error":
			failed = true
		case "pending":
			pending = true
		case "success":
			passed = true
		}
	}
	switch {
	case failed:
		return CIFailed
	case running:
		return CIRunning
	case pending:
		return CIPending
	case cancelled:
		return CIOther
	case passed:
		return CIPassed
	}
	return CINone
}
