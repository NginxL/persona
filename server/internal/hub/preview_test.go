package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/preview"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/streams"
)

// previewNode is a node with previews as the hub sees it: preview.open starts a stream whose far end says ready, sends
// one picture and echoes each message; preview.servers and preview.start answer with what they were asked. open,
// when given, decides what preview.open says instead.
func previewNode(t *testing.T, h *Hub, sessions []proto.Session, open func(params json.RawMessage) error) (*Machine, *[]string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide, nodeSide := rpc.New("h", h2nW), rpc.New("n", n2hW)
	hubMux, nodeMux := streams.New(hubSide.Notify), streams.New(nodeSide.Notify)
	hubSide.OnEvent = func(e string, p json.RawMessage) { hubMux.Event(e, p) }
	nodeSide.OnEvent = func(e string, p json.RawMessage) { nodeMux.Event(e, p) }
	var mu sync.Mutex
	calls := &[]string{}
	nodeSide.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		mu.Lock()
		*calls = append(*calls, method+" "+string(params))
		mu.Unlock()
		switch method {
		case "preview.servers", "preview.start":
			return map[string]any{"asked": json.RawMessage(params)}, nil
		case "preview.open":
			if open != nil {
				if err := open(params); err != nil {
					return nil, err
				}
			}
			var p struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(params, &p)
			mine, theirs := net.Pipe()
			if err := nodeMux.Open(p.ID); err != nil {
				return nil, err
			}
			nodeMux.Start(p.ID, theirs)
			go func() {
				defer mine.Close()
				_ = preview.WriteFrame(mine, preview.Text, []byte(`{"t":"ready"}`))
				_ = preview.WriteFrame(mine, preview.Binary, []byte("f\x00\x00\x00\x01jpeg"))
				br := bufio.NewReader(mine)
				for {
					kind, b, err := preview.ReadFrame(br)
					if err != nil {
						return
					}
					_ = preview.WriteFrame(mine, kind, append([]byte("echo:"), b...))
				}
			}()
			return map[string]any{"browser": "Chromium 131"}, nil
		}
		return nil, fmt.Errorf("unknown method %s", method)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = hubSide.Serve(ctx, n2hR) }()
	go func() { defer wg.Done(); _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); h2nW.Close(); n2hW.Close(); wg.Wait() })
	m := newMachine(h, MachineConfig{ID: "m1", Name: "dev box"})
	m.peer, m.online, m.streams = hubSide, true, hubMux
	m.sessions = sessions
	m.info = proto.NodeInfo{Version: "test", Protocol: 1, Capabilities: []string{nodeproto.Sessions, nodeproto.Preview}}
	h.byID["m1"] = m
	return m, calls
}

func previewURL(srv *httptest.Server, port int, query string) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/machines/m1/preview/connect?port=" + strconv.Itoa(port) + query
}

