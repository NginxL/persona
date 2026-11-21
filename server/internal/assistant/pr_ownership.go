package assistant

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// prOwnershipForge answers whether a GitHub PR was opened by the authenticated viewer. known=false means ownership
// cannot be established (GitHub unconnected, a host the forge does not read): callers keep the conservative delivery
// default instead of treating it as foreign.
type prOwnershipForge interface {
	PROwnedByViewer(ctx context.Context, repo string, number int) (owned, known bool, err error)
}

// PROwnedByViewer compares a PR's opening author with the authenticated login. Both reads are GitHub API data; a
// missing authentication degrades to known=false rather than erroring, matching the gate's host-not-connected rule.
func (g githubForge) PROwnedByViewer(ctx context.Context, repo string, number int) (bool, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var pr struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := g.get(cctx, "/repos/"+repo+"/pulls/"+strconv.Itoa(number), &pr); err != nil {
		if errors.Is(err, errGitHubUnconfigured) {
			return false, false, nil
		}
		return false, false, err
	}
	if strings.TrimSpace(pr.User.Login) == "" {
		return false, false, nil
	}
	var viewer struct {
		Login string `json:"login"`
	}
	if err := g.get(cctx, "/user", &viewer); err != nil {
		if errors.Is(err, errGitHubUnconfigured) {
			return false, false, nil
		}
		return false, false, err
	}
	if strings.TrimSpace(viewer.Login) == "" {
		return false, false, nil
	}
	return strings.EqualFold(pr.User.Login, viewer.Login), true, nil
}
