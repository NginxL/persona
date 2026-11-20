package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/extwire"
)

// An extension's token may not send terminal control characters (docs/extensions/extensions.md "Hub API: scopes"):
// what it sends an agent can be typed into the agent's terminal, now or later in a prompt (a to-do, a knowledge-base
// entry), where they are keys rather than text, and keys can do what the extension was not granted (switch the
// permission mode). Newline, carriage return and tab are text there; every other control character (C0, DEL, C1) is
// refused, in any chat, its extension's own included. Scoped device tokens use the same input check.

// codeTerminalControls answers a request of an extension's token that carries a terminal control character.
const codeTerminalControls = "terminal_control_refused"

// terminalTextMax is the most of a body withoutTerminalControls reads: as much as any of its routes takes (a message
// with its attachments).
const terminalTextMax = 32 << 20

// withoutTerminalControls guards a route on which an extension's token can put text into a terminal or keep it for a
// prompt: a request of one with a terminal control character in its path or query (terminalControlsInURL) or in any
// string, key or value, of any of its body's JSON values is refused and audited (refuseTerminalControls), and one
// whose body is not JSON is refused, as nothing can say what the route would read in it. Other requests go on with
// the body as it came.
func (h *Hub) withoutTerminalControls(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := principalOf(r.Context())
		if p.Role != roleExtension && p.Role != roleScoped {
			next(w, r)
			return
		}
		if terminalControlsInURL(r.URL) {
			h.refuseTerminalControls(w, r, p)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, terminalTextMax))
		_ = r.Body.Close()
		if err != nil {
			writeBodyErr(w, err, err)
			return
		}
		found, err := terminalControls(body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("request body is not JSON: %w", err))
			return
		}
		if found {
			h.refuseTerminalControls(w, r, p)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		next(w, r)
	}
}

// terminalControls reports whether a string of JSON body, a key or a value, has a terminal control character
// (isTerminalControl). Every value of the body is read, not only the first, and one cut short is an error: no part of
// it goes unread. An empty body has none.
func terminalControls(body []byte) (bool, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	depth := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) && depth == 0 {
			return false, nil
		}
		if errors.Is(err, io.EOF) {
			return false, io.ErrUnexpectedEOF
		}
		if err != nil {
			return false, err
		}
		switch tok := tok.(type) {
		case json.Delim:
			if tok == '{' || tok == '[' {
				depth++
			} else {
				depth--
			}
		case string:
			if strings.ContainsFunc(tok, isTerminalControl) {
				return true, nil
			}
		}
	}
}

// terminalControlsInURL reports whether u's decoded path or a key or value of its query has a terminal control
// character: a route keeps text from those too (a knowledge-base entry is named from its path). A query pair that does
// not decode is left out here as it is by the route.
func terminalControlsInURL(u *url.URL) bool {
	if strings.ContainsFunc(u.Path, isTerminalControl) {
		return true
	}
	q, _ := url.ParseQuery(u.RawQuery)
	for k, vs := range q {
		if strings.ContainsFunc(k, isTerminalControl) {
			return true
		}
		for _, v := range vs {
			if strings.ContainsFunc(v, isTerminalControl) {
				return true
			}
		}
	}
	return false
}

// isTerminalControl is a control character (Unicode Cc: C0, DEL and C1) other than newline, carriage return and tab.
func isTerminalControl(r rune) bool {
	return unicode.Is(unicode.Cc, r) && r != '\n' && r != '\r' && r != '\t'
}

// refuseTerminalControls answers 403 to a request of extension token p that carries terminal control characters, and
// has its manager record it in the extension's audit: the route and its path, never the text.
func (h *Hub) refuseTerminalControls(w http.ResponseWriter, r *http.Request, p principal) {
	if p.Role == roleScoped {
		writeErrCode(w, http.StatusForbidden, codeTerminalControls, errors.New("a scoped device may not send terminal control characters"))
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	route, path := rowText(r.Pattern, 128), rowText(r.URL.EscapedPath(), 256)
	audit := extwire.Audit{Extension: p.Extension, Action: extwire.AuditTerminalControlRefused,
		Detail: map[string]any{"route": route, "path": path}}
	if err := h.extCallTo(ctx, p.Manager, extwire.HubAudit, audit, nil); err != nil {
		h.logf("extension %s: %s refused for terminal control characters; not recorded in its audit: %v", p.Extension, path, err)
	}
	writeErrCode(w, http.StatusForbidden, codeTerminalControls, errors.New("an extension may not send terminal control characters"))
}
