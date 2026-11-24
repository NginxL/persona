package extmgr

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Only settings writes fail: reads, audit and the fake platform stay available.
type pairWriteStore struct {
	*botStore
	writeMu  sync.Mutex
	reject   bool
	attempts int
	failAt   int
}

func (s *pairWriteStore) PutSetting(ctx context.Context, key string, value any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if key == botSettingsKey {
		s.attempts++
		if s.reject || s.attempts == s.failAt {
			return errors.New("synthetic private storage diagnostic")
		}
	}
	return s.botStore.PutSetting(ctx, key, value)
}

func pairStoreWorld(t *testing.T) (*botWorld, *pairWriteStore, FeishuBotSettings) {
	t.Helper()
	h := newTestManager(t)
	api := newFakeOpenAPI(t)
	secret, err := h.sealKey("synthetic-pair-secret")
	if err != nil {
		t.Fatal(err)
	}
	seed := FeishuBotSettings{Enabled: true, AppID: "cli_pair_test", SecretEnc: secret, Domain: api.srv.URL,
		Features: defaultBotFeatures, Pairing: &botPairing{Code: "654321", Expires: time.Now().UTC().Add(botPairFor)}}
	st := &pairWriteStore{botStore: newBotStore()}
	if err := st.botStore.PutSetting(t.Context(), botSettingsKey, seed); err != nil {
		t.Fatal(err)
	}
	h.store = st
	a := withAssistant(h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.startBots(ctx)
	t.Cleanup(func() {
		cancel()
		h.bots.mu.Lock()
		if h.bots.stop != nil {
			h.bots.stop()
		}
		h.bots.mu.Unlock()
	})
	mux := http.NewServeMux()
	h.botRoutes(mux)
	return &botWorld{h: h, api: api, a: a, mux: mux, ctx: ctx}, st, seed
}

// Wait for the actual botEvent goroutine, including its outgoing note and effects.
func pairStoreEvent(t *testing.T, w *botWorld, id, sender, text string) {
	t.Helper()
	if _, err := w.h.botEvent(w.ctx, botMessageEvent(id, sender, "text", textContent(text), nil)); err != nil {
		t.Fatal(err)
	}
	w.h.bots.mu.Lock()
	done := w.h.bots.line
	w.h.bots.mu.Unlock()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("bot event did not finish")
	}
}

func pairStored(t *testing.T, st *pairWriteStore) FeishuBotSettings {
	t.Helper()
	var saved FeishuBotSettings
	if ok, err := st.Setting(t.Context(), botSettingsKey, &saved); !ok || err != nil {
		t.Fatalf("read: %v %v", ok, err)
	}
	return saved
}

// A name is optional enrichment after the owner/code commit. Its failure cannot
// undo the durable owner or leave a name that a restart would forget.
func TestBotPairStoreNameFailureKeepsCommittedOwner(t *testing.T) {
	needsLarkModule(t)
	w, st, seed := pairStoreWorld(t)
	w.h.opts.AssistantEvent = nil
	st.failAt = 2
	pairStoreEvent(t, w, "om_pair_name_failure", "ou_pair_owner", seed.Pairing.Code)
	api, live := w.h.botAPI()
	saved := pairStored(t, st)
	if api == nil || live.Owner == nil || live.Owner.OpenID != "ou_pair_owner" || live.Owner.Name != "" || live.Pairing != nil || !reflect.DeepEqual(live, saved) {
		t.Fatalf("name failure changed committed owner: live=%+v saved=%+v", live, saved)
	}
	if st.attempts != 2 {
		t.Fatalf("expected owner then name write: %d", st.attempts)
	}
	if rows := rowsWith(st.takeAudit(), auditBotPaired); len(rows) != 1 {
		t.Fatalf("committed owner audit: %v", rows)
	}
	if !strings.Contains(w.api.since(0), i18n.T(w.h.userLang(), "hub-bots_chat.paired_body")) {
		t.Fatal("durable binding lost successful receipt")
	}
	if strings.Contains(w.api.since(0), i18n.T(w.h.userLang(), "hub-bots_chat.pair_save_failed")) {
		t.Fatal("optional name failure reported the binding failed")
	}
	restarted := newTestManager(t)
	restarted.store = st
	_, restored := restarted.botAPI()
	if !reflect.DeepEqual(restored, saved) {
		t.Fatal("restart disagrees with committed binding")
	}
}

