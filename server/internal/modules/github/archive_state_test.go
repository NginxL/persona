package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// stateAPI is search and threads of example-org/example-repo on made-up data whose state a test changes; nothing but
// the description is ever said in them.
type stateAPI struct {
	mu     sync.Mutex
	items  map[int]string // number -> the thread's JSON
	search []int          // what search finds
	asked  []string
	refuse map[int]refusal // number -> how GitHub refuses the thread instead
}

// refusal is an error answer: its status, headers and message.
type refusal struct {
	code    int
	headers map[string]string
	message string
}

func (f *stateAPI) set(n int, state string, pr bool, updated time.Duration, closed, merged string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	extra := ""
	if closed != "" {
		extra += `,"closed_at":"` + closed + `"`
	}
	if pr {
		m := "null"
		if merged != "" {
			m = `"` + merged + `"`
		}
		extra += `,"pull_request":{"merged_at":` + m + `}`
	}
	f.items[n] = `{"number":` + jsonInt(n) + `,"title":"Thread ` + jsonInt(n) + `","state":"` + state + `","user":{"login":"alice"},"body":"示例",` +
		`"repository_url":"https://api.github.com/repos/example-org/example-repo","created_at":"` + fixtureAt(dayBefore) + `",` +
		`"updated_at":"` + fixtureAt(updated) + `"` + extra + `}`
}

func jsonInt(n int) string { b, _ := json.Marshal(n); return string(b) }

