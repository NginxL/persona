package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// machineFileRoutes registers human-only machine file browser endpoints (Phase 6A read-only + Phase 6B write/upload).
// These routes require full credentials (no extension scope) and expose zero AI MCP tools.
func (h *Hub) machineFileRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/machines/{mid}/files/roots", h.withMachine(h.machineFileRoots))
	api.HandleFunc("GET /api/v1/machines/{mid}/files/list", h.withMachine(h.machineFileList))
	api.HandleFunc("GET /api/v1/machines/{mid}/files/preview", h.withMachine(h.machineFilePreview))
	api.HandleFunc("GET /api/v1/machines/{mid}/files/tail", h.withMachine(h.machineFileTail))
	api.HandleFunc("GET /api/v1/machines/{mid}/files/download", h.withMachine(h.machineFileDownload))
	api.HandleFunc("PUT /api/v1/machines/{mid}/files/content", h.withMachine(h.machineFileWrite))
	api.HandleFunc("POST /api/v1/machines/{mid}/files/create", h.withMachine(h.machineFileCreate))
	api.HandleFunc("PATCH /api/v1/machines/{mid}/files/entry", h.withMachine(h.machineFileRename))
	api.HandleFunc("DELETE /api/v1/machines/{mid}/files/entry", h.withMachine(h.machineFileDelete))
	api.HandleFunc("POST /api/v1/machines/{mid}/files/upload", h.withMachine(h.machineFileUpload))
}

func (h *Hub) machineFileCallErr(w http.ResponseWriter, r *http.Request, err error) {
	if h.nodeOutdated(w, r, err, "hub-download.outdated_file", nil) {
		return
	}
	var re *rpc.Error
	if errors.As(err, &re) {
		var extra map[string]any
		if len(re.Data) > 0 {
			_ = json.Unmarshal(re.Data, &extra)
		}
		switch re.Code {
		case proto.CodeMachineFileBoundary:
			writeCode(w, http.StatusForbidden, proto.CodeMachineFileBoundary, re.Message, extra)
			return
		case proto.CodeMachineFileBinary:
			writeCode(w, http.StatusUnsupportedMediaType, proto.CodeMachineFileBinary, re.Message, extra)
			return
		case proto.CodeFileChanged:
			writeCode(w, http.StatusConflict, proto.CodeFileChanged, re.Message, extra)
			return
		case proto.CodeExists:
			writeCode(w, http.StatusConflict, proto.CodeExists, re.Message, extra)
			return
		case proto.CodeMachineFileNotEmpty:
			writeCode(w, http.StatusConflict, proto.CodeMachineFileNotEmpty, re.Message, extra)
			return
		case proto.CodeMachineFileSensitiveConfirm:
			writeCode(w, http.StatusPreconditionFailed, proto.CodeMachineFileSensitiveConfirm, re.Message, extra)
			return
		case proto.CodeNotFound:
			writeCode(w, http.StatusNotFound, proto.CodeNotFound, re.Message, extra)
			return
		case proto.CodeTooLarge:
			writeCode(w, http.StatusRequestEntityTooLarge, proto.CodeTooLarge, re.Message, extra)
			return
		}
	}
	callErr(w, err)
}

func (h *Hub) machineFileRoots(w http.ResponseWriter, r *http.Request, m *Machine) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var out proto.MachineFileRootsResponse
	if err := m.call(ctx, proto.MethodMachineFilesRoots, map[string]any{}, &out); err != nil {
		h.machineFileCallErr(w, r, err)
		return
	}
	writeJSON(w, out)
}

