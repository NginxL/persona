package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Reject only the nudge write, not updateBot's earlier, separate settings commit.
// All data and failures are synthetic; no bot connection or external service starts.
type nudgePersistenceStore struct {
	*botStore
	control sync.Mutex
	fail    bool
	entered chan struct{}
	release chan struct{}
}

func (s *nudgePersistenceStore) PutSetting(ctx context.Context, key string, value any) error {
	settings, ok := value.(FeishuBotSettings)
	s.control.Lock()
	fail := s.fail && key == botSettingsKey && ok && settings.NudgeAfter != nil && *settings.NudgeAfter == 0
	entered, release := s.entered, s.release
	if fail {
		s.entered = nil
	}
	s.control.Unlock()
	if fail {
		if entered != nil {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return errors.New("synthetic storage failure detail")
	}
	return s.botStore.PutSetting(ctx, key, value)
}

func newNudgePersistenceManager(t *testing.T, initial *int) (*Manager, *nudgePersistenceStore, http.Handler, string) {
	t.Helper()
	h := newTestManager(t)
	st := &nudgePersistenceStore{botStore: newBotStore(), fail: true}
	h.store = st
	withAssistant(h)
	secret, err := h.sealKey("synthetic-app-secret")
	if err != nil {
		t.Fatal(err)
	}
	delay := 7
	seed := FeishuBotSettings{
		AppID: "cli_example", SecretEnc: secret, Domain: "lark",
		Features:   BotFeatures{Main: true, Cards: true, Reports: true, Agents: true},
		Owner:      &BotOwner{OpenID: "ou_example", ChatID: "oc_owner", Name: "Example", At: time.Unix(100, 0).UTC()},
		Pairing:    &botPairing{Code: "123456", Expires: time.Unix(200, 0).UTC(), Fails: 1},
		TopicChats: []BotChat{{ChatID: "oc_topic", RequireOwnerMention: true}},
		AISources:  []BotAISource{{ChatID: "oc_source", MessageID: "om_example", Replies: 1}},
		NudgeAfter: initial, SourceReportDelayMinutes: &delay, SourceReportFullReply: true,
	}
	if err := st.botStore.PutSetting(t.Context(), botSettingsKey, seed); err != nil {
		t.Fatal(err)
	}
	return h, st, h.routes(), deviceToken("nudge_phone", roleFull)
}

func nudgePersistenceSnapshot(t *testing.T, h *Manager, st *nudgePersistenceStore) FeishuBotSettings {
	t.Helper()
	h.bots.mu.Lock()
	h.loadBotsLocked(t.Context())
	live := h.bots.set.copy()
	h.bots.mu.Unlock()
	var saved FeishuBotSettings
	if ok, err := st.Setting(t.Context(), botSettingsKey, &saved); err != nil || !ok {
		t.Fatalf("read saved settings: %v, %v", ok, err)
	}
	if !reflect.DeepEqual(live, saved) {
		t.Errorf("live and saved settings differ:\nlive: %+v\nsaved: %+v", live, saved)
	}
	return saved
}

func TestBotNudgePersistenceFailure(t *testing.T) {
	needsLarkModule(t)
	thirty := 30
	for _, tc := range []struct {
		name    string
		initial *int
		want    float64
	}{
		{"explicit", &thirty, 30}, {"unset", nil, 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, st, mux, token := newNudgePersistenceManager(t, tc.initial)
			before := nudgePersistenceSnapshot(t, h, st)
			code, out := call(t, mux, "PUT", "/api/v1/bots/feishu", token, `{"nudge_after":0}`)
			if code != http.StatusInternalServerError {
				t.Errorf("failed write reported HTTP %d, body %v; want 500", code, out)
			}
			if out["error"] != i18n.T(i18n.Default, "hub-bots.nudge_save_failed") {
				t.Errorf("save error = %v", out)
			}
			message, _ := out["error"].(string)
			if strings.Contains(message, "synthetic storage") {
				t.Error("internal store error exposed")
			}
			_, view := call(t, mux, "GET", "/api/v1/bots/feishu", token, "")
			if view["nudge_after"] != tc.want {
				t.Errorf("live nudge_after = %v, want %v", view["nudge_after"], tc.want)
			}
			if saved := nudgePersistenceSnapshot(t, h, st); !reflect.DeepEqual(saved, before) {
				t.Errorf("failed nudge changed saved settings: %+v", saved)
			}
			if rows := st.takeAudit(); len(rows) != 0 {
				t.Errorf("failed write left success audit: %+v", rows)
			}

			// A fresh manager reads the committed value, including nil rather than an explicit default.
			reloaded := newTestManager(t)
			reloaded.store = st
			withAssistant(reloaded)
			_, view = call(t, reloaded.routes(), "GET", "/api/v1/bots/feishu", token, "")
			if view["nudge_after"] != tc.want {
				t.Errorf("reloaded nudge_after = %v, want %v", view["nudge_after"], tc.want)
			}

			st.control.Lock()
			st.fail = false
			st.control.Unlock()
			for range 2 {
				code, out = call(t, mux, "PUT", "/api/v1/bots/feishu", token, `{"nudge_after":0}`)
				if code != 200 || out["nudge_after"] != float64(0) {
					t.Fatalf("retry: %d %v", code, out)
				}
			}
			expected := before.copy()
			zero := 0
			expected.NudgeAfter = &zero
			if saved := nudgePersistenceSnapshot(t, h, st); !reflect.DeepEqual(saved, expected) {
				t.Errorf("retry changed unrelated settings: %+v", saved)
			}
			rows := st.takeAudit()
			if len(rows) != 1 || rows[0].Action != auditBotNudgeAfter || rows[0].Actor != "device:nudge_phone" || rows[0].Detail["minutes"] != float64(0) {
				t.Errorf("retry audit: %+v", rows)
			}
			// Read through the assistant's existing status entry too.
			raw, handled, err := h.AppCall(t.Context(), assistantwire.AppCall{AppID: assistantwire.AppFeishu, Method: assistantwire.FeishuStatusGet})
			var status map[string]any
			if !handled || err != nil || json.Unmarshal(raw, &status) != nil || status["nudge_after"] != float64(0) {
				t.Errorf("assistant status: %s, %v", raw, err)
			}
		})
	}
}

