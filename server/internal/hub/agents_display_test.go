package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestAgentsDisplayLookupUsesDefaultManager(t *testing.T) {
	h, srv, _ := newBusHub(t)
	helloAssistant(t, h, srv, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var req assistantwire.HTTPRequest
		if method != assistantwire.MethodHTTP || json.Unmarshal(params, &req) != nil || req.Method != http.MethodGet || req.Path != "/api/v1/agents/display-settings" {
			return nil, fmt.Errorf("unexpected request %s %s", method, params)
		}
		return assistantwire.HTTPResponse{Status: http.StatusOK, Body: []byte(`{"use_herdr_tab_name":true}`)}, nil
	})
	ctx := context.Background()
	p := assistantwire.AppCall{AppID: assistantwire.AppSettings, Method: assistantwire.AgentsDisplayGet}
	if raw, handled, err := h.assistantAppCall(ctx, assistantwire.AssistantID, p); err != nil || !handled || string(raw) != `{}` {
		t.Fatalf("owner should read its own setting: %s %v %v", raw, handled, err)
	}
	if raw, handled, err := h.assistantAppCall(ctx, "m-0d0d0d", p); err != nil || !handled || string(raw) != `{"use_herdr_tab_name":true}` {
		t.Fatalf("another manager should read the owner's setting: %s %v %v", raw, handled, err)
	}
}