func (h *Hub) machineFileList(w http.ResponseWriter, r *http.Request, m *Machine) {
	q := r.URL.Query()
	req := proto.MachineFileListRequest{
		Path:  q.Get("path"),
		Root:  q.Get("root"),
		Limit: atoi(q.Get("limit")),
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var out proto.MachineFileListResponse
	if err := m.call(ctx, proto.MethodMachineFilesList, req, &out); err != nil {
		h.machineFileCallErr(w, r, err)
		return
	}
	writeJSON(w, out)
}

func (h *Hub) machineFilePreview(w http.ResponseWriter, r *http.Request, m *Machine) {
	q := r.URL.Query()
	req := proto.MachineFilePreviewRequest{
		Path:     q.Get("path"),
		Root:     q.Get("root"),
		MaxBytes: atoi(q.Get("max_bytes")),
	}
	if strings.TrimSpace(req.Path) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var out proto.MachineFilePreviewResponse
	if err := m.call(ctx, proto.MethodMachineFilesPreview, req, &out); err != nil {
		h.machineFileCallErr(w, r, err)
		return
	}
	writeJSON(w, out)
}

func (h *Hub) machineFileTail(w http.ResponseWriter, r *http.Request, m *Machine) {
	q := r.URL.Query()
	req := proto.MachineFileTailRequest{
		Path:     q.Get("path"),
		Root:     q.Get("root"),
		Lines:    atoi(q.Get("lines")),
		MaxBytes: atoi(q.Get("max_bytes")),
	}
	if strings.TrimSpace(req.Path) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var out proto.MachineFileTailResponse
	if err := m.call(ctx, proto.MethodMachineFilesTail, req, &out); err != nil {
		h.machineFileCallErr(w, r, err)
		return
	}
	writeJSON(w, out)
}

// machineFileDownload streams a single regular file chunk-by-chunk over HTTP without buffering the whole file in memory.
func (h *Hub) machineFileDownload(w http.ResponseWriter, r *http.Request, m *Machine) {
	q := r.URL.Query()
	req := proto.MachineFileChunkRequest{
		Path: q.Get("path"),
		Root: q.Get("root"),
	}
	if strings.TrimSpace(req.Path) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	fetch := func(p proto.MachineFileChunkRequest) (*proto.MachineFileChunkResponse, error) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		var out proto.MachineFileChunkResponse
		if err := m.call(ctx, proto.MethodMachineFilesChunk, p, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}

	info, err := fetch(req)
	if err != nil {
		h.machineFileCallErr(w, r, err)
		return
	}
	if info.Size < 0 || info.Revision == "" {
		writeErr(w, http.StatusBadGateway, errors.New("invalid file metadata"))
		return
	}
	req.Path = info.Path
	req.Revision = info.Revision

	contentType := info.Mime
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", fmt.Sprintf("%q", info.Revision))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")

	reader := &machineFileStreamReader{
		fetch:   fetch,
		request: req,
		size:    info.Size,
		back:    func(pause time.Duration, until time.Time) bool { return awaitMachine(r.Context(), m, pause, until) },
	}
	http.ServeContent(w, r, info.Name, info.ModTime, reader)
	if failed := reader.failed.Load(); failed != nil {
		if r.Context().Err() == nil {
			h.logf("[%s] machine file download from %s aborted at byte %d of %d: %v",
				w.Header().Get("X-Request-Id"), m.cfg.ID, failed.at, info.Size, failed.err)
		}
		panic(http.ErrAbortHandler)
	}
}

type machineFileStreamReader struct {
	fetch   func(proto.MachineFileChunkRequest) (*proto.MachineFileChunkResponse, error)
	back    func(pause time.Duration, until time.Time) bool
	request proto.MachineFileChunkRequest
	size    int64
	pos     int64
	buf     []byte
	failed  atomic.Pointer[readFailure]
}

