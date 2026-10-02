package mrwatch

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Kind string

const (
	GitLab Kind = "gitlab"
	GitHub Kind = "github"
)

type Ref struct {
	URL    string `json:"url"`
	Kind   Kind   `json:"kind"`
	Host   string `json:"host"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

func (r Ref) RepoKey() string { return r.Host + "/" + r.Repo }

func ParseURL(raw string) (Ref, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return Ref{}, fmt.Errorf("%q is not an MR or PR URL", raw)
	}
	path := strings.Trim(u.Path, "/")
	if repo, num, ok := strings.Cut(path, "/-/merge_requests/"); ok {
		n, err := strconv.Atoi(strings.Trim(num, "/"))
		if err != nil || n <= 0 || repo == "" {
			return Ref{}, fmt.Errorf("%q is not a GitLab merge request URL", raw)
		}
		return Ref{URL: raw, Kind: GitLab, Host: strings.ToLower(u.Host), Repo: repo, Number: n}, nil
	}
	parts := strings.Split(path, "/")
	if len(parts) == 4 && parts[2] == "pull" {
		n, err := strconv.Atoi(parts[3])
		if err != nil || n <= 0 {
			return Ref{}, fmt.Errorf("%q is not a GitHub pull request URL", raw)
		}
		return Ref{URL: raw, Kind: GitHub, Host: strings.ToLower(u.Host), Repo: parts[0] + "/" + parts[1], Number: n}, nil
	}
	return Ref{}, fmt.Errorf("%q is not a GitLab merge request or GitHub pull request URL", raw)
}

type State string

const (
	Open   State = "open"
	Merged State = "merged"
	Closed State = "closed"
)

type CI string

const (
	CINone    CI = ""
	CIPending CI = "pending"
	CIRunning CI = "running"
	CIPassed  CI = "passed"
	CIFailed  CI = "failed"
	CIOther   CI = "other"
)

type Note struct {
	ID        int64     `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	URL       string    `json:"url,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Source    Source    `json:"source,omitempty"`
}

type Source string

const (
	SourceNote          Source = ""
	SourceReviewComment Source = "review-comment"
	SourceReviewSummary Source = "review-summary"
)

type NoteCursor struct {
	Note int64
}

type Snapshot struct {
	State    State
	HeadSHA  string
	CI       CI
	Approved bool
	Notes    []Note
}

type Client interface {
	Fetch(ctx context.Context, r Ref, after NoteCursor) (Snapshot, error)
}

type Clients struct {
	GitLab *GitLabClient
	GitHub *GitHubClient
}

func NewClients() *Clients {
	return &Clients{GitLab: &GitLabClient{}, GitHub: &GitHubClient{}}
}

func (c *Clients) For(k Kind) (Client, error) {
	switch k {
	case GitLab:
		return c.GitLab, nil
	case GitHub:
		return c.GitHub, nil
	}
	return nil, fmt.Errorf("no MR client for kind %q", k)
}

type Position struct {
	Polled               bool      `json:"polled,omitempty"`
	State                State     `json:"state,omitempty"`
	HeadSHA              string    `json:"head_sha,omitempty"`
	CI                   CI        `json:"ci,omitempty"`
	CIRed                bool      `json:"ci_red,omitempty"`
	Approved             bool      `json:"approved,omitempty"`
	LastNoteID           int64     `json:"last_note_id,omitempty"`
	SeenReviewCommentIDs []int64   `json:"seen_review_comment_ids,omitempty"`
	SeenReviewSummaryIDs []int64   `json:"seen_review_summary_ids,omitempty"`
	ReadyReported        bool      `json:"ready_reported,omitempty"`
	PolledAt             time.Time `json:"polled_at,omitzero"`
	FailingSince         time.Time `json:"failing_since,omitzero"`
	FailReported         bool      `json:"fail_reported,omitempty"`
	LastError            string    `json:"last_error,omitempty"`
}

func (p Position) Cursor() NoteCursor {
	return NoteCursor{Note: p.LastNoteID}
}

type FactKind string

const (
	FactMerged   FactKind = "mr-merged"
	FactClosed   FactKind = "mr-closed"
	FactCIRed    FactKind = "mr-ci-red"
	FactCIGreen  FactKind = "mr-ci-green"
	FactComments FactKind = "mr-comments"
	FactReady    FactKind = "mr-ready"
	FactFailing  FactKind = "mr-watch-failing"
)

type Fact struct {
	Kind  FactKind `json:"kind"`
	Ref   Ref      `json:"ref"`
	Text  string   `json:"text"`
	Notes []Note   `json:"notes,omitempty"`
}

func (k FactKind) NeedsCaptain() bool {
	switch k {
	case FactClosed, FactCIRed, FactComments, FactReady:
		return true
	}
	return false
}
