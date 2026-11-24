package repository

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// GitLabReview reads merge request link of the GitLab repository id (GET /api/v4/projects/:id/merge_requests/:iid,
// :id the URL-encoded project path) over the guarded transport Check uses. token, when set, goes in the PRIVATE-TOKEN
// header; without it only a public project answers. GitLab's states map to ReviewStatus's: opened and locked (being
// merged) are open, closed and merged their own. The answer must name the same merge request (iid and web_url), or
// it is inconclusive. Errors are *CheckError: authentication, unavailable (missing or hidden), rate_limited, network,
// timeout, inconclusive.
func (c Checker) GitLabReview(ctx context.Context, id Identity, link TaskLink, token string) (ReviewStatus, error) {
	if id.Provider != GitLab || link.Kind != "pull_request" || link.Number == "" {
		return ReviewStatus{}, &CheckError{"inconclusive"}
	}
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	endpoint := id.InstanceURL + "/api/v4/projects/" + url.PathEscape(id.Path) + "/merge_requests/" + link.Number
	var header http.Header
	if token != "" {
		header = http.Header{"Private-Token": {token}}
	}
	raw, err := c.getJSON(ctx, id, endpoint, header)
	if err != nil {
		return ReviewStatus{}, err
	}
	var mr struct {
		IID             int64     `json:"iid"`
		State           string    `json:"state"`
		WebURL          string    `json:"web_url"`
		MergeCommitSHA  *string   `json:"merge_commit_sha"`
		SquashCommitSHA *string   `json:"squash_commit_sha"`
		MergedAt        time.Time `json:"merged_at"`
		ClosedAt        time.Time `json:"closed_at"`
	}
	if json.Unmarshal(raw, &mr) != nil || strconv.FormatInt(mr.IID, 10) != link.Number {
		return ReviewStatus{}, &CheckError{"inconclusive"}
	}
	if web, got, err := ParseTaskLink(mr.WebURL); err != nil || web != id || got.Kind != link.Kind || got.Number != link.Number {
		return ReviewStatus{}, &CheckError{"inconclusive"}
	}
	switch mr.State {
	case "opened", "locked":
		return ReviewStatus{State: ReviewOpen}, nil
	case "closed":
		return ReviewStatus{State: ReviewClosed, TerminalAt: mr.ClosedAt}, nil
	case "merged":
		return ReviewStatus{State: ReviewMerged, MergeCommit: cmp.Or(deref(mr.MergeCommitSHA), deref(mr.SquashCommitSHA)), TerminalAt: mr.MergedAt}, nil
	}
	return ReviewStatus{}, &CheckError{"inconclusive"}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
