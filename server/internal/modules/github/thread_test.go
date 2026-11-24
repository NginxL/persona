package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// fakeThreadAPI is GitHub's REST API for one made-up PR (example-org/example-repo#12) and issue (#7), answering
// If-None-Match with 304; it counts the requests by path.
func fakeThreadAPI(t *testing.T, avatars string) (*Connector, map[string]int) {
	av := avatars + "/u/1?v=4"
	routes := map[string]string{
		"/user": `{"login":"alice"}`,
		"/repos/example-org/example-repo/issues/12": `{"title":"Add the example","state":"open","user":{"login":"bob","avatar_url":"` + av + `"},
			"body":"上传失败时**重试**三次。","html_url":"https://github.com/example-org/example-repo/pull/12","created_at":"2026-01-01T00:00:00Z",
			"labels":[{"name":"enhancement","color":"a2eeef"}],"pull_request":{"merged_at":null}}`,
		"/repos/example-org/example-repo/pulls/12": `{"draft":false,"merged":false,"head":{"ref":"feat/example","sha":"0123456789abcdef"},"base":{"ref":"main"}}`,
		"/repos/example-org/example-repo/issues/12/comments": `[
			{"id":1,"user":{"login":"bob","avatar_url":"` + av + `"},"body":"请看一下。","created_at":"2026-01-01T01:00:00Z","updated_at":"2026-01-01T01:00:00Z"},
			{"id":2,"user":{"login":"alice"},"body":"好。","created_at":"2026-01-01T05:00:00Z","updated_at":"2026-01-01T06:00:00Z"}]`,
		"/repos/example-org/example-repo/issues/12/events": `[
			{"id":10,"event":"labeled","actor":{"login":"bob"},"label":{"name":"enhancement"},"created_at":"2026-01-01T00:00:10Z"},
			{"id":11,"event":"subscribed","actor":{"login":"bob"},"created_at":"2026-01-01T00:00:11Z"}]`,
		"/repos/example-org/example-repo/pulls/12/reviews": `[
			{"id":20,"user":{"login":"alice"},"state":"CHANGES_REQUESTED","body":"改一下。","submitted_at":"2026-01-01T02:00:00Z"},
			{"id":21,"user":{"login":"alice"},"state":"COMMENTED","body":"","submitted_at":"2026-01-01T02:00:01Z"}]`,
		"/repos/example-org/example-repo/pulls/12/comments": `[
			{"id":30,"user":{"login":"alice"},"body":"这里漏了退避。","path":"src/upload.go","line":42,
			 "diff_hunk":"@@ -30,8 +30,9 @@\n a\n b\n c\n d\n e\n f\n+retry()","created_at":"2026-01-01T02:00:01Z","updated_at":"2026-01-01T02:00:01Z"},
			{"id":31,"user":{"login":"bob"},"body":"加上了。","path":"src/upload.go","line":42,"diff_hunk":"@@","in_reply_to_id":30,
			 "created_at":"2026-01-01T03:00:00Z","updated_at":"2026-01-01T03:00:00Z"}]`,
		"/repos/example-org/example-repo/commits/0123456789abcdef/check-runs": `{"check_runs":[
			{"id":40,"name":"test","status":"completed","conclusion":"failure","html_url":"https://github.com/example-org/example-repo/actions/runs/1/job/40",
			 "completed_at":"2026-01-01T04:00:00Z","app":{"slug":"github-actions"}},
			{"id":41,"name":"lint","status":"completed","conclusion":"success","completed_at":"2026-01-01T03:30:00Z","app":{"slug":"github-actions"}}]}`,
		"/repos/example-org/example-repo/actions/jobs/40/logs": strings.Repeat("setup line\n", 2000) + "--- FAIL: TestUpload\n",
		"/repos/example-org/example-repo/issues/7": `{"title":"Example bug","state":"closed","user":{"login":"bob"},"locked":true,
			"html_url":"https://github.com/example-org/example-repo/issues/7","created_at":"2026-01-01T00:00:00Z"}`,
		"/repos/example-org/example-repo/issues/7/comments": `[]`,
		"/repos/example-org/example-repo/issues/7/events": `[
			{"id":50,"event":"closed","actor":{"login":"bob"},"created_at":"2026-01-02T00:00:00Z"}]`,
		"/users/carol": `{"login":"carol","avatar_url":"` + avatars + `/u/3?v=4"}`,
	}
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		tag := `"` + r.URL.Path + `"`
		if r.Header.Get("If-None-Match") == tag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", tag)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	m := &machine{tokens: []string{oldToken}}
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", m.fetch)
	c.api = srv.URL
	c.check(context.Background())
	return c, hits
}

