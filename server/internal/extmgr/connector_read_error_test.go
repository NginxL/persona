package extmgr

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"

	"github.com/nwverse/persona/server/internal/rpc"
)

func TestConversationReadErrorKeepsItsCategory(t *testing.T) {
	for _, tc := range []struct {
		code         string
		status       int
		responseCode string
	}{{"rate_limited", 429, "rate_limited"}, {"no_permission", 403, "no_permission"}, {"not_signed_in", 409, "unavailable"}} {
		response := httptest.NewRecorder()
		(&Manager{}).threadFailed(response, context.Background(), nil, &rpc.Error{Code: tc.code, Message: "Example read failed"})
		if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.responseCode) {
			t.Fatalf("%s = %d %s", tc.code, response.Code, response.Body.String())
		}
	}
}

type noThreadPage struct{}

func (noThreadPage) ID() string                   { return "exampleplain" }
func (noThreadPage) Name() string                 { return "Example" }
func (noThreadPage) Manifest() connector.Manifest { return connector.Manifest{} }

func TestConversationCapabilityHasStableErrorCode(t *testing.T) {
	registry := connector.NewRegistry(nil)
	if err := registry.Add(noThreadPage{}); err != nil {
		t.Fatal(err)
	}
	h := &Manager{connectors: registry}
	request := httptest.NewRequest("GET", "/api/v1/connectors/exampleplain/thread", nil)
	request.SetPathValue("id", "exampleplain")
	response := httptest.NewRecorder()
	_, _, ok := h.threadConnector(response, request)
	if ok || response.Code != 409 || !strings.Contains(response.Body.String(), "conversations_unsupported") {
		t.Fatalf("capability response = %d %s", response.Code, response.Body.String())
	}
}
