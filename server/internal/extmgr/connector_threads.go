package extmgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/subscriptions"
)

// A connector's inbox thread opened as a conversation (connector.Threads, docs/extensions/connectors.md "Interfaces: one manifest, dispatched by declaration"): a
// page of messages at a time, the pictures they show through the hub (kept on disk: a media ref always names the same
// bytes), and a reply through the connector's own action, behind the gate. Every request the connector makes goes
// through its own limits (Telegram's gate).

const (
	threadDefault  = 30
	mediaCacheMost = 256 << 20 // bytes of media kept per hub; the oldest go first
)

// threadConnector is connector id with threads, or writes why there is none: 404 for no such connector, 409 for one
// without threads or not signed in, 429 while it waits out a rate limit.
func (h *Manager) threadConnector(w http.ResponseWriter, r *http.Request) (connector.Connector, connector.Threads, bool) {
	c := h.connectors.Get(r.PathValue("id"))
	if c == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no connector %q", r.PathValue("id")))
		return nil, nil, false
	}
	th, ok := connector.As[connector.Threads](c)
	if !ok {
		writeErrCode(w, http.StatusConflict, "conversations_unsupported", fmt.Errorf("%s has no conversations to open", c.Name()))
		return nil, nil, false
	}
	if !h.threadUsable(w, r.Context(), c) {
		return nil, nil, false
	}
	return c, th, true
}

// noConversation is the page for an Inbox item with no conversation of its own to open (#5537), and whether id's
// thread is one: a subscription's item (its source is the subscription, not a connector, and its content is already
// the whole of it) or a connector's without threads. Every app, older ones too, shows what the hub collected for a
// page that is neither a chat nor GitHub's thread, with no error card or Retry (an error said it could not be read).
func (h *Manager) noConversation(ctx context.Context, id, thread string) (connector.ThreadPage, bool) {
	page := connector.ThreadPage{Chat: connector.ThreadChat{ID: thread, Kind: connector.ThreadKindNone}, Messages: []connector.ThreadMessage{}}
	if c := h.connectors.Get(id); c != nil {
		_, threads := connector.As[connector.Threads](c)
		return page, !threads
	}
	return page, subscriptions.OwnsThread(id, thread) || h.subs != nil && h.subs.IsSubscription(ctx, id)
}

// threadUsable writes why c cannot be read now (signed out, switched off, rate limited) and says whether it can.
func (h *Manager) threadUsable(w http.ResponseWriter, ctx context.Context, c connector.Connector) bool {
	switch st := connector.StatusOf(ctx, c); st.State {
	case connector.StateOK:
		return true
	case connector.StateRateLimited:
		writeErrCode(w, http.StatusTooManyRequests, "rate_limited", errors.New(st.Error))
	default:
		why := st.Error
		if why == "" {
			why = st.State
		}
		writeErrCode(w, http.StatusConflict, "unavailable", fmt.Errorf("%s: %s", c.Name(), why))
	}
	return false
}

// threadFailed writes a connector's failure: as a rate limit when the connector is waiting one out now.
func (h *Manager) threadFailed(w http.ResponseWriter, ctx context.Context, c connector.Connector, err error) {
	var readError *rpc.Error
	if errors.As(err, &readError) {
		switch readError.Code {
		case "rate_limited":
			writeErrCode(w, http.StatusTooManyRequests, readError.Code, err)
			return
		case "no_permission":
			writeErrCode(w, http.StatusForbidden, readError.Code, err)
			return
		case "not_signed_in":
			// Source login is separate from the app device token; 401 would sign the app out.
			writeErrCode(w, http.StatusConflict, "unavailable", err)
			return
		}
	}
	if connector.StatusOf(ctx, c).State == connector.StateRateLimited {
		writeErrCode(w, http.StatusTooManyRequests, "rate_limited", err)
		return
	}
	writeErr(w, http.StatusBadGateway, err)
}