func TestThreadOfAPullRequest(t *testing.T) {
	c, _ := fakeThreadAPI(t, "https://avatars.example.com")
	page, err := c.Thread(context.Background(), "example-org/example-repo#12", "", 30)
	if err != nil {
		t.Fatal(err)
	}
	ch := page.Chat
	if ch.Kind != "pr" || ch.State != "open" || ch.Name != "Add the example" || ch.Reply != "github_comment" || ch.BodyFormat != "markdown" ||
		ch.Branch == nil || ch.Branch.Head != "feat/example" || ch.Branch.Base != "main" || len(ch.Labels) != 1 || ch.Labels[0].Color != "a2eeef" ||
		ch.Author == nil || ch.Author.Name != "bob" || ch.Author.Avatar != "avatar:bob:4" {
		t.Fatalf("chat %+v", ch)
	}
	var ids []string
	for _, m := range page.Messages {
		ids = append(ids, m.ID)
	}
	// oldest first; the subscribed event and the empty review that only holds a line's comment are not shown
	want := "e10 c1 r20 rc30 rc31 ck41 ck40 c2"
	if got := strings.Join(ids, " "); got != want || page.Before != "" {
		t.Fatalf("messages %s (before %q), want %s", got, page.Before, want)
	}
	by := map[string]connectorwire.ThreadMessage{}
	for _, m := range page.Messages {
		by[m.ID] = m
	}
	if e := by["e10"]; !e.Event || e.Text != "bob 加了标签 enhancement" {
		t.Fatalf("event %+v", e)
	}
	if r := by["r20"]; r.Kind != "review" || r.Review != "changes_requested" || !r.Mine {
		t.Fatalf("review %+v", r)
	}
	if rc := by["rc30"]; rc.Kind != "code" || rc.Code == nil || rc.Code.Path != "src/upload.go" || rc.Code.Line != 42 ||
		rc.Code.Hunk != " b\n c\n d\n e\n f\n+retry()" || rc.ReplyTo != nil {
		t.Fatalf("code comment %+v %+v", rc, rc.Code)
	}
	if rc := by["rc31"]; rc.ReplyTo == nil || rc.ReplyTo.ID != "rc30" || rc.ReplyTo.From != "alice" || rc.Code.Hunk != "" {
		t.Fatalf("an answer on the line %+v", rc)
	}
	if ck := by["ck40"]; ck.Kind != "check" || ck.Check.State != "failure" || !strings.HasSuffix(ck.Check.Log, "--- FAIL: TestUpload") ||
		len(ck.Check.Log) > checkLogTail+8 {
		t.Fatalf("failed check %+v", ck.Check)
	}
	if ck := by["ck41"]; ck.Check.State != "success" || ck.Check.Log != "" {
		t.Fatalf("passed check %+v", ck.Check)
	}
	if c2 := by["c2"]; !c2.Mine || !c2.Edited || c2.Format != "markdown" {
		t.Fatalf("my comment %+v", c2)
	}
}

// Pages go back from the newest: before names the oldest entry of the page shown.
func TestThreadPages(t *testing.T) {
	c, _ := fakeThreadAPI(t, "https://avatars.example.com")
	ctx := context.Background()
	p1, err := c.Thread(ctx, "example-org/example-repo#12", "", 3)
	if err != nil || len(p1.Messages) != 3 || p1.Messages[0].ID != "ck41" || p1.Before != "ck41" {
		t.Fatalf("first page %+v %v", p1, err)
	}
	p2, err := c.Thread(ctx, "example-org/example-repo#12", p1.Before, 3)
	if err != nil || len(p2.Messages) != 3 || p2.Messages[0].ID != "r20" || p2.Messages[2].ID != "rc31" || p2.Before != "r20" {
		t.Fatalf("second page %+v %v", p2, err)
	}
	p3, err := c.Thread(ctx, "example-org/example-repo#12", p2.Before, 3)
	if err != nil || len(p3.Messages) != 2 || p3.Before != "" || p3.Chat.Name != "Add the example" {
		t.Fatalf("last page %+v %v", p3, err)
	}
	if _, err := c.Thread(ctx, "example-org/example-repo#12", "c999", 3); err == nil {
		t.Fatal("a cursor the thread no longer has was taken")
	}
	for _, bad := range []string{"notification:7", "ci:example-org/example-repo@main", "../x#1"} {
		if _, err := c.Thread(ctx, bad, "", 3); err == nil {
			t.Fatalf("%s opened as a thread", bad)
		}
	}
}

