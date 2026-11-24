package voice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// listService is an upstream's GET /models and GET /voices that can be made to fail (#2411).
type listService struct {
	srv          *httptest.Server
	modelsFail   atomic.Int32 // the status /models answers with; 0: its list
	voicesFail   atomic.Int32 // the status /voices answers with; 0: its list
	voicesAsked  atomic.Int32
	voicesPrefix atomic.Value // the voices' id prefix, so a test can tell one read from the next
}

func newListService(t *testing.T) *listService {
	s := &listService{}
	s.voicesPrefix.Store("v")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/live/models", func(w http.ResponseWriter, r *http.Request) {
		if code := s.modelsFail.Load(); code != 0 {
			http.Error(w, "upstream is down", int(code))
			return
		}
		_, _ = io.WriteString(w, `{"models":[{"id":"m1"},{"id":"m2"}]}`)
	})
	mux.HandleFunc("GET /v1/live/voices", func(w http.ResponseWriter, r *http.Request) {
		s.voicesAsked.Add(1)
		if code := s.voicesFail.Load(); code != 0 {
			http.Error(w, "upstream is down", int(code))
			return
		}
		id := s.voicesPrefix.Load().(string) + "_" + r.URL.Query().Get("model")
		_, _ = fmt.Fprintf(w, `{"voices":[{"id":%q,"name":"V"}],"defaultVoice":%q}`, id, id)
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *listService) url() string { return "ws" + strings.TrimPrefix(s.srv.URL, "http") + "/v1/live" }

// testCatalog is a catalog over one live upstream, its clock moved by hand and its log kept.
type testCatalog struct {
	*catalog
	mu    sync.Mutex
	at    time.Time
	lines []string
}

func newTestCatalog(p Provider) *testCatalog {
	if p.ID == "" {
		p.ID = "rt"
	}
	cfg := Config{Providers: []Provider{p}}
	tc := &testCatalog{at: time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)}
	tc.catalog = &catalog{cfg: &cfg, now: func() time.Time {
		tc.mu.Lock()
		defer tc.mu.Unlock()
		return tc.at
	}, logf: func(format string, args ...any) {
		tc.mu.Lock()
		defer tc.mu.Unlock()
		tc.lines = append(tc.lines, fmt.Sprintf(format, args...))
	}}
	return tc
}

func (tc *testCatalog) advance(d time.Duration) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.at = tc.at.Add(d)
}

func (tc *testCatalog) logged() []string {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return append([]string(nil), tc.lines...)
}

func ids(v voiceList) string {
	var out []string
	for _, c := range v.Voices {
		out = append(out, c.ID)
	}
	return strings.Join(out, ",")
}

// A list the service gave stands in while it cannot give one, marked stale; the failure is logged once, and a good
// read clears both.
func TestVoicesFallBackToTheLastGoodList(t *testing.T) {
	up := newListService(t)
	tc := newTestCatalog(Provider{URL: up.url(), Key: "k"})
	v, err := tc.voices(t.Context(), "rt/m1")
	if err != nil || ids(v) != "v_m1" || v.Stale {
		t.Fatalf("first read: %+v %v", v, err)
	}
	// Kept 10 minutes: the service is not asked again meanwhile.
	tc.advance(9 * time.Minute)
	if v, _ := tc.voices(t.Context(), "rt/m1"); ids(v) != "v_m1" || v.Stale || up.voicesAsked.Load() != 1 {
		t.Fatalf("cached: %+v asked %d", v, up.voicesAsked.Load())
	}
	tc.advance(2 * time.Minute)
	up.voicesFail.Store(http.StatusBadGateway)
	for range 3 {
		v, err = tc.voices(t.Context(), "rt/m1")
		if err != nil || ids(v) != "v_m1" || !v.Stale || v.Default != "v_m1" {
			t.Fatalf("while it fails: %+v %v", v, err)
		}
	}
	if up.voicesAsked.Load() != 4 {
		t.Fatalf("a failure is not kept as an answer: asked %d times", up.voicesAsked.Load())
	}
	if lines := tc.logged(); len(lines) != 1 || !strings.Contains(lines[0], "voices:rt/m1") || !strings.Contains(lines[0], "HTTP 502") ||
		!strings.Contains(lines[0], "last good list") || strings.Contains(lines[0], "upstream is down") {
		t.Fatalf("logged once, without the service's words: %q", lines)
	}
	up.voicesFail.Store(0)
	up.voicesPrefix.Store("w")
	if v, _ := tc.voices(t.Context(), "rt/m1"); ids(v) != "w_m1" || v.Stale {
		t.Fatalf("read again: %+v", v)
	}
	// Failing again after it read is logged again.
	tc.advance(11 * time.Minute)
	up.voicesFail.Store(http.StatusServiceUnavailable)
	if v, _ := tc.voices(t.Context(), "rt/m1"); ids(v) != "w_m1" || !v.Stale {
		t.Fatalf("the newer good list: %+v", v)
	}
	if lines := tc.logged(); len(lines) != 2 || !strings.Contains(lines[1], "HTTP 503") {
		t.Fatalf("%q", lines)
	}
}

