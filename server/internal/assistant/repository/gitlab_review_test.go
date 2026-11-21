package repository

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// GitLabReview asks GET /api/v4/projects/<encoded path>/merge_requests/<iid> (field names as GitLab's REST docs give
// them, doc/api/merge_requests.md "Retrieve a merge request"), sends the token only in PRIVATE-TOKEN, and maps the
// documented states; an answer about another merge request, or a status other than 200, is never a state.
func TestGitLabReview(t *testing.T) {
	const mr = "https://gitlab.example.test/group/sub/project/-/merge_requests/17"
	id, link, err := ParseTaskLink(mr)
	if err != nil || id.Provider != GitLab {
		t.Fatalf("%+v %v", id, err)
	}
	answer := func(state, extra string) string {
		return `{"iid":17,"state":"` + state + `","web_url":"` + mr + `"` + extra + `}`
	}
	for _, tc := range []struct {
		name, body, token, want, code string
		status                        int
	}{
		{name: "opened", body: answer("opened", `,"merge_commit_sha":null`), want: "open"},
		{name: "locked is still being merged", body: answer("locked", ""), want: "open"},
		{name: "closed", body: answer("closed", ""), want: "closed"},
		{name: "merged", body: answer("merged", `,"merge_commit_sha":"abc123","squash_commit_sha":null`), want: "merged", token: "secret-token"},
		{name: "merged by squash", body: answer("merged", `,"merge_commit_sha":null,"squash_commit_sha":"def456"`), want: "merged"},
		{name: "unknown state", body: answer("draft", ""), code: "inconclusive"},
		{name: "another merge request", body: `{"iid":18,"state":"merged","web_url":"https://gitlab.example.test/group/sub/project/-/merge_requests/18"}`, code: "inconclusive"},
		{name: "another project", body: `{"iid":17,"state":"merged","web_url":"https://gitlab.example.test/group/other/-/merge_requests/17"}`, code: "inconclusive"},
		{name: "hidden or missing", status: 404, body: `{"message":"404 Not found"}`, code: "unavailable"},
		{name: "bad token", status: 401, body: `{}`, token: "stale", code: "authentication"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Checker{transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != "https://gitlab.example.test/api/v4/projects/group%2Fsub%2Fproject/merge_requests/17" {
					t.Errorf("asked %s", req.URL)
				}
				if got := req.Header.Get("PRIVATE-TOKEN"); got != tc.token || req.Header.Get("Authorization") != "" {
					t.Errorf("token header %q, want %q", got, tc.token)
				}
				status := tc.status
				if status == 0 {
					status = 200
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
			})}
			got, err := c.GitLabReview(context.Background(), id, link, tc.token)
			var ce *CheckError
			switch {
			case tc.code != "":
				if !errors.As(err, &ce) || ce.Code != tc.code {
					t.Fatalf("%+v %v, want %s", got, err, tc.code)
				}
			case err != nil || got.State != tc.want:
				t.Fatalf("%+v %v, want %s", got, err, tc.want)
			case tc.want == "merged" && got.MergeCommit == "":
				t.Fatalf("merged without its commit: %+v", got)
			}
		})
	}
	// Only a GitLab merge request is asked about.
	c := Checker{transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("asked")
		return nil, nil
	})}
	issue := link
	issue.Kind = "issue"
	gh, ghLink, _ := ParseTaskLink("https://github.com/o/r/pull/1")
	for _, bad := range []struct {
		id   Identity
		link TaskLink
	}{{id, issue}, {gh, ghLink}} {
		if _, err := c.GitLabReview(context.Background(), bad.id, bad.link, ""); err == nil {
			t.Errorf("%+v %+v: read", bad.id, bad.link)
		}
	}
}
