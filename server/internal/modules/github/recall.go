package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// Taking back what the assistant wrote (connectorwire.Recaller, docs/extensions/connectors.md "Sent as the user"): a comment can be
// deleted, a review cannot. GitHub's REST API deletes only a review that was never submitted ("Submitted reviews
// cannot be deleted"), and dismissing one leaves it on the page; there is nothing to call for a review that went out.

var _ connectorwire.Recaller = (*Connector)(nil)

// How long a recall may take: all of it (the delete and the lookups after a 404), and one lookup. Variables so that
// tests can shorten them.
var (
	recallTimeout      = 30 * time.Second
	recallProbeTimeout = 5 * time.Second
)

// CanRecall: the comments, at any age (whoever wrote a comment can delete it at any time); never a review.
func (c *Connector) CanRecall(action string, _ time.Duration) bool { return action == "github_comment" }

// Recall deletes the comment (DELETE /repos/{owner}/{repo}/issues/comments/{id}) that the row says github_comment made:
// the id is its message ref and the repository the one in its target. Only a 204 is a success: the client follows a
// redirect (a repository that moved answers 301) and sends it on as a GET, which answers 200 and deletes nothing, so a
// repository renamed or transferred since the write cannot be recalled (the outcome is unknown). A 404 says the comment
// is not there, or that the token cannot see it, or cannot delete it: recallGone looks.
func (c *Connector) Recall(ctx context.Context, m connectorwire.SentMessage) error {
	if m.Action != "github_comment" {
		return fmt.Errorf("%w: GitHub deletes a comment, not a submitted review (%s)", connectorwire.ErrCannotRecall, m.Action)
	}
	repo, number, err := recallTarget(m.Target)
	if err != nil {
		return err
	}
	// The ref goes into the request's path: only the whole number GitHub gave (wholeID), as it wrote it.
	id, err := strconv.ParseInt(m.Ref, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != m.Ref {
		return fmt.Errorf("%w: the ledger row has no comment id that GitHub could have given", connectorwire.ErrCannotRecall)
	}
	ctx, cancel := context.WithTimeout(ctx, recallTimeout)
	defer cancel()
	code, _, err := c.request(ctx, http.MethodDelete, commentPath(repo, id), nil, nil, nil)
	switch {
	case err == nil && code == http.StatusNoContent:
		return nil
	case err == nil: // a 2xx that is not the 204 the documentation gives, or a 304: it does not say the comment is gone
		return fmt.Errorf("%w: GitHub answered %d, not 204", connectorwire.ErrRecallUnknown, code)
	case code == http.StatusNotFound:
		return c.recallGone(ctx, repo, number, id, err)
	case code >= 500: // the request got to GitHub, which may have deleted the comment before it failed
		return fmt.Errorf("%w: %v", connectorwire.ErrRecallUnknown, err)
	case code == http.StatusUnauthorized: // the token was refused: nothing was deleted
		return fmt.Errorf("%w: %w", connectorwire.ErrRecallUnavailable, err)
	case code != 0: // refused: 403 (the token may not delete it), 422, a redirect that was not followed
		return err
	case beforeSending(err): // no request went out: no token, no machine, a bad proxy
		return fmt.Errorf("%w: %w", connectorwire.ErrRecallUnavailable, err)
	}
	// No answer at all (a timeout, a connection that broke): the comment may be gone.
	return fmt.Errorf("%w: %v", connectorwire.ErrRecallUnknown, err)
}

func commentPath(repo string, id int64) string {
	return fmt.Sprintf("/repos/%s/issues/comments/%d", repo, id)
}

// recallGone is what a 404 on the delete means. GitHub answers 404 for a comment that is not there, but also for one the
// token can read and may not delete (a public repository) and for a repository the token cannot see, so it looks: the
// comment itself can still be read, so it is there and the delete was refused; it cannot be read and the issue it was on
// can (or was deleted, which takes its comments), so the token sees the repository and the comment is gone, which is
// what was asked for, and a repeat after a lost answer succeeds too; anything else leaves nothing known. Nothing was
// deleted by the 404, so what it cannot tell is a plain error, not an unknown outcome.
func (c *Connector) recallGone(ctx context.Context, repo string, number int, id int64, notFound error) error {
	unknown := func(why string, err error) error {
		return fmt.Errorf("GitHub says the comment is not there, but %s (%v): %w", why, err, notFound)
	}
	look := func(path string) (int, error) {
		ctx, cancel := context.WithTimeout(ctx, recallProbeTimeout)
		defer cancel()
		code, _, err := c.request(ctx, http.MethodGet, path, nil, nil, nil)
		return code, err
	}
	code, err := look(commentPath(repo, id))
	switch {
	case err == nil:
		return fmt.Errorf("GitHub would not delete the comment and it is still there: %w", notFound)
	case code != http.StatusNotFound:
		return unknown("it could not be looked up to tell", err)
	}
	code, err = look(fmt.Sprintf("/repos/%s/issues/%d", repo, number))
	if err != nil && code != http.StatusGone {
		return unknown(fmt.Sprintf("%s#%d cannot be read to tell whether the comment was deleted", repo, number), err)
	}
	return nil
}

// recallTarget is the repository and the issue number a row's target {"ref": "owner/name#12"} names.
func recallTarget(raw json.RawMessage) (string, int, error) {
	var t struct {
		Ref string `json:"ref"`
	}
	if json.Unmarshal(raw, &t) == nil {
		if m := threadRef.FindStringSubmatch(t.Ref); m != nil && validRepo(m[1]) {
			if n, err := strconv.Atoi(m[2]); err == nil && n > 0 {
				return m[1], n, nil
			}
		}
	}
	return "", 0, fmt.Errorf("%w: the ledger row does not say which repository and issue the comment is on", connectorwire.ErrCannotRecall)
}

// beforeSending says whether err is one that stopped a request before it went out: the token could not be had (the
// sources' errors, a device login that is gone) or the proxy is malformed.
func beforeSending(err error) bool {
	for _, e := range []error{ErrNoMachine, ErrOffline, ErrNodeTooOld, ErrNoGh, ErrNotSignedIn, errNoDeviceLogin, errBadProxy} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}