func (f *machineFileStreamReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if f.pos >= f.size {
		return 0, io.EOF
	}
	if len(f.buf) == 0 {
		q := f.request
		q.Offset = f.pos
		q.Length = int(min(int64(proto.MachineFileDownloadChunkBytes), f.size-f.pos))
		chunk, err := f.readChunk(q)
		if err == nil && (chunk.Offset != q.Offset || chunk.Size != f.size || chunk.Revision != q.Revision || len(chunk.Data) != q.Length) {
			err = errors.New("file chunk does not match the download")
		}
		if err != nil {
			f.failed.Store(&readFailure{at: q.Offset, err: err})
			return 0, err
		}
		f.buf = chunk.Data
	}
	n := copy(p, f.buf)
	f.buf = f.buf[n:]
	f.pos += int64(n)
	return n, nil
}

func (f *machineFileStreamReader) readChunk(q proto.MachineFileChunkRequest) (*proto.MachineFileChunkResponse, error) {
	var until time.Time
	pause := downloadRetryPoll
	for {
		c, err := f.fetch(q)
		if err == nil || f.back == nil || !nodeAway(err) {
			return c, err
		}
		if until.IsZero() {
			until = time.Now().Add(downloadRetryWindow)
		}
		if !f.back(pause, until) {
			return nil, err
		}
		pause = min(2*pause, downloadRetryMax)
	}
}

func (f *machineFileStreamReader) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = f.pos + offset
	case io.SeekEnd:
		target = f.size + offset
	default:
		return 0, errors.New("invalid seek origin")
	}
	if target < 0 || target > f.size {
		return 0, errors.New("seek outside the file")
	}
	if target != f.pos {
		f.pos, f.buf = target, nil
	}
	return f.pos, nil
}

const maxMachineFileJSONWriteBody = proto.MachineFileEditMaxBytes*2 + (64 << 10)

type machineFileWriteHTTPBody struct {
	Path             string    `json:"path"`
	Root             string    `json:"root,omitempty"`
	Text             *string   `json:"text"`
	ExpectedMtime    time.Time `json:"expected_mtime"`
	ExpectedSHA256   string    `json:"expected_sha256"`
	ExpectedRevision string    `json:"expected_revision,omitempty"`
	ConfirmSensitive bool      `json:"confirm_sensitive,omitempty"`
}

func (h *Hub) machineFileWrite(w http.ResponseWriter, r *http.Request, m *Machine) {
	var body machineFileWriteHTTPBody
	if err := decodeJSONBody(w, r, &body, maxMachineFileJSONWriteBody); err != nil {
		writeBodyErr(w, err, err)
		return
	}
	if strings.TrimSpace(body.Path) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	if body.Text == nil {
		writeErr(w, http.StatusBadRequest, errors.New("text is required"))
		return
	}
	if len(*body.Text) > proto.MachineFileEditMaxBytes {
		writeCode(w, http.StatusRequestEntityTooLarge, proto.CodeTooLarge,
			fmt.Sprintf("text exceeds maximum editable size (%d bytes)", proto.MachineFileEditMaxBytes),
			map[string]any{"limit": proto.MachineFileEditMaxBytes})
		return
	}
	if body.ExpectedMtime.IsZero() || strings.TrimSpace(body.ExpectedSHA256) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("expected_mtime and expected_sha256 are required"))
		return
	}
	req := proto.MachineFileWriteRequest{
		Path:             body.Path,
		Root:             body.Root,
		Text:             *body.Text,
		ExpectedMtime:    body.ExpectedMtime,
		ExpectedSHA256:   body.ExpectedSHA256,
		ExpectedRevision: body.ExpectedRevision,
		ConfirmSensitive: body.ConfirmSensitive,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var out proto.MachineFileWriteResponse
	if err := m.call(ctx, proto.MethodMachineFilesWrite, req, &out); err != nil {
		h.machineFileCallErr(w, r, err)
		return
	}
	writeJSON(w, out)
}

