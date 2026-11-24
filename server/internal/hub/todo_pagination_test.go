package hub

import (
	"net/http"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

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
