package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestTodoListPaginationWithoutManagers(t *testing.T) {
	h := newTestHub(t)
	h.cfg.Assistant.External = true
	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)
	for _, path := range []string{"/api/v1/todos", "/api/v1/todos?limit=1"} {
		code, body := ownershipGet(t, h, srv, http.MethodGet, path, nil)
		if code != http.StatusServiceUnavailable || body["code"] != assistantwire.CodeAssistantOffline {
			t.Errorf("%s: got %d %v, want 503 assistant_offline", path, code, body)
		}
	}
}

func TestTodoListPaginationWithLegacyManager(t *testing.T) {
	h, srv, _, _, ra, _ := todoViewHub(t)
	ra.set(`{"todos":[{"id":"one"},{"id":"two"}]}`, "", http.StatusOK)
	code, body := ownershipGet(t, h, srv, http.MethodGet, "/api/v1/todos?manager=assistant&limit=1", nil)
	if code != http.StatusNotImplemented || body["code"] != "todos_pagination_unsupported" {
		t.Fatalf("legacy manager answered a page request: got %d %v", code, body)
	}
	resp, full := todoViewGet(t, h, srv, "/api/v1/todos?manager=assistant", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(full), `"id":"two"`) {
		t.Errorf("legacy full list: %d %s", resp.StatusCode, full)
	}
}

// A paginated to-dos read is one manager's list (#14506): the hub relays it to the manager the request names (an
// extension's token implies its own) and keeps that manager's next_cursor and has_more, instead of merging pages of
// several lists, which would silently drop rows. Without pagination the merged list is what it always was.

func TestTodoListPaginationAsksOneManager(t *testing.T) {
	h, srv, _, _, ra, rb := todoViewHub(t)
	ra.set(`{"todos":[{"id":"a"}],"next_cursor":"ca","has_more":true}`, "", http.StatusOK)
	rb.set(`{"todos":[{"id":"b"}],"next_cursor":"cb","has_more":true}`, "", http.StatusOK)

	resp, body := todoViewGet(t, h, srv, "/api/v1/todos?limit=1", nil)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "todos_pagination_manager_required") {
		t.Fatalf("a paginated read with no manager named: %d %s", resp.StatusCode, body)
	}
	if full, _ := ra.reads(); full != 0 {
		t.Errorf("the default manager was asked for part of the list")
	}
	if full, _ := rb.reads(); full != 0 {
		t.Errorf("the other manager was asked for part of the list")
	}

	resp, body = todoViewGet(t, h, srv, "/api/v1/todos?limit=1&manager=assistant", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a paginated read naming a manager: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"next_cursor":"ca"`) || !strings.Contains(string(body), `"has_more":true`) {
		t.Errorf("the manager's cursor and has_more were not kept: %s", body)
	}
	if strings.Contains(string(body), `"id":"b"`) {
		t.Errorf("another manager's row was merged into a page: %s", body)
	}
	if full, _ := ra.reads(); full != 1 {
		t.Errorf("the named manager was read %d times", full)
	}
	if full, _ := rb.reads(); full != 0 {
		t.Errorf("the other manager was asked for part of the list")
	}

	ra.set(`{"todos":[{"id":"a"}]}`, "", http.StatusOK)
	rb.set(`{"todos":[{"id":"b"}]}`, "", http.StatusOK)
	resp, body = todoViewGet(t, h, srv, "/api/v1/todos", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"id":"a"`) || !strings.Contains(string(body), `"id":"b"`) {
		t.Fatalf("the complete list stopped merging: %d %s", resp.StatusCode, body)
	}
}

func TestTodoListPaginationSingleManagerNeedsNoName(t *testing.T) {
	h, srv, _ := newBusHub(t)
	ra := &todoReplies{}
	connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, ra.answer)
	ra.set(`{"todos":[{"id":"a"}],"has_more":false}`, "", http.StatusOK)

	resp, body := todoViewGet(t, h, srv, "/api/v1/todos?limit=1", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"id":"a"`) {
		t.Fatalf("a paginated read on a one-manager hub: %d %s", resp.StatusCode, body)
	}
	if full, _ := ra.reads(); full != 1 {
		t.Errorf("the only manager was read %d times", full)
	}
}
