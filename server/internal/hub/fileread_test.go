package hub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// oldTSNode answers file.read as a node from before #7974 did: by the name, so every .ts is video/mp2t and a streamed
// read only describes it. Without stream it sends text when the bytes are text, as nodes always have.
type oldTSNode struct {
	mu      sync.Mutex
	files   map[string][]byte
	noChunk bool
	calls   []string
}

func (f *oldTSNode) handle(_ context.Context, method string, params json.RawMessage) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method)
	var p struct {
		Path   string `json:"path"`
		Stream bool   `json:"stream"`
		Length int    `json:"length"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	data, ok := f.files[p.Path]
	if !ok {
		return nil, errors.New("no such file")
	}
	switch {
	case method == "file.read" && p.Stream:
		return map[string]any{"path": p.Path, "name": filepath.Base(p.Path), "size": len(data), "mime": "video/mp2t", "stream": true, "revision": "r1"}, nil
	case method == "file.read" && utf8.Valid(data):
		return map[string]any{"path": p.Path, "name": filepath.Base(p.Path), "size": len(data), "mime": "text/plain; charset=utf-8", "text": string(data)}, nil
	case method == "file.read":
		return map[string]any{"path": p.Path, "name": filepath.Base(p.Path), "size": len(data), "mime": "video/mp2t", "data": base64.StdEncoding.EncodeToString(data)}, nil
	case method == "file.chunk" && !f.noChunk:
		return proto.FileChunk{Path: p.Path, Name: filepath.Base(p.Path), Size: int64(len(data)), Mime: "video/mp2t", Revision: "r1",
			Modified: time.Unix(1700000000, 0), Data: data[:min(len(data), p.Length)]}, nil
	}
	return nil, errors.New("unknown method " + method)
}

func fileReadHub(t *testing.T, f *oldTSNode) http.Handler {
	t.Helper()
	h := newTestHub(t)
	toNode, nodeIn := io.Pipe()
	toHub, hubIn := io.Pipe()
	hubPeer, nodePeer := rpc.New("h", nodeIn), rpc.New("n", hubIn)
	nodePeer.OnRequest = f.handle
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); toNode.Close(); toHub.Close() })
	go hubPeer.Serve(ctx, toHub)
	go nodePeer.Serve(ctx, toNode)
	m := newMachine(h, MachineConfig{ID: "m1", Name: "box"})
	m.peer = hubPeer
	h.byID["m1"] = m
	api := http.NewServeMux()
	h.fileRoutes(api)
	return api
}

// The hub types a .ts by its bytes whatever node the computer runs (#7974): a real TypeScript module comes back as
// text for the code view, a real MPEG-TS segment stays a streamed video.
func TestFileReadTypesTSByItsBytesForOldNodes(t *testing.T) {
	fixtures := filepath.Join("..", "..", "..", "spec", "fixtures", "file-types")
	script, err := os.ReadFile(filepath.Join(fixtures, "typescript.ts"))
	if err != nil {
		t.Fatal(err)
	}
	segment, err := os.ReadFile(filepath.Join(fixtures, "segment.ts"))
	if err != nil {
		t.Fatal(err)
	}
	read := func(api http.Handler, query string) (out struct {
		Mime   string `json:"mime"`
		Text   string `json:"text"`
		Stream bool   `json:"stream"`
	}) {
		t.Helper()
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/machines/m1/file?"+query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	node := &oldTSNode{files: map[string][]byte{"/w/app.ts": script, "/w/clip.ts": segment}}
	api := fileReadHub(t, node)
	if got := read(api, "path=/w/app.ts&stream=1"); got.Stream || got.Text != string(script) {
		t.Fatalf("TypeScript = %+v", got)
	}
	if got := read(api, "path=/w/clip.ts&stream=1"); !got.Stream || got.Mime != proto.MimeTransportStream || got.Text != "" {
		t.Fatalf("MPEG-TS = %+v", got)
	}
	if got := read(api, "path=/w/app.ts"); got.Text != string(script) { // without stream the node's answer as it is
		t.Fatalf("TypeScript without stream = %+v", got)
	}
	node.mu.Lock()
	calls := node.calls
	node.mu.Unlock()
	want := []string{"file.read", "file.chunk", "file.read", "file.read", "file.chunk", "file.read"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", calls, want)
		}
	}
	// A node that cannot read the first bytes: its own answer stands.
	if got := read(fileReadHub(t, &oldTSNode{files: node.files, noChunk: true}), "path=/w/app.ts&stream=1"); !got.Stream || got.Mime != proto.MimeTransportStream {
		t.Fatalf("without file.chunk = %+v", got)
	}
}
