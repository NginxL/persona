package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// commentsServer is GitHub with comments on example-org/example-repo#12: it notes every request and answers the DELETE
// of a comment as the test says; a POST makes comment 3000000001, GET of the comment answers comment (404 when 0) and
// GET of the issue answers issue (200 when 0), unless lookups answers both.
type commentsServer struct {
	mu      sync.Mutex
	seen    []string // "METHOD path" of every request, in order
	auth    []string // the Authorization header of each
	answer  func(w http.ResponseWriter, r *http.Request)
	lookups func(w http.ResponseWriter, r *http.Request) // when set, answers the GETs instead of comment and issue
	comment int
	issue   int
}

func (s *commentsServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func newCommentsServer(t *testing.T, m *machine) (*Connector, *commentsServer) {
	t.Helper()
	s := &commentsServer{answer: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		s.mu.Lock()
		s.seen = append(s.seen, r.Method+" "+r.URL.Path)
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		answer, lookups, comment, issue := s.answer, s.lookups, s.comment, s.issue
		s.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && lookups != nil:
			lookups(w, r)
		case r.Method == http.MethodDelete:
			answer(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/example-org/example-repo/issues/12/comments":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":3000000001,"html_url":"https://github.com/example-org/example-repo/issues/12#issuecomment-3000000001"}`))
		case r.Method == http.MethodGet && r.URL.Path == theComment:
			if comment == 0 {
				comment = http.StatusNotFound
			}
			w.WriteHeader(comment)
			if comment == http.StatusOK {
				_, _ = w.Write([]byte(`{"id":3000000001}`))
			}
		case r.Method == http.MethodGet && r.URL.Path == "/repos/example-org/example-repo/issues/12":
			if issue != 0 {
				w.WriteHeader(issue)
				return
			}
			_, _ = w.Write([]byte(`{"title":"Add the example"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	if m == nil {
		m = &machine{tokens: []string{oldToken}}
	}
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", m.fetch)
	c.api = srv.URL
	return c, s
}

func answering(code int, message string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		if message != "" {
			_, _ = w.Write([]byte(`{"message":"` + message + `"}`))
		}
	}
}

const (
	commentTarget = `{"ref":"example-org/example-repo#12"}`
	commentID     = "3000000001"
	theComment    = "/repos/example-org/example-repo/issues/comments/" + commentID
	theIssue      = "/repos/example-org/example-repo/issues/12"
)

func sentComment(ref, target string) connectorwire.SentMessage {
	return connectorwire.SentMessage{Action: "github_comment", Ref: ref, Target: json.RawMessage(target)}
}

// shortTimeouts makes a recall give up after d for the whole and for each lookup, as long as the test runs.
func shortTimeouts(t *testing.T, d time.Duration) {
	t.Helper()
	was, wasProbe := recallTimeout, recallProbeTimeout
	recallTimeout, recallProbeTimeout = d, d
	t.Cleanup(func() { recallTimeout, recallProbeTimeout = was, wasProbe })
}

// A comment is taken back by deleting it, by the id the write gave, in the repository its row says, with the token and
// nothing else asked of GitHub.
func TestRecallDeletesTheComment(t *testing.T) {
	c, s := newCommentsServer(t, nil)
	if err := c.Recall(context.Background(), sentComment(commentID, commentTarget)); err != nil {
		t.Fatal(err)
	}
	if got := s.requests(); len(got) != 1 || got[0] != "DELETE "+theComment || s.auth[0] != "Bearer "+oldToken {
		t.Fatalf("requests %v, authorization %v", got, s.auth)
	}
}

// What the write answered is what a recall goes by: the ref DoRef gave and the target the ledger keeps for that write.
func TestRecallTakesBackWhatWasWritten(t *testing.T) {
	c, s := newCommentsServer(t, nil)
	ctx := context.Background()
	done, err := connectorwire.Write(ctx, c, "github_comment", json.RawMessage(`{"repo":"example-org/example-repo","number":12,"body":"ok"}`))
	if err != nil || done.MessageRef != commentID {
		t.Fatalf("%+v %v", done, err)
	}
	if err := c.Recall(ctx, sentComment(done.MessageRef, commentTarget)); err != nil {
		t.Fatal(err)
	}
	if got := s.requests(); len(got) != 2 || got[0] != "POST /repos/example-org/example-repo/issues/12/comments" || got[1] != "DELETE "+theComment {
		t.Fatalf("requests %v", got)
	}
}

// Only a comment can be taken back, at any age: GitHub deletes a review only before it is submitted, so one that was
// submitted is left, and asking for it sends nothing.
func TestOnlyAComment(t *testing.T) {
	c, s := newCommentsServer(t, nil)
	for _, age := range []time.Duration{0, time.Minute, 24 * time.Hour, 5000 * time.Hour} {
		if !c.CanRecall("github_comment", age) {
			t.Errorf("a comment %v old cannot be taken back", age)
		}
	}
	for _, action := range []string{"github_review", "sent.record", "mail_send", ""} {
		if c.CanRecall(action, time.Minute) {
			t.Errorf("%q can be taken back", action)
		}
		m := sentComment("80123", commentTarget)
		m.Action = action
		if err := c.Recall(context.Background(), m); !errors.Is(err, connectorwire.ErrCannotRecall) {
			t.Errorf("%q: %v", action, err)
		}
	}
	if got := s.requests(); len(got) != 0 {
		t.Fatalf("sent %v", got)
	}
}

// The ref and the target come from the ledger and go into the request's path, so only what GitHub gives (a whole
// number, as written; owner/name#number) is used: nothing else is sent, and it says it could not have been, not
// "unknown": nothing was tried.
func TestRecallSendsNothingItCannotTrust(t *testing.T) {
	c, s := newCommentsServer(t, nil)
	for _, tc := range []struct{ name, ref, target string }{
		{"no ref", "", commentTarget},
		{"zero", "0", commentTarget},
		{"negative", "-5", commentTarget},
		{"letters", "abc", commentTarget},
		{"padded", " 12", commentTarget},
		{"signed", "+7", commentTarget},
		{"leading zero", "07", commentTarget},
		{"a path", "1/../../x", commentTarget},
		{"a query", "12?x=1", commentTarget},
		{"past int64", "9223372036854775808", commentTarget},
		{"an exponent", "1e3", commentTarget},
		{"no target", commentID, `{}`},
		{"a null target", commentID, `null`},
		{"a target that is not JSON", commentID, `not json`},
		{"a repository that is a path", commentID, `{"ref":"../x#12"}`},
		{"a repository that is deeper", commentID, `{"ref":"a/b/c#12"}`},
		{"a dot name", commentID, `{"ref":"example-org/..#12"}`},
		{"no issue", commentID, `{"ref":"example-org/example-repo"}`},
		{"issue zero", commentID, `{"ref":"example-org/example-repo#0"}`},
		{"issue past an int", commentID, `{"ref":"example-org/example-repo#99999999999999999999"}`},
		{"a ref that is a number", commentID, `{"ref":12}`},
	} {
		if err := c.Recall(context.Background(), sentComment(tc.ref, tc.target)); !errors.Is(err, connectorwire.ErrCannotRecall) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	if got := s.requests(); len(got) != 0 {
		t.Fatalf("sent %v", got)
	}
}

// How GitHub's answer reads: only the 204 is a success. A server error or no answer at all may have deleted the comment
// (unknown); a refusal has changed nothing, and a token that is refused or cannot be had says the connector is not
// available. A 404 is looked into: the comment can still be read (so it is there, and the token may not delete it)
// is a refusal; it cannot be read and the issue can, or it was deleted, is a comment that is gone, which is what was
// asked for; anything else leaves nothing known, and is not "unknown" either, as the 404 deleted nothing. Whatever
// GitHub says, the token is not in the words.
func TestRecallReadsGitHubsAnswer(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	for _, tc := range []struct {
		name    string
		answer  func(http.ResponseWriter, *http.Request)
		comment int // the status of the comment's own lookup after a 404
		issue   int // the status of the issue's lookup
		timeout time.Duration
		err     string // "" is success; "unknown" wraps ErrRecallUnknown; "unavailable" ErrRecallUnavailable; else a plain error containing it
	}{
		{"204", answering(204, ""), 0, 0, 0, ""},
		{"200, which is not what a delete answers", answering(200, ""), 0, 0, 0, "unknown"},
		{"202", answering(202, ""), 0, 0, 0, "unknown"},
		{"304", answering(304, ""), 0, 0, 0, "unknown"},
		{"500", answering(500, "Server Error "+oldToken), 0, 0, 0, "unknown"},
		{"502", answering(502, ""), 0, 0, 0, "unknown"},
		{"503", answering(503, ""), 0, 0, 0, "unknown"},
		{"403", answering(403, "Resource not accessible "+oldToken), 0, 0, 0, "403"},
		{"401", answering(401, "Bad credentials "+oldToken), 0, 0, 0, "unavailable"},
		{"422", answering(422, "Validation Failed"), 0, 0, 0, "422"},
		{"404, the comment is not found and the issue can be read", answering(404, "Not Found"), 0, 0, 0, ""},
		{"404, the comment is not found and the issue was deleted", answering(404, "Not Found"), 0, 410, 0, ""},
		{"404, but the comment can be read: the token may not delete it", answering(404, "Not Found"), 200, 0, 0, "still there"},
		{"404, the comment and the issue are not found", answering(404, "Not Found"), 0, 404, 0, "cannot be read"},
		{"404, the comment is not found and the issue cannot be read now", answering(404, "Not Found"), 0, 500, 0, "cannot be read"},
		{"404, and the comment cannot be looked up now", answering(404, "Not Found"), 500, 0, 0, "could not be looked up"},
		{"a connection that breaks", func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }, 0, 0, 0, "unknown"},
		{"no answer in time", func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-hang:
			case <-r.Context().Done():
			}
		}, 0, 0, 100 * time.Millisecond, "unknown"},
	} {
		c, s := newCommentsServer(t, nil)
		s.answer, s.comment, s.issue = tc.answer, tc.comment, tc.issue
		ctx := context.Background()
		if tc.timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, tc.timeout)
			defer cancel()
		}
		err := c.Recall(ctx, sentComment(commentID, commentTarget))
		switch {
		case tc.err == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.err == "unknown" && !errors.Is(err, connectorwire.ErrRecallUnknown):
			t.Errorf("%s: want unknown, got %v", tc.name, err)
		case tc.err == "unavailable" && (!errors.Is(err, connectorwire.ErrRecallUnavailable) || errors.Is(err, connectorwire.ErrRecallUnknown)):
			t.Errorf("%s: want unavailable, got %v", tc.name, err)
		case tc.err != "" && tc.err != "unknown" && tc.err != "unavailable" && (err == nil || !strings.Contains(err.Error(), tc.err) ||
			errors.Is(err, connectorwire.ErrRecallUnknown) || errors.Is(err, connectorwire.ErrRecallUnavailable) || errors.Is(err, connectorwire.ErrCannotRecall)):
			t.Errorf("%s: want a plain error with %q, got %v", tc.name, tc.err, err)
		}
		if err != nil && strings.Contains(err.Error(), oldToken) {
			t.Errorf("%s: the token is in %q", tc.name, err)
		}
	}
}