// With no good list yet, the dialect's own stands in, marked stale and not kept: the service's comes as soon as it
// answers.
func TestVoicesFallBackToTheBuiltInList(t *testing.T) {
	up := newListService(t)
	up.voicesFail.Store(http.StatusInternalServerError)
	tc := newTestCatalog(Provider{URL: up.url(), Key: "k"})
	v, err := tc.voices(t.Context(), "rt/m1")
	if err != nil || !v.Stale || v.Default != "marin" || len(v.Voices) != len(liveVoices) || v.Voices[0].Name != "Alloy" {
		t.Fatalf("built-in: %+v %v", v, err)
	}
	up.voicesFail.Store(0)
	if v, _ := tc.voices(t.Context(), "rt/m1"); ids(v) != "v_m1" || v.Stale {
		t.Fatalf("no time has passed, and the service's list comes: %+v", v)
	}
	// A codex upstream has its own list and is never asked.
	codex := newTestCatalog(Provider{URL: "wss://gateway.example.com/v1/live", Dialect: DialectCodex})
	if v, err := codex.voices(t.Context(), "rt/gpt-live-1-codex"); err != nil || v.Stale || v.Default != "cove" {
		t.Fatalf("codex: %+v %v", v, err)
	}
}

// A named model's fallback is its own last good list or the built-in one, never another model's or another
// upstream's: their voices may not exist for it.
func TestVoicesNeverFallBackToAnotherModelsList(t *testing.T) {
	up := newListService(t)
	other := newListService(t)
	tc := newTestCatalog(Provider{URL: up.url(), Key: "k"})
	tc.cfg.Providers = append(tc.cfg.Providers, Provider{ID: "rt2", URL: other.url(), Key: "k"})
	other.voicesPrefix.Store("o")
	if v, err := tc.voices(t.Context(), "rt/m1"); err != nil || ids(v) != "v_m1" {
		t.Fatalf("m1: %+v %v", v, err)
	}
	if v, err := tc.voices(t.Context(), "rt2/m2"); err != nil || ids(v) != "o_m2" {
		t.Fatalf("rt2/m2: %+v %v", v, err)
	}
	up.voicesFail.Store(http.StatusBadGateway)
	other.voicesFail.Store(http.StatusBadGateway)
	for _, model := range []string{"rt/m2", "rt2/m1"} {
		v, err := tc.voices(t.Context(), model)
		if err != nil || !v.Stale || v.Default != "marin" || len(v.Voices) != len(liveVoices) {
			t.Fatalf("%s while failing, never read: the built-in list, got %+v %v", model, v, err)
		}
	}
}

// A service without a voices list (404) has the built-in voices as its list: not stale, kept like any good answer.
func TestVoicesOfAServiceWithoutAList(t *testing.T) {
	up := newListService(t)
	up.voicesFail.Store(http.StatusNotFound)
	tc := newTestCatalog(Provider{URL: up.url(), Key: "k"})
	for range 2 {
		if v, err := tc.voices(t.Context(), "rt/m1"); err != nil || v.Stale || v.Default != "marin" {
			t.Fatalf("%+v %v", v, err)
		}
	}
	if up.voicesAsked.Load() != 1 || len(tc.logged()) != 0 {
		t.Fatalf("asked %d, logged %q", up.voicesAsked.Load(), tc.logged())
	}
}

// No model named and the service's model list unreadable (the default model unknown): the voices still come, the
// last good list of the upstream's, else the built-in one, instead of a 502 (#2411). Once the model list was read, its
// last good copy finds the default model.
func TestVoicesWhenTheDefaultModelCannotBeRead(t *testing.T) {
	up := newListService(t)
	up.modelsFail.Store(http.StatusBadGateway)
	tc := newTestCatalog(Provider{URL: up.url(), Key: "k"})
	v, err := tc.voices(t.Context(), "")
	if err != nil || !v.Stale || v.Default != "marin" {
		t.Fatalf("never read: %+v %v", v, err)
	}
	up.modelsFail.Store(0)
	if v, _ := tc.voices(t.Context(), ""); ids(v) != "v_m1" || v.Stale {
		t.Fatalf("the default model's: %+v", v)
	}
	tc.advance(11 * time.Minute)
	up.modelsFail.Store(http.StatusBadGateway)
	up.voicesFail.Store(http.StatusBadGateway)
	if v, _ := tc.voices(t.Context(), ""); ids(v) != "v_m1" || !v.Stale {
		t.Fatalf("both lists failing: the last good ones: %+v", v)
	}
	// A model of another provider, or no upstream at all, is still an error.
	if _, err := tc.voices(t.Context(), "nope/x"); err == nil {
		t.Fatal("unknown provider")
	}
	empty := &catalog{cfg: &Config{}}
	if _, err := empty.voices(t.Context(), ""); err == nil {
		t.Fatal("no upstream")
	}
}

// The API answers 200 with stale: true where it answered 502, and the log never carries the credential or what the
// key command printed.
func TestVoicesEndpointAnswersStale(t *testing.T) {
	up := newListService(t)
	up.voicesFail.Store(http.StatusBadGateway)
	var logs bytes.Buffer
	pl := New(Config{Providers: []Provider{
		{ID: "rt", URL: up.url(), Key: "k", Dialect: DialectLive},
		{ID: "cmd", URL: up.url(), KeyCommand: "echo very-secret-token >&2; exit 3", Dialect: DialectLive},
	}}, "http://127.0.0.1:1", "", log.New(&logs, "", 0))
	srv := httptest.NewServer(pl.Handler())
	defer srv.Close()
	for _, model := range []string{"rt/m1", "cmd/m1"} {
		res, err := http.Get(srv.URL + "/voices?model=" + model)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		_ = json.NewDecoder(res.Body).Decode(&body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK || body["stale"] != true || body["default"] != "marin" {
			t.Fatalf("%s: %d %v", model, res.StatusCode, body)
		}
	}
	if out := logs.String(); !strings.Contains(out, "voices:cmd/m1") || strings.Contains(out, "very-secret-token") || strings.Contains(out, "Bearer") {
		t.Fatalf("log: %s", out)
	}
}
