package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type machineFilesTestNode struct {
	data           []byte
	chunkRequests  []proto.MachineFileChunkRequest
	uploadChunks   []proto.MachineFileUploadChunkRequest
	uploadAborted  []string
	uploadMaxBytes int64
	uploadBuf      bytes.Buffer
	m              *Machine
	closeFn        func()
}

func (n *machineFilesTestNode) connect(t *testing.T) {
	t.Helper()
	toNode, nodeIn := io.Pipe()
	toHub, hubIn := io.Pipe()
	hubPeer, nodePeer := rpc.New("h", nodeIn), rpc.New("n", hubIn)
	nodePeer.OnRequest = n.handle
	ctx, cancel := context.WithCancel(context.Background())
	go hubPeer.Serve(ctx, toHub)
	go nodePeer.Serve(ctx, toNode)
	n.closeFn = func() {
		cancel()
		_ = toNode.Close()
		_ = toHub.Close()
		hubPeer.Close(rpc.ErrClosed)
	}
	n.m.mu.Lock()
	n.m.peer = hubPeer
	n.m.mu.Unlock()
	t.Cleanup(n.closeFn)
}

func (n *machineFilesTestNode) handle(_ context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case proto.MethodMachineFilesRoots:
		return proto.MachineFileRootsResponse{
			Home: "/home/dev",
			OS:   "linux",
			Roots: []proto.MachineFileRoot{
				{ID: "home", Label: "$HOME", Path: "/home/dev", Kind: "home", Exists: true},
			},
		}, nil
	case proto.MethodMachineFilesList:
		var req proto.MachineFileListRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		if req.Path == "/etc/shadow" {
			return nil, &rpc.Error{Code: proto.CodeMachineFileBoundary, Message: "path escapes the allowed root boundary"}
		}
		return proto.MachineFileListResponse{
			Path:          "/home/dev",
			CanonicalPath: "/home/dev",
			Entries: []proto.MachineFileEntry{
				{Name: ".env", Path: "/home/dev/.env", Kind: "file", Size: 16, Mode: "-rw-------", Perm: 0o600, Sensitive: true, SensitiveReason: "env"},
			},
			Total: 1,
		}, nil
	case proto.MethodMachineFilesPreview:
		var req proto.MachineFilePreviewRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		if req.Path == "/home/dev/image.png" {
			return proto.MachineFilePreviewResponse{
				Path:     req.Path,
				Name:     "image.png",
				Size:     1024,
				Mime:     "image/png",
				Revision: "r1",
				Binary:   true,
				MaxBytes: proto.MachineFilePreviewDefaultBytes,
			}, nil
		}
		return proto.MachineFilePreviewResponse{
			Path:      req.Path,
			Name:      "app.log",
			Size:      12,
			Mime:      "text/plain; charset=utf-8",
			Revision:  "r1",
			Text:      "hello world\n",
			BytesRead: 12,
			MaxBytes:  proto.MachineFilePreviewDefaultBytes,
		}, nil
	case proto.MethodMachineFilesTail:
		var req proto.MachineFileTailRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return proto.MachineFileTailResponse{
			Path:           req.Path,
			Name:           "app.log",
			Size:           10000,
			Mime:           "text/plain; charset=utf-8",
			Revision:       "r1",
			Text:           "last line\n",
			Lines:          1,
			RequestedLines: req.Lines,
			BytesRead:      10,
			Offset:         9990,
			Truncated:      true,
		}, nil
	case proto.MethodMachineFilesChunk:
		var req proto.MachineFileChunkRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		n.chunkRequests = append(n.chunkRequests, req)
		if req.Path == "/home/dev/.ssh/id_rsa" {
			return nil, &rpc.Error{Code: proto.CodeMachineFileBoundary, Message: "this path holds secrets and is not readable remotely"}
		}
		out := proto.MachineFileChunkResponse{
			Path:          req.Path,
			CanonicalPath: req.Path,
			Name:          "bundle.bin",
			Size:          int64(len(n.data)),
			Mime:          "application/octet-stream",
			ModTime:       time.Unix(1700000000, 0).UTC(),
			Revision:      "rev-1",
			Offset:        req.Offset,
		}
		if req.Length > 0 {
			end := min(int64(len(n.data)), req.Offset+int64(req.Length))
			out.Data = n.data[req.Offset:end]
		}
		return out, nil
	case proto.MethodMachineFilesWrite:
		var req proto.MachineFileWriteRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		if req.Path == "/home/dev/conflict.txt" {
			data, _ := json.Marshal(map[string]any{"revision": "rev-new", "sha256": "newsha"})
			return nil, &rpc.Error{Code: proto.CodeFileChanged, Message: "file was modified externally", Data: data}
		}
		if req.Path == "/home/dev/.env" && !req.ConfirmSensitive {
			data, _ := json.Marshal(map[string]any{"sensitive_reason": "env"})
			return nil, &rpc.Error{Code: proto.CodeMachineFileSensitiveConfirm, Message: "sensitive confirmation required", Data: data}
		}
		if req.Path == "/home/dev/image.png" {
			return nil, &rpc.Error{Code: proto.CodeMachineFileBinary, Message: "binary file"}
		}
		return proto.MachineFileWriteResponse{
			Path:          req.Path,
			CanonicalPath: req.Path,
			Name:          "notes.txt",
			Size:          int64(len(req.Text)),
			Mode:          "-rw-r--r--",
			Perm:          0o644,
			ModTime:       time.Unix(1700000100, 0).UTC(),
			Revision:      "rev-2",
			SHA256:        "sha-2",
		}, nil
	case proto.MethodMachineFilesCreate:
		var req proto.MachineFileCreateRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		if req.Name == "exists.txt" {
			return nil, &rpc.Error{Code: proto.CodeExists, Message: "already exists"}
		}
		return proto.MachineFileCreateResponse{
			Entry: proto.MachineFileEntry{
				Name: req.Name,
				Path: req.Dir + "/" + req.Name,
				Kind: req.Kind,
				Size: int64(len(req.Text)),
			},
			CanonicalPath: req.Dir + "/" + req.Name,
			Revision:      "rev-new",
			SHA256:        "sha-new",
		}, nil
	case proto.MethodMachineFilesRename:
		var req proto.MachineFileRenameRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		if req.DestPath == "/etc/escape" {
			return nil, &rpc.Error{Code: proto.CodeMachineFileBoundary, Message: "path escapes the allowed root boundary"}
		}
		return proto.MachineFileRenameResponse{
			OldPath:       req.Path,
			Path:          req.DestPath,
			CanonicalPath: req.DestPath,
			Name:          "renamed.txt",
			Entry:         proto.MachineFileEntry{Name: "renamed.txt", Path: req.DestPath, Kind: "file"},
		}, nil
	case proto.MethodMachineFilesDelete:
		var req proto.MachineFileDeleteRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		if req.Path == "/home/dev/nonempty" {
			return nil, &rpc.Error{Code: proto.CodeMachineFileNotEmpty, Message: "directory is not empty"}
		}
		if req.Path == "/home/dev/missing.txt" {
			return nil, &rpc.Error{Code: proto.CodeNotFound, Message: "not found"}
		}
		return proto.MachineFileDeleteResponse{
			Path:          req.Path,
			CanonicalPath: req.Path,
			Kind:          "file",
			Deleted:       true,
		}, nil
	case proto.MethodMachineFilesUploadBegin:
		var req proto.MachineFileUploadBeginRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		n.uploadBuf.Reset()
		maxB := int64(proto.MachineFileUploadMaxBytes)
		if n.uploadMaxBytes > 0 {
			maxB = n.uploadMaxBytes
		}
		if req.Size > maxB {
			return nil, &rpc.Error{Code: proto.CodeTooLarge, Message: "too large"}
		}
		return proto.MachineFileUploadBeginResponse{
			UploadID:   "up-1",
			Dir:        req.Dir,
			Name:       req.Name,
			MaxBytes:   maxB,
			ChunkBytes: proto.MachineFileUploadChunkBytes,
		}, nil
	case proto.MethodMachineFilesUploadChunk:
		var req proto.MachineFileUploadChunkRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		n.uploadChunks = append(n.uploadChunks, req)
		n.uploadBuf.Write(req.Data)
		return proto.MachineFileUploadChunkResponse{
			UploadID: req.UploadID,
			Written:  int64(n.uploadBuf.Len()),
		}, nil
	case proto.MethodMachineFilesUploadEnd:
		return proto.MachineFileUploadEndResponse{
			Entry: proto.MachineFileEntry{
				Name: "uploaded.bin",
				Path: "/home/dev/uploaded.bin",
				Kind: "file",
				Size: int64(n.uploadBuf.Len()),
			},
			CanonicalPath: "/home/dev/uploaded.bin",
			Size:          int64(n.uploadBuf.Len()),
			SHA256:        "upload-sha",
		}, nil
	case proto.MethodMachineFilesUploadAbort:
		var req proto.MachineFileUploadAbortRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		n.uploadAborted = append(n.uploadAborted, req.UploadID)
		return map[string]any{"aborted": true}, nil
	default:
		return nil, errors.New("unexpected method " + method)
	}
}

