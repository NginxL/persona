package hub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func hubTodoPNG(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func createHubTodoImage(t *testing.T, h *Hub, data string) (string, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"title": "Screenshot regression", "notes": "read the attached image, not a pathname",
		"images": []map[string]any{{"id": "d0000000-0000-4000-8000-000000000001", "mime": "image/png", "data": data}},
	})
	w := hubCall(h, http.MethodPost, "/api/v1/todos", string(body))
	var out struct {
		Todo struct {
			ID     string `json:"id"`
			Images []struct {
				URL  string `json:"url"`
				Data string `json:"data"`
			} `json:"images"`
		} `json:"todo"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Todo.ID == "" || len(out.Todo.Images) != 1 || out.Todo.Images[0].URL == "" {
		t.Fatalf("create with an image: %d %s", w.Code, w.Body)
	}
	if out.Todo.Images[0].Data != "" {
		t.Fatalf("the to-do list carries image bytes instead of private metadata: %s", w.Body)
	}
	return out.Todo.ID, out.Todo.Images[0].URL
}

func nodeImages(t *testing.T, params json.RawMessage) []map[string]any {
	t.Helper()
	var p struct{ Images []map[string]any }
	if err := json.Unmarshal(params, &p); err != nil {
		t.Fatal(err)
	}
	return p.Images
}

func checkHandoffImage(t *testing.T, params json.RawMessage, data string) {
	t.Helper()
	images := nodeImages(t, params)
	if len(images) != 1 || images[0]["mime"] != "image/png" || images[0]["data"] != data {
		t.Fatalf("handoff images = %s, want the stored PNG bytes", params)
	}
	if path, _ := images[0]["path"].(string); path != "" {
		t.Fatalf("handoff substituted a machine-local path %q for the image", path)
	}
}

// The manager's stored bytes cross the hub/node boundary for either explicit handoff. Nothing runs merely because the
// to-do was created, and its media URL is not a public link.
func TestTodoImageHandoffCarriesStoredBytes(t *testing.T) {
	h := newTestHub(t)
	data := hubTodoPNG(t)
	var calls []string
	m := capturingNode(t, h, "remote-machine", func(method string, params json.RawMessage) any {
		calls = append(calls, method)
		switch method {
		case "session.start":
			checkHandoffImage(t, params, data)
			return map[string]any{"session": proto.Session{MachineID: "remote-machine", SessionID: "s_new", Cwd: "/code", Agent: "claude"}}
		case "session.send":
			checkHandoffImage(t, params, data)
			return map[string]any{"ok": true}
		}
		return map[string]any{}
	})
	m.sessions = append(m.sessions, proto.Session{MachineID: "remote-machine", SessionID: "s_old", Title: "Existing Agent", Cwd: "/code", Agent: "claude"})
	connectAssistant(t, h)
	todoID, imageURL := createHubTodoImage(t, h, data)
	if len(calls) != 0 {
		t.Fatalf("creating the to-do started or messaged an Agent: %v", calls)
	}

	w := hubCall(h, http.MethodPost, "/api/v1/sessions/remote-machine/s_old/messages", `{"text":"Inspect the screenshot","todo_id":"`+todoID+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("existing-Agent handoff: %d %s", w.Code, w.Body)
	}
	w = hubCall(h, http.MethodPost, "/api/v1/sessions", `{"machine_id":"remote-machine","cwd":"/code","prompt":"Inspect the screenshot","todo_id":"`+todoID+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("new-Agent handoff: %d %s", w.Code, w.Body)
	}
	if len(calls) != 2 || calls[0] != "session.send" || calls[1] != "session.start" {
		t.Fatalf("handoff calls = %v", calls)
	}

	anonymous := todoRequest(http.MethodGet, imageURL, nil)
	withoutCredentials := httptest.NewRecorder()
	h.routes().ServeHTTP(withoutCredentials, anonymous)
	if withoutCredentials.Code != http.StatusUnauthorized && withoutCredentials.Code != http.StatusForbidden {
		t.Fatalf("anonymous image read = %d %s", withoutCredentials.Code, withoutCredentials.Body)
	}
	withCredentials := hubCall(h, http.MethodGet, imageURL, "")
	want, _ := base64.StdEncoding.DecodeString(data)
	if withCredentials.Code != http.StatusOK || !bytes.Equal(withCredentials.Body.Bytes(), want) ||
		withCredentials.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("authenticated image read = %d %v (%d bytes)", withCredentials.Code, withCredentials.Header(), withCredentials.Body.Len())
	}
}

