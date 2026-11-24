package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
)

// The rule a ledger row gives (connector.ChatRuleFor) is matched against the arguments this connector's gate really
// sees: with the real connector behind a Registry, a comment on the issue the row named is posted without a card, and
// another issue, another repository, a review and the thread page's argument form still ask. Kept as JSON and read
// back first, as a store will.
func TestChatRuleAllowsOnlyThatIssue(t *testing.T) {
	var mu sync.Mutex
	var posted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/user":
			_, _ = w.Write([]byte(`{"login":"alice"}`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"title":"Some issue"}`))
		case r.Method == http.MethodPost:
			_, _ = io.Copy(io.Discard, r.Body)
			mu.Lock()
			posted = append(posted, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":501,"html_url":"https://github.com/example-org/example-repo/issues/12#issuecomment-501"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", (&machine{tokens: []string{oldToken}}).fetch)
	c.api = srv.URL
	c.check(context.Background())
	posts := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := posted
		posted = nil
		return out
	}

	// What the ledger keeps of a comment the assistant made (extmgr's githubTarget), and the rule it gives.
	rule, err := connector.ChatRuleFor("github", "github_comment", json.RawMessage(`{"ref":"example-org/example-repo#12"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]connector.UserRule{rule})
	var kept []connector.UserRule
	if err := json.Unmarshal(raw, &kept); err != nil {
		t.Fatal(err)
	}

	r := connector.NewRegistry(nil)
	if err := r.Add(c); err != nil {
		t.Fatal(err)
	}
	asked := 0
	ask := func(context.Context, string, json.RawMessage) bool { asked++; return false } // every card is declined
	call := func(tool, args string) bool {
		asked = 0
		return !r.Call(context.Background(), tool, json.RawMessage(args), ask).IsError
	}
	issue12 := `{"repo":"example-org/example-repo","number":12,"body":"ok"}`

	// Before the rule: a card, declined, nothing posted.
	if ok := call("github_comment", issue12); ok || asked != 1 || len(posts()) != 0 {
		t.Fatalf("before the rule: ran %v after %d cards", ok, asked)
	}
	if err := r.SetUserRules(kept); err != nil {
		t.Fatal(err)
	}
	if ok := call("github_comment", issue12); !ok || asked != 0 || len(posts()) != 1 {
		t.Fatalf("the issue of the rule: ran %v after %d cards", ok, asked)
	}
	for _, other := range []struct{ name, tool, args string }{
		{"another issue", "github_comment", `{"repo":"example-org/example-repo","number":13,"body":"ok"}`},
		{"another repository", "github_comment", `{"repo":"example-org/other","number":12,"body":"ok"}`},
		{"a review on the issue", "github_review", `{"repo":"example-org/example-repo","number":12,"event":"APPROVE"}`},
		{"the thread page's form", "github_comment", `{"chat":"example-org/example-repo#12","text":"ok"}`},
	} {
		if ok := call(other.tool, other.args); ok || asked != 1 || len(posts()) != 0 {
			t.Errorf("%s: ran %v after %d cards (the card was declined)", other.name, ok, asked)
		}
	}

	// Dropped, it asks again.
	_ = r.SetUserRules(nil)
	if ok := call("github_comment", issue12); ok || asked != 1 || len(posts()) != 0 {
		t.Errorf("after dropping the rule: ran %v after %d cards", ok, asked)
	}
}
