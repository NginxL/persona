package extmgr

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
)

// The messaging extension point (docs/extensions/extension-messaging.md, #2138): an extension gives the apps' "Messages" entry its
// data and actions. The hub lists the sources (GET /api/v1/messaging), hands /api/v1/messaging/<source>/… to the
// extension that has the source, checks what it answers against schema/extension/messaging.schema.json before the
// apps see it, and pushes the extension's messaging.changed to the apps.

// messagingRoute is one route of the protocol: Method, Path under the source ("*" one segment, a trailing "**" the
// rest), and Answer, the schema definition a 2xx answer must hold to ("" when it is not checked: an empty {} or bytes).
type messagingRoute struct {
	Method, Path, Answer string
}

// messagingRoutes are the routes the hub passes on; anything else under a source is 404, so a source cannot grow a
// vendor's own API the apps would then depend on (docs "4. API").
var messagingRoutes = []messagingRoute{
	{"GET", "status", "MessagingStatus"},
	{"GET", "me", "MessagingMe"},
	{"GET", "accounts", "MessagingAccounts"},
	{"GET", "conversations", "MessagingConversationPage"},
	{"GET", "pinned", "MessagingConversationPage"},
	{"GET", "conversations/*/messages", "MessagingMessagePage"},
	{"POST", "conversations/*/messages", "MessagingSendReceipt"},
	{"POST", "conversations/*/attachments", "MessagingSendReceipt"},
	{"POST", "conversations/*/compose", "MessagingComposeAnswer"},
	{"POST", "conversations/*/read", ""},
	{"POST", "conversations/*/read-states", "MessagingReadStates"},
	{"GET", "conversations/*/members", "MessagingMembers"},
	{"GET", "conversations/*/pins", "MessagingChatPins"},
	{"GET", "conversations/*/pins/*", "MessagingPinTargetAnswer"},
	{"POST", "conversations/*/pins", "MessagingPinAnswer"},
	{"GET", "messages/*", "MessagingMessageAnswer"},
	{"GET", "messages/*/thread", "MessagingMessagePage"},
	{"POST", "messages/*/reactions", ""},
	{"POST", "messages/*/actions/*", "ActionResult"},
	{"GET", "messages/*/url-preview", "MessagingURLPreview"},
	{"GET", "emojis", "MessagingEmojis"},
	{"GET", "reactions/quick", "MessagingQuickReactions"},
	{"GET", "users/*", "MessagingProfile"},
	{"POST", "users/*/chat", "MessagingChatOpened"},
	{"GET", "media/**", ""},
	{"GET", "messages/*/media/**", ""},
	{"GET", "documents", "MessagingDocument"},
	{"GET", "documents/comments", "MessagingCommentThreads"},
	{"POST", "documents/comments", ""},
	{"POST", "documents/comments/*/replies", ""},
	{"POST", "documents/comments/*/resolve", ""},
	{"GET", "posts/*", "MessagingPost"},
	{"GET", "posts/*/comments", "MessagingPostComments"},
}

// messagingAnswerMost is the largest checked answer read whole (a page of messages with forwards); media is streamed.
const messagingAnswerMost = 16 << 20

// match says whether path (under the source, no leading /) is the route's.
func (rt messagingRoute) match(method, path string) bool {
	if rt.Method != method {
		return false
	}
	want, got := strings.Split(rt.Path, "/"), strings.Split(path, "/")
	for i, w := range want {
		if w == "**" {
			return len(got) > i && got[i] != ""
		}
		if i >= len(got) || (w != "*" && w != got[i]) || got[i] == "" {
			return false
		}
	}
	return len(got) == len(want)
}

// lookupMessagingRoute is the route of a request under a source, if the protocol has it.
func lookupMessagingRoute(method, path string) (messagingRoute, bool) {
	for _, rt := range messagingRoutes {
		if rt.match(method, path) {
			return rt, true
		}
	}
	return messagingRoute{}, false
}

// messagingSources are the enabled, running extensions' sources, in the registry's order.
func (ps *extensions) messagingSources() []wire.MessagingSource {
	if ps == nil {
		return nil
	}
	var out []wire.MessagingSource
	for _, p := range ps.reg.List() {
		if !p.Record.Enabled || p.Problem != "" {
			continue
		}
		if hp := ps.get(p.Record.ID); hp == nil || hp.proc == nil || hp.proxy == nil {
			continue
		}
		for _, it := range p.Manifest.Messaging() {
			out = append(out, wire.MessagingSource{Extension: p.Record.ID, ID: it.ID, Name: it.Name, Icon: it.Icon,
				Capabilities: it.Capabilities, ComposerModes: it.ComposerModes, SettingsUI: it.SettingsUI, Links: it.Links})
		}
	}
	return out
}

// messagingSource is the running extension that has the source, nil when none does.
func (ps *extensions) messagingSource(source string) *hubExtension {
	hp, _ := ps.messagingOwner(source)
	return hp
}

