package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/githubwire"
	"github.com/nwverse/persona/server/internal/subscriptions"
)

// stepClock is the subscriptions' time in a test: it moves only when the runner waits or the test says.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *stepClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.advance(d)
	return nil
}
func (c *stepClock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// The built-in GitHub connector's search, subscribed to through the manager's routes (#3976): its catalog offers
// it, a dry run finds its list with an id on every item, and runs bring each new pull request once, as a row of its
// own in the Inbox. The models still read the text.
func TestGitHubSearchSubscribesItemByItem(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Second)
	var mu sync.Mutex
	merged := 2
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/issues" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		n := merged
		mu.Unlock()
		var items []string
		for i := n; i >= 1; i-- {
			at := start.Add(-time.Duration(n-i) * time.Hour).Format(time.RFC3339)
			items = append(items, fmt.Sprintf(`{"number":%d,"title":"Example change %d","state":"closed","user":{"login":"alice"},
				"body":"Made-up description %d.","html_url":"https://github.com/example-org/example-repo/pull/%d",
				"repository_url":"https://api.github.com/repos/example-org/example-repo","created_at":%q,"updated_at":%q,
				"closed_at":%q,"comments":0,"pull_request":{"merged_at":%q}}`, 100+i, i, i, 100+i, at, at, at, at))
		}
		fmt.Fprintf(w, `{"total_count":%d,"items":[%s]}`, n, strings.Join(items, ","))
	}))
	t.Cleanup(srv.Close)

	h := newTestManager(t)
	h.connectors = h.newConnectors(nil)
	gh := newTestGitHub(t, githubwire.Config{Auth: githubwire.AuthConfig{FromMachine: "mac"}, APIRoot: srv.URL}, "Mac", func(context.Context, string) (string, error) {
		return "gho_example000000000000000000000000000001", nil
	})
	if err := h.addConnector(signedInGitHub{gh.(fullGitHub)}); err != nil {
		t.Fatal(err)
	}
	clock := &stepClock{t: start}
	opts := h.subscriptionOptions(subscriptions.NewMemStore())
	opts.Clock = clock
	h.subs = subscriptions.New(opts)
	ctx := context.Background()
	mux := http.NewServeMux()
	h.subscriptionRoutes(mux)
	phone := principal{ID: "dev_phone", Role: roleFull}
	call := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		mux.ServeHTTP(rec, req.WithContext(withPrincipal(req.Context(), phone)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, out := call("GET", "/api/v1/subscriptions/catalog?connector=github", "")
	if code != http.StatusOK || !strings.Contains(string(mustJSON(out)), `"name":"search"`) {
		t.Fatalf("catalog: %d %v", code, out)
	}
	for _, src := range out["sources"].([]any) {
		for _, tool := range src.(map[string]any)["tools"].([]any) {
			if tl := tool.(map[string]any); tl["name"] == "search" && tl["subscribable"] != true {
				t.Fatalf("search is not subscribable: %v", tl)
			}
		}
	}

	recipe := `{"mode":"items","tool":"search","args":{"query":"repo:example-org/example-repo is:pr is:merged updated:>={{since_date}}"},
		"fields":{"id":"$.id","title":"$.title","sender":"$.author","time":"$.time","body":"$.body","link":"$.link"},
		"kind":"sub_update","group_by":"thread"}`
	code, dr := call("POST", "/api/v1/subscriptions/-/dry-run", `{"connector":"github","source":"github","recipe":`+recipe+`}`)
	if code != http.StatusOK || dr["ok"] != true || dr["items_path"] != "$.items" || dr["count"] != float64(2) || dr["weak_dedup"] != false {
		t.Fatalf("dry run: %d %v", code, dr)
	}
	code, sub := call("POST", "/api/v1/subscriptions", `{"connector":"github","source":"github","label":"Merged pull requests","recipe":`+recipe+`}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, sub)
	}
	id := sub["id"].(string)
	code, sub = call("PUT", "/api/v1/subscriptions/"+id, fmt.Sprintf(`{"revision":%v,"to_inbox":true}`, sub["revision"]))
	if code != http.StatusOK || sub["to_inbox"] != true {
		t.Fatalf("inbox on: %d %v", code, sub)
	}

	lastRun := func() map[string]any {
		t.Helper()
		code, got := call("GET", "/api/v1/subscriptions/"+id, "")
		runs, _ := got["runs"].([]any)
		if code != http.StatusOK || len(runs) == 0 {
			t.Fatalf("runs: %d %v", code, got)
		}
		return runs[0].(map[string]any)
	}
	rows := func() []string {
		t.Helper()
		srcs, err := h.subs.InboxSources(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, src := range srcs {
			for _, th := range src.Things {
				if src.ID == id && !strings.HasSuffix(th.Thread, ":failing") {
					out = append(out, th.Title)
				}
			}
		}
		return out
	}

	h.subs.RunDue(ctx)
	if run := lastRun(); run["new"] != float64(2) || run["count"] != float64(2) || run["error"] != nil {
		t.Fatalf("first run: %v", run)
	}
	if got := rows(); len(got) != 2 {
		t.Fatalf("Inbox rows after the first run: %q", got)
	}

	mu.Lock()
	merged = 3
	mu.Unlock()
	clock.advance(20 * time.Minute)
	h.subs.RunDue(ctx)
	if run := lastRun(); run["new"] != float64(1) || run["count"] != float64(3) || run["error"] != nil {
		t.Fatalf("second run: %v", run)
	}
	if got := strings.Join(rows(), " | "); strings.Count(got, "Example change") != 3 || !strings.Contains(got, "Example change 3") {
		t.Fatalf("Inbox rows after the second run: %s", got)
	}
	clock.advance(20 * time.Minute)
	h.subs.RunDue(ctx)
	if run := lastRun(); run["new"] != float64(0) || run["count"] != float64(3) {
		t.Fatalf("nothing new: %v", run)
	}

	// The models read the text, as before.
	if r := h.callConnector(ctx, asker{}, "github_search", json.RawMessage(`{"query":"repo:example-org/example-repo is:pr is:merged"}`)); r.IsError ||
		len(r.Content) == 0 || !strings.HasPrefix(r.Content[0].Text, "3 results\nexample-org/example-repo#103 [PR merged] Example change 3") {
		t.Fatalf("the models' answer: %+v", r)
	}
}