func machineFilesFixture(t *testing.T, data []byte) (*http.ServeMux, *machineFilesTestNode) {
	t.Helper()
	h := newTestHub(t)
	node := &machineFilesTestNode{data: data}
	node.m = newMachine(h, MachineConfig{ID: "m1", Name: "devbox"})
	h.byID["m1"] = node.m
	node.connect(t)
	mux := http.NewServeMux()
	h.machineFileRoutes(mux)
	return mux, node
}

func TestMachineFilesHubRoutesAndBoundaryError(t *testing.T) {
	mux, _ := machineFilesFixture(t, []byte("payload"))

	// Roots
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/machines/m1/files/roots", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("roots status=%d body=%s", rec.Code, rec.Body.String())
	}
	var roots proto.MachineFileRootsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &roots); err != nil || len(roots.Roots) != 1 {
		t.Fatalf("roots decode: %v %+v", err, roots)
	}

	// List
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/machines/m1/files/list?path=/home/dev", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Boundary error maps to 403 machine_file_boundary
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/machines/m1/files/list?path=/etc/shadow", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("boundary status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(proto.CodeMachineFileBoundary)) {
		t.Fatalf("expected boundary code in body: %s", rec.Body.String())
	}

	// Preview (text & binary) and Tail
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/machines/m1/files/preview?path=/home/dev/image.png", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("binary preview status=%d", rec.Code)
	}
	var prev proto.MachineFilePreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &prev); err != nil || !prev.Binary || prev.Text != "" {
		t.Fatalf("binary preview mismatch: %v %+v", err, prev)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/machines/m1/files/tail?path=/home/dev/app.log&lines=50", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("tail status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Missing path validation
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/machines/m1/files/preview", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing path status=%d want 400", rec.Code)
	}
}

