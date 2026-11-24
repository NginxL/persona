package hub

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The hub-wide to-do revision is a hash of every participating manager's id and token. A partial or failed answer never
// gets one: treating an old manager's omission as an empty list could incorrectly 304 a mixed-fleet read.
func todoRevisionOfAnswers(answers []listAnswer) (string, bool) {
	if len(answers) == 0 {
		return "", false
	}
	h := sha256.New()
	for _, a := range answers {
		tag := a.header.Get(assistantwire.TodoRevisionHeader)
		if !a.ok() || tag == "" {
			return "", false
		}
		_, _ = h.Write([]byte(a.id))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(tag))
		_, _ = h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return `W/"tr1-hub-` + hex.EncodeToString(sum[:16]) + `"`, true
}

// mergeTodoRevision answers the pushed web read without asking any manager for GET /todos. A 304 proves that every
// manager's write counter and stable view classes match those of the full answer where the client got its token.
// Unsupported or temporarily unavailable managers fail closed with 409; the web then uses the original full read.
func (h *Hub) mergeTodoRevision(w http.ResponseWriter, r *http.Request) {
	ids, body, ok := h.mergedAsk(w, r)
	if !ok {
		return
	}
	answers := make([]listAnswer, 0, len(ids))
	for _, id := range ids {
		bw := newBufferedWriter()
		asked := withBody(r, body)
		asked.Header.Del("If-None-Match") // the merged validator, not one manager's, decides the client's 304
		h.forwardHTTP(bw, asked, id)
		answers = append(answers, listAnswer{id: id, status: cmp.Or(bw.status, http.StatusOK), header: bw.header, body: bw.body.Bytes()})
	}
	tag, complete := todoRevisionOfAnswers(answers)
	if !complete {
		writeErrCode(w, http.StatusConflict, "todo_revision_unavailable", errors.New("every manager must provide a current to-do revision"))
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "application/json")
	hd.Set("ETag", tag)
	hd.Set(assistantwire.TodoRevisionHeader, tag)
	if lines := r.Header.Values("If-None-Match"); len(lines) > 0 && tagMatches(lines, tag) {
		hd.Del("Content-Length")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	bodyOut, _ := json.Marshal(struct {
		Revision string `json:"revision"`
	}{Revision: tag})
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(bodyOut)
}
