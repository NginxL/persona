package assistant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// echoDevices answers every relayed request itself with what the route would see of it.
type echoDevices struct {
	fakeDevices
	served atomic.Int64
}

func (e *echoDevices) Handler(http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.served.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sum := sha256.Sum256(body)
		approver, _ := assistantwire.HandoffApproverOf(r.Context())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"method": r.Method, "path": r.URL.EscapedPath(), "query": r.URL.RawQuery,
			"header": r.Header, "content_length": r.ContentLength, "size": len(body), "sum": hex.EncodeToString(sum[:]),
			"human": assistantwire.HumanStartOf(r.Context()), "approver": approver, "caller": devicewire.CallerOf(r.Context())})
	})
}

// relayFake is the hub's relay channel: each id writes what its function says.
func relayFake(t *testing.T, token string, ids map[string]func(ctx context.Context, c *websocket.Conn)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+devicewire.RelayPath, func(w http.ResponseWriter, r *http.Request) {
		write, ok := ids[r.URL.Query().Get("id")]
		if r.Header.Get("Authorization") != "Bearer "+token || !ok {
			http.Error(w, "no such relay", http.StatusNotFound)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		write(r.Context(), c)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// http.request_streamed is http.request with the body fetched from the relay first: the route sees the same
// request (method, escaped path, query, headers, length, body, the hub's hand-off approver, human start and caller)
// and the hub gets the same answer, byte for byte. A body that does not arrive whole is refused before the route runs.
func TestStreamedRequestIsTheInlineOne(t *testing.T) {
	body := make([]byte, 1<<20+3)
	if _, err := rand.Read(body); err != nil {
		t.Fatal(err)
	}
	const token = "jb_x"
	srv := relayFake(t, token, map[string]func(context.Context, *websocket.Conn){
		"whole": func(ctx context.Context, c *websocket.Conn) { _ = bus.WriteBody(ctx, c, body) },
		"short": func(ctx context.Context, c *websocket.Conn) { _ = bus.WriteBody(ctx, c, body[:len(body)-1]) },
		"cut": func(ctx context.Context, c *websocket.Conn) {
			_ = c.Write(ctx, websocket.MessageBinary, body[:bus.BodyChunk])
			_ = c.CloseNow()
		},
	})
	devices := &echoDevices{}
	b := NewBusHub(BusOptions{Hub: srv.URL, Token: token, HTTPClient: srv.Client(), Devices: devices})
	b.helloOnce.Do(func() { close(b.helloed) })
	b.mu.Lock()
	b.svc = New(Options{Hub: newFakeHub()})
	b.mu.Unlock()
	req := assistantwire.HTTPRequest{Method: http.MethodPost, Path: "/api/v1/echo/a%2Fb", Query: "x=1&y=%20",
		Header:          map[string][]string{"Content-Type": {"application/json"}, assistantwire.RoleHeader: {"full"}, "X-Test": {"one", "two"}},
		Caller:          &devicewire.Caller{Kind: "device", CredentialID: "cred-1"},
		HandoffApprover: &assistantwire.HandoffApprover{DeviceID: "device-1", DeviceName: "Phone"},
		Human:           true, Body: body}
	call := func(method string, r assistantwire.HTTPRequest) (assistantwire.HTTPResponse, error) {
		t.Helper()
		params, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := b.request(ctx, method, params)
		if err != nil {
			return assistantwire.HTTPResponse{}, err
		}
		return out.(assistantwire.HTTPResponse), nil
	}
	streamed := func(id string) assistantwire.HTTPRequest {
		r := req
		r.Body, r.BodyStream = nil, &assistantwire.BodyStream{ID: id, Size: int64(len(body))}
		return r
	}

	inline, err := call(assistantwire.MethodHTTP, req)
	if err != nil || inline.Status != http.StatusCreated {
		t.Fatalf("inline: %v %+v", err, inline)
	}
	got, err := call(assistantwire.MethodHTTPStreamed, streamed("whole"))
	if err != nil {
		t.Fatalf("streamed: %v", err)
	}
	if !reflect.DeepEqual(got, inline) {
		t.Fatalf("streamed answer differs:\n%s\ninline:\n%s", got.Body, inline.Body)
	}
	var seen struct {
		Size  int    `json:"size"`
		Human bool   `json:"human"`
		Sum   string `json:"sum"`
	}
	if err := json.Unmarshal(got.Body, &seen); err != nil || seen.Size != len(body) || !seen.Human {
		t.Fatalf("what the route saw: %v %s", err, got.Body)
	}

	before := devices.served.Load()
	for _, id := range []string{"short", "cut", "unknown"} {
		_, err := call(assistantwire.MethodHTTPStreamed, streamed(id))
		var coded *rpc.Error
		if !errors.As(err, &coded) || coded.Code != assistantwire.CodeBodyIncomplete {
			t.Fatalf("%s: %v, want %s", id, err, assistantwire.CodeBodyIncomplete)
		}
	}
	noStream := req
	noStream.Body = nil
	if _, err := call(assistantwire.MethodHTTPStreamed, noStream); err == nil {
		t.Fatal("a streamed request without a body stream was served")
	}
	if n := devices.served.Load() - before; n != 0 {
		t.Fatalf("the route ran %d times for bodies that did not arrive whole", n)
	}
}