func TestMachineFilesStreamingDownloadMultiChunkAndRange(t *testing.T) {
	// > 2 chunks so download must stream multiple bounded RPC chunks rather than one full buffer.
	data := bytes.Repeat([]byte{0xde, 0xad, 0xbe, 0xef}, (proto.MachineFileDownloadChunkBytes/2)+17)
	mux, node := machineFilesFixture(t, data)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/machines/m1/files/download?path=/home/dev/bundle.bin", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("download status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), data) {
		t.Fatalf("downloaded bytes length=%d want=%d", rec.Body.Len(), len(data))
	}
	if rec.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("missing sandbox CSP header")
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing nosniff header")
	}
	// Verify chunked streaming: 1 metadata call + >= 3 chunk calls, each <= MachineFileDownloadChunkBytes.
	if len(node.chunkRequests) < 4 {
		t.Fatalf("expected multi-chunk streaming, got %d requests", len(node.chunkRequests))
	}
	for _, q := range node.chunkRequests {
		if q.Length > proto.MachineFileDownloadChunkBytes {
			t.Fatalf("chunk request exceeded bound: %d", q.Length)
		}
	}

	// Range request (206 Partial Content).
	req := httptest.NewRequest("GET", "/api/v1/machines/m1/files/download?path=/home/dev/bundle.bin", nil)
	req.Header.Set("Range", "bytes=10-25")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range status=%d want 206", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), data[10:26]) {
		t.Fatalf("range body mismatch")
	}
	wantRange := "bytes 10-25/" + strconv.Itoa(len(data))
	if got := rec.Header().Get("Content-Range"); got != wantRange {
		t.Fatalf("Content-Range=%q want %q", got, wantRange)
	}
}

