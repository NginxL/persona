package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// uploadFile streams a file from the phone to a machine's temp directory (docs/protocol/protocol.md "Sending files"):
// POST /api/v1/machines/{mid}/files?name=<file name>, the raw bytes as the body. The body is read a chunk at a time
// and each chunk is one file.upload.chunk call, so neither side holds the file and no JSON line carries all of it.
// The app cancelling the request (or going away) aborts the upload and the node removes what it wrote.
// dest=terminal (the web terminal's paste and drop, docs/integrations/terminal.md): the node keeps the file under its Persona
// directory for a week instead of the temp directory, and the limit is proto.MaxTerminalUploadBytes.
// dest=health (a report for the health space): the node keeps it in the health directory's reports/: the machine's
// manager's (the built-in's health/, another's health-<id>/, docs/assistant/managers.md "Directories on a computer").
// dest=dir&dir=<folder>[&cwd=][&conflict=replace|keep] (the app's file browser, docs/protocol/protocol.md "Changing files"): the file
// keeps its name in that folder, which must be in the home; a taken name is 409 exists unless conflict says what
// to do, up to proto.MaxDirUploadBytes.
// dest=inbox (a file sent to the computer to keep, docs/protocol/file-transfer.md): it keeps its name in the machine's
// receive folder (hub.json machines[].inbox_dir), a taken name gets " 2"; up to proto.MaxInboxUploadBytes; only while
// the machine's file transfer switch is on (403 file_transfer_disabled otherwise).
// dest=attachment (a file sent with a chat message, #8361): the node keeps it beside the chat's pictures in its
// uploads/<date>/ under its own name; a node from before it gets the upload without dest, into its temp directory, as
// chat attachments always went, so a send never fails on an old node.
func (h *Hub) uploadFile(w http.ResponseWriter, r *http.Request, m *Machine) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	size := r.ContentLength
	dest, limit := r.URL.Query().Get("dest"), int64(proto.MaxUploadBytes)
	switch dest {
	case "", proto.UploadDestHealth, proto.UploadDestAttachment:
	case proto.UploadDestTerminal:
		limit = proto.MaxTerminalUploadBytes
	case proto.UploadDestInbox:
		limit = proto.MaxInboxUploadBytes
	case proto.UploadDestDir:
		limit = proto.MaxDirUploadBytes
		if strings.TrimSpace(r.URL.Query().Get("dir")) == "" {
			writeErr(w, http.StatusBadRequest, errors.New("dir is required"))
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, errors.New("unknown dest"))
		return
	}
	switch {
	case name == "":
		writeErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	case size < 0:
		writeErr(w, http.StatusLengthRequired, errors.New("Content-Length is required"))
		return
	case size > limit:
		writeCode(w, http.StatusRequestEntityTooLarge, proto.CodeTooLarge,
			i18n.T(h.requestLang(r), "hub-upload.too_large", i18n.Args{"mb": limit >> 20}), map[string]any{"limit": limit})
		return
	}
	// A file sent to keep needs the machine's file transfer switch on, as its manager reports it (internal/devices
	// transfer_settings.go): off, never set, or a manager that does not know the switch is 403, before any byte.
	if dest == proto.UploadDestInbox && !m.fileTransferOn() {
		writeCode(w, http.StatusForbidden, proto.CodeFileTransferDisabled,
			i18n.T(h.requestLang(r), "devices-file_transfer.disabled", i18n.Args{"name": m.name()}), map[string]any{"machine": m.cfg.ID})
		return
	}
	ctx := r.Context()
	step := func(method string, params, out any) error {
		c, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		return m.call(c, method, params, out)
	}
	var begin struct {
		UploadID  string `json:"upload_id"`
		Dest      string `json:"dest"`       // echoed by a node that knows dest=dir
		HealthDir string `json:"health_dir"` // echoed by a node that knows another manager's health space
	}
	beginParams := map[string]any{"name": name, "size": size}
	if dest != "" {
		beginParams["dest"] = dest // an old node ignores it and uses the temp directory
	}
	// A report goes in the health space of the machine's manager (whose health conversations run there); the
	// built-in's is named as ever (nothing), another's needs a node that knows it.
	space := ""
	if dest == proto.UploadDestHealth {
		if space = identity.HealthDirName(h.machineOwner(m.cfg.ID)); space == identity.SpaceHealth {
			space = ""
		} else if !m.hasCapability(nodeproto.HealthSpaces) {
			writeCode(w, http.StatusConflict, proto.CodeNodeOutdated, i18n.T(h.requestLang(r), "hub-upload.outdated", i18n.Args{"name": m.name()}), nil)
			return
		} else {
			beginParams["health_dir"] = space
		}
	}
	if dest == proto.UploadDestDir {
		q := r.URL.Query()
		beginParams["dir"], beginParams["cwd"], beginParams["conflict"] = q.Get("dir"), q.Get("cwd"), q.Get("conflict")
	}
	if dest == proto.UploadDestInbox {
		beginParams["dir"] = m.cfg.InboxDir // "": the node's default
	}
	err := step("file.upload.begin", beginParams, &begin)
	if err != nil && dest == proto.UploadDestAttachment && unknownUploadDest(err) {
		// A node from before dest=attachment refuses it: the temp directory, as before (one that ignores dest opened
		// it there already).
		delete(beginParams, "dest")
		err = step("file.upload.begin", beginParams, &begin)
	}
	if err != nil {
		var re *rpc.Error
		// A node from before file.upload.*, or before dest=dir / inbox: "unknown upload destination".
		if h.nodeOutdated(w, r, err, "hub-upload.outdated", i18n.Args{"name": m.name()}) {
			return
		}
		if errors.As(err, &re) && re.Code == "" && strings.HasPrefix(re.Message, "unknown upload destination") {
			writeCode(w, http.StatusConflict, proto.CodeNodeOutdated, i18n.T(h.requestLang(r), "hub-upload.outdated", i18n.Args{"name": m.name()}), nil)
			return
		}
		callErr(w, err)
		return
	}
	outdated := func() {
		writeCode(w, http.StatusConflict, proto.CodeNodeOutdated, i18n.T(h.requestLang(r), "hub-upload.outdated", i18n.Args{"name": m.name()}), nil)
	}
	if (dest == proto.UploadDestDir || dest == proto.UploadDestInbox) && begin.Dest != dest || begin.HealthDir != space { // an old node ignored dest (or health_dir) and opened it elsewhere
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = m.call(c, "file.upload.abort", map[string]any{"upload_id": begin.UploadID}, nil)
		cancel()
		outdated()
		return
	}
	done := false
	defer func() {
		if !done { // not ctx: it is cancelled when the app gives up, which is when this matters most
			c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = m.call(c, "file.upload.abort", map[string]any{"upload_id": begin.UploadID}, nil)
		}
	}()
	body := http.MaxBytesReader(w, r.Body, size)
	buf := make([]byte, proto.UploadChunkBytes)
	var off int64
	for {
		n, err := io.ReadFull(body, buf)
		if n > 0 {
			if cerr := step("file.upload.chunk", map[string]any{"upload_id": begin.UploadID, "offset": off, "data": buf[:n]}, nil); cerr != nil {
				callErr(w, cerr)
				return
			}
			off += int64(n)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	if off != size {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("upload cut short: %d of %d bytes", off, size))
		return
	}
	var out proto.UploadedFile
	if err := step("file.upload.end", map[string]any{"upload_id": begin.UploadID}, &out); err != nil {
		callErr(w, err)
		return
	}
	done = true
	h.logf("[%s] file from the phone: %d bytes", m.cfg.ID, out.Size)
	writeJSON(w, out)
}

// unknownUploadDest: file.upload.begin was refused by a node that does not know the destination asked for.
func unknownUploadDest(err error) bool {
	var re *rpc.Error
	return errors.As(err, &re) && re.Code == "" && strings.HasPrefix(re.Message, "unknown upload destination")
}

// writeCode answers with an error code the app acts on, plus extra fields.
func writeCode(w http.ResponseWriter, status int, code, msg string, extra map[string]any) {
	body := map[string]any{"error": msg, "code": code}
	for k, v := range extra {
		body[k] = v
	}
	apierr.WriteBody(w, status, body)
}

// fileTransferOn is the machine's file transfer switch as its manager last reported it (proto.Machine.FileTransfer);
// off until a report says it is on.
func (m *Machine) fileTransferOn() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snap != nil && m.snap.FileTransfer != nil && m.snap.FileTransfer.Enabled
}
