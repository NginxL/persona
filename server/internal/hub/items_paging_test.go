package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// The hub hands the node (and through it a runtime) only the paging the app gave: an empty or missing before_seq,
// limit or turns is left out rather than sent as 0, which a runtime may refuse as a bad value on every page.
func TestSessionItemsForwardsOnlyTheGivenPaging(t *testing.T) {
	for _, c := range []struct {
		query string
		want  map[string]any
	}{
		{"before_seq=&turns=2&lazy_images=1", map[string]any{"session_id": "s1", "turns": 2.0, "lazy_images": true}},
		{"", map[string]any{"session_id": "s1"}},
		{"user_inputs=1", map[string]any{"session_id": "s1", "user_inputs": true}},
		{"user_inputs=0", map[string]any{"session_id": "s1"}},
		{"before_seq=40&limit=50", map[string]any{"session_id": "s1", "before_seq": 40.0, "limit": 50.0}},
		{"before_seq=0", map[string]any{"session_id": "s1", "before_seq": 0.0}},
	} {
		var got map[string]any
		m := wireNode(t, func(p json.RawMessage) any {
			_ = json.Unmarshal(p, &got)
			if got["user_inputs"] == true {
				return map[string]any{"total": 0, "inputs": []any{}, "history": "fixture"}
			}
			return map[string]any{"items": []any{}, "has_more": false}
		})
		req := httptest.NewRequest("GET", "/api/v1/sessions/m1/s1/items?"+c.query, nil)
		req.SetPathValue("sid", "s1")
		w := httptest.NewRecorder()
		(&Hub{}).sessionItems(w, req, m)
		if w.Code != 200 {
			t.Fatalf("?%s: %d %s", c.query, w.Code, w.Body)
		}
		g, _ := json.Marshal(got)
		e, _ := json.Marshal(c.want)
		if string(g) != string(e) {
			t.Errorf("?%s: the node got %s, want %s", c.query, g, e)
		}
	}
}

// Index mode has a position-only wire shape, including when no inputs exist. A node that ignores the flag
// must not silently become a zero count: older nodes keep the client's loaded-only compatibility rail.
func TestSessionItemsUserInputsWireShape(t *testing.T) {
	for _, c := range []struct {
		name     string
		response any
		status   int
		want     string
	}{
		{"empty", map[string]any{"total": 0, "inputs": []any{}, "history": "fixture"}, 200,
			`{"total":0,"inputs":[],"history":"fixture"}`},
		{"positions only", map[string]any{
			"total": 1, "inputs": []any{map[string]any{"id": "input-1#0", "seq": 7, "text": "synthetic body"}},
			"history": "fixture", "items": []any{map[string]any{"text": "synthetic body"}},
		}, 200, `{"total":1,"inputs":[{"id":"input-1#0","seq":7}],"history":"fixture"}`},
		{"older node", map[string]any{"items": []any{}, "has_more": false}, 502, ""},
		{"partial index", map[string]any{"total": 2, "inputs": []any{map[string]any{"id": "input-1#0", "seq": 7}}, "history": "fixture"}, 502, ""},
		{"null index", map[string]any{"total": 0, "inputs": nil, "history": "fixture"}, 502, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := wireNode(t, func(p json.RawMessage) any { return c.response })
			req := httptest.NewRequest("GET", "/api/v1/sessions/m1/s1/items?user_inputs=1", nil)
			req.SetPathValue("sid", "s1")
			w := httptest.NewRecorder()
			(&Hub{}).sessionItems(w, req, m)
			if w.Code != c.status {
				t.Fatalf("got %d %s, want %d", w.Code, w.Body, c.status)
			}
			if c.status == 200 {
				var got any
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				body, _ := json.Marshal(got)
				var want any
				if err := json.Unmarshal([]byte(c.want), &want); err != nil {
					t.Fatal(err)
				}
				expected, _ := json.Marshal(want)
				if string(body) != string(expected) {
					t.Fatalf("got %s, want %s", body, expected)
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("the input index must not be cached")
				}
			}
		})
	}
}

// The private export can distinguish a strict local read from a cached reply or
// a terminal-only runtime's otherwise valid placeholder page.
func TestStrictExportTranscriptHubContract(t *testing.T) {
	for _, strict := range []bool{false, true} {
		for _, nodeAck := range []bool{false, true} {
			t.Run(fmt.Sprintf("strict=%t/node-ack=%t", strict, nodeAck), func(t *testing.T) {
				m := wireNodeRPC(t, func(_ context.Context, method string, raw json.RawMessage) (any, error) {
					var request map[string]any
					if err := json.Unmarshal(raw, &request); err != nil {
						return nil, err
					}
					if (request["strict_read"] == true) != strict || request["session_id"] != "s1" {
						t.Errorf("incorrect strict request: %s %v", method, request)
					}
					item := proto.Item{ID: "i#0", Seq: 1, Role: "assistant", Kind: "text", Text: "complete"}
					if method == "session.item" {
						if request["item_id"] != item.ID {
							t.Errorf("wrong full item id: %v", request)
						}
						return struct {
							proto.Item
							StrictRead      bool `json:"strict_read,omitempty"`
							SourceTextBytes int  `json:"source_text_bytes,omitempty"`
						}{item, nodeAck, len(item.Text)}, nil
					}
					return map[string]any{"items": []proto.Item{item}, "has_more": false,
						"history": "fixture", "from_seq": 0, "to_seq": 1, "strict_read": nodeAck}, nil
				})
				h := &Hub{}
				for _, target := range []struct {
					path string
					read func(http.ResponseWriter, *http.Request, *Machine)
				}{
					{"/items", h.sessionItems},
					{"/items/i%230/full", h.sessionItemFull},
				} {
					req := httptest.NewRequest("GET", "/api/v1/sessions/m1/s1"+target.path, nil)
					if strict {
						req.URL.RawQuery = "strict=1"
					}
					req.SetPathValue("sid", "s1")
					req.SetPathValue("iid", "i#0")
					w := httptest.NewRecorder()
					target.read(w, req, m)
					if strict && !nodeAck {
						if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "node_outdated") {
							t.Fatalf("unsupported read yielded %d: %s", w.Code, w.Body)
						}
						continue
					}
					if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "complete") ||
						(strict && !strings.Contains(w.Body.String(), `"strict_read":true`)) {
						t.Fatalf("unexpected wire response %d: %s", w.Code, w.Body)
					}
				}
			})
		}
	}
}
