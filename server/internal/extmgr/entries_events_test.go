package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
)

func TestEntriesWrongEventRequiresGrantedRead(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			h, _ := extensionTestHub(t)
			mux := h.routes()
			dir := uiPackage(t)
			path := filepath.Join(dir, extension.ManifestFile)
			raw, _ := os.ReadFile(path)
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			scopes := []string{}
			if allowed {
				scopes = append(scopes, extension.ScopeAssistantEntriesRead)
			}
			m["permissions"] = map[string]any{"hub": scopes, "launcher_keys": true}
			m["contributes"].(map[string]any)["events"] = []any{map[string]any{"id": "entries", "subscribe": []string{assistantwire.EventAssistantEntriesChanged, "agent.finished"}}}
			raw, _ = json.Marshal(m)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			code, pv := call(t, mux, "POST", "/api/v1/extensions/-/preview", h.cfg.Token, `{"source":"dir:`+dir+`"}`)
			if code != 200 {
				t.Fatalf("preview %d %v", code, pv)
			}
			code, out := call(t, mux, "POST", "/api/v1/extensions/-/install", h.cfg.Token, fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, pv["preview_id"], pv["permissions_digest"]))
			if code != 200 {
				t.Fatalf("install %d %v", code, out)
			}
			waitLong(t, "entries subscriber ready", func() bool { p := h.extensions.get("example-ui"); return p != nil && p.proc != nil && p.proc.Ready() })
			for _, bad := range []string{`{}`, `{"id":"e","record_id":"r","status":"active"}`} {
				h.AssistantSaid(assistantwire.EventAssistantEntriesChanged, json.RawMessage(bad))
			}
			if !h.AssistantSaid(assistantwire.EventAssistantEntriesChanged, json.RawMessage(`{"id":"entry-example","record_id":"entry-correction","status":"wrong","personal":true,"quote":"must not be forwarded"}`)) {
				t.Fatal("event unhandled")
			}
			// Both notices use the same queue; this sentinel proves the preceding one was processed.
			h.extensions.queueEvent("agent.finished", map[string]any{})
			want := "value:1"
			if allowed {
				want = "value:2"
			}
			waitLong(t, "entries event processed", func() bool {
				_, out := call(t, mux, "POST", "/api/v1/extensions/example-ui/-/ui/home", h.cfg.Token, `{}`)
				return strings.Contains(fmt.Sprint(out["view"]), want)
			})
			if err := h.extensions.reg.SetEnabled(context.Background(), "example-ui", false, "test"); err != nil {
				t.Fatal(err)
			}
			if ids := h.extensions.reg.EventSubscribers(assistantwire.EventAssistantEntriesChanged, extension.ScopeAssistantEntriesRead); len(ids) != 0 {
				t.Fatal("disabled subscriber retained")
			}
		})
	}
}
