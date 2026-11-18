package hub

import (
	"context"
	"testing"
)

// A literal JSON null body is refused with 400; any JSON object, empty or not, gets through.
func TestProxyBodyRefusesOnlyANullBody(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		body    string
		want400 bool
	}{
		{"null/proxyBody", "/api/v1/sessions/box/s1/attach", "null", true},
		{"null/sendMessage", sendPath, "null", true},
		{"null/renameSession", "/api/v1/sessions/box/s1/rename", "null", true},
		{"null/interruptSession", "/api/v1/sessions/box/s1/interrupt", "null", true},
		{"null/sessionModes", "/api/v1/sessions/box/s1/modes", "null", true},
		{"emptyObject", "/api/v1/sessions/box/s1/attach", "{}", false},
		{"normalObject", "/api/v1/sessions/box/s1/attach", `{"foo":"bar"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			h.store = newSentStore()
			pipeNode(t, h, "box", func(context.Context) error { return nil })
			mux := h.routes()

			code, out := call(t, mux, "POST", tc.path, h.cfg.Token, tc.body)
			if tc.want400 && code != 400 {
				t.Fatalf("body %s: got %d, want 400; body: %v", tc.body, code, out)
			}
			if !tc.want400 && code == 400 {
				t.Fatalf("body %s should not be rejected, got %d", tc.body, code)
			}
		})
	}
}
