package extmgr

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extmgr/imessageport"
	"github.com/nwverse/persona/server/internal/imessagewire"
)

// needsIMessageModule is the iMessage module's factory, as the manager gets it (imessageport, filled by
// internal/modules/all, which modules_link_test.go links); the test is skipped in a build without the module.
func needsIMessageModule(t *testing.T) imessagewire.Factory {
	t.Helper()
	f := imessageport.Factory()
	if f == nil {
		t.Skip("this build has no iMessage module (persona_without_imessage or persona_core_only)")
	}
	return f
}

// newTestIMessage is the module's connector reading cfg.Machine (named machine) through call.
func newTestIMessage(t *testing.T, cfg imessagewire.Config, machine string, call imessagewire.Caller) imessagewire.Remote {
	t.Helper()
	return needsIMessageModule(t)(cfg, machine, call)
}

// fullIMessage is the module's connector with every interface it implements, so that a wrapper keeps them.
type fullIMessage interface {
	imessagewire.Remote
	connector.ReadTools
	connector.ReadJSON
	connector.Catalog
	connector.Events
	connector.Inbox
	connector.Archive
	connector.Machines
	connector.Runner
}

// imessagePersonKey is a person's id in the knowledge base as the module makes it: a hash of their number or address.
func imessagePersonKey(handle string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(handle))))
	return hex.EncodeToString(sum[:6])
}

// Without the iMessage module (persona_without_imessage, persona_core_only) the manager has no Messages connector, even
// with manager.json's imessage.machine set: none is registered, there is no built-in Messages extension, and
// /api/v1/imessage answers that it is not configured.
func TestManagerWithoutIMessage(t *testing.T) {
	if imessageport.Factory() != nil {
		t.Skip("this build has the iMessage module")
	}
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	h.cfg.IMessage = &imessagewire.Config{Machine: "mac"}
	if err := h.addIMessage(*h.cfg.IMessage); err == nil {
		t.Fatal("a Messages connector without the module")
	}
	if h.connectors.Get(imessagewire.ID) != nil || h.imessage() != nil {
		t.Fatal("a Messages connector is registered without the module")
	}
	if m := imessageManifest(h); m != nil {
		t.Fatalf("a built-in Messages extension without the module: %+v", m)
	}
	mux := http.NewServeMux()
	h.imessageRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/imessage", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/imessage: %d, want 404", rec.Code)
	}
}