// If only the first write fails, the later name write must never persist the
// owner that the redemption failed to commit.
func TestBotPairStoreFirstWriteFailureStopsBeforeEnrichment(t *testing.T) {
	needsLarkModule(t)
	w, st, seed := pairStoreWorld(t)
	st.failAt = 1
	pairStoreEvent(t, w, "om_first_write_failure", "ou_pair_owner", seed.Pairing.Code)
	_, live := w.h.botAPI()
	if st.attempts != 1 || !reflect.DeepEqual(live, seed) || !reflect.DeepEqual(pairStored(t, st), seed) {
		t.Fatalf("failed initial commit reached enrichment or consumed code: attempts=%d live=%+v", st.attempts, live)
	}
	if len(st.takeAudit()) != 0 || w.a.wasTold(assistantwire.EventFeishuOwner) {
		t.Fatal("failed initial write reported success")
	}
	pairStoreEvent(t, w, "om_first_write_retry", "ou_pair_owner", seed.Pairing.Code)
	_, live = w.h.botAPI()
	if live.Owner == nil || live.Pairing != nil || !reflect.DeepEqual(live, pairStored(t, st)) {
		t.Fatal("same-code retry after transient write failure did not commit")
	}
}

func TestBotPairStoreWrongCodeFailureKeepsAttempts(t *testing.T) {
	needsLarkModule(t)
	for _, fails := range []int{0, botPairTries - 1} {
		t.Run(string(rune('0'+fails)), func(t *testing.T) {
			w, st, seed := pairStoreWorld(t)
			seed.Pairing.Fails = fails
			if err := st.botStore.PutSetting(t.Context(), botSettingsKey, seed); err != nil {
				t.Fatal(err)
			}
			w.h.bots.mu.Lock()
			w.h.bots.set = seed.copy()
			w.h.bots.mu.Unlock()
			st.reject = true
			pairStoreEvent(t, w, "om_guess_failed", "ou_stranger", "111111")
			_, live := w.h.botAPI()
			if !reflect.DeepEqual(live, seed) || !reflect.DeepEqual(pairStored(t, st), seed) {
				t.Fatal("failed guess write consumed attempts or expired the code")
			}
			if rows := st.takeAudit(); len(rows) != 0 {
				t.Fatalf("failed guess audited a change: %v", rows)
			}
			if !strings.Contains(w.api.since(0), i18n.T(w.h.userLang(), "hub-bots_chat.pair_save_failed")) {
				t.Fatal("missing failure feedback")
			}
			st.writeMu.Lock()
			st.reject = false
			st.writeMu.Unlock()
			pairStoreEvent(t, w, "om_guess_retry", "ou_stranger", "111111")
			_, live = w.h.botAPI()
			if fails == botPairTries-1 {
				if live.Pairing != nil || len(rowsWith(st.takeAudit(), auditBotPairGuessed)) != 1 {
					t.Fatal("committed final guess did not expire and audit the code")
				}
			} else if live.Pairing == nil || live.Pairing.Fails != fails+1 {
				t.Fatal("successful guess retry did not count exactly once")
			}
			if !reflect.DeepEqual(live, pairStored(t, st)) {
				t.Fatal("guess retry disagrees with storage")
			}
		})
	}
}

func TestBotPairStoreFailureKeepsCodeAndCanRetry(t *testing.T) {
	needsLarkModule(t)
	for _, lang := range []i18n.Lang{"en", "zh-Hans", "ja"} {
		for _, offline := range []bool{false, true} {
			name := string(lang) + "/assistant"
			if offline {
				name = string(lang) + "/hub-greeting"
			}
			t.Run(name, func(t *testing.T) { testBotPairStoreRetry(t, lang, offline) })
		}
	}
}

