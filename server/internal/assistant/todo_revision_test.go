package assistant

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The pushed read's cheap validator (#13804): a full read leaves a token for exactly the rows and stable views it sent.
// Repeating the same conditional read neither rebuilds the list nor changes the token; every write that can change the
// list does, including marks that deliberately leave updated_at alone.
func TestTodosRevisionCoversFullRowsAndStableViews(t *testing.T) {
	s := manyTodos(40)
	s.todos.mu.Lock()
	s.todos.todos["todo_bench000003"].DoneAt = nil // Someday accepts only an open to-do.
	s.todos.mu.Unlock()
	read := func(ifNoneMatch string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/todos/revision", nil)
		if ifNoneMatch != "" {
			r.Header.Set("If-None-Match", ifNoneMatch)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	full := httptest.NewRecorder()
	s.ServeHTTP(full, httptest.NewRequest(http.MethodGet, "/api/v1/todos", nil))
	tag := full.Header().Get("X-Persona-Todos-Revision")
	if full.Code != http.StatusOK || tag == "" {
		t.Fatalf("full list: %d, revision %q", full.Code, tag)
	}
	if got := read(tag); got.Code != http.StatusNotModified || got.Body.Len() != 0 || got.Header().Get("ETag") != tag {
		t.Fatalf("unchanged revision: %d %q %q, want 304 with no body", got.Code, got.Header().Get("ETag"), got.Body.String())
	}
	// A push clears the per-push told book, not the stable classes the revision uses. Repeating that notification alone
	// must not manufacture a full-list change.
	s.todoViewPushed()
	if got := read(tag); got.Code != http.StatusNotModified {
		t.Fatalf("the push alone changed the revision: %d %s", got.Code, got.Body.String())
	}

	// Exercise the public mark routes rather than updated_at: seen, restored and Someday all notify without an edit.
	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"seen", http.MethodPut, "/api/v1/todos/todo_bench000001/seen", ""},
		{"restored", http.MethodPut, "/api/v1/todos/todo_bench000002/restored", ""},
		{"someday", http.MethodPut, "/api/v1/todos/todo_bench000003/someday", `{"until":"2027-01-01T00:00:00Z"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := read("").Header().Get("ETag")
			r := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.body != "" {
				r = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("mark: %d %s", w.Code, w.Body.String())
			}
			after := read("").Header().Get("ETag")
			if after == "" || after == before {
				t.Fatalf("revision stayed %q after %s", before, tc.name)
			}
		})
	}
}

// A revision read is a lock-and-hash operation, not another projection. Once a full read has produced its token, even a
// forced reload boundary does not make the conditional read rebuild or reload the list just to answer 304.
func TestTodosRevisionUnchangedDoesNotReadTheStore(t *testing.T) {
	s := manyTodos(40)
	full := httptest.NewRecorder()
	s.ServeHTTP(full, httptest.NewRequest(http.MethodGet, "/api/v1/todos", nil))
	tag := full.Header().Get("X-Persona-Todos-Revision")
	if tag == "" {
		t.Fatal("full list has no revision")
	}
	s.todos.mu.Lock()
	s.todos.loaded = false
	s.todos.mu.Unlock()

	r := httptest.NewRequest(http.MethodGet, "/api/v1/todos/revision", nil)
	r.Header.Set("If-None-Match", tag)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusNotModified {
		t.Fatalf("revision rebuilt or reloaded the list: %d %s", w.Code, w.Body.String())
	}
}

func TestTodosRevisionFullListBodyStaysComplete(t *testing.T) {
	s := manyTodos(4)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/todos", nil))
	if w.Code != http.StatusOK || len(w.Body.Bytes()) < 1000 {
		t.Fatalf("legacy full list: %d, %d bytes", w.Code, w.Body.Len())
	}
	for _, field := range []string{`"brief"`, `"lineage"`, `"view"`, `"notes"`} {
		if !strings.Contains(w.Body.String(), field) {
			t.Errorf("legacy full list lost %s", field)
		}
	}
}

func TestTodosRevisionChangesWithBrief(t *testing.T) {
	s := manyTodos(1)
	full := httptest.NewRecorder()
	s.ServeHTTP(full, httptest.NewRequest(http.MethodGet, "/api/v1/todos", nil))
	tag := full.Header().Get("X-Persona-Todos-Revision")
	if tag == "" {
		t.Fatal("full list has no revision")
	}
	_, changed, err := s.changeBrief(t.Context(), "brief_bench000000", func(tb *taskBrief) bool {
		tb.st.Log = append(tb.st.Log, BriefEvent{Kind: "scope", Change: "cover the narrow case too"})
		return true
	})
	if err != nil || !changed {
		t.Fatalf("brief change: changed %v, %v", changed, err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/todos/revision", nil)
	r.Header.Set("If-None-Match", tag)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("ETag") == tag {
		t.Fatalf("brief change left revision %q: %d", tag, w.Code)
	}
}