func (ps *extensions) messagingOwner(source string) (*hubExtension, wire.MessagingItem) {
	if ps == nil {
		return nil, wire.MessagingItem{}
	}
	for _, p := range ps.reg.List() {
		if !p.Record.Enabled || p.Problem != "" {
			continue
		}
		for _, it := range p.Manifest.Messaging() {
			if it.ID == source {
				if hp := ps.get(p.Record.ID); hp != nil && hp.proc != nil && hp.proxy != nil {
					return hp, it
				}
				return nil, wire.MessagingItem{}
			}
		}
	}
	return nil, wire.MessagingItem{}
}

func (h *Manager) messagingRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/messaging", func(w http.ResponseWriter, r *http.Request) {
		sources := h.extensions.messagingSources()
		if sources == nil {
			sources = []wire.MessagingSource{}
		}
		writeJSON(w, wire.MessagingSources{Sources: sources})
	})
	api.HandleFunc("/api/v1/messaging/{source}/{rest...}", h.serveMessaging)
}

// serveMessaging hands a request under a source to its extension and checks the answer.
func (h *Manager) serveMessaging(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	// The route is matched on the escaped path: a segment the app escaped (posts/<a link>) stays one segment.
	rest := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v1/messaging/"+url.PathEscape(source)+"/")
	rt, ok := lookupMessagingRoute(r.Method, rest)
	if !ok {
		writeErrCode(w, http.StatusNotFound, "not_found", errors.New("no such messaging route"))
		return
	}
	hp, item := h.extensions.messagingOwner(source)
	if hp == nil {
		writeErrCode(w, http.StatusNotFound, "not_found", errors.New("no messaging source "+strconv.Quote(source)))
		return
	}
	end, err := hp.proc.BeginHTTP(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	defer end()
	// Who is asking, for the extension (it never gets the credential); never what the caller says it is.
	for k := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-persona-") {
			r.Header.Del(k)
		}
	}
	p := principalOf(r.Context())
	r.Header.Set("X-Persona-Device", p.ID)
	r.Header.Set("X-Persona-Role", p.Role)
	// No receipt for sends (docs "Sends are not repeated"): one would keep a refusal the user can fix (the switch off,
	// a sealed token expired) and replay it for the client_id; the extension drops a repeat by its client_id itself.
	h.messagingAnswer(w, r, hp, rt, slices.Contains(item.Capabilities, "accounts"))
}

// messagingAnswer passes the extension's answer on: as it is for bytes, errors and unchecked routes; a checked 2xx
// only when it holds to its schema definition (502 extension_bad_response otherwise, logged).
func (h *Manager) messagingAnswer(w http.ResponseWriter, r *http.Request, hp *hubExtension, rt messagingRoute, multiple bool) {
	if rt.Answer == "" {
		hp.serveExtensionHTTP(w, r)
		return
	}
	rec := &bufferedAnswer{header: http.Header{}}
	hp.serveExtensionHTTP(rec, r)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	if rec.status < 200 || rec.status > 299 {
		rec.copyTo(w)
		return
	}
	if rec.over {
		h.logf("extension %s: messaging %s %s: answer over %d bytes", hp.id, rt.Method, rt.Path, messagingAnswerMost)
		writeErrCode(w, http.StatusBadGateway, "extension_bad_response", errors.New("the extension's answer is too large"))
		return
	}
	if err := wire.ValidateJSON(rt.Answer, rec.body.Bytes()); err != nil {
		// A newer extension's parts and blocks this hub does not know: in their plainest form, then checked again.
		fitted, changed := fitMessagingAnswer(rec.body.Bytes())
		if !changed || wire.ValidateJSON(rt.Answer, fitted) != nil {
			h.logf("extension %s: messaging %s %s: %s does not hold: %v", hp.id, rt.Method, rt.Path, rt.Answer, err)
			writeErrCode(w, http.StatusBadGateway, "extension_bad_response", errors.New("the extension answered something the apps cannot read"))
			return
		}
		rec.body.Reset()
		rec.body.Write(fitted)
	}
	source := r.PathValue("source")
	account := r.URL.Query().Get("account")
	// An older multi-account page may omit its account. Only a successful
	// read with relative pictures needs a second, explicitly pinned read:
	// the source's default could change between reading the page and GET me.
	if addMessagingAvatars(rec, rt.Answer, source, account, multiple) && account == "" && r.Method == http.MethodGet {
		if current := defaultMessagingAccount(r.Context(), hp, source); current != "" {
			pinned := r.Clone(r.Context())
			q := pinned.URL.Query()
			q.Set("account", current)
			pinned.URL.RawQuery = q.Encode()
			h.messagingAnswer(w, pinned, hp, rt, multiple)
			return
		}
	}
	rec.copyTo(w)
}