func testBotPairStoreRetry(t *testing.T, lang i18n.Lang, offline bool) {
	w, st, seed := pairStoreWorld(t)
	w.h.opts.Lang = func() i18n.Lang { return lang }
	if offline {
		w.h.opts.AssistantEvent = nil
	}
	st.reject = true
	pairStoreEvent(t, w, "om_failed_pair", "ou_pair_owner", seed.Pairing.Code)
	_, live := w.h.botAPI()
	saved := pairStored(t, st)
	_, view := w.do(t, "GET", "/api/v1/bots/feishu", "")
	rows := rowsWith(st.takeAudit(), auditBotPaired)
	probe := newTestManager(t)
	probe.store = st
	_, recovered := probe.botAPI()
	t.Logf("restart after failed redemption: owner=%v code=%v", recovered.Owner, recovered.Pairing)
	if recovered.Owner != nil || !reflect.DeepEqual(recovered.Pairing, seed.Pairing) {
		t.Error("restart lost uncommitted code")
	}
	if len(rows) != 0 {
		t.Errorf("failed redemption recorded success audit: %v", rows)
	}
	t.Logf("failed redemption: live owner=%v code=%v; durable owner=%v code=%v; paired audit=%d assistant=%v writes=%d", live.Owner, live.Pairing, saved.Owner, saved.Pairing, len(rows), w.a.wasTold(assistantwire.EventFeishuOwner), st.attempts)
	if !reflect.DeepEqual(live, seed) {
		t.Errorf("failed write changed live pairing: %+v", live)
	}
	if !reflect.DeepEqual(saved, seed) {
		t.Errorf("failed write changed durable pairing: %+v", saved)
	}
	if view["owner"] != nil || view["pairing"] == nil {
		t.Errorf("settings route reports a binding after failed commit: %v", view)
	}
	if w.a.wasTold(assistantwire.EventFeishuOwner) {
		t.Error("assistant received pairing success before persistence")
	}
	if calls := w.api.since(0); strings.Contains(calls, "/contact/v3/users/") || strings.Contains(calls, `"msg_type":"interactive"`) {
		t.Errorf("premature pairing success effects: %s", calls)
	}
	if t.Failed() {
		t.FailNow()
	}
	if w.api.posts() != 1 || !strings.Contains(w.api.since(0), i18n.T(lang, "hub-bots_chat.pair_save_failed")) {
		t.Fatalf("failed pairing did not send localized recoverable feedback: %s", w.api.since(0))
	}
	if st.attempts != 1 {
		t.Fatalf("uncommitted redemption reached name write: %d", st.attempts)
	}
	if _, err := w.h.feishuCall(w.ctx, assistantwire.FeishuText, []byte(`{"text":"uncommitted send"}`)); !errors.Is(err, errBotOff) {
		t.Fatalf("uncommitted owner can send: %v", err)
	}
	pairStoreEvent(t, w, "om_failed_owner_message", "ou_pair_owner", "hello")
	if len(w.handed()) != 0 {
		t.Fatal("uncommitted owner message reached assistant")
	}

	if strings.Contains(w.api.since(0), "synthetic private storage diagnostic") {
		t.Fatal("storage error leaked to chat")
	}
	st.writeMu.Lock()
	st.reject = false
	st.writeMu.Unlock()
	// No advancing the clock or issuing another code: same path, fresh message id.
	pairStoreEvent(t, w, "om_retry_pair", "ou_pair_owner", seed.Pairing.Code)
	_, live = w.h.botAPI()
	saved = pairStored(t, st)
	if live.Owner == nil || live.Owner.OpenID != "ou_pair_owner" || live.Pairing != nil || !reflect.DeepEqual(live, saved) {
		t.Fatalf("retry did not commit: live=%+v saved=%+v", live, saved)
	}
	if offline {
		if !strings.Contains(w.api.since(0), i18n.T(lang, "hub-bots_chat.paired_body")) {
			t.Fatal("successful retry lost hub greeting")
		}
	} else if !w.a.wasTold(assistantwire.EventFeishuOwner) {
		t.Fatal("successful retry did not notify assistant")
	}
	_, view = w.do(t, "GET", "/api/v1/bots/feishu", "")
	if view["owner"] == nil || view["pairing"] != nil {
		t.Fatalf("successful route view: %v", view)
	}
	pairStoreEvent(t, w, "om_stranger_after_pair", "ou_stranger", seed.Pairing.Code)
	if len(w.handed()) != 0 {
		t.Fatal("stranger reached assistant after binding")
	}
	pairStoreEvent(t, w, "om_owner_after_pair", "ou_pair_owner", "hello")
	if ids := w.handed(); len(ids) != 1 || ids[0] != "om_owner_after_pair" {
		t.Fatalf("committed owner's message not delivered: %v", ids)
	}

	if rows := rowsWith(st.takeAudit(), auditBotPaired); len(rows) != 1 {
		t.Fatalf("successful retry audits: %v", rows)
	}
	// A new manager reloads the same durable row after the database recovers.
	restarted := newTestManager(t)
	restarted.store = st
	_, restored := restarted.botAPI()
	if !reflect.DeepEqual(restored, saved) {
		t.Fatalf("restart lost committed binding: %+v", restored)
	}
}