// The hub carries a preview's messages as they are, text as text and pictures as binary, both ways.
func TestPreviewRelaysMessages(t *testing.T) {
	h, srv := portHub(t)
	_, port := devServer(t)
	_, calls := previewNode(t, h, []proto.Session{{SessionID: "s1", Ports: []proto.Port{{Port: port}}}}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, previewURL(srv, port, "&path=/settings&w=800&h=600&dpr=2&mobile=1"),
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer test-token"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	for _, want := range []struct {
		typ websocket.MessageType
		msg string
	}{{websocket.MessageText, `{"t":"ready"}`}, {websocket.MessageBinary, "f\x00\x00\x00\x01jpeg"}} {
		typ, b, err := c.Read(ctx)
		if err != nil || typ != want.typ || string(b) != want.msg {
			t.Fatalf("read %v %q %v, want %v %q", typ, b, err, want.typ, want.msg)
		}
	}
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"t":"ack","seq":1}`)); err != nil {
		t.Fatal(err)
	}
	if typ, b, err := c.Read(ctx); err != nil || typ != websocket.MessageText || string(b) != `echo:{"t":"ack","seq":1}` {
		t.Fatalf("echo: %v %q %v", typ, b, err)
	}
	if len(*calls) != 1 || !strings.Contains((*calls)[0], `"path":"/settings"`) || !strings.Contains((*calls)[0], `"w":800`) ||
		!strings.Contains((*calls)[0], `"dpr":2`) || !strings.Contains((*calls)[0], `"mobile":true`) {
		t.Fatalf("preview.open was asked %v", *calls)
	}
}

func upgrade(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", "http"+strings.TrimPrefix(url, "ws"), nil)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	return res.StatusCode, body
}

// Refused before the upgrade, with what the panel needs to say why.
func TestPreviewRefusals(t *testing.T) {
	h, srv := portHub(t)
	_, port := devServer(t)
	_, own := devServer(t)
	var why error
	m, _ := previewNode(t, h, []proto.Session{{SessionID: "s1", Ports: []proto.Port{{Port: port}}}}, func(json.RawMessage) error { return why })

	if got, _ := upgrade(t, previewURL(srv, own, "")); got != http.StatusForbidden {
		t.Errorf("a port nobody opened: %d", got)
	}
	if got, _ := upgrade(t, previewURL(srv, 0, "")); got != http.StatusBadRequest {
		t.Errorf("port 0: %d", got)
	}
	// what the node says when it cannot open one comes after the upgrade, as a message (a browser cannot read why a
	// WebSocket was refused)
	for code, msg := range map[string]string{"port_gone": "nothing is listening on port", "no_browser": "no Chrome, Chromium, Edge or Brave on this computer"} {
		why = &rpc.Error{Code: code, Message: msg}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		c, _, err := websocket.Dial(ctx, previewURL(srv, port, ""), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer test-token"}}})
		if err != nil {
			t.Fatalf("%s: %v", code, err)
		}
		_, b, err := c.Read(ctx)
		var got map[string]any
		_ = json.Unmarshal(b, &got)
		if err != nil || got["t"] != "error" || got["code"] != code || got["error"] != msg {
			t.Errorf("%s: %s %v", code, b, err)
		}
		if _, _, err := c.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
			t.Errorf("%s: not closed after the error: %v", code, err)
		}
		cancel()
		c.CloseNow()
	}
	why = nil
	m.info = proto.NodeInfo{Version: "old", Protocol: 1, Capabilities: []string{nodeproto.Sessions}}
	if got, body := upgrade(t, previewURL(srv, port, "")); got != http.StatusConflict || body["code"] != proto.CodeNodeOutdated {
		t.Errorf("an old node: %d %v", got, body)
	}
	m.info = proto.NodeInfo{Version: "older", Protocol: 0}
	if got, _ := upgrade(t, previewURL(srv, port, "")); got != http.StatusConflict {
		t.Errorf("a node from before the handshake: %d", got)
	}
	m.info = proto.NodeInfo{Version: "test", Protocol: 1, Capabilities: []string{nodeproto.Sessions, nodeproto.Preview}}
	m.mu.Lock()
	m.online = false
	m.mu.Unlock()
	if got, _ := upgrade(t, previewURL(srv, port, "")); got != http.StatusServiceUnavailable {
		t.Errorf("offline: %d", got)
	}
	m.mu.Lock()
	m.online = true
	m.mu.Unlock()

	// A read-only device (the watch) neither previews nor starts servers.
	for name, fn := range map[string]func(http.ResponseWriter, *http.Request, *Machine){"connect": h.previewConnect, "start": h.previewStart} {
		ro := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/machines/m1/preview/connect?port="+strconv.Itoa(port), strings.NewReader(`{"cwd":"/p","name":"web","workspace":"w1"}`))
		req.SetPathValue("mid", "m1")
		req = req.WithContext(context.WithValue(req.Context(), principalKey{}, principal{Role: roleAnswer}))
		h.withMachine(fn)(ro, req)
		if ro.Code != http.StatusForbidden {
			t.Errorf("read-only device, %s: %d", name, ro.Code)
		}
	}
}

// servers and start go to the node as asked; start needs what it runs.
func TestPreviewServersAndStart(t *testing.T) {
	h, srv := portHub(t)
	_, calls := previewNode(t, h, nil, nil)
	do := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-token")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := do("GET", "/api/v1/machines/m1/preview/servers?cwd=/home/me/proj", ""); code != 200 || !strings.Contains(body, `"cwd":"/home/me/proj"`) {
		t.Errorf("servers: %d %s", code, body)
	}
	if code, _ := do("GET", "/api/v1/machines/m1/preview/servers", ""); code != http.StatusBadRequest {
		t.Errorf("servers without cwd: %d", code)
	}
	if code, body := do("POST", "/api/v1/machines/m1/preview/start", `{"cwd":"/home/me/proj","name":"web","workspace":"w1"}`); code != 200 || !strings.Contains(body, `"name":"web"`) {
		t.Errorf("start: %d %s", code, body)
	}
	if code, _ := do("POST", "/api/v1/machines/m1/preview/start", `{"cwd":"/home/me/proj","name":"web"}`); code != http.StatusBadRequest {
		t.Errorf("start without a workspace: %d", code)
	}
	if code, body := do("POST", "/api/v1/machines/m1/preview/start", `{"cwd":"`+strings.Repeat("x", 64<<10)+`","name":"web","workspace":"w1"}`); code != http.StatusRequestEntityTooLarge || !strings.Contains(body, `"code":"too_large"`) {
		t.Errorf("oversized start: %d %s", code, body)
	}
	if len(*calls) != 2 {
		t.Errorf("node calls: %v", *calls)
	}
}