func newStateAPI(t *testing.T) (*stateAPI, *Connector) {
	f := &stateAPI{items: map[int]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" {
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.asked = append(f.asked, r.URL.Path)
		switch {
		case r.URL.Path == "/user":
			_, _ = w.Write([]byte(`{"login":"me"}`))
		case r.URL.Path == "/search/issues":
			var items []string
			for _, n := range f.search {
				items = append(items, f.items[n])
			}
			_, _ = w.Write([]byte(`{"total_count":` + jsonInt(len(items)) + `,"items":[` + strings.Join(items, ",") + `]}`))
		case strings.HasSuffix(r.URL.Path, "/comments") || strings.HasSuffix(r.URL.Path, "/reviews"):
			_, _ = w.Write([]byte(`[]`))
		case strings.HasPrefix(r.URL.Path, "/repos/example-org/example-repo/issues/"):
			var n int
			_ = json.Unmarshal([]byte(strings.TrimPrefix(r.URL.Path, "/repos/example-org/example-repo/issues/")), &n)
			if r, ok := f.refuse[n]; ok {
				for k, v := range r.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(r.code)
				_, _ = w.Write([]byte(`{"message":"` + r.message + `"}`))
				return
			}
			if b, ok := f.items[n]; ok {
				_, _ = w.Write([]byte(b))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", (&machine{tokens: []string{oldToken}}).fetch)
	c.api = srv.URL
	c.check(context.Background())
	return f, c
}

func stateLines(b connectorwire.ChatBatch) string {
	var out []string
	for _, m := range b.Messages {
		if m.Type == "SYSTEM" {
			out = append(out, m.ID+"|"+m.Text)
		}
	}
	return strings.Join(out, "\n")
}

// A merge, close or reopen adds no comment, yet the chat carries the new state and one line per change (#9427).
func TestChangesSendsStateChangesWithoutComments(t *testing.T) {
	f, c := newStateAPI(t)
	f.search = []int{5}
	f.set(5, "open", false, 9*time.Hour, "", "")
	batches, next, _, err := c.Changes(context.Background(), "", 20)
	if err != nil || len(batches) != 1 || batches[0].Chat.State != "open" || stateLines(batches[0]) != "" {
		t.Fatalf("open: %+v, %v", batches, err)
	}

	closed := fixtureAt(10 * time.Hour)
	f.set(5, "closed", false, 10*time.Hour, closed, "")
	batches, next, _, err = c.Changes(context.Background(), next, 20)
	closedAt, _ := time.Parse(time.RFC3339, closed)
	if err != nil || len(batches) != 1 {
		t.Fatalf("closed: %d batches, %v", len(batches), err)
	}
	b := batches[0]
	if b.Chat.State != "closed" || !b.Chat.StateAt.Equal(closedAt) || len(b.Messages) != 1 ||
		stateLines(b) != "state-closed-"+jsonInt(int(closedAt.Unix()))+"|（状态）已关闭" || !b.Messages[0].At.Equal(closedAt) {
		t.Fatalf("closed batch %+v", b)
	}

	// Read again with nothing new: nothing is sent twice.
	batches, next, _, err = c.Changes(context.Background(), next, 20)
	if err != nil || len(batches) != 0 {
		t.Fatalf("again: %+v, %v", batches, err)
	}

	f.set(5, "open", false, 11*time.Hour, "", "")
	batches, next, _, err = c.Changes(context.Background(), next, 20)
	reopened, _ := time.Parse(time.RFC3339, fixtureAt(11*time.Hour))
	if err != nil || len(batches) != 1 || batches[0].Chat.State != "open" ||
		stateLines(batches[0]) != "state-open-"+jsonInt(int(reopened.Unix()))+"|（状态）重新打开" {
		t.Fatalf("reopened: %+v, %v", batches, err)
	}
	var cur archiveCursor
	_ = json.Unmarshal([]byte(next), &cur)
	if len(cur.States) != 0 || !cur.Rechecked {
		t.Fatalf("an open thread keeps no state in the cursor: %s", next)
	}

	// A pull request merged: merged, not closed, at its merge time.
	f.search = []int{6}
	merged := fixtureAt(12 * time.Hour)
	f.set(6, "closed", true, 12*time.Hour, merged, merged)
	batches, next, _, err = c.Changes(context.Background(), next, 20)
	if err != nil || len(batches) != 1 || batches[0].Chat.State != "merged" || batches[0].Chat.Kind != "pr" ||
		!strings.HasSuffix(stateLines(batches[0]), "|（状态）已合并") {
		t.Fatalf("merged: %+v, %v", batches, err)
	}
	var last archiveCursor
	_ = json.Unmarshal([]byte(next), &last)
	if last.States["example-org/example-repo#6"] != "merged" {
		t.Fatalf("cursor states %v", last.States)
	}
}

// A cursor from before states were sent has every thread it knows read once, so a pull request merged then is recorded
// too; a thread gone or moved leaves the queue, and the limit holds.
func TestChangesRechecksThreadsOfAnOlderCursor(t *testing.T) {
	f, c := newStateAPI(t)
	merged := fixtureAt(9 * time.Hour)
	f.set(7, "closed", true, 9*time.Hour, merged, merged)
	f.set(8, "open", true, 9*time.Hour, "", "")
	f.set(12, "open", false, 9*time.Hour, "", "")
	f.items[11] = f.items[12] // #11 was moved: GitHub answers with another thread
	old := `{"since":"` + fixtureAt(10*time.Hour) + `","threads":{`
	for i, n := range []string{"7", "8", "9", "11"} {
		if i > 0 {
			old += ","
		}
		old += `"example-org/example-repo#` + n + `":"` + merged + `"`
	}
	old += `}}`

	// The queue is in name order: #11 (moved), #7, then #8, #9 (gone).
	batches, next, more, err := c.Changes(context.Background(), old, 2)
	if err != nil || !more || len(batches) != 1 {
		t.Fatalf("first call: %d batches more=%v %v", len(batches), more, err)
	}
	if b := batches[0]; b.Chat.ID != "example-org-example-repo-7" || b.Chat.State != "merged" || !strings.HasSuffix(stateLines(b), "|（状态）已合并") {
		t.Fatalf("merged thread %+v", b)
	}
	var cur archiveCursor
	_ = json.Unmarshal([]byte(next), &cur)
	if !cur.Rechecked || strings.Join(cur.Recheck, ",") != "example-org/example-repo#8,example-org/example-repo#9" || cur.States["example-org/example-repo#7"] != "merged" {
		t.Fatalf("cursor after the first call %s", next)
	}

	batches, next, _, err = c.Changes(context.Background(), next, 2)
	if err != nil || len(batches) != 1 {
		t.Fatalf("second call: %+v, %v", batches, err)
	}
	if b := batches[0]; b.Chat.ID != "example-org-example-repo-8" || b.Chat.State != "open" || len(b.Messages) != 0 {
		t.Fatalf("open thread %+v", b)
	}
	var after archiveCursor
	_ = json.Unmarshal([]byte(next), &after)
	if len(after.Recheck) != 0 || len(after.States) != 1 {
		t.Fatalf("cursor after the queue %s", next)
	}
	f.mu.Lock()
	n := len(f.asked)
	f.mu.Unlock()
	if _, _, _, err := c.Changes(context.Background(), next, 2); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.asked[n:] {
		if strings.HasPrefix(p, "/repos/example-org/example-repo/issues/") && !strings.HasSuffix(p, "/comments") {
			t.Fatalf("read a thread again after the recheck: %s", p)
		}
	}
}

// A rate limit while a thread's state is read once keeps it first in the queue, and the call stops without asking for
// more; once the limit lifts the read is made and the state recorded (#9427 review). Only a refusal for good drops it.
func TestChangesKeepsARecheckThroughARateLimit(t *testing.T) {
	for name, r := range map[string]refusal{
		"429":                   {code: http.StatusTooManyRequests, message: "Too Many Requests"},
		"secondary limit":       {code: http.StatusForbidden, headers: map[string]string{"Retry-After": "60"}, message: "You have exceeded a secondary rate limit"},
		"core quota":            {code: http.StatusForbidden, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Limit": "5000", "X-RateLimit-Resource": "core"}, message: "API rate limit exceeded"},
		"other quota used up":   {code: http.StatusForbidden, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Limit": "100", "X-RateLimit-Resource": "integration_manifest"}, message: "Forbidden"},
		"older abuse detection": {code: http.StatusForbidden, message: "You have triggered an abuse detection mechanism"},
		"sign-in lapsed":        {code: http.StatusUnauthorized, message: "Bad credentials"},
	} {
		t.Run(name, func(t *testing.T) {
			f, c := newStateAPI(t)
			merged := fixtureAt(9 * time.Hour)
			f.set(7, "closed", true, 9*time.Hour, merged, merged)
			f.set(8, "open", true, 9*time.Hour, "", "")
			f.refuse = map[int]refusal{7: r}
			old := `{"since":"` + fixtureAt(10*time.Hour) + `","threads":{"example-org/example-repo#7":"` + merged + `","example-org/example-repo#8":"` + merged + `"}}`

			batches, next, more, _ := c.Changes(context.Background(), old, 5)
			var cur archiveCursor
			_ = json.Unmarshal([]byte(next), &cur)
			if more || len(batches) != 0 || len(cur.Recheck) == 0 || cur.Recheck[0] != "example-org/example-repo#7" {
				t.Fatalf("refused: %d batches more=%v cursor %s", len(batches), more, next)
			}

			f.mu.Lock()
			f.refuse = nil
			f.mu.Unlock()
			c.mu.Lock()
			c.pausedUntil, c.backoffUntil, c.pauseStep, c.rateLow = time.Time{}, time.Time{}, 0, false
			c.mu.Unlock()
			batches, next, _, err := c.Changes(context.Background(), next, 5)
			if err != nil || len(batches) == 0 || batches[0].Chat.ID != "example-org-example-repo-7" || batches[0].Chat.State != "merged" {
				t.Fatalf("after the limit: %+v, %v", batches, err)
			}
			var after archiveCursor
			_ = json.Unmarshal([]byte(next), &after)
			if len(after.Recheck) != 0 || after.States["example-org/example-repo#7"] != "merged" {
				t.Fatalf("cursor after the limit %s", next)
			}
		})
	}
}
