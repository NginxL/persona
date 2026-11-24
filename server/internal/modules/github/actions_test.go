package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

func fakeWrites(t *testing.T) (*Connector, *[]string) {
	var mu sync.Mutex
	var posted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			posted = append(posted, r.URL.Path+" "+string(b))
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"html_url":"https://github.com/example-org/example-repo/pull/12#x"}`))
		case r.URL.Path == "/repos/example-org/example-repo/issues/12":
			_, _ = w.Write([]byte(`{"title":"Add the example"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", (&machine{tokens: []string{oldToken}}).fetch)
	c.api = srv.URL
	return c, &posted
}

func TestWritesDescribeTheCardAndPost(t *testing.T) {
	c, posted := fakeWrites(t)
	ctx := context.Background()
	if names := []string{c.Actions()[0].Name, c.Actions()[1].Name}; names[0] != "github_comment" || names[1] != "github_review" {
		t.Fatalf("%v", names)
	}

	comment := json.RawMessage(`{"repo":"example-org/example-repo","number":12,"body":"看过了，可以合。"}`)
	act, card, _ := c.Describe(ctx, "github_comment", comment)
	if card != "在 GitHub 上评论 example-org/example-repo#12「Add the example」：\n\n看过了，可以合。" ||
		act.Visible != "example-org/example-repo 仓库能看到的所有人（公开仓库是所有人）" {
		t.Fatalf("card %q, visible %q", card, act.Visible)
	}
	if out, err := c.Do(ctx, "github_comment", comment); err != nil || !strings.Contains(out, "Commented on example-org/example-repo#12") {
		t.Fatalf("%q %v", out, err)
	}

	approve := json.RawMessage(`{"repo":"example-org/example-repo","number":12,"event":"APPROVE"}`)
	if _, card, _ := c.Describe(ctx, "github_review", approve); !strings.HasPrefix(card, "【批准】在 GitHub 上批准（approve）这个 PR：example-org/example-repo#12") {
		t.Fatalf("approve card %q", card)
	}
	if _, err := c.Do(ctx, "github_review", approve); err != nil {
		t.Fatal(err)
	}
	want := []string{`/repos/example-org/example-repo/issues/12/comments {"body":"看过了，可以合。"}`, `/repos/example-org/example-repo/pulls/12/reviews {"event":"APPROVE"}`}
	if strings.Join(*posted, "\n") != strings.Join(want, "\n") {
		t.Fatalf("posted\n%s", strings.Join(*posted, "\n"))
	}

	// Offline (GitHub does not answer the title): the card still says where and what.
	other := json.RawMessage(`{"repo":"example-org/other","number":3,"event":"REQUEST_CHANGES","body":"请补测试"}`)
	if _, card, _ := c.Describe(ctx, "github_review", other); card != "在 GitHub 上要求修改（request changes）example-org/other#3：\n\n请补测试" {
		t.Fatalf("card %q", card)
	}
}

func TestBadWritesPostNothing(t *testing.T) {
	c, posted := fakeWrites(t)
	for _, tc := range []struct{ name, args string }{
		{"github_comment", `{"repo":"example-org/example-repo","number":12,"body":"  "}`},
		{"github_comment", `{"repo":"../x","number":12,"body":"x"}`},
		{"github_review", `{"repo":"example-org/example-repo","number":12,"event":"MERGE"}`},
		{"github_review", `{"repo":"example-org/example-repo","number":12,"event":"REQUEST_CHANGES"}`},
	} {
		if _, err := c.Do(context.Background(), tc.name, json.RawMessage(tc.args)); err == nil {
			t.Errorf("%s %s ran", tc.name, tc.args)
		}
	}
	if len(*posted) != 0 {
		t.Fatalf("posted %v", *posted)
	}
}

