package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAPI is GitHub's REST API on made-up data: example-org/example-repo, alice and bob. Paths not listed are 404s.
func fakeAPI(t *testing.T) (*Connector, *[]string) {
	long := strings.Repeat("示例描述。", 1000)
	routes := map[string]string{
		"/search/issues": `{"total_count":2,"items":[
			{"number":12,"title":"Add the example","state":"open","user":{"login":"alice"},"html_url":"https://github.com/example-org/example-repo/pull/12",
			 "repository_url":"https://api.github.com/repos/example-org/example-repo","updated_at":"2026-01-02T03:04:05Z","comments":3,"pull_request":{"merged_at":null}},
			{"number":7,"title":"Example bug","state":"closed","user":{"login":"bob"},"html_url":"https://github.com/example-org/example-repo/issues/7",
			 "repository_url":"https://api.github.com/repos/example-org/example-repo","updated_at":"2026-01-01T00:00:00Z","closed_at":"2026-01-01T00:00:00Z","comments":0}]}`,
		"/search/code": `{"total_count":1,"items":[{"path":"src/example.go","html_url":"https://github.com/example-org/example-repo/blob/main/src/example.go",
			"repository":{"full_name":"example-org/example-repo"}}]}`,
		"/repos/example-org/example-repo/issues/12": `{"number":12,"title":"Add the example","state":"open","user":{"login":"alice"},
			"body":"` + long + `","html_url":"https://github.com/example-org/example-repo/pull/12","created_at":"2026-01-01T00:00:00Z",
			"updated_at":"2026-01-02T03:04:05Z","comments":2,"labels":[{"name":"enhancement"}],"pull_request":{"merged_at":null}}`,
		"/repos/example-org/example-repo/pulls/12": `{"head":{"ref":"feat/example","sha":"0123456789abcdef0123456789abcdef01234567"},"base":{"ref":"main"},
			"mergeable_state":"clean","additions":10,"deletions":2,"changed_files":2,"requested_reviewers":[{"login":"bob"}],"requested_teams":[{"slug":"reviewers"}]}`,
		"/repos/example-org/example-repo/pulls/12/reviews": `[
			{"id":301,"user":{"login":"bob"},"state":"CHANGES_REQUESTED","body":"请改一下示例。","submitted_at":"2026-01-02T01:00:00Z",
			 "html_url":"https://github.com/example-org/example-repo/pull/12#pullrequestreview-301"},
			{"id":302,"user":{"login":"bob"},"state":"COMMENTED","body":"","submitted_at":"2026-01-02T01:00:01Z"}]`,
		"/repos/example-org/example-repo/issues/12/comments": `[
			{"id":201,"user":{"login":"ci-bot[bot]","type":"Bot"},"body":"Preview ready.","created_at":"2026-01-01T12:00:00Z",
			 "html_url":"https://github.com/example-org/example-repo/pull/12#issuecomment-201"},
			{"id":202,"user":{"login":"alice"},"body":"改好了。","created_at":"2026-01-02T03:00:00Z",
			 "html_url":"https://github.com/example-org/example-repo/pull/12#issuecomment-202"}]`,
		"/repos/example-org/example-repo/pulls/12/files": `[
			{"filename":"src/example.go","status":"modified","additions":8,"deletions":2,"patch":"@@ -1 +1 @@\n-old\n+new"},
			{"filename":"docs/new.md","previous_filename":"docs/old.md","status":"renamed","additions":2,"deletions":0},
			{"filename":"assets/big.bin","status":"added","additions":0,"deletions":0}]`,
		"/repos/example-org/example-repo/commits/0123456789abcdef0123456789abcdef01234567/check-runs": `{"total_count":2,"check_runs":[
			{"id":101,"name":"test","status":"completed","conclusion":"failure","html_url":"https://github.com/example-org/example-repo/actions/runs/1/job/101",
			 "started_at":"2026-01-02T03:05:00Z","completed_at":"2026-01-02T03:09:00Z","app":{"slug":"github-actions"},"output":{"title":"Process completed with exit code 1."}},
			{"id":102,"name":"lint","status":"in_progress","started_at":"2026-01-02T03:05:00Z","app":{"slug":"github-actions"}}]}`,
		"/repos/example-org/example-repo/actions/jobs/101/logs": strings.Repeat("setup line\n", 2000) + "--- FAIL: TestExample\nexit status 1\n",
	}
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		seen = append(seen, r.URL.RequestURI())
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	m := &machine{tokens: []string{oldToken}}
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", m.fetch)
	c.api = srv.URL
	return c, &seen
}

func call(t *testing.T, c *Connector, name string, args string) string {
	t.Helper()
	out, err := c.Call(context.Background(), name, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s %s: %v", name, args, err)
	}
	return out
}

