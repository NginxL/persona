package assistant

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
)

// Paginated reads of the to-dos (#14506): a list can grow past what one HTTP response can carry (some clients refuse
// a body over 4 MiB whole), so GET /api/v1/todos takes ?limit and ?cursor. Without either it answers exactly what it
// always did: every row, the counts and the revision header. With them it answers one page and says how to read the
// next one.
//
// The order is the list's own (created_at descending, ties by id ascending, todo_view_routes.go). The cursor is the
// last row's key, not an offset: a row inserted or removed between two reads cannot make a page skip or repeat one.
// It never expires; a cursor whose row was deleted still names the place after it. A page holds at most
// todoPageLimitMax rows and stops before todoPageBudgetBytes, so a normal page stays well under 4 MiB. One row larger
// than the budget is answered alone and flagged oversized rather than truncated or skipped.
const (
	todoPageLimitDefault = 50
	todoPageLimitMax     = 200
	todoPageBudgetBytes  = 3 << 20 // leaves room for the counts and the JSON envelope under 4 MiB
	todoCursorMaxLen     = 512
	todoCursorMaxIDLen   = 200
)

var (
	errTodoPageLimit  = errors.New("limit must be an integer from 1 to 200")
	errTodoPageCursor = errors.New("cursor is invalid")
)

// todoPageCursor is where the next page starts: the last row's created_at and id, in the list's order.
type todoPageCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

// todoPage is GET /api/v1/todos's answer with ?limit or ?cursor: the page, the list's counts, and how to continue.
// HasMore is always present in this mode; NextCursor only while HasMore. Oversized marks a page holding one row that
// alone is larger than todoPageBudgetBytes: it is complete, never truncated.
type todoPage struct {
	Todos      []listedTodo             `json:"todos"`
	Counts     assistantwire.TodoCounts `json:"counts"`
	NextCursor string                   `json:"next_cursor,omitempty"`
	HasMore    bool                     `json:"has_more"`
	Oversized  bool                     `json:"oversized,omitempty"`
}

// todoListPageQuery reads a list request's page. paged is false when it names neither parameter: the complete answer.
func todoListPageQuery(v url.Values) (limit int, after *todoPageCursor, paged bool, err error) {
	limit = todoPageLimitDefault
	if v.Has("limit") {
		n, convErr := strconv.Atoi(v.Get("limit"))
		if convErr != nil || n < 1 || n > todoPageLimitMax {
			return 0, nil, true, errTodoPageLimit
		}
		limit, paged = n, true
	}
	if v.Has("cursor") {
		c, decErr := decodeTodoPageCursor(v.Get("cursor"))
		if decErr != nil {
			return 0, nil, true, decErr
		}
		after, paged = &c, true
	}
	return limit, after, paged, nil
}

// decodeTodoPageCursor reads a cursor this manager wrote. A cursor names the sort key and id explicitly, so a deleted
// anchor row does not make it stale; one that is malformed, too long or names no id is refused.
func decodeTodoPageCursor(raw string) (todoPageCursor, error) {
	if raw == "" || len(raw) > todoCursorMaxLen {
		return todoPageCursor{}, errTodoPageCursor
	}
	body, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return todoPageCursor{}, errTodoPageCursor
	}
	var c todoPageCursor
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return todoPageCursor{}, errTodoPageCursor
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return todoPageCursor{}, errTodoPageCursor
	}
	if c.ID == "" || len(c.ID) > todoCursorMaxIDLen {
		return todoPageCursor{}, errTodoPageCursor
	}
	if _, err := time.Parse(time.RFC3339Nano, c.CreatedAt); err != nil {
		return todoPageCursor{}, errTodoPageCursor
	}
	return c, nil
}

// encodeTodoPageCursor is row's place in the list, as the next request's ?cursor.
func encodeTodoPageCursor(row listedTodo) string {
	raw, _ := json.Marshal(todoPageCursor{CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339Nano), ID: row.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// todoAfterCursor: row comes after the cursor in the list's order (created_at descending, ties by id ascending).
func todoAfterCursor(row listedTodo, c todoPageCursor) bool {
	at, err := time.Parse(time.RFC3339Nano, c.CreatedAt)
	if err != nil { // decodeTodoPageCursor checked it; a bad cursor never reaches here
		return false
	}
	switch row.CreatedAt.Compare(at) {
	case -1:
		return true
	case 1:
		return false
	}
	return row.ID > c.ID
}

// todoPageOf cuts one page of list, which is already in the list's order. It answers the cursor of the last row while
// more rows remain. A first row larger than the budget is the page's only row and is flagged oversized: the row is
// never truncated and the next page still follows.
func todoPageOf(list []listedTodo, limit int, after *todoPageCursor) (page []listedTodo, next string, hasMore, oversized bool) {
	start := 0
	if after != nil {
		start = sort.Search(len(list), func(i int) bool { return todoAfterCursor(list[i], *after) })
	}
	page = make([]listedTodo, 0, min(limit, len(list)-start))
	used := 0
	for i := start; i < len(list); i++ {
		if len(page) == limit {
			break
		}
		raw, err := json.Marshal(list[i])
		if err != nil { // listedTodo holds only JSON types; keep the page readable if that ever changes
			break
		}
		size := len(raw) + 1 // the comma between rows
		if len(page) > 0 && used+size > todoPageBudgetBytes {
			break
		}
		page = append(page, list[i])
		used += size
		if len(page) == 1 && size > todoPageBudgetBytes {
			oversized = true
		}
	}
	if len(page) == 0 { // only a row that would not marshal; nothing to continue from
		return page, "", false, false
	}
	hasMore = start+len(page) < len(list)
	if hasMore {
		next = encodeTodoPageCursor(page[len(page)-1])
	}
	return page, next, hasMore, oversized
}

// writeTodoPage answers GET /api/v1/todos's paginated read; p is the projection of the whole visible list, which the
// counts describe.
func writeTodoPage(w http.ResponseWriter, list []listedTodo, p todoProjection, limit int, after *todoPageCursor) {
	page, next, hasMore, oversized := todoPageOf(list, limit, after)
	apierr.WriteJSON(w, todoPage{Todos: page, Counts: p.counts(), NextCursor: next, HasMore: hasMore, Oversized: oversized})
}