func (h *Hub) machineFileCreate(w http.ResponseWriter, r *http.Request, m *Machine) {
	var req proto.MachineFileCreateRequest
	if err := decodeJSONBody(w, r, &req, maxMachineFileJSONWriteBody); err != nil {
		writeBodyErr(w, err, err)
		return
	}
	if strings.TrimSpace(req.Dir) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("dir is required"))
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	if req.Kind != "file" && req.Kind != "dir" {
		writeErr(w, http.StatusBadRequest, errors.New("kind must be 'file' or 'dir'"))
		return
	}
	if len(req.Text) > proto.MachineFileEditMaxBytes {
		writeCode(w, http.StatusRequestEntityTooLarge, proto.CodeTooLarge,
			fmt.Sprintf("text exceeds maximum editable size (%d bytes)", proto.MachineFileEditMaxBytes),
			map[string]any{"limit": proto.MachineFileEditMaxBytes})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var out proto.MachineFileCreateResponse
	if err := m.call(ctx, proto.MethodMachineFilesCreate, req, &out); err != nil {
		h.machineFileCallErr(w, r, err)
		return
	}
	writeJSON(w, out)
}

func (h *Hub) machineFileRename(w http.ResponseWriter, r *http.Request, m *Machine) {
	var req proto.MachineFileRenameRequest
	if err := decodeJSONBody(w, r, &req, 64<<10); err != nil {
		writeBodyErr(w, err, err)
		return
	}
	if strings.TrimSpace(req.Path) == "" || strings.TrimSpace(req.DestPath) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path and dest_path are required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var out proto.MachineFileRenameResponse
	if err := m.call(ctx, proto.MethodMachineFilesRename, req, &out); err != nil {
		h.machineFileCallErr(w, r, err)
		return
	}
	writeJSON(w, out)
}

func (h *Hub) machineFileDelete(w http.ResponseWriter, r *http.Request, m *Machine) {
	var req proto.MachineFileDeleteRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSONBody(w, r, &req, 64<<10); err != nil && !errors.Is(err, io.EOF) {
			writeBodyErr(w, err, err)
			return
		}
	}
	q := r.URL.Query()
	if strings.TrimSpace(req.Path) == "" {
		req.Path = q.Get("path")
	}
	if strings.TrimSpace(req.Root) == "" {
		req.Root = q.Get("root")
	}
	if !req.ConfirmSensitive && queryBool(q.Get("confirm_sensitive")) {
		req.ConfirmSensitive = true
	}
	if strings.TrimSpace(req.Path) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var out proto.MachineFileDeleteResponse
	if err := m.call(ctx, proto.MethodMachineFilesDelete, req, &out); err != nil {
		h.machineFileCallErr(w, r, err)
		return
	}
	writeJSON(w, out)
}

func queryBool(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	return v == "1" || v == "true" || v == "yes"
}

