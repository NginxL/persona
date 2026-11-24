package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Which of a session's writes keep an operation receipt (X-Persona-Op-Id, withClientOp, #12532). A write that makes
// the computer act again when it is repeated (a message typed, keys pressed, a queued message sent) must keep one, so
// a client's retry after a lost response is answered from the receipt instead of acting twice. A write that is the
// same however often it arrives may go without one, with the reason here. A new write route in sessionRoutes has to
// be added to one of the two lists.
var sessionWritesWithReceipts = []string{
	"POST /api/v1/sessions",
	"POST /api/v1/sessions/{mid}/{sid}/messages",
	"POST /api/v1/sessions/{mid}/{sid}/agent-messages",
	"POST /api/v1/sessions/{mid}/{sid}/interrupt",
	"POST /api/v1/sessions/{mid}/{sid}/keys",
	"POST /api/v1/sessions/{mid}/{sid}/rewind",
	"POST /api/v1/sessions/{mid}/{sid}/restore-model",
	"POST /api/v1/sessions/{mid}/{sid}/continue-in-new-chat",
	"POST /api/v1/sessions/{mid}/{sid}/fork",
	"POST /api/v1/sessions/{mid}/{sid}/close",
	"POST /api/v1/sessions/{mid}/{sid}/queue/send-now",
	"POST /api/v1/sessions/{mid}/{sid}/rename",
	"POST /api/v1/sessions/{mid}/{sid}/modes",
}

var sessionWritesWithoutReceipts = map[string]string{
	"POST /api/v1/sessions/{mid}/{sid}/attach":        "focuses the session's pane: done twice, the same pane has focus",
	"POST /api/v1/sessions/{mid}/{sid}/queue":         "replaces the queue with the list it carries: done twice, the same list",
	"DELETE /api/v1/sessions/{mid}/{sid}":             "records the deletion time: done twice, the session stays deleted",
	"POST /api/v1/machines/{mid}/spaces/{wid}/rename": "sets the name it carries: done twice, the same name",
	"POST /api/v1/sessions/{mid}/{sid}/screen/before": "reads the scrollback above an anchor; changes nothing",
}

var sessionWriteRoute = regexp.MustCompile(`api\.HandleFunc\("((?:POST|PUT|PATCH|DELETE) [^"]+)"`)

// sessionWriteRoutes are the write routes session_routes.go registers, read from its source: a route is listed even
// when its handler is wrapped in something the mux does not show.
func sessionWriteRoutes(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("session_routes.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range sessionWriteRoute.FindAllStringSubmatch(string(src), -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

func TestEverySessionWriteRouteSaysWhetherItKeepsAReceipt(t *testing.T) {
	listed := map[string]bool{}
	for _, route := range sessionWritesWithReceipts {
		listed[route] = true
	}
	for route := range sessionWritesWithoutReceipts {
		if listed[route] {
			t.Errorf("%s is on both lists", route)
		}
		listed[route] = true
	}
	registered := map[string]bool{}
	for _, route := range sessionWriteRoutes(t) {
		registered[route] = true
		if !listed[route] {
			t.Errorf("%s is a new session write: add it to sessionWritesWithReceipts (and wrap it in withClientOp) or to sessionWritesWithoutReceipts with why repeating it changes nothing", route)
		}
	}
	for route := range listed {
		if !registered[route] {
			t.Errorf("%s is listed but session_routes.go no longer registers it", route)
		}
	}
}

// A route behind withClientOp refuses an operation id that is not a UUID before anything runs; one that is not
// wrapped never looks at the header. The probe needs no machine, node or database.
func TestSessionWritesThatActAgainWhenRepeatedKeepAReceipt(t *testing.T) {
	h := &Hub{}
	mux := http.NewServeMux()
	h.sessionRoutes(mux)
	for _, route := range sessionWritesWithReceipts {
		method, pattern, _ := strings.Cut(route, " ")
		path := strings.NewReplacer("{mid}", "m1", "{sid}", "s1", "{wid}", "w1").Replace(pattern)
		r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		// a paired full device: the full-device-only routes let it through to their receipt
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{ID: "phone", Role: roleFull}))
		r.Header.Set("X-Persona-Op-Id", "not-a-uuid")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		// The Agent-to-Agent route goes further and takes no write without a valid op id at all.
		required := w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), `"operation_id_required"`)
		if !required && (w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "operation id must be a UUID")) {
			t.Errorf("%s does not keep a receipt (withClientOp): a bad op id got %d %s", route, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
}