func (h *Manager) connectorThreadRoutes(api *http.ServeMux) {
	// A page of a thread's messages: ?thread=<the inbox item's thread>&before=<the last page's before>&limit=.
	api.HandleFunc("GET /api/v1/connectors/{id}/thread", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		thread := q.Get("thread")
		if page, ok := h.noConversation(r.Context(), r.PathValue("id"), thread); ok {
			writeJSON(w, page)
			return
		}
		c, th, ok := h.threadConnector(w, r)
		if !ok {
			return
		}
		if thread == "" {
			writeErr(w, http.StatusBadRequest, errors.New("thread is required"))
			return
		}
		limit := threadDefault
		if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
			limit = min(n, connector.ThreadMost)
		}
		page, err := th.Thread(r.Context(), thread, q.Get("before"), limit)
		if err != nil {
			h.threadFailed(w, r.Context(), c, err)
			return
		}
		if page.Messages == nil {
			page.Messages = []connector.ThreadMessage{}
		}
		writeJSON(w, page)
	})
	// One media's bytes: ?ref=<a page's avatar or media ref>. Kept on disk, and by the app for good.
	api.HandleFunc("GET /api/v1/connectors/{id}/media", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		ref := r.URL.Query().Get("ref")
		if ref == "" {
			writeErr(w, http.StatusBadRequest, errors.New("ref is required"))
			return
		}
		id := r.PathValue("id")
		m, cached := h.media.get(h.mediaDir(), id, ref)
		if !cached {
			c, th, ok := h.threadConnector(w, r)
			if !ok {
				return
			}
			var err error
			m, err = h.media.fetch(r.Context(), id+"\x00"+ref, func(ctx context.Context) (connector.Media, error) { return th.Media(ctx, ref) })
			if err != nil {
				h.threadFailed(w, r.Context(), c, err)
				return
			}
		}
		if len(m.Data) == 0 || len(m.Data) > connector.MediaMost {
			writeErr(w, http.StatusBadGateway, fmt.Errorf("media of %d bytes", len(m.Data)))
			return
		}
		// Connector MIME claims and old cache headers are untrusted. Only passive media whose type can be
		// recognized from the bytes may be served on the hub origin; this is not a full codec validation.
		m.Mime = http.DetectContentType(m.Data)
		switch m.Mime {
		case "image/jpeg", "image/png", "image/gif", "image/webp",
			"audio/aiff", "audio/mpeg", "audio/midi", "audio/wave",
			"video/avi", "video/mp4", "video/webm", "application/ogg":
		default:
			writeErr(w, http.StatusUnsupportedMediaType, errors.New("unsupported media type"))
			return
		}
		if !cached {
			if err := h.media.put(h.mediaDir(), id, ref, m); err != nil {
				h.logf("connector %s: keep media: %v", id, err)
			}
		}
		w.Header().Set("Content-Type", m.Mime)
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		_, _ = w.Write(m.Data)
	})
	// A reply in the thread through the action its page named (ThreadChat.Reply). Sent from the user's own app it runs
	// as they sent it; anything else (the pairing token: a script, maybe the AI's) goes through the gate like the
	// assistant's writes, by default allowed on the phone with the card's words (to whom, the whole text).
	api.HandleFunc("POST /api/v1/connectors/{id}/thread/reply", func(w http.ResponseWriter, r *http.Request) {
		c, _, ok := h.threadConnector(w, r)
		if !ok {
			return
		}
		var in struct {
			Thread  string `json:"thread"`
			Action  string `json:"action"`
			Text    string `json:"text"`
			ReplyTo string `json:"reply_to"`
		}
		if err := decodeJSONBody(w, r, &in, 1<<20); err != nil || in.Thread == "" || strings.TrimSpace(in.Text) == "" {
			writeBodyErr(w, err, errors.New("thread and text are required"))
			return
		}
		a, ok := connector.As[connector.Actions](c)
		if !ok || !strings.HasPrefix(in.Action, c.ID()+"_") || !slices.ContainsFunc(a.Actions(), func(t proto.Tool) bool { return t.Name == in.Action }) {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("%s has no action %q", c.Name(), in.Action))
			return
		}
		args := map[string]any{"chat": in.Thread, "text": in.Text}
		if in.ReplyTo != "" {
			if n, err := strconv.Atoi(in.ReplyTo); err == nil {
				args["reply_to"] = n // a message id the connector counts in (Telegram's)
			} else {
				args["reply_to"] = in.ReplyTo
			}
		}
		raw, _ := json.Marshal(args)
		var res proto.ToolResult
		if byOwnDevice(r.Context()) {
			res = h.connectors.Run(r.Context(), in.Action, raw)
		} else {
			res = h.callConnector(r.Context(), asker{}, in.Action, raw)
		}
		text := ""
		if len(res.Content) > 0 {
			text = res.Content[0].Text
		}
		if res.IsError {
			status := http.StatusBadGateway
			switch {
			case res.ErrorKind == proto.ToolErrorDeclined:
				status = http.StatusForbidden
			case res.ErrorKind == proto.ToolErrorRateLimited, connector.StatusOf(r.Context(), c).State == connector.StateRateLimited:
				status = http.StatusTooManyRequests
			}
			writeErrCode(w, status, map[int]string{http.StatusForbidden: "declined", http.StatusTooManyRequests: "rate_limited"}[status], errors.New(text))
			return
		}
		var out json.RawMessage
		if json.Valid([]byte(text)) {
			out = json.RawMessage(text)
		} else {
			out, _ = json.Marshal(text)
		}
		writeJSON(w, map[string]any{"ok": true, "result": out})
	})
}

