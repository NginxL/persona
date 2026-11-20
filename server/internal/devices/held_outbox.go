package devices

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

type heldOutboxQuery struct {
	machine string
	session string
	state   string
	after   int64
	limit   int
}

func outboxQuery(r *http.Request, machine string) (heldOutboxQuery, bool) {
	q := r.URL.Query()
	out := heldOutboxQuery{machine: machine, session: q.Get("session"), state: q.Get("state"), limit: devicewire.HeldOutboxPageSize}
	if !devicewire.HeldOutboxStateValid(out.state) {
		return out, false
	}
	if size := q.Get("limit"); size != "" {
		n, err := strconv.Atoi(size)
		if err != nil || n < 1 || n > devicewire.HeldOutboxMaxPage {
			return out, false
		}
		out.limit = n
	}
	if raw := q.Get("after"); raw != "" {
		buf, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || len(buf) != 16 {
			return out, false
		}
		out.after = int64(binary.BigEndian.Uint64(buf))
		if out.after <= 0 || raw != out.cursor(out.after) {
			return out, false
		}
	}
	return out, true
}

// A page cursor follows the database insertion sequence, even if a prior row disappears or is retried. Binding the
// cursor to its filters prevents accidentally resuming a different search. It carries no authorization.
func (q heldOutboxQuery) cursor(seq int64) string {
	scope := sha256.Sum256([]byte(strings.Join([]string{q.machine, q.session, q.state}, "\x00")))
	var buf [16]byte
	binary.BigEndian.PutUint64(buf[:], uint64(seq))
	copy(buf[8:], scope[:8])
	return base64.RawURLEncoding.EncodeToString(buf[:])
}

func (d *Devices) listHeldOutbox(w http.ResponseWriter, r *http.Request, m *Machine) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get(assistantwire.RoleHeader) != "full" || r.Header.Get(assistantwire.PersonHeader) != "1" {
		writeErrCode(w, http.StatusForbidden, proto.CodeReadOnly, i18n.Err("outbox.read_only"))
		return
	}
	q, ok := outboxQuery(r, m.cfg.ID)
	if !ok {
		writeErr(w, http.StatusBadRequest, i18n.Err("outbox.invalid_page"))
		return
	}
	page, ready := d.held.outboxPage(q, m.name(), i18n.From(r.Context()))
	if !ready {
		// Loading the queue also reconciles interrupted sends. The read must leave that to the delivery loop.
		writeErrCode(w, http.StatusServiceUnavailable, "outbox_unavailable", i18n.Err("outbox.unavailable"))
		return
	}
	writeJSON(w, page)
}

type outboxRow struct {
	row     heldRow
	behind  bool
	revoked bool
}

func (h *heldBook) outboxPage(q heldOutboxQuery, computer string, lang i18n.Lang) (devicewire.HeldOutboxPage, bool) {
	rows, cursor, ready := h.outboxRows(q)
	if !ready {
		return devicewire.HeldOutboxPage{}, false
	}
	page := devicewire.HeldOutboxPage{
		MachineID: q.machine, Computer: computer, Messages: make([]devicewire.HeldOutboxMessage, 0, len(rows)),
		NextCursor: cursor,
	}
	for _, r := range rows {
		page.Messages = append(page.Messages, r.message(lang, computer))
	}
	return page, true
}

func (h *heldBook) outboxRows(q heldOutboxQuery) ([]outboxRow, string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.loaded {
		return nil, "", false
	}
	rows := h.rows[q.machine]
	out := make([]outboxRow, 0, min(len(rows), q.limit))
	blocked := make(map[string]bool)
	var last int64
	for _, r := range rows {
		if q.session != "" && r.SessionID != q.session {
			continue
		}
		behind := blocked[r.SessionID]
		if r.awaitsOwner() {
			blocked[r.SessionID] = true
		}
		if r.Seq <= q.after || q.state != "" && q.state != heldOutboxState(r) {
			continue
		}
		if len(out) == q.limit {
			return out, q.cursor(last), true
		}
		out = append(out, outboxRow{row: *r, behind: behind, revoked: h.revoked[r.CredentialID]})
		last = r.Seq
	}
	return out, "", true
}

func heldOutboxState(r *heldRow) string {
	if r.checking {
		return devicewire.HeldOutboxChecking
	}
	switch r.State {
	case db.HeldSending:
		return devicewire.HeldOutboxSending
	case db.HeldFailed:
		return devicewire.HeldOutboxFailed
	default:
		return devicewire.HeldOutboxWaiting
	}
}

func (r outboxRow) message(lang i18n.Lang, computer string) devicewire.HeldOutboxMessage {
	row := r.row
	state := heldOutboxState(&row)
	var detail string
	switch {
	case row.Failure == db.HeldRefused && state == devicewire.HeldOutboxFailed:
		// An Agent's raw refusal can quote message data. The inventory only shows its category.
		detail = i18n.T(lang, "outbox.refused")
	case state == devicewire.HeldOutboxChecking:
		detail = i18n.T(lang, "outbox.checking")
	case r.behind && state == devicewire.HeldOutboxWaiting:
		detail = i18n.T(lang, "outbox.behind")
	default:
		detail = row.wire(lang, computer, false).Error
	}
	canDiscard := state != devicewire.HeldOutboxSending && !row.busy
	return devicewire.HeldOutboxMessage{
		ID: row.OpID, SessionID: row.SessionID, SentAt: row.SentAt, HeldAt: row.HeldAt,
		State: state, Failure: row.Failure, Detail: detail,
		CanRetry:   state == devicewire.HeldOutboxFailed && canDiscard && !r.revoked && row.Failure != db.HeldSignedOut,
		CanDiscard: canDiscard,
	}
}
