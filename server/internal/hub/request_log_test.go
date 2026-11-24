package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// capturedLog has h's log written to a buffer it returns.
func capturedLog(h *Hub) *bytes.Buffer {
	var b bytes.Buffer
	h.logger = log.New(&b, "", 0)
	return &b
}

// A server error's request line says the code and the technical words the hub answered with: the detail a localized
// failure keeps apart from its generic error, else the error; one line, quoted, clipped. Other answers' lines, and a
// server error whose body is not the hub's JSON error, are as they were (#13468).
func TestServerErrorRequestLineSaysTheError(t *testing.T) {
	h := newTestHub(t)
	logs := capturedLog(h)
	handler := logRequests(h, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/node":
			apierr.Call(w, errors.New("no such session"))
		case "/long":
			writeErr(w, http.StatusInternalServerError, errors.New("first line\nsecond "+strings.Repeat("x", 400)))
		case "/words":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"code":"assistant_offline","error":"the manager is not connected"}`)
		case "/plain":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "upstream says no")
		case "/refused":
			writeErr(w, http.StatusConflict, errors.New("already handled"))
		}
	}))
	for _, path := range []string{"/node", "/long", "/words", "/plain", "/refused"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("log:\n%s", logs.String())
	}
	if !strings.Contains(lines[0], "GET /node -> 502 (") || !strings.HasSuffix(lines[0], ` code=internal detail="no such session"`) {
		t.Errorf("node error line: %s", lines[0])
	}
	_, quoted, ok := strings.Cut(lines[1], ` detail=`)
	if !ok || !strings.Contains(lines[1], "-> 500 (") || !strings.HasPrefix(quoted, `"first line second xxx`) ||
		utf8.RuneCountInString(quoted) > loggedErrorText+3 || strings.Contains(quoted, `\n`) {
		t.Errorf("long error line: %s", lines[1])
	}
	if !strings.HasSuffix(lines[2], ` code=assistant_offline error="the manager is not connected"`) {
		t.Errorf("an error without a detail: %s", lines[2])
	}
	if !strings.HasSuffix(lines[3], ")") || strings.Contains(lines[3], "upstream says no") {
		t.Errorf("a body that is not the hub's error is not logged: %s", lines[3])
	}
	if !strings.Contains(lines[4], "-> 409 (") || strings.Contains(lines[4], "already handled") {
		t.Errorf("a refusal's line is as it was: %s", lines[4])
	}
}

// itemsNode is a node whose session.items answers page as it is: a runtime's page, which the node passes on raw.
func itemsNode(t *testing.T, h *Hub, page string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide, nodeSide := rpc.New("h", h2nW), rpc.New("n", n2hW)
	nodeSide.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "session.items" {
			return json.RawMessage(page), nil
		}
		return nil, fmt.Errorf("unknown method %s", method)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = hubSide.Serve(ctx, n2hR) }()
	go func() { defer wg.Done(); _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); h2nW.Close(); n2hW.Close(); wg.Wait() })
	m := newMachine(h, MachineConfig{ID: "m1", Name: "dev box"})
	m.peer, m.online = hubSide, true
	m.info = proto.NodeInfo{Version: "test", Protocol: 1, Capabilities: []string{nodeproto.Sessions}}
	h.byID["m1"] = m
}

func itemsRequest(h *Hub) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/m1/agy-0f4e2d1c/items?limit=50&fold_work=1", nil)
	r.SetPathValue("mid", "m1")
	r.SetPathValue("sid", "agy-0f4e2d1c")
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{Role: roleFull, ID: "dev"}))
	w := httptest.NewRecorder()
	logRequests(h, h.withMachine(h.sessionItems)).ServeHTTP(w, r)
	return w
}

// A runtime page with an item field of another JSON type than proto.Item's fails the whole page in the hub's own
// decode (the node passes a runtime's page on raw): 502 "internal", the decoder's words in detail. The request line
// names them; a page that decodes is served and leaves no line.
func TestRuntimeItemsPageThatDoesNotDecodeIsLogged(t *testing.T) {
	h := newTestHub(t)
	logs := capturedLog(h)
	itemsNode(t, h, `{"items":[{"id":"1#0","seq":"1","ts":"2026-11-22T03:12:16Z","role":"assistant","kind":"text","text":"done"}],"has_more":false}`)
	w := itemsRequest(h)
	var body struct{ Code, Detail string }
	if w.Code != http.StatusBadGateway || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Code != "internal" ||
		!strings.Contains(body.Detail, "cannot unmarshal string into Go struct field") || !strings.Contains(body.Detail, "seq of type int") {
		t.Fatalf("answer %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(logs.String(), `/items -> 502 (`) || !strings.Contains(logs.String(), `code=internal detail="`+body.Detail+`"`) {
		t.Fatalf("log:\n%s", logs.String())
	}

	h2 := newTestHub(t)
	logs2 := capturedLog(h2)
	itemsNode(t, h2, `{"items":[{"id":"1#0","seq":1,"ts":"2026-11-22T03:12:16Z","role":"assistant","kind":"text","text":"done"}],"has_more":false}`)
	if w := itemsRequest(h2); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"text":"done"`) || logs2.Len() != 0 {
		t.Fatalf("a page that decodes: %d %s, log %q", w.Code, w.Body.String(), logs2.String())
	}
}