func mustContain(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

func TestToolsAreTheFourReadOnes(t *testing.T) {
	c, _ := fakeAPI(t)
	var names []string
	for _, tl := range c.Tools() {
		names = append(names, tl.Name)
		if tl.Description == "" || !strings.HasPrefix(string(tl.InputSchema), `{"type":"object"`) {
			t.Errorf("%s: %+v", tl.Name, tl)
		}
	}
	if fmt.Sprint(names) != "[github_search github_read github_diff github_checks]" {
		t.Fatalf("%v", names)
	}
}

func TestSearch(t *testing.T) {
	c, seen := fakeAPI(t)
	out := call(t, c, "github_search", `{"query":"involves:@me is:open","limit":5}`)
	mustContain(t, out, "2 results",
		"example-org/example-repo#12 [PR open] Add the example — alice, updated 2026-01-02 03:04Z, 3 comments https://github.com/example-org/example-repo/pull/12",
		"example-org/example-repo#7 [issue closed] Example bug — bob")
	if q := (*seen)[0]; !strings.Contains(q, "q=involves%3A%40me+is%3Aopen") || !strings.Contains(q, "per_page=5") {
		t.Errorf("query %s", q)
	}
	out = call(t, c, "github_search", `{"query":"repo:example-org/example-repo Example","kind":"code"}`)
	mustContain(t, out, "1 results", "example-org/example-repo src/example.go")
	if _, err := c.Call(context.Background(), "github_search", json.RawMessage(`{"query":" "}`)); err == nil {
		t.Error("an empty query was searched")
	}
}

func TestReadAPullRequest(t *testing.T) {
	c, _ := fakeAPI(t)
	out := call(t, c, "github_read", `{"repo":"example-org/example-repo","number":12}`)
	mustContain(t, out, "Pull request example-org/example-repo#12: Add the example", "State: open · by alice",
		"Labels: enhancement", "Branch: feat/example → main (head 0123456789ab) · 2 files, +10 −2 · mergeable: clean",
		"Review requested from: bob, team reviewers", "more characters cut]",
		"--- 3 comments and reviews, newest first",
		"2026-01-02 03:00Z alice:\n改好了。", "bob requested changes:\n请改一下示例。", "ci-bot[bot] (bot):")
	// newest first; an empty COMMENTED review (inline comments only) is not an entry
	if strings.Index(out, "改好了") > strings.Index(out, "请改一下") || strings.Index(out, "请改一下") > strings.Index(out, "Preview ready") {
		t.Errorf("not newest first:\n%s", out)
	}
	if strings.Count(out, "bob reviewed") != 0 {
		t.Errorf("an empty review is listed:\n%s", out)
	}
	if len(out) > bodyCap+3000 {
		t.Errorf("%d bytes: the description was not cut", len(out))
	}
	out = call(t, c, "github_read", `{"repo":"example-org/example-repo","number":12,"comments":1}`)
	mustContain(t, out, "(the newest 1)", "改好了")
	if strings.Contains(out, "Preview ready") {
		t.Errorf("more than one comment:\n%s", out)
	}
}

func TestDiff(t *testing.T) {
	c, _ := fakeAPI(t)
	out := call(t, c, "github_diff", `{"repo":"example-org/example-repo","number":12}`)
	mustContain(t, out, "3 files changed, +10 −2", "modified src/example.go +8 −2", "renamed docs/old.md → docs/new.md +2 −0")
	out = call(t, c, "github_diff", `{"repo":"example-org/example-repo","number":12,"path":"src/example.go"}`)
	mustContain(t, out, "src/example.go (modified, +8 −2)", "+new")
	out = call(t, c, "github_diff", `{"repo":"example-org/example-repo","number":12,"path":"assets/big.bin"}`)
	mustContain(t, out, "no patch")
	if _, err := c.Call(context.Background(), "github_diff", json.RawMessage(`{"repo":"example-org/example-repo","number":12,"path":"nope"}`)); err == nil {
		t.Error("a file the PR does not change")
	}
}

func TestChecksWithTheEndOfAFailedLog(t *testing.T) {
	c, _ := fakeAPI(t)
	out := call(t, c, "github_checks", `{"repo":"example-org/example-repo","number":12}`)
	mustContain(t, out, "2 checks on 0123456789ab", "test: failure", "Process completed with exit code 1.", "lint: in_progress",
		"--- FAIL: TestExample\nexit status 1")
	if len(out) > logTail+1000 {
		t.Errorf("%d bytes: the log was not cut to its end", len(out))
	}
}

func TestBadArguments(t *testing.T) {
	c, seen := fakeAPI(t)
	for _, args := range []string{`{"repo":"../../user","number":1}`, `{"repo":"example-org","number":1}`, `{"repo":"a/b?x=1","number":1}`, `{"repo":"../user","number":1}`, `{"repo":"x/..","number":1}`} {
		if _, err := c.Call(context.Background(), "github_read", json.RawMessage(args)); err == nil {
			t.Errorf("%s was read", args)
		}
	}
	if len(*seen) != 0 {
		t.Errorf("requests sent: %v", *seen)
	}
	if _, err := c.Call(context.Background(), "github_checks", json.RawMessage(`{"repo":"example-org/example-repo"}`)); err == nil {
		t.Error("checks without number or ref")
	}
	if _, err := c.Call(context.Background(), "github_read", json.RawMessage(`{"repo":"example-org/example-repo","number":99}`)); err == nil ||
		!strings.Contains(err.Error(), "404 Not Found") {
		t.Errorf("a missing issue: %v", err)
	}
}
