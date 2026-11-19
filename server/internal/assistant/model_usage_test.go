package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/db"
)

// usageStore keeps the counted calls in memory, added up by purpose as db.DB does.
type usageStore struct {
	*downStore
	mu    sync.Mutex
	calls []db.ModelCall
}

func (u *usageStore) AddModelCall(_ context.Context, _ time.Time, c db.ModelCall) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls = append(u.calls, c)
	return nil
}

func (u *usageStore) ModelUsageByPurpose(context.Context, time.Time, int) (map[string]db.ModelUsage, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := map[string]db.ModelUsage{}
	for _, c := range u.calls {
		v := out[c.Purpose]
		v.Calls++
		if c.Failed {
			v.Failures++
		}
		v.InputTokens += c.InputTokens
		v.OutputTokens += c.OutputTokens
		v.CacheReadInputTokens += c.CacheReadInputTokens
		v.CacheCreationInputTokens += c.CacheCreationInputTokens
		out[c.Purpose] = v
	}
	return out, nil
}

// Each finished purpose call is counted, and the list gives a category its own calls and those of its unlisted uses.
func TestModelUsageCountedAndListedByPurpose(t *testing.T) {
	f := newFakeHub().withMachine()
	f.ownModels = true
	st := &usageStore{downStore: &downStore{memStore: &memStore{}}}
	s := newServiceOn(t, f, st)
	usage := ai.Usage{InputTokens: 14, OutputTokens: 7, CacheReadInputTokens: 95, CacheCreationInputTokens: 83}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "relay-model", "usage": usage,
			"content": []map[string]string{{"type": "text", "text": "ok"}}})
	}))
	defer relay.Close()
	f.hello.Models.AI = ai.Config{BaseURL: relay.URL, APIKey: "test-key", Model: "relay-model"}

	ctx := context.Background()
	for _, c := range []*ai.Client{s.purposeModelFor(purposeTriage, "inbox_group"), s.purposeModel("inbox.judge")} {
		if _, err := c.Complete(ctx, "", "x", 16); err != nil {
			t.Fatal(err)
		}
	}
	// a call its caller stopped is neither answered nor failed
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.purposeModel("inbox.judge").Complete(stopped, "", "x", 16); err == nil {
		t.Fatal("a stopped call answered")
	}
	if len(st.calls) != 2 || st.calls[0] != (db.ModelCall{Purpose: "inbox.triage", Route: "api", Provider: "legacy.triage",
		Model: "relay-model", InputTokens: 14, OutputTokens: 7, CacheReadInputTokens: 95, CacheCreationInputTokens: 83}) ||
		st.calls[1].Purpose != "inbox.judge" {
		t.Fatalf("counted: %+v", st.calls)
	}

	r := modelRequest(s, "GET", "", "")
	var body struct{ Purposes []ModelPurpose }
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatal(r.Body.String())
	}
	for _, p := range body.Purposes {
		switch {
		case p.ID == "inbox.triage":
			if p.Usage == nil || *p.Usage != (db.ModelUsage{Calls: 2, InputTokens: 28, OutputTokens: 14, CacheReadInputTokens: 190, CacheCreationInputTokens: 166}) {
				t.Fatalf("inbox.triage usage: %+v", p.Usage)
			}
		case p.Usage != nil:
			t.Fatalf("%s has usage without calls: %+v", p.ID, p.Usage)
		}
	}
}