func TestBotNudgePersistencePermissions(t *testing.T) {
	needsLarkModule(t)
	h, st, mux, token := newNudgePersistenceManager(t, nil)
	before := nudgePersistenceSnapshot(t, h, st)
	for _, invalid := range []string{"", "invalid-token", h.extTokens.issueAll("example", "Example"), h.extTokens.issue("reader", "Reader", []string{extension.ScopeAgentsRead})} {
		code, _ := call(t, mux, "PUT", "/api/v1/bots/feishu", invalid, `{"nudge_after":0}`)
		if code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Errorf("unauthorized save: HTTP %d", code)
		}
	}
	for _, body := range []string{`{}`, `{"nudge_after":null}`} {
		if code, out := call(t, mux, "PUT", "/api/v1/bots/feishu", token, body); code != 200 || out["nudge_after"] != float64(60) {
			t.Errorf("omitted setting: %d %v", code, out)
		}
	}
	if saved := nudgePersistenceSnapshot(t, h, st); !reflect.DeepEqual(saved, before) {
		t.Error("unauthorized or omitted setting changed state")
	}
	if rows := st.takeAudit(); len(rows) != 0 {
		t.Errorf("unexpected audit: %+v", rows)
	}
}

func TestBotNudgePersistenceConcurrentSave(t *testing.T) {
	needsLarkModule(t)
	for _, tc := range []struct {
		name, body string
		minutes    int
		fullReply  bool
	}{
		{"another nudge", `{"nudge_after":90}`, 90, true},
		{"unrelated setting", `{"source_report_full_reply":false}`, 30, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			thirty := 30
			h, st, mux, token := newNudgePersistenceManager(t, &thirty)
			before := nudgePersistenceSnapshot(t, h, st)
			st.entered, st.release = make(chan struct{}), make(chan struct{})
			entered, release := st.entered, st.release
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			type result struct {
				code int
				out  map[string]any
			}
			failed, succeeding := make(chan result, 1), make(chan result, 1)
			go func() {
				code, out := call(t, mux, "PUT", "/api/v1/bots/feishu", token, `{"nudge_after":0}`)
				failed <- result{code, out}
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("nudge write never reached the store")
			}
			started := make(chan struct{})
			go func() {
				close(started)
				code, out := call(t, mux, "PUT", "/api/v1/bots/feishu", token, tc.body)
				succeeding <- result{code, out}
			}()
			<-started
			eventually(t, 5*time.Second, "both settings requests admitted during the blocked write", func() bool {
				return h.Serving() == 2
			})
			unblock()
			for _, want := range []struct {
				ch   <-chan result
				code int
			}{{failed, 500}, {succeeding, 200}} {
				select {
				case got := <-want.ch:
					if got.code != want.code {
						t.Errorf("save HTTP %d, want %d: %v", got.code, want.code, got.out)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("concurrent save did not finish")
				}
			}
			expected := before.copy()
			expected.NudgeAfter = &tc.minutes
			expected.SourceReportFullReply = tc.fullReply
			if saved := nudgePersistenceSnapshot(t, h, st); !reflect.DeepEqual(saved, expected) {
				t.Errorf("concurrent save lost fields: %+v", saved)
			}
			rows := st.takeAudit()
			for _, row := range rowsWith(rows, auditBotNudgeAfter) {
				if row.Detail["minutes"] == float64(0) {
					t.Error("failed concurrent nudge was audited")
				}
			}
		})
	}
}

func TestBotNudgePersistenceLocalizedFailure(t *testing.T) {
	needsLarkModule(t)
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		t.Run(string(lang), func(t *testing.T) {
			_, _, mux, token := newNudgePersistenceManager(t, nil)
			localized := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.Header.Set("Accept-Language", string(lang))
				mux.ServeHTTP(w, r)
			})
			code, out := call(t, localized, "PUT", "/api/v1/bots/feishu", token, `{"nudge_after":0}`)
			if code != 500 || out["error"] != i18n.T(lang, "hub-bots.nudge_save_failed") {
				t.Errorf("localized error: %d %v", code, out)
			}
		})
	}
}

// The existing route saves its fields in steps. A nudge failure must not undo
// an earlier successful feature save or continue with later settings writes.
func TestBotNudgePersistenceKeepsEarlierSave(t *testing.T) {
	needsLarkModule(t)
	thirty := 30
	h, st, mux, token := newNudgePersistenceManager(t, &thirty)
	before := nudgePersistenceSnapshot(t, h, st)
	code, out := call(t, mux, "PUT", "/api/v1/bots/feishu", token, `{"features":{"read_group":true},"nudge_after":0,"source_report_full_reply":false}`)
	if code != 500 {
		t.Errorf("failed nudge: %d %v", code, out)
	}
	expected := before.copy()
	expected.Features.ReadGroup = true
	if saved := nudgePersistenceSnapshot(t, h, st); !reflect.DeepEqual(saved, expected) {
		t.Errorf("separate settings commits changed: %+v", saved)
	}
	rows := st.takeAudit()
	if len(rows) != 1 || rows[0].Action != auditBotFeatures {
		t.Errorf("only the successful feature change should be audited: %+v", rows)
	}
}
