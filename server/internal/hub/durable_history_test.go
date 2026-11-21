package hub

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The hub is only transport: keep the manager's partial-coverage and source
// reference evidence on both HTTP pages and subscription catch-up.
func TestHubRelaysDurableHistoryCompleteness(t *testing.T) {
	for _, partial := range []bool{false, true} {
		m := wireNode(t, func(json.RawMessage) any {
			return map[string]any{
				"history": "h1", "from_seq": 10, "to_seq": 20, "partial": partial, "has_more": true,
				"items": []proto.Item{{ID: "tool-1", Seq: 11, Role: "user", Kind: proto.KindToolResult, Text: "bounded preview", FullSize: 100000,
					Stored: &proto.StoredItem{Complete: false, SourceBytes: 100000, MachineID: "m1", SessionID: "s1", History: "h1", ItemID: "tool-1"}}},
			}
		})
		r := httptest.NewRequest("GET", "/api/v1/sessions/m1/s1/items?lazy_images=1", nil)
		r.SetPathValue("sid", "s1")
		w := httptest.NewRecorder()
		(&Hub{}).sessionItems(w, r, m)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		check := func(raw []byte) {
			t.Helper()
			var page struct {
				Partial bool         `json:"partial"`
				Items   []proto.Item `json:"items"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				t.Fatal(err)
			}
			if page.Partial != partial || len(page.Items) != 1 || page.Items[0].Stored == nil || page.Items[0].Stored.ItemID != "tool-1" || page.Items[0].Stored.Complete {
				t.Fatalf("lost completeness evidence: %s", raw)
			}
		}
		check(w.Body.Bytes())
		c := &client{out: make(chan []byte, 8), subs: map[[2]string]bool{}}
		(&Hub{}).sendBacklog(context.Background(), c, m, "s1", 10)
		select {
		case raw := <-c.out:
			check(raw)
		default:
			t.Fatal("no subscription catch-up")
		}
	}
}

func TestDurableHistoryRoutesRequireFullCredentials(t *testing.T) {
	patterns := routePatterns(t)
	for _, method := range []string{"GET", "PUT"} {
		pattern := method + " /api/v1/assistant/history"
		if !slices.Contains(patterns, pattern) {
			t.Fatalf("missing history route: %s", pattern)
		}
		if scope := extwire.RouteScopes[pattern]; scope != "" {
			t.Fatalf("history operations must require full credentials: %s has scope %s", pattern, scope)
		}
	}
}
