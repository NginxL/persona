package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// setAttentionClockForTest makes the attention mode read now instead of the wall clock.
func (s *Service) setAttentionClockForTest(now func() time.Time) {
	s.attention.mu.Lock()
	s.attention.now = now
	s.attention.cached = nil
	s.attention.mu.Unlock()
}

// attentionCall calls an attention route as role ("" is full) and decodes the answer.
func attentionCall(t *testing.T, s *Service, method, role, body string) (int, map[string]json.RawMessage) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, "/api/v1/assistant/attention", strings.NewReader(body))
	if role != "" {
		r.Header.Set(RoleHeader, role)
	}
	s.ServeHTTP(w, r)
	var out map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func decodeAttention(t *testing.T, raw json.RawMessage) assistantwire.Attention {
	t.Helper()
	var a assistantwire.Attention
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("attention %s: %v", raw, err)
	}
	return a
}

func TestNewAttentionValidates(t *testing.T) {
	now := time.Date(2026, 11, 22, 21, 30, 0, 0, time.UTC)
	known := func(slug string) bool { return slug == "qa-plain" }
	for _, c := range []struct {
		name string
		set  assistantwire.AttentionSet
		ok   bool
	}{
		{"normal needs no minutes", assistantwire.AttentionSet{Mode: "normal"}, true},
		{"unknown mode", assistantwire.AttentionSet{Mode: "focus", Minutes: 30}, false},
		{"empty mode", assistantwire.AttentionSet{Minutes: 30}, false},
		{"a mode needs a time limit", assistantwire.AttentionSet{Mode: "quiet"}, false},
		{"a minute at least", assistantwire.AttentionSet{Mode: "fragment", Minutes: 0}, false},
		{"a day at most", assistantwire.AttentionSet{Mode: "quiet", Minutes: 24*60 + 1}, false},
		{"a whole day", assistantwire.AttentionSet{Mode: "quiet", Minutes: 24 * 60}, true},
		{"negative minutes", assistantwire.AttentionSet{Mode: "immersive", Minutes: -5}, false},
		{"a known track in focus", assistantwire.AttentionSet{Mode: "immersive", Minutes: 30, Focus: &assistantwire.AttentionFocus{Track: "qa-plain"}}, true},
		{"an unknown track in focus", assistantwire.AttentionSet{Mode: "immersive", Minutes: 30, Focus: &assistantwire.AttentionFocus{Track: "qa-gone"}}, false},
		{"a session in focus", assistantwire.AttentionSet{Mode: "immersive", Minutes: 30, Focus: &assistantwire.AttentionFocus{MachineID: "mac", SessionID: "s1"}}, true},
		{"a session id too long", assistantwire.AttentionSet{Mode: "immersive", Minutes: 30, Focus: &assistantwire.AttentionFocus{SessionID: strings.Repeat("x", 201)}}, false},
	} {
		a, ok := newAttention(c.set, assistantwire.AttentionByUser, now, known)
		if ok != c.ok {
			t.Errorf("%s: ok %v, want %v", c.name, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if a.SetBy != assistantwire.AttentionByUser || !a.SetAt.Equal(now) {
			t.Errorf("%s: set by %q at %v", c.name, a.SetBy, a.SetAt)
		}
		if a.Mode == assistantwire.AttentionNormal && !a.Until.IsZero() {
			t.Errorf("%s: normal has an end %v", c.name, a.Until)
		}
		if a.Mode != assistantwire.AttentionNormal && !a.Until.Equal(now.Add(time.Duration(c.set.Minutes)*time.Minute)) {
			t.Errorf("%s: ends %v", c.name, a.Until)
		}
	}
	// A focus is immersive mode's only.
	if a, ok := newAttention(assistantwire.AttentionSet{Mode: "quiet", Minutes: 30, Focus: &assistantwire.AttentionFocus{Track: "qa-plain"}},
		assistantwire.AttentionByUser, now, known); !ok || a.Focus != nil {
		t.Errorf("quiet with a focus: %+v %v", a, ok)
	}
}

func TestAttentionRoutes(t *testing.T) {
	ft := newFleetTest(t)
	s := ft.s
	var mu sync.Mutex
	now := time.Date(2026, 11, 22, 21, 30, 0, 0, time.UTC)
	s.setAttentionClockForTest(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })

	code, out := attentionCall(t, s, http.MethodGet, "", "")
	if code != http.StatusOK || decodeAttention(t, out["attention"]).Mode != assistantwire.AttentionNormal {
		t.Fatalf("before any: %d %s", code, out["attention"])
	}
	// Only the user sets it.
	for _, role := range []string{roleExtension, "agent", "scoped"} {
		if code, _ := attentionCall(t, s, http.MethodPut, role, `{"mode":"quiet","minutes":30}`); code != http.StatusForbidden {
			t.Errorf("%s PUT: %d", role, code)
		}
	}
	if code, _ := attentionCall(t, s, http.MethodPut, roleFull, `{"mode":"quiet"}`); code != http.StatusBadRequest {
		t.Errorf("no minutes: %d", code)
	}
	code, out = attentionCall(t, s, http.MethodPut, roleFull, `{"mode":"immersive","minutes":30,"focus":{"machine_id":"mac","session_id":"s1"}}`)
	if code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	a := decodeAttention(t, out["attention"])
	if a.Mode != assistantwire.AttentionImmersive || !a.Until.Equal(now.Add(30*time.Minute)) || a.Focus == nil || a.Focus.SessionID != "s1" {
		t.Fatalf("PUT answered %+v", a)
	}
	// The hub is told in the profile.
	if p := ft.f.lastSnapshot().Profile; p == nil || p.Attention == nil || p.Attention.Mode != assistantwire.AttentionImmersive {
		t.Fatalf("profile %+v", p)
	}
	// A client without full access reads the mode, never the focus (the hub keeps extension tokens off this route).
	code, out = attentionCall(t, s, http.MethodGet, roleExtension, "")
	if a := decodeAttention(t, out["attention"]); code != http.StatusOK || a.Mode != assistantwire.AttentionImmersive || a.Focus != nil {
		t.Errorf("extension GET: %d %+v", code, a)
	}
	// Immersive mode counts as the low-distraction switch on, and takes questions as ones that can wait.
	if !s.quietModeOn(context.Background()) || !s.attentionDefersQuestions(context.Background()) {
		t.Error("immersive: switch off or questions not deferred")
	}
	// A question from the session in focus goes at once; one from elsewhere waits.
	task := Todo{ID: "t1", Supervision: &Supervision{Supervisor: "lead"}}
	if s.attentionDefersQuestion(context.Background(), task, "mac", "s1") {
		t.Error("the focused session's question was deferred")
	}
	if !s.attentionDefersQuestion(context.Background(), task, "mac", "s2") {
		t.Error("a question outside the focus was not deferred")
	}

	// Its time runs out: normal again, by the system, and the profile says nothing of it.
	mu.Lock()
	now = now.Add(31 * time.Minute)
	mu.Unlock()
	if got := s.attentionNow(context.Background()); got.Active() {
		t.Fatalf("after its end: %+v", got)
	}
	if next := s.attentionDue(context.Background()); !next.IsZero() {
		t.Errorf("next end %v", next)
	}
	if stored := s.attentionStored(context.Background()); stored.Mode != assistantwire.AttentionNormal || stored.SetBy != assistantwire.AttentionBySystem {
		t.Errorf("stored after its end: %+v", stored)
	}
	if p := ft.f.lastSnapshot().Profile; p == nil || p.Attention != nil {
		t.Errorf("profile after its end %+v", p)
	}
	if s.quietModeOn(context.Background()) || s.attentionDefersQuestions(context.Background()) {
		t.Error("normal again but the switch still reads on")
	}
}

// The mode is never turned on by a store that does not answer, nor by an end that is already past.
func TestAttentionNormalWithoutAStore(t *testing.T) {
	ft := newFleetTestNoStore(t)
	if a := ft.s.attentionNow(context.Background()); a.Active() {
		t.Fatalf("no store: %+v", a)
	}
	if code, _ := attentionCall(t, ft.s, http.MethodPut, roleFull, `{"mode":"quiet","minutes":30}`); code != http.StatusServiceUnavailable {
		t.Errorf("PUT without a store: %d", code)
	}
}

// A question is in focus when the focus names its task's track, the session that reports, or the task's supervising
// conversation; with no focus, nothing is.
func TestQuestionInFocus(t *testing.T) {
	task := Todo{ID: "t1", Supervision: &Supervision{Supervisor: "lead"}}
	cases := []struct {
		name  string
		f     *assistantwire.AttentionFocus
		track string
		want  bool
	}{
		{"no focus", nil, "qa-finance", false},
		{"track", &assistantwire.AttentionFocus{Track: "qa-finance"}, "qa-finance", true},
		{"other track", &assistantwire.AttentionFocus{Track: "qa-plain"}, "qa-finance", false},
		{"reporting session", &assistantwire.AttentionFocus{MachineID: "mac", SessionID: "s1"}, "", true},
		{"reporting session, any machine", &assistantwire.AttentionFocus{SessionID: "s1"}, "", true},
		{"reporting session, other machine", &assistantwire.AttentionFocus{MachineID: "pc", SessionID: "s1"}, "", false},
		{"supervisor", &assistantwire.AttentionFocus{MachineID: "hubmac", SessionID: "lead"}, "", true},
		{"supervisor, other machine", &assistantwire.AttentionFocus{MachineID: "pc", SessionID: "lead"}, "", false},
		{"other session", &assistantwire.AttentionFocus{SessionID: "s9"}, "", false},
	}
	for _, c := range cases {
		if got := questionInFocus(c.f, c.track, "hubmac", task, "mac", "s1"); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// A read that began before a write does not cache what it read: the write's mode stays cached.
func TestAttentionStaleReadKeepsTheWrite(t *testing.T) {
	var c attentionClock
	c.mu.Lock()
	gen := c.gen // a read begins
	c.gen++      // putAttention writes quiet and caches it
	written := assistantwire.Attention{Mode: assistantwire.AttentionQuiet}
	c.cached, c.cachedAt = &written, time.Now()
	c.mu.Unlock()
	c.remember(gen, assistantwire.Attention{Mode: assistantwire.AttentionNormal}) // the read ends with the old value
	if c.cached == nil || c.cached.Mode != assistantwire.AttentionQuiet {
		t.Fatalf("cached %+v, want the write's quiet", c.cached)
	}
	c.remember(c.gen, assistantwire.Attention{Mode: assistantwire.AttentionFragment}) // a read with no write meanwhile
	if c.cached.Mode != assistantwire.AttentionFragment {
		t.Fatalf("cached %+v, want the fresh read", c.cached)
	}
}