// Image creation is offered only while every manager whose to-dos the list merges can persist them. An older manager
// must never receive a create whose additive images field it would drop (#11084).
func TestTodoImagesFeatureWhileEveryManagerNamesIt(t *testing.T) {
	h, _, a, b, _, _ := todoViewHub(t)
	listed := func() bool { return slices.Contains(h.features(), "todo_images") }
	todoViewCapabilities(t, a, "todo_images")
	todoViewCapabilities(t, b, assistantwire.CapabilityTodoView)
	if listed() {
		t.Fatal("listed while an older manager cannot store images")
	}
	todoViewCapabilities(t, b, "todo_images")
	if !listed() {
		t.Fatal("not listed with every manager capable of storing images")
	}
	todoViewCapabilities(t, a)
	if listed() {
		t.Fatal("listed after the default manager became too old to store images")
	}
}

// Create itself joins the existing operation-receipt seam: an accepted retry gets the first response without another
// manager write, while another title/body under that operation is a conflict. Clients without the header are unchanged.
func TestTodoImageCreateOperationReceipt(t *testing.T) {
	h := newTestHub(t)
	h.store = &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	manager, _ := connectInboxManager(t, h)
	var calls atomic.Int32
	manager.mu.Lock()
	manager.http = func(_ context.Context, r assistantwire.HTTPRequest) (any, error) {
		if r.Method != http.MethodPost || r.Path != "/api/v1/todos" {
			return assistantwire.HTTPResponse{Status: http.StatusNotFound, Body: []byte(`{"error":"not found"}`)}, nil
		}
		calls.Add(1)
		return assistantwire.HTTPResponse{Status: http.StatusOK, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: r.Body}, nil
	}
	manager.mu.Unlock()
	mux := h.routes()
	const op = "d0000000-0000-4000-8000-000000000010"
	const body = `{"title":"Create once","notes":"same accepted operation","images":[{"id":"d0000000-0000-4000-8000-000000000001","mime":"image/png","data":"aW1hZ2U="}]}`
	var first string
	for range 2 {
		code, out := postTodo(t, context.Background(), h, mux, "/api/v1/todos", op, body)
		if code != http.StatusOK {
			t.Fatalf("create/retry = %d %s", code, out)
		}
		if first == "" {
			first = out
		} else if out != first {
			t.Fatalf("retry returned a different result:\n%s\n%s", first, out)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("same accepted operation reached the manager %d times", got)
	}
	if code, out := postTodo(t, context.Background(), h, mux, "/api/v1/todos", op, strings.Replace(body, "Create once", "Conflicting title", 1)); code != http.StatusConflict || !strings.Contains(out, "op_id_reused") {
		t.Fatalf("conflicting reuse = %d %s", code, out)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("conflicting reuse reached the manager: %d calls", got)
	}
	for range 2 {
		if code, out := postTodo(t, context.Background(), h, mux, "/api/v1/todos", "", body); code != http.StatusOK {
			t.Fatalf("legacy create without an op id = %d %s", code, out)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("requests without an op id were unexpectedly deduplicated: %d calls", got)
	}
}

// An image-less create keeps its pre-image relay behavior even when a caller supplies an operation ID: no receipt is
// claimed, no 202 is invented and a repeated request reaches the manager again (#11084 review).
func TestTodoCreateWithoutImagesKeepsLegacyRelay(t *testing.T) {
	h := newTestHub(t)
	ops := &clientOpsStore{ops: map[string]db.ClientOp{}}
	h.store = &receiptStore{memStore: newMemStore(), ops: ops}
	manager, _ := connectInboxManager(t, h)
	var calls atomic.Int32
	manager.mu.Lock()
	manager.http = func(_ context.Context, r assistantwire.HTTPRequest) (any, error) {
		if r.Method != http.MethodPost || r.Path != "/api/v1/todos" {
			return assistantwire.HTTPResponse{Status: http.StatusNotFound, Body: []byte(`{"error":"not found"}`)}, nil
		}
		calls.Add(1)
		return assistantwire.HTTPResponse{Status: http.StatusOK, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: r.Body}, nil
	}
	manager.mu.Unlock()
	const op = "d0000000-0000-4000-8000-000000000012"
	for _, body := range []string{`{"title":"Plain create"}`, `{"title":"Empty images","images":[]}`, `{"title":"Null images","images":null}`} {
		for range 2 {
			if code, out := postTodo(t, context.Background(), h, h.routes(), "/api/v1/todos", op, body); code != http.StatusOK {
				t.Fatalf("legacy create %s = %d %s", body, code, out)
			}
		}
	}
	if got := calls.Load(); got != 6 {
		t.Fatalf("image-less creates were deduplicated: %d manager calls, want 6", got)
	}
	if len(ops.ops) != 0 {
		t.Fatalf("image-less creates claimed receipts: %+v", ops.ops)
	}
}

// Offline proves no create ran, so the operation may be tried again. In doubt keeps the original HTTP outcome instead of
// risking a second create: the two failures are deliberately not the same retry.
func TestTodoImageCreateOperationRetryOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		secondCode int
		calls      int32
	}{
		{"offline releases the operation", http.StatusServiceUnavailable, `{"code":"assistant_offline","error":"offline"}`, http.StatusOK, 2},
		{"in doubt replays the outcome", http.StatusBadGateway, `{"code":"assistant_in_doubt","delivered":"unknown","error":"lost"}`, http.StatusBadGateway, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			h.store = &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
			manager, _ := connectInboxManager(t, h)
			var calls atomic.Int32
			manager.mu.Lock()
			manager.http = func(_ context.Context, r assistantwire.HTTPRequest) (any, error) {
				if calls.Add(1) == 1 {
					return assistantwire.HTTPResponse{Status: tc.status, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(tc.body)}, nil
				}
				return assistantwire.HTTPResponse{Status: http.StatusOK, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: r.Body}, nil
			}
			manager.mu.Unlock()
			const op = "d0000000-0000-4000-8000-000000000011"
			const body = `{"title":"Create once","images":[{"id":"d0000000-0000-4000-8000-000000000001","mime":"image/png","data":"aW1hZ2U="}]}`
			firstCode, firstBody := postTodo(t, context.Background(), h, h.routes(), "/api/v1/todos", op, body)
			if firstCode != tc.status {
				t.Fatalf("first attempt = %d %s, want %d", firstCode, firstBody, tc.status)
			}
			secondCode, secondBody := postTodo(t, context.Background(), h, h.routes(), "/api/v1/todos", op, body)
			if secondCode != tc.secondCode {
				t.Fatalf("retry = %d %s, want %d", secondCode, secondBody, tc.secondCode)
			}
			if tc.status == http.StatusBadGateway && secondBody != firstBody {
				t.Fatalf("in-doubt retry changed the response:\n%s\n%s", firstBody, secondBody)
			}
			if got := calls.Load(); got != tc.calls {
				t.Fatalf("manager calls = %d, want %d", got, tc.calls)
			}
		})
	}
}

