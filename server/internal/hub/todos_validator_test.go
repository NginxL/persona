package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The merged to-dos list carries a validator: the same list again answers 304 with no body, a changed one 200 with a
// new tag, an error no tag (#9590).
func TestTodosListValidator(t *testing.T) {
	answers := func(title string, status int) []listAnswer {
		return []listAnswer{{id: "assistant", status: status, header: http.Header{"Content-Type": {"application/json"}},
			body: []byte(`{"todos":[{"id":"todo_example","title":"` + title + `"}]}`)}}
	}
	get := func(inm string, a []listAnswer) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/todos", nil)
		if inm != "" {
			r.Header.Set("If-None-Match", inm)
		}
		w := httptest.NewRecorder()
		writeTaggedList(w, r, "todos", a, todoListSums(a))
		return w
	}

	first := get("", answers("a", http.StatusOK))
	tag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || !strings.HasPrefix(tag, `W/"t1-`) || !strings.Contains(first.Body.String(), `"manager":"assistant"`) {
		t.Fatalf("first: %d %q %s", first.Code, tag, first.Body)
	}
	same := get(tag, answers("a", http.StatusOK))
	if same.Code != http.StatusNotModified || same.Body.Len() != 0 || same.Header().Get("ETag") != tag {
		t.Fatalf("the same list: %d %q %d bytes, want 304 with its tag and no body", same.Code, same.Header().Get("ETag"), same.Body.Len())
	}
	if strong := get(strings.TrimPrefix(tag, "W/"), answers("a", http.StatusOK)); strong.Code != http.StatusNotModified {
		t.Fatalf("a tag sent without W/: %d, want 304 (weak comparison)", strong.Code)
	}
	changed := get(tag, answers("b", http.StatusOK))
	if changed.Code != http.StatusOK || changed.Header().Get("ETag") == tag || changed.Body.Len() == 0 {
		t.Fatalf("a changed list: %d %q", changed.Code, changed.Header().Get("ETag"))
	}
	failed := get(tag, answers("a", http.StatusServiceUnavailable))
	if failed.Code != http.StatusServiceUnavailable || failed.Header().Get("ETag") != "" {
		t.Fatalf("an error: %d %q, want 503 and no tag", failed.Code, failed.Header().Get("ETag"))
	}
}