// A request that GitHub never answers ends within the connector's own time, with no help from the caller's ctx: the
// delete as an outcome nobody can tell, a lookup after a 404 as a plain error (the 404 deleted nothing).
func TestRecallEndsWhenGitHubNeverAnswers(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	never := func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}
	shortTimeouts(t, 150*time.Millisecond)
	for _, tc := range []struct {
		name    string
		answer  func(http.ResponseWriter, *http.Request)
		lookups func(http.ResponseWriter, *http.Request)
		unknown bool
	}{
		{"the delete", never, nil, true},
		{"the comment's lookup", answering(404, ""), never, false},
		{"the issue's lookup", answering(404, ""), func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == theComment {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			never(w, r)
		}, false},
	} {
		c, s := newCommentsServer(t, nil)
		s.answer, s.lookups = tc.answer, tc.lookups
		start := time.Now()
		err := c.Recall(context.Background(), sentComment(commentID, commentTarget))
		if took := time.Since(start); took > 3*time.Second {
			t.Errorf("%s: took %v", tc.name, took)
		}
		switch {
		case err == nil:
			t.Errorf("%s: a request nobody answered is a success", tc.name)
		case errors.Is(err, connectorwire.ErrRecallUnknown) != tc.unknown:
			t.Errorf("%s: unknown is %v: %v", tc.name, !tc.unknown, err)
		}
	}
}