// A closed, locked issue: no reply, no branch, its state.
func TestThreadOfALockedIssue(t *testing.T) {
	c, _ := fakeThreadAPI(t, "https://avatars.example.com")
	page, err := c.Thread(context.Background(), "example-org/example-repo#7", "", 30)
	if err != nil {
		t.Fatal(err)
	}
	if page.Chat.Kind != "issue" || page.Chat.State != "closed" || page.Chat.Reply != "" || page.Chat.Branch != nil ||
		len(page.Messages) != 1 || page.Messages[0].Text != "bob 关闭了" {
		t.Fatalf("%+v", page)
	}
}

// The next page soon after is read from memory; later GitHub is asked with If-None-Match and its 304 reuses what was
// read; a failed job's log is read once.
func TestThreadAsksWithETags(t *testing.T) {
	c, hits := fakeThreadAPI(t, "https://avatars.example.com")
	ctx := context.Background()
	const issue = "/repos/example-org/example-repo/issues/12"
	if _, err := c.Thread(ctx, "example-org/example-repo#12", "", 30); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Thread(ctx, "example-org/example-repo#12", "", 30); err != nil {
		t.Fatal(err)
	}
	if hits[issue] != 1 {
		t.Fatalf("the issue was read %d times within %v", hits[issue], threadFresh)
	}
	c.etags.mu.Lock()
	for _, e := range c.etags.m {
		e.at = time.Now().Add(-time.Hour)
	}
	c.etags.mu.Unlock()
	page, err := c.Thread(ctx, "example-org/example-repo#12", "", 30)
	if err != nil || len(page.Messages) != 8 || page.Chat.Name != "Add the example" {
		t.Fatalf("after a 304: %+v %v", page, err)
	}
	if hits[issue] != 2 || hits["/repos/example-org/example-repo/actions/jobs/40/logs"] != 1 {
		t.Fatalf("hits %v", hits)
	}
}

// An avatar comes from GitHub's avatar host without the token, small; one not seen yet is looked up first.
func TestThreadAvatar(t *testing.T) {
	var auth []string
	img := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization")+"|"+r.URL.Query().Get("s"))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG" + r.URL.Path))
	}))
	t.Cleanup(img.Close)
	c, _ := fakeThreadAPI(t, img.URL)
	c.client = img.Client()
	ctx := context.Background()
	if _, err := c.Thread(ctx, "example-org/example-repo#12", "", 30); err != nil {
		t.Fatal(err)
	}
	m, err := c.Media(ctx, "avatar:bob:4")
	if err != nil || m.Mime != "image/png" || string(m.Data) != "\x89PNG/u/1" {
		t.Fatalf("bob's avatar %+v %v", m, err)
	}
	m, err = c.Media(ctx, "avatar:carol:4")
	if err != nil || string(m.Data) != "\x89PNG/u/3" {
		t.Fatalf("carol's avatar %+v %v", m, err)
	}
	if len(auth) != 2 || auth[0] != "|96" || auth[1] != "|96" {
		t.Fatalf("avatar requests %v", auth)
	}
	if _, err := c.Media(ctx, "photo:1"); err == nil {
		t.Fatal("a ref that is not an avatar was fetched")
	}
}

// The thread page's reply ({chat, text}) is a github_comment like the assistant's ({repo, number, body}).
func TestCommentFromTheThreadPage(t *testing.T) {
	a, err := parseWrite("github_comment", json.RawMessage(`{"chat":"example-org/example-repo#12","text":" 好的 "}`))
	if err != nil || a.Repo != "example-org/example-repo" || a.Number != 12 || a.Body != "好的" {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err := parseWrite("github_comment", json.RawMessage(`{"chat":"notification:7","text":"x"}`)); err == nil {
		t.Fatal("a thread that is not a PR or issue was commented on")
	}
}

// A comment posted from the page is on the page read right after it, not the answer kept a moment ago.
func TestThreadReadsAgainAfterAComment(t *testing.T) {
	c, hits := fakeThreadAPI(t, "https://avatars.example.com")
	ctx := context.Background()
	if _, err := c.Thread(ctx, "example-org/example-repo#12", "", 30); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Thread(ctx, "example-org/example-repo#7", "", 30); err != nil {
		t.Fatal(err)
	}
	c.forget("example-org/example-repo", 12)
	if _, err := c.Thread(ctx, "example-org/example-repo#12", "", 30); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Thread(ctx, "example-org/example-repo#7", "", 30); err != nil {
		t.Fatal(err)
	}
	if hits["/repos/example-org/example-repo/issues/12/comments"] != 2 || hits["/repos/example-org/example-repo/pulls/12/reviews"] != 2 ||
		hits["/repos/example-org/example-repo/issues/7/comments"] != 1 {
		t.Fatalf("hits %v", hits)
	}
}