// A comment's id and a review's id are what the write says it made, in decimal and whole (they pass 2^31), and the
// text the model gets is what it always was. An answer without an id leaves the id empty; a failed write has none.
func TestWritesSayWhichMessageTheyMade(t *testing.T) {
	answers := map[string]string{
		"/repos/example-org/example-repo/issues/12/comments": `{"id":3000000001,"node_id":"IC_x","html_url":"https://github.com/example-org/example-repo/issues/12#issuecomment-3000000001"}`,
		"/repos/example-org/example-repo/pulls/12/reviews":   `{"id":80123,"node_id":"PRR_x","html_url":"https://github.com/example-org/example-repo/pull/12#pullrequestreview-80123"}`,
		"/repos/example-org/example-repo/issues/13/comments": `{"html_url":"https://github.com/example-org/example-repo/issues/13#issuecomment-1"}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, ok := answers[r.URL.Path]; ok && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", (&machine{tokens: []string{oldToken}}).fetch)
	c.api = srv.URL
	ctx := context.Background()

	for _, tc := range []struct{ name, tool, args, ref, text string }{
		{"a comment", "github_comment", `{"repo":"example-org/example-repo","number":12,"body":"ok"}`, "3000000001",
			"Commented on example-org/example-repo#12: https://github.com/example-org/example-repo/issues/12#issuecomment-3000000001"},
		{"a review", "github_review", `{"repo":"example-org/example-repo","number":12,"event":"APPROVE"}`, "80123",
			"Review (APPROVE) submitted on example-org/example-repo#12: https://github.com/example-org/example-repo/pull/12#pullrequestreview-80123"},
		{"an answer without an id", "github_comment", `{"repo":"example-org/example-repo","number":13,"body":"ok"}`, "",
			"Commented on example-org/example-repo#13: https://github.com/example-org/example-repo/issues/13#issuecomment-1"},
	} {
		done, err := connectorwire.Write(ctx, c, tc.tool, json.RawMessage(tc.args))
		if err != nil || done.MessageRef != tc.ref || done.Text != tc.text {
			t.Errorf("%s: %+v %v, want ref %q and text %q", tc.name, done, err, tc.ref, tc.text)
		}
		if text, err := c.Do(ctx, tc.tool, json.RawMessage(tc.args)); err != nil || text != tc.text {
			t.Errorf("%s: Do answered %q %v", tc.name, text, err)
		}
	}
	for _, tc := range []struct{ name, tool, args string }{
		{"GitHub refuses", "github_comment", `{"repo":"example-org/example-repo","number":99,"body":"ok"}`},
		{"arguments it cannot use", "github_comment", `{"repo":"../x","number":12,"body":"ok"}`},
		{"a tool it has not", "github_merge", `{"repo":"example-org/example-repo","number":12}`},
	} {
		if done, err := connectorwire.Write(ctx, c, tc.tool, json.RawMessage(tc.args)); err == nil || done != (connectorwire.DoResult{}) {
			t.Errorf("%s: %+v %v", tc.name, done, err)
		}
	}
}

// The id is read after the post went out, so whatever GitHub puts in it must never turn the post into a failure (the
// model would post it again): the write succeeds with the same text, and the message ref is empty unless the id is a
// positive whole number.
func TestAnUnreadableIDNeverFailsAPost(t *testing.T) {
	var mu sync.Mutex
	posts := 0
	id := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		posts++
		answer := `{"id":` + id + `,"html_url":"https://github.com/example-org/example-repo/issues/12#issuecomment-1"}`
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", (&machine{tokens: []string{oldToken}}).fetch)
	c.api = srv.URL
	ctx := context.Background()
	args := json.RawMessage(`{"repo":"example-org/example-repo","number":12,"body":"ok"}`)
	text := "Commented on example-org/example-repo#12: https://github.com/example-org/example-repo/issues/12#issuecomment-1"

	for _, tc := range []struct{ name, id, ref string }{
		{"a whole number", `1234567`, "1234567"},
		{"a string", `"abc"`, ""},
		{"a string of digits", `"123"`, ""},
		{"a fraction", `1.5`, ""},
		{"an exponent", `1e3`, ""},
		{"an object", `{"x":1}`, ""},
		{"a list", `[1]`, ""},
		{"null", `null`, ""},
		{"a boolean", `true`, ""},
		{"negative", `-3`, ""},
		{"zero", `0`, ""},
		{"past int64", `99999999999999999999`, ""},
	} {
		mu.Lock()
		id, posts = tc.id, 0
		mu.Unlock()
		done, err := connectorwire.Write(ctx, c, "github_comment", args)
		if err != nil || done.Text != text || done.MessageRef != tc.ref {
			t.Errorf("%s: %+v %v, want the usual text and ref %q", tc.name, done, err, tc.ref)
		}
		mu.Lock()
		n := posts
		mu.Unlock()
		if n != 1 {
			t.Errorf("%s: %d posts", tc.name, n)
		}
	}
}
