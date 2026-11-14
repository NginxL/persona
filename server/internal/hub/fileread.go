package hub

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// readFile is a file for the viewers (node file.read). A node from before #7974 calls every .ts video/mp2t by its
// name, TypeScript too: when a streamed answer says video/mp2t, the hub reads the file's first packets and, when they
// are text, asks again without stream for the text page, so every client shows code whatever its computer's node is.
func (h *Hub) readFile(w http.ResponseWriter, r *http.Request, m *Machine) {
	q := r.URL.Query()
	stream := q.Get("stream") == "1"
	params := map[string]any{"path": q.Get("path"), "cwd": q.Get("cwd"), "offset": atoi(q.Get("offset")), "stream": stream}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var out json.RawMessage
	if err := m.call(ctx, "file.read", params, &out); err != nil {
		callErr(w, err)
		return
	}
	if stream && streamedScript(ctx, m, q.Get("cwd"), out) {
		params["stream"] = false
		if err := m.call(ctx, "file.read", params, &out); err != nil {
			callErr(w, err)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(out)
}

// streamedScript: a streamed file.read answer typed video/mp2t whose first bytes are text, not a transport stream
// (proto.ScriptOrStream). A file that cannot be read now (or a node without file.chunk) keeps the node's answer.
func streamedScript(ctx context.Context, m *Machine, cwd string, read json.RawMessage) bool {
	var got struct {
		Path     string `json:"path"`
		Mime     string `json:"mime"`
		Stream   bool   `json:"stream"`
		Revision string `json:"revision"`
	}
	if json.Unmarshal(read, &got) != nil || !got.Stream {
		return false
	}
	if t, _, err := mime.ParseMediaType(got.Mime); err != nil || t != proto.MimeTransportStream {
		return false
	}
	var head proto.FileChunk
	p := proto.FileChunkRequest{Path: got.Path, Cwd: cwd, Length: proto.ScriptOrStreamProbe, Revision: got.Revision}
	if err := m.call(ctx, "file.chunk", p, &head); err != nil {
		return false
	}
	return proto.ScriptOrStream(head.Data) == proto.MimeTypeScript
}