// todoImageRefusalNode lets a remote Agent enforce its model's image-input limit. It refuses only a call carrying an
// image, so a handoff that drops the bytes looks deceptively successful here and fails the acceptance assertion.
func todoImageRefusalNode(t *testing.T, h *Hub, id string) *Machine {
	t.Helper()
	m := newMachine(h, MachineConfig{ID: id, Name: id})
	registerMachine(h, m)
	hubIn, nodeOut := io.Pipe()
	nodeIn, hubOut := io.Pipe()
	hubPeer, nodePeer := rpc.New("h", hubOut), rpc.New("n", nodeOut)
	nodePeer.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if len(nodeImages(t, params)) != 0 {
			return nil, &rpc.Error{Code: "image_input_unsupported", Message: "the selected model does not support image input"}
		}
		if method == "session.start" {
			return map[string]any{"session": proto.Session{MachineID: id, SessionID: "s_new", Cwd: "/code", Agent: "claude"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = hubPeer.Serve(ctx, hubIn) }()
	go func() { _ = nodePeer.Serve(ctx, nodeIn) }()
	m.mu.Lock()
	m.peer, m.online = hubPeer, true
	m.mu.Unlock()
	return m
}

func TestTodoImageHandoffDoesNotHideUnsupportedImageInput(t *testing.T) {
	for _, handoff := range []string{"existing", "new"} {
		t.Run(handoff, func(t *testing.T) {
			h := newTestHub(t)
			m := todoImageRefusalNode(t, h, "remote-machine")
			m.sessions = append(m.sessions, proto.Session{MachineID: "remote-machine", SessionID: "s_old", Cwd: "/code", Agent: "claude"})
			connectAssistant(t, h)
			todoID, _ := createHubTodoImage(t, h, hubTodoPNG(t))
			var w *httptest.ResponseRecorder
			if handoff == "existing" {
				w = hubCall(h, http.MethodPost, "/api/v1/sessions/remote-machine/s_old/messages", `{"text":"Read this","todo_id":"`+todoID+`"}`)
			} else {
				w = hubCall(h, http.MethodPost, "/api/v1/sessions", `{"machine_id":"remote-machine","cwd":"/code","prompt":"Read this","todo_id":"`+todoID+`"}`)
			}
			if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "does not support image input") || strings.Contains(w.Body.String(), `"ok":true`) {
				t.Fatalf("unsupported image input was not surfaced explicitly: %d %s", w.Code, w.Body)
			}
		})
	}
}
