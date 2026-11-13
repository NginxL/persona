package hub

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// trafficLog retains bounded diagnostic counters, not application state. Completed
// minute buckets go to the journal so blue-green switches do not erase history.
// A bucket contains only route templates and opaque registration/credential hashes.
type trafficLog struct {
	mu      sync.Mutex
	rows    map[trafficKey]trafficCount
	timer   *time.Timer
	stopped bool
}

type trafficKey struct {
	minute                                                 time.Time
	identity, registration, platform, route, event, action string
	source, platformBasis, sourceBasis                     string
	status                                                 int
}

type trafficCount struct {
	count, bytes, elapsedMS int64
	maxBytes                int64
}

const trafficMaxBuckets = 4096

// trafficIdentity never hashes a bearer token or emits a device's name.
func trafficIdentity(id string) string {
	if id == "" {
		return "unbound"
	}
	sum := sha256.Sum256([]byte("persona-traffic\x00" + id))
	return fmt.Sprintf("%x", sum[:8])
}

func trafficPlatform(platform string) string {
	if len(platform) > 128 {
		return "unknown"
	}
	parts := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(platform)), func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	if len(parts) == 0 {
		return "unknown"
	}
	switch parts[0] {
	case "ios", "iphone":
		return "ios"
	case "watchos", "watch":
		return "watchos"
	case "macos", "mac":
		return "macos"
	case "android":
		return "android"
	case "harmony":
		return "harmony"
	case "web":
		return "web"
	case "visionos":
		return "visionos"
	default:
		return "unknown"
	}
}

func (h *Hub) recordTraffic(key trafficKey, bytes int64, elapsed time.Duration) {
	if h.logger == nil {
		return
	}
	key.minute = key.minute.UTC().Truncate(time.Minute)
	if key.source == "" {
		key.source = "unknown"
	}
	if key.platformBasis == "" {
		key.platformBasis = "unknown"
	}
	if key.sourceBasis == "" {
		key.sourceBasis = "unknown"
	}
	h.traffic.mu.Lock()
	defer h.traffic.mu.Unlock()
	if h.traffic.stopped {
		return
	}
	if h.traffic.rows == nil {
		h.traffic.rows = make(map[trafficKey]trafficCount)
	}
	if _, exists := h.traffic.rows[key]; !exists && len(h.traffic.rows) >= trafficMaxBuckets {
		// Keep accounting at a bounded cardinality even with many authenticated devices.
		key.identity, key.registration, key.platform, key.route, key.event, key.action, key.status = "overflow", "overflow", "unknown", "overflow", "overflow", "overflow", 0
		key.source, key.platformBasis, key.sourceBasis = "unknown", "unknown", "unknown"
	}
	row := h.traffic.rows[key]
	row.count++
	row.bytes += bytes
	row.maxBytes = max(row.maxBytes, bytes)
	row.elapsedMS += elapsed.Milliseconds()
	h.traffic.rows[key] = row
	if h.traffic.timer == nil {
		h.traffic.timer = time.AfterFunc(time.Minute, func() { h.flushTraffic(time.Now(), false) })
	}
}

func (h *Hub) flushTraffic(now time.Time, stop bool) {
	h.traffic.mu.Lock()
	if h.traffic.timer != nil {
		h.traffic.timer.Stop()
		h.traffic.timer = nil
	}
	h.traffic.stopped = h.traffic.stopped || stop
	rows := make(map[trafficKey]trafficCount)
	for key, row := range h.traffic.rows {
		if stop || key.minute.Before(now.UTC().Truncate(time.Minute)) {
			rows[key] = row
			delete(h.traffic.rows, key)
		}
	}
	if !h.traffic.stopped && len(h.traffic.rows) > 0 {
		h.traffic.timer = time.AfterFunc(time.Minute, func() { h.flushTraffic(time.Now(), false) })
	}
	h.traffic.mu.Unlock()
	for key, row := range rows {
		h.logf("traffic minute=%s identity=%s registration=%s platform=%s source=%s platform_basis=%s source_basis=%s route=%q event=%s action=%s status=%d count=%d body_bytes=%d max_body_bytes=%d elapsed_ms=%d", key.minute.Format(time.RFC3339), key.identity, key.registration, key.platform, key.source, key.platformBasis, key.sourceBasis, key.route, key.event, key.action, key.status, row.count, row.bytes, row.maxBytes, row.elapsedMS)
	}
}

type trafficWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *trafficWriter) WriteHeader(status int) {
	if (status >= 200 || status == http.StatusSwitchingProtocols) && w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *trafficWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += int64(n)
	return n, err
}

func (w *trafficWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *trafficWriter) FlushError() error {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

// observeClientRequest runs after authentication. Only registered app clients
// are counted; queries, literal paths, headers, request/response bodies and names
// never enter the key. body_bytes is before outer gzip, not bytes on the wire.
func (h *Hub) observeClientRequest(next http.Handler, w http.ResponseWriter, r *http.Request, p principal) {
	if p.ID == "" || (p.Role != roleFull && p.Role != roleAnswer) {
		next.ServeHTTP(w, r)
		return
	}
	start := time.Now()
	pattern := "unmatched"
	if h.apiMux != nil {
		_, matched := h.apiMux.Handler(r)
		if matched != "" {
			pattern = matched
		}
	}
	a := h.requestTrafficAttribution(r, p)
	key := trafficKey{minute: start, identity: trafficIdentity(p.ID), registration: "none", platform: a.platform, source: a.source, platformBasis: a.platformBasis, sourceBasis: a.sourceBasis, route: pattern, event: "http", action: "started"}
	if r.URL.Path == "/api/v1/ws" {
		key.event = "websocket"
	}
	h.recordTraffic(key, 0, 0)
	writer := &trafficWriter{ResponseWriter: w}
	next.ServeHTTP(writer, r)
	key.minute = time.Now()
	key.action = "completed"
	key.status = writer.status
	if key.status == 0 {
		key.status = http.StatusOK
	}
	contentType, _, _ := strings.Cut(writer.Header().Get("Content-Type"), ";")
	if strings.TrimSpace(contentType) == "text/event-stream" {
		key.event = "sse"
	}
	if r.URL.Path == "/api/v1/ws" && key.status == http.StatusSwitchingProtocols {
		key.event = "websocket"
	}
	h.recordTraffic(key, writer.bytes, time.Since(start))
}
