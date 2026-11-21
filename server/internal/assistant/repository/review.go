package repository

import (
	"context"
	"strings"
)

// ReviewStatus is what a code host says about one review request (a GitHub pull request, a GitLab merge request, a
// provider's pull_request link). State is open, closed or merged; "" when the host said only that it is not merged
// (GitHub's merge read does not tell open from closed). MergeCommit is set only when merged. The rest is what a
// provider's repo.pr_status says, when it does: empty, and ViewerIsAuthor nil, when unknown.
type ReviewStatus struct {
	State          string
	MergeCommit    string
	HeadSHA        string
	HeadRef        string
	BaseRef        string
	Author         string
	ViewerIsAuthor *bool
}

// Review states.
const (
	ReviewOpen   = "open"
	ReviewClosed = "closed"
	ReviewMerged = "merged"
)

// Merged: the request is merged.
func (r ReviewStatus) Merged() bool { return r.State == ReviewMerged }

// ReviewReader is the extensions' repo.pr_status (extmgr.Manager), for providers whose contribution declares the
// pr_status capability. Errors are *ProviderError.
type ReviewReader interface {
	RepositoryPRStatus(ctx context.Context, provider, canonicalURL string) (ReviewStatus, error)
}

// PRStatusAnswer is repo.pr_status's answer as the program sent it.
type PRStatusAnswer struct {
	State, MergeCommit, HeadSHA, HeadRef, BaseRef, Author string
	ViewerIsAuthor                                        *bool
}

// CheckPRStatusAnswer re-checks repo.pr_status's answer: state must be open, closed or merged (else the answer is
// refused); a commit that is not 7 to 64 lowercase hex digits, a merge commit on a request not merged and a name that
// is not one plain line of at most 255 bytes are dropped.
func CheckPRStatusAnswer(a PRStatusAnswer) (ReviewStatus, error) {
	if a.State != ReviewOpen && a.State != ReviewClosed && a.State != ReviewMerged {
		return ReviewStatus{}, &AnswerError{Why: "state is not open, closed or merged"}
	}
	out := ReviewStatus{State: a.State, HeadSHA: commitID(a.HeadSHA), HeadRef: plainName(a.HeadRef), BaseRef: plainName(a.BaseRef),
		Author: plainName(a.Author)}
	if a.State == ReviewMerged {
		out.MergeCommit = commitID(a.MergeCommit)
	}
	if a.ViewerIsAuthor != nil {
		v := *a.ViewerIsAuthor
		out.ViewerIsAuthor = &v
	}
	return out, nil
}

// commitID is s when it is 7 to 64 lowercase hex digits, else "".
func commitID(s string) string {
	if len(s) < 7 || len(s) > 64 || strings.Trim(s, "0123456789abcdef") != "" {
		return ""
	}
	return s
}

// plainName is s when it is one line of at most 255 bytes with no control characters or spaces around it, else "".
func plainName(s string) string {
	if len(s) > 255 || s != strings.TrimSpace(s) || oneLine(s, 255) != s {
		return ""
	}
	return s
}
