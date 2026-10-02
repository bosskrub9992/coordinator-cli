package mrwatch

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	FailingAfter    = time.Hour
	SeenReviewLimit = 2000
)

func Diff(prev Position, s Snapshot, ref Ref, addedAt, now time.Time) ([]Fact, Position) {
	next := prev
	next.Polled = true
	next.State = s.State
	next.HeadSHA = s.HeadSHA
	next.CI = s.CI
	next.Approved = s.Approved
	next.PolledAt = now
	next.FailingSince = time.Time{}
	next.FailReported = false
	next.LastError = ""

	var fresh []Note
	var reviewComments, reviewSummaries []int64
	for _, n := range s.Notes {
		var isNew bool
		switch n.Source {
		case SourceReviewComment:
			isNew = !seen(prev.SeenReviewCommentIDs, n.ID)
			reviewComments = append(reviewComments, n.ID)
		case SourceReviewSummary:
			isNew = !seen(prev.SeenReviewSummaryIDs, n.ID)
			reviewSummaries = append(reviewSummaries, n.ID)
		default:
			isNew = n.ID > prev.LastNoteID
			next.LastNoteID = max(next.LastNoteID, n.ID)
		}
		if !prev.Polled {
			isNew = !addedAt.IsZero() && n.CreatedAt.After(addedAt)
		}
		if isNew {
			fresh = append(fresh, n)
		}
	}
	next.SeenReviewCommentIDs = remember(prev.SeenReviewCommentIDs, reviewComments)
	next.SeenReviewSummaryIDs = remember(prev.SeenReviewSummaryIDs, reviewSummaries)

	var facts []Fact
	fact := func(kind FactKind, text string) {
		facts = append(facts, Fact{Kind: kind, Ref: ref, Text: text})
	}

	if !prev.Polled || s.State != prev.State {
		switch {
		case s.State == Merged:
			fact(FactMerged, "merged: "+ref.URL)
		case s.State == Closed && (prev.State == Open || !prev.Polled):
			fact(FactClosed, "closed without merging: "+ref.URL)
		}
	}

	switch {
	case s.CI == CIFailed && (prev.CI != CIFailed || s.HeadSHA != prev.HeadSHA):
		fact(FactCIRed, "CI failed on "+ref.URL)
		next.CIRed = true
	case s.CI == CIPassed && prev.CIRed:
		fact(FactCIGreen, "CI green again on "+ref.URL)
		next.CIRed = false
	}

	if len(fresh) > 0 {
		facts = append(facts, Fact{Kind: FactComments, Ref: ref, Text: commentsText(ref, fresh), Notes: fresh})
	}

	ready := s.State == Open && s.Approved && s.CI == CIPassed
	if !s.Approved || s.CI != CIPassed || s.HeadSHA != prev.HeadSHA {
		next.ReadyReported = false
	}
	if ready && !next.ReadyReported {
		fact(FactReady, "approved with green CI, ready to merge: "+ref.URL)
		next.ReadyReported = true
	}
	return facts, next
}

func Failed(prev Position, ref Ref, err error, now time.Time) ([]Fact, Position) {
	next := prev
	if next.FailingSince.IsZero() {
		next.FailingSince = now
	}
	next.LastError = err.Error()
	failing := now.Sub(next.FailingSince)
	if failing < FailingAfter || next.FailReported {
		return nil, next
	}
	next.FailReported = true
	return []Fact{{Kind: FactFailing, Ref: ref, Text: fmt.Sprintf("cannot read %s for %s: %v", ref.URL, hoursMinutes(failing), err)}}, next
}

func seen(ids []int64, id int64) bool {
	if len(ids) >= SeenReviewLimit && id < ids[0] {
		return true
	}
	_, ok := slices.BinarySearch(ids, id)
	return ok
}

func remember(ids, more []int64) []int64 {
	if len(more) == 0 {
		return ids
	}
	all := slices.Concat(ids, more)
	slices.Sort(all)
	all = slices.Compact(all)
	if len(all) > SeenReviewLimit {
		all = all[len(all)-SeenReviewLimit:]
	}
	return slices.Clip(all)
}

func commentsText(ref Ref, notes []Note) string {
	var authors []string
	for _, n := range notes {
		if !slices.Contains(authors, n.Author) {
			authors = append(authors, n.Author)
		}
	}
	noun := "comments"
	if len(notes) == 1 {
		noun = "comment"
	}
	return fmt.Sprintf("%d new %s on %s from %s", len(notes), noun, ref.URL, strings.Join(authors, ", "))
}

func hoursMinutes(d time.Duration) string {
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h == 0:
		return fmt.Sprintf("%dm", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}
