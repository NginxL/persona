package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func (h *Hub) downloadFile(w http.ResponseWriter, r *http.Request, m *Machine) {
	q := r.URL.Query()
	p := proto.FileChunkRequest{Path: q.Get("path"), Cwd: q.Get("cwd")}
	if strings.TrimSpace(p.Path) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	get := func(p proto.FileChunkRequest) (*proto.FileChunk, error) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		var out proto.FileChunk
		if err := m.call(ctx, "file.chunk", p, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
	info, err := get(p)
	if err != nil {
		if h.nodeOutdated(w, r, err, "hub-download.outdated_file", nil) {
			return
		}
		callErr(w, err)
		return
	}
	if info.Size < 0 || info.Revision == "" {
		writeErr(w, http.StatusBadGateway, errors.New("invalid file metadata"))
		return
	}
	p.Path, p.Revision = info.Path, info.Revision
	w.Header().Set("Content-Type", info.Mime)
	if info.Mime == "" {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("ETag", fmt.Sprintf("%q", info.Revision))
	disposition := "attachment"
	if q.Get("inline") == "1" && inlineMime(info.Mime) {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": info.Name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// The hub serves the web app on this origin: a file opened by itself (an SVG, an HTML page saved as .png) must
	// not run as the app. Chrome will not show a PDF in a sandbox, and a PDF runs no page script anyway.
	if !strings.HasPrefix(info.Mime, "application/pdf") {
		w.Header().Set("Content-Security-Policy", "sandbox")
	}
	reader := &remoteFileReader{get: get, request: p, size: info.Size}
	http.ServeContent(w, r, info.Name, info.Modified, reader)
}

// inlineMime: what a browser may show in place (?inline=1: <img>, <video>, <audio>, a PDF in a frame); anything
// else is always downloaded. HTML is previewed from file.read text in a sandboxed frame (docs/protocol/protocol.md "File preview").
func inlineMime(m string) bool {
	return strings.HasPrefix(m, "image/") || strings.HasPrefix(m, "video/") || strings.HasPrefix(m, "audio/") ||
		strings.HasPrefix(m, "application/pdf")
}

// fileThumb is a file's thumbnail (node file.thumb): 304 for the ETag the client has, 404 no_thumbnail when the
// machine cannot draw it.
func (h *Hub) fileThumb(w http.ResponseWriter, r *http.Request, m *Machine) {
	q := r.URL.Query()
	p := proto.FileThumbRequest{Path: q.Get("path"), Cwd: q.Get("cwd"), Size: atoi(q.Get("size")),
		ETag: strings.Trim(r.Header.Get("If-None-Match"), `"`)}
	if strings.TrimSpace(p.Path) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var out proto.FileThumb
	if err := m.call(ctx, "file.thumb", p, &out); err != nil {
		var re *rpc.Error
		switch {
		case h.nodeOutdated(w, r, err, "hub-download.outdated_thumbnail", nil):
		case errors.As(err, &re) && re.Code == proto.CodeNoThumbnail:
			writeCode(w, http.StatusNotFound, proto.CodeNoThumbnail, re.Message, nil)
		default:
			callErr(w, err)
		}
		return
	}
	w.Header().Set("ETag", fmt.Sprintf("%q", out.ETag))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	if out.NotModified {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if !strings.HasPrefix(out.Mime, "image/") {
		writeErr(w, http.StatusBadGateway, errors.New("invalid thumbnail"))
		return
	}
	w.Header().Set("Content-Type", out.Mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(out.Data)
}

type remoteFileReader struct {
	get     func(proto.FileChunkRequest) (*proto.FileChunk, error)
	request proto.FileChunkRequest
	size    int64
	pos     int64
	buf     []byte
}

func (f *remoteFileReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if f.pos >= f.size {
		return 0, io.EOF
	}
	if len(f.buf) == 0 {
		q := f.request
		q.Offset, q.Length = f.pos, int(min(int64(proto.FileDownloadChunkBytes), f.size-f.pos))
		chunk, err := f.get(q)
		if err != nil {
			return 0, err
		}
		if chunk.Offset != q.Offset || chunk.Size != f.size || chunk.Revision != q.Revision || len(chunk.Data) != q.Length {
			return 0, errors.New("file chunk does not match the download")
		}
		f.buf = chunk.Data
	}
	n := copy(p, f.buf)
	f.buf = f.buf[n:]
	f.pos += int64(n)
	return n, nil
}

func (f *remoteFileReader) Seek(offset int64, whence int) (int64, error) {
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

// fileConvert asks the machine's LibreOffice for a PDF of a document (node file.convert): 404 no_converter when it
// has none, 409 node_outdated for an older node. The answer names the PDF on the machine, read with file/download.
func (h *Hub) fileConvert(w http.ResponseWriter, r *http.Request, m *Machine) {
	q := r.URL.Query()
	p := proto.FileConvertRequest{Path: q.Get("path"), Cwd: q.Get("cwd"), To: q.Get("to")}
	if strings.TrimSpace(p.Path) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	var out proto.FileConverted
	if err := m.call(ctx, "file.convert", p, &out); err != nil {
		var re *rpc.Error
		switch {
		case h.nodeOutdated(w, r, err, "hub-download.outdated_convert", nil):
		case errors.As(err, &re) && re.Code == proto.CodeNoConverter:
			writeCode(w, http.StatusNotFound, proto.CodeNoConverter, re.Message, nil)
		default:
			callErr(w, err)
		}
		return
	}
	writeJSON(w, out)
}