// fitMessagingAnswer turns what this hub does not know of a newer extension's answer into what it does (docs "12.
// Versions and compatibility"): a part of an unknown kind becomes a text part of its text (dropped when it has none), a
// document block of an unknown kind a paragraph. changed says whether anything was.
func fitMessagingAnswer(raw []byte) ([]byte, bool) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw, false
	}
	changed := false
	var walk func(x any, key string) any
	walk = func(x any, key string) any {
		switch t := x.(type) {
		case map[string]any:
			for k, e := range t {
				t[k] = walk(e, k)
			}
			if k, ok := t["kind"].(string); ok && (key == "blocks" || key == "items") && t["parts"] != nil && !slices.Contains(wire.MessagingDocBlockKinds, k) {
				t["kind"] = "paragraph"
				changed = true
			}
			return t
		case []any:
			out := t[:0]
			for _, e := range t {
				e = walk(e, key)
				if m, ok := e.(map[string]any); ok && key == "parts" {
					if k, ok := m["kind"].(string); ok && !slices.Contains(wire.MessagingPartKinds, k) {
						changed = true
						text, _ := m["text"].(string)
						if text == "" {
							continue
						}
						e = map[string]any{"kind": "text", "text": text}
					}
				}
				out = append(out, e)
			}
			return out
		}
		return x
	}
	v = walk(v, "")
	if !changed {
		return raw, false
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw, false
	}
	return out, true
}

// bufferedAnswer keeps an answer whole (up to messagingAnswerMost) to check it before passing it on.
type bufferedAnswer struct {
	header http.Header
	status int
	body   bytes.Buffer
	over   bool
}

func (b *bufferedAnswer) Header() http.Header { return b.header }
func (b *bufferedAnswer) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}
func (b *bufferedAnswer) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	if b.body.Len()+len(p) > messagingAnswerMost {
		b.over = true
		return len(p), nil
	}
	return b.body.Write(p)
}

func (b *bufferedAnswer) copyTo(w http.ResponseWriter) {
	for k, v := range b.header {
		w.Header()[k] = v
	}
	w.Header().Set("Content-Length", strconv.Itoa(b.body.Len()))
	w.WriteHeader(b.status)
	_, _ = w.Write(b.body.Bytes())
}

// messagingFeature is in the hub's features while some extension has a messaging source: the apps use the "Messages"
// entry of the protocol then (and the old /api/v1/lark otherwise).
const messagingFeature = "messaging"

// messagingChanges coalesces an extension's messaging.changed over a second per source and account before pushing it.
type messagingChanges struct {
	mu      sync.Mutex
	pending map[string]*wire.MessagingChanged // "<source>\x00<account>"
	timer   *time.Timer
}

// messagingChanged is an extension's event messaging.changed: only for a source it has; pushed to the apps as
// {type: messaging, source, conversations, account?, settings_changed?} within a second.
func (ps *extensions) messagingChanged(id string, params json.RawMessage) {
	if ps == nil || ps.h == nil {
		return
	}
	var ev wire.MessagingChanged
	if json.Unmarshal(params, &ev) != nil || wire.ValidateJSON("MessagingChanged", params) != nil {
		ps.logf("extension %s: messaging.changed that is not one: dropped", id)
		return
	}
	p := ps.reg.Get(id)
	if p == nil || !slices.ContainsFunc(p.Manifest.Messaging(), func(it wire.MessagingItem) bool { return it.ID == ev.Source }) {
		ps.logf("extension %s: messaging.changed for a source it does not have (%q): dropped", id, ev.Source)
		return
	}
	c := &ps.messaging
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil {
		c.pending = map[string]*wire.MessagingChanged{}
	}
	key := ev.Source + "\x00" + ev.Account
	if have := c.pending[key]; have != nil {
		mergeMessagingChanged(have, &ev)
	} else {
		c.pending[key] = &ev
	}
	if c.timer == nil {
		c.timer = time.AfterFunc(time.Second, ps.flushMessaging)
	}
}

// maxMessagingChanged is how many chats one push names; more is the same as none: read everything again
// (docs/extensions/extension-messaging.md"8", the schema's maxItems).
const maxMessagingChanged = 500

// mergeMessagingChanged folds a later change into one waiting to go out: the chats of both, or every chat when either
// says so (it names none) or together they name more than a push can.
func mergeMessagingChanged(have, ev *wire.MessagingChanged) {
	if len(have.Conversations) == 0 || len(ev.Conversations) == 0 {
		have.Conversations = nil
	} else {
		for _, conv := range ev.Conversations {
			if !slices.Contains(have.Conversations, conv) {
				have.Conversations = append(have.Conversations, conv)
			}
		}
		if len(have.Conversations) > maxMessagingChanged {
			have.Conversations = nil
		}
	}
	have.SettingsChanged = have.SettingsChanged || ev.SettingsChanged
}

func (ps *extensions) flushMessaging() {
	c := &ps.messaging
	c.mu.Lock()
	pending := c.pending
	c.pending, c.timer = nil, nil
	c.mu.Unlock()
	for _, ev := range pending {
		out := map[string]any{"type": "messaging", "source": ev.Source, "conversations": ev.Conversations}
		if ev.Conversations == nil {
			out["conversations"] = []string{}
		}
		if ev.Account != "" {
			out["account"] = ev.Account
		}
		if ev.SettingsChanged {
			out["settings_changed"] = true
		}
		ps.h.broadcast(out)
	}
}
