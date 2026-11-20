package repository

// ReviewStatus is what a code host says about one review request (a GitHub pull request, a GitLab merge request, a
// provider's pull_request link). State is open, closed or merged; "" when the host said only that it is not merged
// (GitHub's merge read does not tell open from closed). MergeCommit is set only when merged.
type ReviewStatus struct {
	State       string
	MergeCommit string
}

// Review states.
const (
	ReviewOpen   = "open"
	ReviewClosed = "closed"
	ReviewMerged = "merged"
)

// Merged: the request is merged.
func (r ReviewStatus) Merged() bool { return r.State == ReviewMerged }
