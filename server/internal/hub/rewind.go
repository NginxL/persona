package hub

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// POST /api/v1/sessions/{mid}/{sid}/rewind (#2436): go back in a Claude session to before one of its messages — the node
// types Claude Code's /rewind into the session's terminal and walks its screens (node/rewind.go). That restores files
// and removes the conversation after the message, so only a full device may ask; an answer-only device (the watch) and
// the tokens an extension holds are refused here and by the lists of what they may call. The body is read field by
// field: only the named fields reach the node, so a request cannot make it press anything else.
//
//	{"prompt": "<the message>", "occurrence": 1, "prompts_after": 0, "restore": "conversation"|"code_and_conversation"}
//
// occurrence counts the messages with this exact text from the end (1 = the last one), prompts_after the user's
// messages that came after it (optional). Answers {"prompt", "restore", "code_restored", "input_cleared"}; the node's
// refusals (dialog_open, input_draft, rewind_busy, rewind_unsupported, rewind_nothing, rewind_mismatch, rewind_failed)
// are 409 with their code and a message to show.

const (
	rewindMaxPrompt = 256 << 10
	rewindMaxCount  = 100000
	// The node gives up after 90 s (node/rewind.go); the hub waits a little longer to hear why.
	rewindTimeout = 100 * time.Second
)

func (h *Hub) rewindSession(w http.ResponseWriter, r *http.Request, m *Machine) {
	if principalOf(r.Context()).Role != roleFull {
		writeErr(w, http.StatusForbidden, errors.New("rewinding a session needs a full device"))
		return
	}
	sid := r.PathValue("sid")
	if paneAddress(sid) {
		writeErr(w, http.StatusBadRequest, errors.New("a terminal opened by pane is not a session"))
		return
	}
	var body struct {
		Prompt       string `json:"prompt"`
		Occurrence   int    `json:"occurrence"`
		PromptsAfter *int   `json:"prompts_after"`
		Restore      string `json:"restore"`
	}
	if err := decodeJSONBody(w, r, &body, rewindMaxPrompt+4<<10); err != nil {
		writeBodyErr(w, err, errors.New("bad request body"))
		return
	}
	switch {
	case strings.TrimSpace(body.Prompt) == "" || len(body.Prompt) > rewindMaxPrompt:
		writeErr(w, http.StatusBadRequest, errors.New("prompt is required"))
		return
	case body.Occurrence < 1 || body.Occurrence > rewindMaxCount:
		writeErr(w, http.StatusBadRequest, errors.New("occurrence is required"))
		return
	case body.PromptsAfter != nil && (*body.PromptsAfter < 0 || *body.PromptsAfter > rewindMaxCount):
		writeErr(w, http.StatusBadRequest, errors.New("prompts_after is out of range"))
		return
	case body.Restore != "conversation" && body.Restore != "code_and_conversation":
		writeErr(w, http.StatusBadRequest, errors.New("restore is conversation or code_and_conversation"))
		return
	}
	params := map[string]any{"session_id": sid, "prompt": body.Prompt, "occurrence": body.Occurrence, "restore": body.Restore}
	if body.PromptsAfter != nil {
		params["prompts_after"] = *body.PromptsAfter
	}
	out, ok := h.callNode(w, r, m, "session.rewind", params, rewindTimeout, "hub-rewind.outdated", nil)
	if !ok {
		return
	}
	if len(out) == 0 {
		out = json.RawMessage(`{}`)
	}
	writeRawJSON(w, out)
}