func TestMachineFilesWriteCreateRenameDeleteHubRoutes(t *testing.T) {
	mux, _ := machineFilesFixture(t, nil)

	// Missing CAS tokens -> 400
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/v1/machines/m1/files/content", bytes.NewBufferString(`{"path":"/home/dev/notes.txt","text":"hi"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing CAS status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}

	// Valid CAS write -> 200
	validWrite := `{"path":"/home/dev/notes.txt","text":"updated\n","expected_mtime":"2026-11-20T00:00:00Z","expected_sha256":"abc123"}`
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/v1/machines/m1/files/content", bytes.NewBufferString(validWrite)))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid write status=%d body=%s", rec.Code, rec.Body.String())
	}

	// CAS conflict -> 409 file_changed with structured details
	conflictWrite := `{"path":"/home/dev/conflict.txt","text":"stale","expected_mtime":"2026-11-20T00:00:00Z","expected_sha256":"old"}`
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/v1/machines/m1/files/content", bytes.NewBufferString(conflictWrite)))
	if rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte(proto.CodeFileChanged)) {
		t.Fatalf("conflict status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Sensitive file without confirm -> 412 machine_file_sensitive_confirm
	envWrite := `{"path":"/home/dev/.env","text":"A=1","expected_mtime":"2026-11-20T00:00:00Z","expected_sha256":"old"}`
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/v1/machines/m1/files/content", bytes.NewBufferString(envWrite)))
	if rec.Code != http.StatusPreconditionFailed || !bytes.Contains(rec.Body.Bytes(), []byte(proto.CodeMachineFileSensitiveConfirm)) {
		t.Fatalf("sensitive confirm status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Binary refusal -> 415
	binWrite := `{"path":"/home/dev/image.png","text":"x","expected_mtime":"2026-11-20T00:00:00Z","expected_sha256":"old"}`
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/v1/machines/m1/files/content", bytes.NewBufferString(binWrite)))
	if rec.Code != http.StatusUnsupportedMediaType || !bytes.Contains(rec.Body.Bytes(), []byte(proto.CodeMachineFileBinary)) {
		t.Fatalf("binary write status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Create file & conflict
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/machines/m1/files/create", bytes.NewBufferString(`{"dir":"/home/dev","name":"new.txt","kind":"file","text":"hello"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/machines/m1/files/create", bytes.NewBufferString(`{"dir":"/home/dev","name":"exists.txt","kind":"file"}`)))
	if rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte(proto.CodeExists)) {
		t.Fatalf("duplicate create status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Rename & boundary escape refusal
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("PATCH", "/api/v1/machines/m1/files/entry", bytes.NewBufferString(`{"path":"/home/dev/a.txt","dest_path":"/home/dev/b.txt"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("PATCH", "/api/v1/machines/m1/files/entry", bytes.NewBufferString(`{"path":"/home/dev/a.txt","dest_path":"/etc/escape"}`)))
	if rec.Code != http.StatusForbidden || !bytes.Contains(rec.Body.Bytes(), []byte(proto.CodeMachineFileBoundary)) {
		t.Fatalf("rename escape status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Delete: success, non-empty dir 409, missing 404
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/v1/machines/m1/files/entry", bytes.NewBufferString(`{"path":"/home/dev/a.txt"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/v1/machines/m1/files/entry?path=/home/dev/nonempty", nil))
	if rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte(proto.CodeMachineFileNotEmpty)) {
		t.Fatalf("delete non-empty status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/v1/machines/m1/files/entry?path=/home/dev/missing.txt", nil))
	if rec.Code != http.StatusNotFound || !bytes.Contains(rec.Body.Bytes(), []byte(proto.CodeNotFound)) {
		t.Fatalf("delete missing status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type failingReader struct {
	prefix []byte
	sent   bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		n := copy(p, r.prefix)
		return n, nil
	}
	return 0, errors.New("client disconnected mid-stream")
}

func TestMachineFilesStreamingUploadMultiChunkAndAbortCleanup(t *testing.T) {
	// > 2 chunks so Hub must stream chunk-by-chunk without buffering the whole payload.
	payload := bytes.Repeat([]byte{0xca, 0xfe, 0xba, 0xbe}, (proto.MachineFileUploadChunkBytes/2)+33)
	mux, node := machineFilesFixture(t, nil)

	req := httptest.NewRequest("POST", "/api/v1/machines/m1/files/upload?dir=/home/dev&name=uploaded.bin", bytes.NewReader(payload))
	req.ContentLength = int64(len(payload))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(node.uploadBuf.Bytes(), payload) {
		t.Fatalf("uploaded bytes mismatch: got=%d want=%d", node.uploadBuf.Len(), len(payload))
	}
	if len(node.uploadChunks) < 3 {
		t.Fatalf("expected >= 3 streaming chunks, got %d", len(node.uploadChunks))
	}
	for _, c := range node.uploadChunks {
		if len(c.Data) > proto.MachineFileUploadChunkBytes {
			t.Fatalf("upload chunk exceeded bound: %d", len(c.Data))
		}
	}
	if len(node.uploadAborted) != 0 {
		t.Fatalf("unexpected aborts on successful upload: %v", node.uploadAborted)
	}

	// Mid-stream disconnect / read error triggers machine.files.upload.abort cleanup.
	badReq := httptest.NewRequest("POST", "/api/v1/machines/m1/files/upload?dir=/home/dev&name=cut.bin", &failingReader{prefix: []byte("partial")})
	badReq.ContentLength = -1
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, badReq)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("aborted stream status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	if len(node.uploadAborted) != 1 || node.uploadAborted[0] != "up-1" {
		t.Fatalf("expected upload abort RPC on mid-stream disconnect, got %v", node.uploadAborted)
	}

	// Chunked upload exceeding node maxBytes returns 413 and aborts temp file.
	node.uploadMaxBytes = 1024
	node.uploadAborted = nil
	overReq := httptest.NewRequest("POST", "/api/v1/machines/m1/files/upload?dir=/home/dev&name=over.bin", bytes.NewReader(bytes.Repeat([]byte("z"), 2048)))
	overReq.ContentLength = -1
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, overReq)
	if rec.Code != http.StatusRequestEntityTooLarge || !bytes.Contains(rec.Body.Bytes(), []byte(proto.CodeTooLarge)) {
		t.Fatalf("oversized chunked upload status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(node.uploadAborted) != 1 {
		t.Fatalf("expected abort cleanup on oversized chunked upload, got %v", node.uploadAborted)
	}

	// Chunked upload at exact cap succeeds, while cap + 1 byte returns 413 (never silently truncated).
	node.uploadMaxBytes = int64(proto.MachineFileUploadChunkBytes)
	node.uploadAborted = nil
	exactPayload := bytes.Repeat([]byte("e"), proto.MachineFileUploadChunkBytes)
	exactReq := httptest.NewRequest("POST", "/api/v1/machines/m1/files/upload?dir=/home/dev&name=exact.bin", bytes.NewReader(exactPayload))
	exactReq.ContentLength = -1
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, exactReq)
	if rec.Code != http.StatusOK || !bytes.Equal(node.uploadBuf.Bytes(), exactPayload) {
		t.Fatalf("exact-cap chunked upload status=%d got=%d want=%d body=%s", rec.Code, node.uploadBuf.Len(), len(exactPayload), rec.Body.String())
	}
	if len(node.uploadAborted) != 0 {
		t.Fatalf("unexpected abort on exact-cap chunked upload: %v", node.uploadAborted)
	}

	node.uploadAborted = nil
	plusOnePayload := bytes.Repeat([]byte("o"), proto.MachineFileUploadChunkBytes+1)
	plusOneReq := httptest.NewRequest("POST", "/api/v1/machines/m1/files/upload?dir=/home/dev&name=plus1.bin", bytes.NewReader(plusOnePayload))
	plusOneReq.ContentLength = -1
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, plusOneReq)
	if rec.Code != http.StatusRequestEntityTooLarge || !bytes.Contains(rec.Body.Bytes(), []byte(proto.CodeTooLarge)) {
		t.Fatalf("cap+1 chunked upload status=%d want 413 body=%s", rec.Code, rec.Body.String())
	}
	if len(node.uploadAborted) != 1 {
		t.Fatalf("expected abort cleanup on cap+1 chunked upload, got %v", node.uploadAborted)
	}

	// Under-reported Content-Length whose stream continues past maxBytes also returns 413 and aborts.
	node.uploadAborted = nil
	underReq := httptest.NewRequest("POST", "/api/v1/machines/m1/files/upload?dir=/home/dev&name=under.bin", bytes.NewReader(plusOnePayload))
	underReq.ContentLength = int64(proto.MachineFileUploadChunkBytes)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, underReq)
	if rec.Code != http.StatusRequestEntityTooLarge || !bytes.Contains(rec.Body.Bytes(), []byte(proto.CodeTooLarge)) {
		t.Fatalf("under-reported oversized upload status=%d want 413 body=%s", rec.Code, rec.Body.String())
	}
	if len(node.uploadAborted) != 1 {
		t.Fatalf("expected abort cleanup on under-reported oversized upload, got %v", node.uploadAborted)
	}
}

func TestMachineFilesRoutesRequireFullCredentialsOnly(t *testing.T) {
	patterns := routePatterns(t)
	for _, p := range []string{
		"GET /api/v1/machines/{mid}/files/roots",
		"GET /api/v1/machines/{mid}/files/list",
		"GET /api/v1/machines/{mid}/files/preview",
		"GET /api/v1/machines/{mid}/files/tail",
		"GET /api/v1/machines/{mid}/files/download",
		"PUT /api/v1/machines/{mid}/files/content",
		"POST /api/v1/machines/{mid}/files/create",
		"PATCH /api/v1/machines/{mid}/files/entry",
		"DELETE /api/v1/machines/{mid}/files/entry",
		"POST /api/v1/machines/{mid}/files/upload",
	} {
		if !slices.Contains(patterns, p) {
			t.Errorf("missing machine files route %s", p)
		}
		if scope := extwire.RouteScopes[p]; scope != "" {
			t.Errorf("machine files route %s must not grant extension scope %q", p, scope)
		}
	}
}