// mediaDir is where connectors' media are kept: <hub dir>/cache/connector-media/<connector>/<hash of the ref>.
func (h *Manager) mediaDir() string { return filepath.Join(h.cfg.dir, "cache", "connector-media") }

// mediaCache keeps connectors' media on disk and fetches one ref once at a time (an avatar on every row of a page).
type mediaCache struct {
	mu       sync.Mutex
	inflight map[string]*mediaFetch
	writes   int
}

type mediaFetch struct {
	done chan struct{}
	m    connector.Media
	err  error
}

func mediaFile(dir, id, ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return filepath.Join(dir, id, hex.EncodeToString(sum[:16]))
}

// get is a kept media: the file holds its type on the first line, then the bytes.
func (c *mediaCache) get(dir, id, ref string) (connector.Media, bool) {
	b, err := os.ReadFile(mediaFile(dir, id, ref))
	if err != nil {
		return connector.Media{}, false
	}
	mime, data, ok := strings.Cut(string(b), "\n")
	if !ok || data == "" {
		return connector.Media{}, false
	}
	return connector.Media{Mime: mime, Data: []byte(data)}, true
}

func (c *mediaCache) put(dir, id, ref string, m connector.Media) error {
	f := mediaFile(dir, id, ref)
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, append([]byte(strings.ReplaceAll(m.Mime, "\n", "")+"\n"), m.Data...), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, f); err != nil {
		return err
	}
	c.mu.Lock()
	c.writes++
	prune := c.writes%50 == 1
	c.mu.Unlock()
	if prune {
		pruneMedia(dir, mediaCacheMost)
	}
	return nil
}

// fetch runs get once for key however many ask for it at the same time.
func (c *mediaCache) fetch(ctx context.Context, key string, get func(context.Context) (connector.Media, error)) (connector.Media, error) {
	c.mu.Lock()
	if f, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.m, f.err
		case <-ctx.Done():
			return connector.Media{}, ctx.Err()
		}
	}
	if c.inflight == nil {
		c.inflight = map[string]*mediaFetch{}
	}
	f := &mediaFetch{done: make(chan struct{})}
	c.inflight[key] = f
	c.mu.Unlock()
	f.m, f.err = get(ctx)
	c.mu.Lock()
	delete(c.inflight, key)
	c.mu.Unlock()
	close(f.done)
	return f.m, f.err
}

// pruneMedia removes the least recently written media until what is kept is under most bytes.
func pruneMedia(dir string, most int64) {
	type kept struct {
		path string
		size int64
		at   time.Time
	}
	var all []kept
	var total int64
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			all = append(all, kept{p, info.Size(), info.ModTime()})
			total += info.Size()
		}
		return nil
	})
	if total <= most {
		return
	}
	slices.SortFunc(all, func(a, b kept) int { return a.at.Compare(b.at) })
	for _, k := range all {
		if total <= most {
			return
		}
		if os.Remove(k.path) == nil {
			total -= k.size
		}
	}
}
