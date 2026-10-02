package mrwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type GitLabClient struct {
	Bin  string
	Env  []string
	self selfUser
}

type gitlabMR struct {
	State        string `json:"state"`
	SHA          string `json:"sha"`
	WebURL       string `json:"web_url"`
	HeadPipeline *struct {
		Status string `json:"status"`
	} `json:"head_pipeline"`
}

type gitlabNote struct {
	ID     int64  `json:"id"`
	Body   string `json:"body"`
	System bool   `json:"system"`
	Author struct {
		Username string `json:"username"`
	} `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}

func (c *GitLabClient) Fetch(ctx context.Context, r Ref, after NoteCursor) (Snapshot, error) {
	a := api{name: "glab", bin: c.Bin, env: c.Env}
	me, err := c.self.name(r.Host, func() (string, error) {
		var u struct {
			Username string `json:"username"`
		}
		err := a.object(ctx, r.Host, "user", &u)
		return u.Username, err
	})
	if err != nil {
		return Snapshot{}, err
	}
	base := "projects/" + url.PathEscape(r.Repo) + "/merge_requests/" + strconv.Itoa(r.Number)
	var mr gitlabMR
	if err := a.object(ctx, r.Host, base, &mr); err != nil {
		return Snapshot{}, err
	}
	state, err := gitlabState(mr.State)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", r.URL, err)
	}
	s := Snapshot{State: state, HeadSHA: mr.SHA, CI: CINone}
	if mr.HeadPipeline != nil {
		s.CI = gitlabCI(mr.HeadPipeline.Status)
	}
	var approvals struct {
		Approved   bool              `json:"approved"`
		ApprovedBy []json.RawMessage `json:"approved_by"`
	}
	if err := a.object(ctx, r.Host, base+"/approvals", &approvals); err != nil {
		return Snapshot{}, err
	}
	s.Approved = approvals.Approved && len(approvals.ApprovedBy) > 0
	notes, err := list[gitlabNote](ctx, a, r.Host, base+"/notes?sort=asc&order_by=created_at&per_page=100")
	if err != nil {
		return Snapshot{}, err
	}
	link := mr.WebURL
	if link == "" {
		link = r.URL
	}
	for _, n := range notes {
		note := Note{ID: n.ID, Author: n.Author.Username, Body: n.Body, URL: link + "#note_" + strconv.FormatInt(n.ID, 10), CreatedAt: n.CreatedAt}
		if !n.System && n.ID > after.Note && !strings.EqualFold(n.Author.Username, me) {
			s.Notes = append(s.Notes, note)
		}
	}
	return s, nil
}

func gitlabState(s string) (State, error) {
	switch s {
	case "opened", "locked":
		return Open, nil
	case "merged":
		return Merged, nil
	case "closed":
		return Closed, nil
	}
	return "", fmt.Errorf("unknown GitLab merge request state %q", s)
}

func gitlabCI(status string) CI {
	switch status {
	case "success":
		return CIPassed
	case "failed":
		return CIFailed
	case "running":
		return CIRunning
	case "created", "pending", "preparing", "waiting_for_resource", "scheduled":
		return CIPending
	case "":
		return CINone
	}
	return CIOther
}