// A comment that is already gone is a comment taken back, so a recall asked again after its answer was lost succeeds,
// and the second one has only looked.
func TestRecallRepeatedSucceeds(t *testing.T) {
	c, s := newCommentsServer(t, nil)
	calls := 0
	s.answer = func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}
	for range 2 {
		if err := c.Recall(context.Background(), sentComment(commentID, commentTarget)); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"DELETE " + theComment, "DELETE " + theComment, "GET " + theComment, "GET " + theIssue}
	if got := s.requests(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests %v", got)
	}
}

// A repository that moved answers the delete with a 301, and the client sends the request on as a GET, which gets a 200
// and deletes nothing: that is not a comment taken back.
func TestRecallIsNotFooledByARedirect(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Method == http.MethodDelete {
			http.Redirect(w, r, "/repositories/77/issues/comments/"+commentID, http.StatusMovedPermanently)
			return
		}
		_, _ = w.Write([]byte(`{"id":` + commentID + `}`))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", (&machine{tokens: []string{oldToken}}).fetch)
	c.api = srv.URL
	if err := c.Recall(context.Background(), sentComment(commentID, commentTarget)); !errors.Is(err, connectorwire.ErrRecallUnknown) {
		t.Fatalf("a 301 followed as a GET is a success: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := "DELETE " + theComment + "\nGET /repositories/77/issues/comments/" + commentID; strings.Join(seen, "\n") != want {
		t.Fatalf("requests %v", seen)
	}
}

// A token that cannot be had stops it before any request goes out: the connector is not available, and nothing was sent.
func TestRecallWithoutAToken(t *testing.T) {
	c, s := newCommentsServer(t, &machine{err: ErrOffline})
	err := c.Recall(context.Background(), sentComment(commentID, commentTarget))
	if !errors.Is(err, connectorwire.ErrRecallUnavailable) || !errors.Is(err, ErrOffline) || errors.Is(err, connectorwire.ErrRecallUnknown) {
		t.Fatalf("%v", err)
	}
	if got := s.requests(); len(got) != 0 {
		t.Fatalf("sent %v", got)
	}
}

// APIRoot is for tests that build the connector with a fake GitHub: it is not a setting, so nothing in hub.json or
// manager.json can send the connector's token to another address.
func TestAPIRootIsNotASetting(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"auth":{"from_machine":"mac"},"APIRoot":"http://other.example","api_root":"http://other.example"}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.APIRoot != "" || cfg.Auth.FromMachine != "mac" {
		t.Fatalf("%+v", cfg)
	}
	if c := New(Config{APIRoot: "http://fake.example"}, "Mac", nil); c.api != "http://fake.example" {
		t.Fatalf("api %q", c.api)
	}
	if c := New(Config{}, "Mac", nil); c.api != "https://api.github.com" {
		t.Fatalf("api %q", c.api)
	}
}
