package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// GET /api/v1/todos's pages (#14506): a walk reads every row once in the list's order, repeated walks are the same,
// a row added between two pages shifts nothing, bad limits and cursors are refused, a full answer over 4 MiB is read
// in pages that stay below it, and a single oversized row is answered whole and flagged rather than truncated.

// todoPageGet is GET /api/v1/todos with query, failing unless it answers 200.
func todoPageGet(t *testing.T, s *Service, query string) (todoPage, []byte) {
	t.Helper()
	w := serve(s, "GET", "/api/v1/todos"+query, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos%s: %d %s", query, w.Code, w.Body)
	}
	var p todoPage
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("GET /api/v1/todos%s: %v", query, err)
	}
	return p, w.Body.Bytes()
}

// walkTodoPages follows next_cursor from the first page, limit rows a page, and answers every page read.
func walkTodoPages(t *testing.T, s *Service, limit int) []todoPage {
	t.Helper()
	var pages []todoPage
	cursor := ""
	for n := 0; ; n++ {
		if n > 1000 {
			t.Fatal("the paginated walk did not end")
		}
		query := fmt.Sprintf("?limit=%d", limit)
		if cursor != "" {
			query += "&cursor=" + url.QueryEscape(cursor)
		}
		page, _ := todoPageGet(t, s, query)
		if len(page.Todos) > limit {
			t.Fatalf("page %d holds %d rows, over limit %d", n, len(page.Todos), limit)
		}
		pages = append(pages, page)
		if !page.HasMore {
			if page.NextCursor != "" {
				t.Fatalf("page %d: no more rows but next_cursor %q", n, page.NextCursor)
			}
			return pages
		}
		if page.NextCursor == "" {
			t.Fatalf("page %d: has_more without next_cursor", n)
		}
		cursor = page.NextCursor
	}
}

