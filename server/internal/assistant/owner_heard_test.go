package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// heardExt is extensions that listen for the owner's words (ownerHeard), and nothing else.
type heardExt struct {
	ExtensionTools
	mu    sync.Mutex
	heard []string
}

// ConnectorTool: none of its tools is a connector's.
func (e *heardExt) ConnectorTool(string) bool { return false }

func (e *heardExt) OwnerSpoke(session string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.heard = append(e.heard, session)
}

func (e *heardExt) got() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.heard)
}

// The owner's message in a conversation, and their answer to its question, are told to the extensions that listen
// (#7590: a computer-use session the user stopped on a Mac starts again on that conversation's next call).
func TestOwnerWordsAreToldToTheExtensions(t *testing.T) {
	e := newAskTodoEnv(t, nil)
	ext := &heardExt{}
	e.s.SetExtensions(ext)
	if e.s.heard == nil {
		t.Fatal("SetExtensions did not take the listener")
	}
	e.s.UserSpoke(context.Background(), SessionRef{MachineID: "server", SessionID: "c1x1x1"})
	if got := ext.got(); !slices.Equal(got, []string{"c1x1x1"}) {
		t.Fatalf("a message: %v", got)
	}
	ix, _ := e.askLater(askLaterArgs)
	e.mustEnd(ix.ID, `{"answers":{"Merge the release PR now?":"Wait"}}`)
	if got := ext.got(); !slices.Equal(got, []string{"c1x1x1", "c1x1x1"}) {
		t.Fatalf("an answer to its question: %v", got)
	}
	e.s.heard = nil // extensions that do not listen: nothing to tell, nothing breaks
	e.s.UserSpoke(context.Background(), SessionRef{MachineID: "server", SessionID: "c1x1x1"})
}

func TestPreparedWebAnswerIsToldToTheExtensions(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	ext := &heardExt{}
	ft.s.SetExtensions(ext)
	done := startPreparedTest(t, ft, "ix_owner_heard_web", "", nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/interactions/ix_owner_heard_web/resolve", strings.NewReader(`{"allow":true}`))
	w := httptest.NewRecorder()
	ft.s.fleetHandler(http.NotFoundHandler()).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("web prepared answer: %d %s", w.Code, w.Body.String())
	}
	if got := ext.got(); !slices.Equal(got, []string{"s1"}) {
		t.Fatalf("prepared web answer owner activity: %v", got)
	}
	if got := awaitPreparedTest(t, done); got.err != nil || got.out.Text != "applied" {
		t.Fatalf("prepared web answer outcome = %+v, %v", got.out, got.err)
	}
}
