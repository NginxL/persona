package extmgr

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Extensions being written (`personad extension dev`, docs/extensions/extensions.md "Command line"): run from their own directory,
// kept in memory only, and what they log and the hub's calls to them streamed to the command. Only over loopback with
// the master token, like installing.

// devTaps are the streams of each dev extension's log lines.
type devTaps struct {
	mu   sync.Mutex
	taps map[string]map[chan string]struct{}
}

func (t *devTaps) publish(id, line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for ch := range t.taps[id] {
		select {
		case ch <- line:
		default: // a slow reader misses lines rather than slowing the extension
		}
	}
}

func (t *devTaps) subscribe(id string) (<-chan string, func()) {
	ch := make(chan string, 256)
	t.mu.Lock()
	if t.taps == nil {
		t.taps = map[string]map[chan string]struct{}{}
	}
	if t.taps[id] == nil {
		t.taps[id] = map[chan string]struct{}{}
	}
	t.taps[id][ch] = struct{}{}
	t.mu.Unlock()
	return ch, func() {
		t.mu.Lock()
		delete(t.taps[id], ch)
		t.mu.Unlock()
	}
}

// devObserver tells a dev extension's stream what the host sees of it (its log, the hub's calls to it, its runs),
// and passes it all on to the extension's stats.
type devObserver struct {
	next host.Observer
	taps *devTaps
	id   string
}

func (o *devObserver) Call(method string, took time.Duration, code string) {
	line := fmt.Sprintf("→ %s %s", method, took.Round(time.Millisecond))
	if code != "" {
		line += "：" + code
	}
	o.taps.publish(o.id, line)
	if o.next != nil {
		o.next.Call(method, took, code)
	}
}

func (o *devObserver) Run(event, detail string) {
	line := "· " + event
	if detail != "" {
		line += "：" + detail
	}
	o.taps.publish(o.id, line)
	if o.next != nil {
		o.next.Run(event, detail)
	}
}

func (o *devObserver) Log(source, line string) {
	if source == host.LogHub {
		line = "hub: " + line
	}
	o.taps.publish(o.id, line)
	if o.next != nil {
		o.next.Log(source, line)
	}
}

func (h *Manager) extensionDevRoutes(api *http.ServeMux) {
	ps := func() *extensions { return h.extensions }
	owner := func(f http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !h.fromThisMachine(r) {
				writeErrCode(w, http.StatusForbidden, "this_machine_only", i18n.Err("hub-extension_dev.owner_only"))
				return
			}
			if ps() == nil {
				writeErrCode(w, http.StatusServiceUnavailable, "no_plugins", errors.New("extensions are not set up"))
				return
			}
			f(w, r)
		}
	}
	api.HandleFunc("POST /api/v1/extensions/-/dev", owner(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Dir     string            `json:"dir"`
			Config  json.RawMessage   `json:"config,omitempty"`
			Secrets map[string]string `json:"secrets,omitempty"`
		}
		if !readExtensionJSON(w, r, &in) {
			return
		}
		if !filepath.IsAbs(in.Dir) {
			writeErrCode(w, http.StatusBadRequest, "invalid_plugin", errors.New("dir: an absolute path on the hub's machine"))
			return
		}
		p, err := ps().reg.AddDevContext(r.Context(), filepath.Clean(in.Dir), in.Config, in.Secrets)
		if err != nil {
			writeErrCode(w, http.StatusBadRequest, "invalid_plugin", err)
			return
		}
		writeJSON(w, detailOut(r, ps().detail(r.Context(), p)))
	}))
	api.HandleFunc("POST /api/v1/extensions/{id}/-/dev/restart", owner(func(w http.ResponseWriter, r *http.Request) {
		if err := ps().reg.RestartDev(r.PathValue("id")); err != nil {
			writeErrCode(w, http.StatusNotFound, "not_found", err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}))
	api.HandleFunc("DELETE /api/v1/extensions/{id}/-/dev", owner(func(w http.ResponseWriter, r *http.Request) {
		if err := ps().reg.RemoveDev(r.PathValue("id")); err != nil {
			writeErrCode(w, http.StatusNotFound, "not_found", err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}))
	// The stream: one line of text per log line or call, until the command goes away.
	api.HandleFunc("GET /api/v1/extensions/{id}/-/dev/logs", owner(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if p := ps().reg.Get(id); p == nil || p.Record.Source != extension.SourceDev {
			writeErrCode(w, http.StatusNotFound, "not_found", i18n.Err("hub-extension_dev.not_found", i18n.Args{"id": id}))
			return
		}
		lines, stop := ps().dev.subscribe(id)
		defer stop()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Time{}) // a stream, not one answer
		flush := func() { _ = rc.Flush() }
		_, _ = fmt.Fprintln(w, "已连上，等它的日志")
		flush()
		ping := time.NewTicker(30 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case l := <-lines:
				if _, err := fmt.Fprintln(w, l); err != nil {
					return
				}
				flush()
			case <-ping.C: // keeps the connection from looking idle
				if _, err := fmt.Fprint(w, ""); err != nil {
					return
				}
				flush()
			}
		}
	}))
}