func todoIDs(rows []listedTodo) []string {
	ids := make([]string, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	return ids
}

func pageIDs(pages []todoPage) []string {
	var ids []string
	for _, p := range pages {
		ids = append(ids, todoIDs(p.Todos)...)
	}
	return ids
}

func TestTodosPaginationWalksEveryRowOnce(t *testing.T) {
	s := manyTodos(23)
	var full todosList
	if err := json.Unmarshal(todosListBody(t, s), &full); err != nil {
		t.Fatal(err)
	}
	if len(full.Todos) != 23 {
		t.Fatalf("the fixture has %d rows", len(full.Todos))
	}
	first, second := walkTodoPages(t, s, 5), walkTodoPages(t, s, 5)
	if len(first) != 5 { // 5+5+5+5+3
		t.Fatalf("the walk read %d pages", len(first))
	}
	if got, want := pageIDs(first), todoIDs(full.Todos); !slices.Equal(got, want) {
		t.Fatalf("the walk read\n%v\nwant\n%v", got, want)
	}
	if got, want := pageIDs(second), pageIDs(first); !slices.Equal(got, want) {
		t.Fatalf("the second walk differs:\n%v\n%v", got, want)
	}
	var rows []listedTodo
	for _, p := range first {
		rows = append(rows, p.Todos...)
		if !reflect.DeepEqual(p.Counts, full.Counts) {
			t.Errorf("a page's counts differ from the list's: %+v, want %+v", p.Counts, full.Counts)
		}
	}
	for i := 1; i < len(rows); i++ {
		switch {
		case rows[i].CreatedAt.After(rows[i-1].CreatedAt):
			t.Fatalf("row %d is newer than the one before it", i)
		case rows[i].CreatedAt.Equal(rows[i-1].CreatedAt) && rows[i].ID <= rows[i-1].ID:
			t.Fatalf("rows at the same time are not ordered by id: %s, %s", rows[i-1].ID, rows[i].ID)
		}
	}
}

// A row added after the first page sorts before the cursor: the rest of the walk neither repeats nor skips a row.
func TestTodosPaginationIsNotAnOffset(t *testing.T) {
	s := manyTodos(11)
	var full todosList
	if err := json.Unmarshal(todosListBody(t, s), &full); err != nil {
		t.Fatal(err)
	}
	first, _ := todoPageGet(t, s, "?limit=4")
	if len(first.Todos) != 4 || !first.HasMore {
		t.Fatalf("the first page: %d rows, has_more %v", len(first.Todos), first.HasMore)
	}
	if _, err := s.createTodo(context.Background(), "a row added between pages", "", "", ""); err != nil {
		t.Fatal(err)
	}
	var got []string
	got = append(got, todoIDs(first.Todos)...)
	cursor := first.NextCursor
	for cursor != "" {
		page, _ := todoPageGet(t, s, "?limit=4&cursor="+url.QueryEscape(cursor))
		got = append(got, todoIDs(page.Todos)...)
		cursor = page.NextCursor
		if !page.HasMore {
			cursor = ""
		}
	}
	if want := todoIDs(full.Todos); !slices.Equal(got, want) {
		t.Fatalf("the walk with a row added read\n%v\nwant the original list\n%v", got, want)
	}
}

func TestTodosPaginationKeepsCompleteAnswerWithoutParameters(t *testing.T) {
	s := manyTodos(7)
	w := serve(s, "GET", "/api/v1/todos", "")
	if w.Code != http.StatusOK {
		t.Fatalf("the complete answer: %d %s", w.Code, w.Body)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"next_cursor", "has_more", "oversized"} {
		if _, ok := fields[k]; ok {
			t.Errorf("the complete answer carries %q", k)
		}
	}
	if w.Header().Get(assistantwire.TodoRevisionHeader) == "" {
		t.Error("the complete answer lost its revision header")
	}
	var full todosList
	if err := json.Unmarshal(w.Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	page, _ := todoPageGet(t, s, "?limit=7")
	if page.HasMore || page.NextCursor != "" || page.Oversized {
		t.Fatalf("a page over every row: %+v", page)
	}
	if !reflect.DeepEqual(page.Todos, full.Todos) || !reflect.DeepEqual(page.Counts, full.Counts) {
		t.Error("a page over every row differs from the complete answer")
	}
	pw := serve(s, "GET", "/api/v1/todos?limit=7", "")
	if pw.Header().Get(assistantwire.TodoRevisionHeader) != "" {
		t.Error("a page claims the complete list's revision")
	}
}

func TestTodosPaginationRefusesBadLimitAndCursor(t *testing.T) {
	s := manyTodos(3)
	first, _ := todoPageGet(t, s, "?limit=1")
	if first.NextCursor == "" {
		t.Fatal("the fixture has no second page")
	}
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	bad := []struct{ query, code string }{
		{"?limit=0", "invalid_todo_limit"},
		{"?limit=-1", "invalid_todo_limit"},
		{"?limit=abc", "invalid_todo_limit"},
		{"?limit=201", "invalid_todo_limit"},
		{"?limit=", "invalid_todo_limit"},
		{"?cursor=", "invalid_todo_cursor"},
		{"?cursor=not-base64!", "invalid_todo_cursor"},
		{"?cursor=" + enc(`{}`), "invalid_todo_cursor"},
		{"?cursor=" + enc(`{"created_at":"not-a-time","id":"x"}`), "invalid_todo_cursor"},
		{"?cursor=" + enc(`{"created_at":"2026-01-01T00:00:00Z","id":""}`), "invalid_todo_cursor"},
		{"?cursor=" + enc(`{"created_at":"2026-01-01T00:00:00Z","id":"x","extra":1}`), "invalid_todo_cursor"},
		{"?cursor=" + strings.Repeat("a", todoCursorMaxLen+1), "invalid_todo_cursor"},
	}
	for _, c := range bad {
		w := serve(s, "GET", "/api/v1/todos"+c.query, "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", c.query, w.Code, w.Body)
			continue
		}
		var body struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Code != c.code {
			t.Errorf("%s: code %q (%v), want %q", c.query, body.Code, err, c.code)
		}
	}
	for _, query := range []string{
		"?limit=1", "?limit=200",
		"?cursor=" + url.QueryEscape(first.NextCursor),
		"?limit=1&cursor=" + url.QueryEscape(first.NextCursor),
	} {
		if w := serve(s, "GET", "/api/v1/todos"+query, ""); w.Code != http.StatusOK {
			t.Errorf("%s: %d %s, want 200", query, w.Code, w.Body)
		}
	}
	// A cursor whose anchor row is gone still names its place: the walk continues, it is not an error.
	gone := base64.RawURLEncoding.EncodeToString([]byte(`{"created_at":"2026-01-01T00:00:00Z","id":"todo_deleted"}`))
	page, _ := todoPageGet(t, s, "?cursor="+url.QueryEscape(gone))
	if len(page.Todos) != 0 || page.HasMore {
		t.Fatalf("a cursor past every row: %d rows, has_more %v", len(page.Todos), page.HasMore)
	}
}

func TestTodosPaginationKeepsPagesUnderFourMiB(t *testing.T) {
	s := manyTodos(30)
	big := strings.Repeat("x", 300<<10)
	s.todos.mu.Lock()
	for _, row := range s.todos.todos {
		row.Notes = big
	}
	s.todos.mu.Unlock()
	fullBody := todosListBody(t, s)
	if len(fullBody) <= 4<<20 {
		t.Fatalf("the fixture's complete answer is %d bytes, not over 4 MiB", len(fullBody))
	}
	var full todosList
	if err := json.Unmarshal(fullBody, &full); err != nil {
		t.Fatal(err)
	}
	pages := walkTodoPages(t, s, todoPageLimitMax)
	if len(pages) < 2 {
		t.Fatalf("the walk read %d pages", len(pages))
	}
	if len(pages[0].Todos) >= len(full.Todos) {
		t.Fatalf("the byte budget did not cut the first page: %d rows", len(pages[0].Todos))
	}
	if got, want := pageIDs(pages), todoIDs(full.Todos); !slices.Equal(got, want) {
		t.Fatalf("the walk read\n%v\nwant\n%v", got, want)
	}
	cursor := ""
	for i, page := range pages {
		query := fmt.Sprintf("?limit=%d", todoPageLimitMax)
		if cursor != "" {
			query += "&cursor=" + url.QueryEscape(cursor)
		}
		_, body := todoPageGet(t, s, query)
		if len(body) >= 4<<20 {
			t.Errorf("page %d is %d bytes, not under 4 MiB", i, len(body))
		}
		cursor = page.NextCursor
	}
}

func TestTodosPaginationFlagsOneOversizedRowWithoutTruncatingIt(t *testing.T) {
	s := manyTodos(3)
	big := strings.Repeat("y", 9<<19) // 4.5 MiB
	s.todos.mu.Lock()
	s.todos.todos["todo_bench000001"].Notes = big
	s.todos.mu.Unlock()

	first, _ := todoPageGet(t, s, "?limit=1")
	if len(first.Todos) != 1 || first.Todos[0].ID != "todo_bench000002" || !first.HasMore || first.Oversized {
		t.Fatalf("the first page: %+v", first)
	}
	second, body := todoPageGet(t, s, "?limit=1&cursor="+url.QueryEscape(first.NextCursor))
	if len(second.Todos) != 1 || second.Todos[0].ID != "todo_bench000001" {
		t.Fatalf("the oversized row's page: %+v", second)
	}
	if !second.Oversized {
		t.Error("an oversized row must be flagged")
	}
	if second.Todos[0].Notes != big {
		t.Error("the oversized row was truncated")
	}
	if len(body) <= 4<<20 {
		t.Fatalf("the oversized page is %d bytes, not over 4 MiB", len(body))
	}
	if !second.HasMore || second.NextCursor == "" {
		t.Fatalf("the walk ends after the oversized row: has_more %v, next_cursor %q", second.HasMore, second.NextCursor)
	}
	third, _ := todoPageGet(t, s, "?limit=1&cursor="+url.QueryEscape(second.NextCursor))
	if len(third.Todos) != 1 || third.Todos[0].ID != "todo_bench000000" || third.HasMore || third.Oversized {
		t.Fatalf("the page after the oversized row: %+v", third)
	}
}

// An extension's pages are cut after its scoping, so a page never carries a to-do it is not shown and the walk still
// reads every row it is shown.
func TestTodosPaginationPagesExtensionRowsAfterScoping(t *testing.T) {
	e := newTodoEnv(t, nil)
	setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
	main := scopeSession("conv1", "Persona", "")
	main.Assistant = true
	e.sessions("server", main)

	ctx := context.Background()
	var hidden []string
	for i := 0; i < 6; i++ {
		if _, err := e.s.createTaskTodo(ctx, fmt.Sprintf("visible %d", i), "", "conv1", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 4; i++ {
		row, err := e.s.createTaskTodo(ctx, fmt.Sprintf("hidden %d", i), "", "conv_unknown", "", "")
		if err != nil {
			t.Fatal(err)
		}
		hidden = append(hidden, row.ID)
	}
	w := e.doAs(roleExtension, "GET", "/api/v1/todos", "")
	if w.Code != http.StatusOK {
		t.Fatalf("the extension's complete list: %d %s", w.Code, w.Body)
	}
	var full todosList
	if err := json.Unmarshal(w.Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	if len(full.Todos) != 6 {
		t.Fatalf("the extension is shown %d rows, want 6", len(full.Todos))
	}
	var got []string
	cursor := ""
	for n := 0; ; n++ {
		if n > 100 {
			t.Fatal("the extension's walk did not end")
		}
		path := "/api/v1/todos?limit=2"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		pw := e.doAs(roleExtension, "GET", path, "")
		if pw.Code != http.StatusOK {
			t.Fatalf("an extension's page: %d %s", pw.Code, pw.Body)
		}
		var page todoPage
		if err := json.Unmarshal(pw.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Todos) > 2 {
			t.Fatalf("an extension's page holds %d rows", len(page.Todos))
		}
		got = append(got, todoIDs(page.Todos)...)
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	if want := todoIDs(full.Todos); !slices.Equal(got, want) {
		t.Fatalf("the extension's walk read\n%v\nwant\n%v", got, want)
	}
	for _, id := range hidden {
		if slices.Contains(got, id) {
			t.Errorf("an extension's page carries the hidden row %s", id)
		}
	}
}