// machineFileUpload streams a raw HTTP request body chunk-by-chunk into the target directory on the Node
// without buffering the whole file in Hub or Node memory, cleaning up partial temp files on abort/disconnect/error.
func (h *Hub) machineFileUpload(w http.ResponseWriter, r *http.Request, m *Machine) {
	q := r.URL.Query()
	dir := strings.TrimSpace(q.Get("dir"))
	name := strings.TrimSpace(q.Get("name"))
	if dir == "" || name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("dir and name are required"))
		return
	}
	size := r.ContentLength
	if size > proto.MachineFileUploadMaxBytes {
		writeCode(w, http.StatusRequestEntityTooLarge, proto.CodeTooLarge,
			fmt.Sprintf("file exceeds maximum upload size (%d bytes)", proto.MachineFileUploadMaxBytes),
			map[string]any{"limit": proto.MachineFileUploadMaxBytes})
		return
	}

	ctx := r.Context()
	step := func(method string, params, out any) error {
		c, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		return m.call(c, method, params, out)
	}

	beginReq := proto.MachineFileUploadBeginRequest{
		Dir:              dir,
		Name:             name,
		Root:             q.Get("root"),
		Size:             size,
		Overwrite:        queryBool(q.Get("overwrite")),
		ConfirmSensitive: queryBool(q.Get("confirm_sensitive")),
	}
	var begin proto.MachineFileUploadBeginResponse
	if err := step(proto.MethodMachineFilesUploadBegin, beginReq, &begin); err != nil {
		h.machineFileCallErr(w, r, err)
		return
	}

	done := false
	defer func() {
		if !done && begin.UploadID != "" {
			c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = m.call(c, proto.MethodMachineFilesUploadAbort, proto.MachineFileUploadAbortRequest{UploadID: begin.UploadID}, nil)
		}
	}()

	maxBytes := int64(proto.MachineFileUploadMaxBytes)
	if begin.MaxBytes > 0 && begin.MaxBytes < maxBytes {
		maxBytes = begin.MaxBytes
	}
	if size > maxBytes {
		writeCode(w, http.StatusRequestEntityTooLarge, proto.CodeTooLarge,
			fmt.Sprintf("file exceeds maximum upload size (%d bytes)", maxBytes),
			map[string]any{"limit": maxBytes})
		return
	}

	chunkBytes := proto.MachineFileUploadChunkBytes
	if begin.ChunkBytes > 0 && begin.ChunkBytes < chunkBytes {
		chunkBytes = begin.ChunkBytes
	}

	// Allow reading 1 byte past maxBytes so chunked or under-reported bodies exceeding the cap
	// are always detected as oversized (413) instead of silently truncated at maxBytes.
	body := http.MaxBytesReader(w, r.Body, maxBytes+1)
	buf := make([]byte, chunkBytes)
	var off int64
	for {
		n, err := io.ReadFull(body, buf)
		if n > 0 {
			if off+int64(n) > maxBytes {
				writeCode(w, http.StatusRequestEntityTooLarge, proto.CodeTooLarge,
					fmt.Sprintf("upload exceeds maximum size (%d bytes)", maxBytes),
					map[string]any{"limit": maxBytes})
				return
			}
			if size >= 0 && off+int64(n) > size {
				writeErr(w, http.StatusBadRequest, fmt.Errorf("upload exceeded declared size (%d bytes)", size))
				return
			}
			chunkReq := proto.MachineFileUploadChunkRequest{
				UploadID: begin.UploadID,
				Offset:   off,
				Data:     buf[:n],
			}
			if cerr := step(proto.MethodMachineFilesUploadChunk, chunkReq, nil); cerr != nil {
				h.machineFileCallErr(w, r, cerr)
				return
			}
			off += int64(n)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			if bodyTooLarge(err) {
				writeCode(w, http.StatusRequestEntityTooLarge, proto.CodeTooLarge,
					fmt.Sprintf("upload exceeds maximum size (%d bytes)", maxBytes),
					map[string]any{"limit": maxBytes})
				return
			}
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	if size < 0 {
		// Probe 1 trailing byte so chunked bodies ending right on a chunk/cap boundary cannot sneak past maxBytes.
		var probe [1]byte
		pn, perr := body.Read(probe[:])
		if pn > 0 || bodyTooLarge(perr) {
			writeCode(w, http.StatusRequestEntityTooLarge, proto.CodeTooLarge,
				fmt.Sprintf("upload exceeds maximum size (%d bytes)", maxBytes),
				map[string]any{"limit": maxBytes})
			return
		}
		if perr != nil && !errors.Is(perr, io.EOF) {
			writeErr(w, http.StatusBadRequest, perr)
			return
		}
	} else if off != size {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("upload cut short: %d of %d bytes", off, size))
		return
	}

	var out proto.MachineFileUploadEndResponse
	if err := step(proto.MethodMachineFilesUploadEnd, proto.MachineFileUploadEndRequest{UploadID: begin.UploadID}, &out); err != nil {
		h.machineFileCallErr(w, r, err)
		return
	}
	done = true
	h.logf("[%s] machine file uploaded: %s (%d bytes)", m.cfg.ID, out.Entry.Path, out.Size)
	writeJSON(w, out)
}
