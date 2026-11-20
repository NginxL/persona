package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func chooseCard(id string, multi bool) proto.Interaction {
	return proto.Interaction{ID: id, MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "Which library?", MultiSelect: multi,
			Options: []proto.QuestionOption{{Label: "The complete first option label"}, {Label: "Second"}, {Label: "Third"}}}}}
}

func chooseRequest(t *testing.T, handler http.Handler, token, id, body, lang, op string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "https://hub.example.com/api/v1/interactions/"+id+"/choose", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Accept-Language", lang)
	if op != "" {
		r.Header.Set("X-Persona-Op-Id", op)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("response %d: %s", w.Code, w.Body.String())
	}
	return w.Code, out
}

func TestChooseValidation(t *testing.T) {
	h := newTestHub(t)
	var calls atomic.Int32
	manager, _ := connectInboxManager(t, h)
	manager.mu.Lock()
	manager.http = func(context.Context, assistantwire.HTTPRequest) (any, error) {
		calls.Add(1)
		return assistantwire.HTTPResponse{Status: 200, Body: []byte(`{"ok":true}`)}, nil
	}
	manager.mu.Unlock()
	one, many := chooseCard("one", false), chooseCard("many", true)
	reportInteraction(t, h, one)
	reportInteraction(t, h, many)
	ineligible := chooseCard("fields", false)
	ineligible.Fields = []proto.Field{{Key: "input", Label: "Input"}}
	reportInteraction(t, h, ineligible)
	// Its asker said going ahead cannot be undone: answered in the app only (#10465), so an option from an older
	// notification is refused too.
	cannotUndo := chooseCard("irreversible", false)
	cannotUndo.ToolName, cannotUndo.Reversible = "ask_user", new(false)
	reportInteraction(t, h, cannotUndo)
	rev, multiRev := askRev(one.Questions[0]), askRev(many.Questions[0])
	mux := h.routes()
	for _, tc := range []struct {
		name, id, body string
		status         int
		code           string
	}{
		{"unknown", "absent", `{"option":0,"rev":"` + rev + `"}`, 409, codeNotPending},
		{"changed", "one", `{"option":0,"rev":"old"}`, 409, codeAskChanged},
		{"range", "one", `{"option":3,"rev":"` + rev + `"}`, 400, "invalid_choice"},
		{"negative", "one", `{"option":-1,"rev":"` + rev + `"}`, 400, "invalid_choice"},
		{"null index", "many", `{"options":[null],"rev":"` + multiRev + `"}`, 400, "invalid_choice"},
		{"duplicate", "many", `{"options":[0,0],"rev":"` + multiRev + `"}`, 400, "invalid_choice"},
		{"single", "one", `{"options":[0,1],"rev":"` + rev + `"}`, 400, "single_select"},
		{"no revision", "one", `{"option":0}`, 400, "bad_request"},
		{"no option", "one", `{"rev":"` + rev + `"}`, 400, "bad_request"},
		{"null option", "one", `{"option":null,"rev":"` + rev + `"}`, 400, "bad_request"},
		{"empty", "one", `{"options":[],"rev":"` + rev + `"}`, 400, "bad_request"},
		{"both", "one", `{"option":0,"options":[0],"rev":"` + rev + `"}`, 400, "bad_request"},
		{"both empty", "one", `{"option":0,"options":[],"rev":"` + rev + `"}`, 400, "bad_request"},
		{"invalid client id", "one", `{"option":0,"rev":"` + rev + `","client_id":123}`, 400, "bad_request"},
		{"fraction", "one", `{"option":0.5,"rev":"` + rev + `"}`, 400, "bad_request"},
		{"malformed", "one", `{"option":`, 400, "bad_request"},
		{"trailing", "one", `{"option":0,"rev":"` + rev + `"} {}`, 400, "bad_request"},
		{"too large", "one", `{"rev":"` + strings.Repeat("x", 4096) + `"}`, 413, "too_large"},
		{"fields", "fields", `{"option":0,"rev":"` + rev + `"}`, 400, "not_choosable"},
		{"cannot be undone", "irreversible", `{"option":0,"rev":"` + rev + `"}`, 400, "not_choosable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
				code, out := chooseRequest(t, mux, h.cfg.Token, tc.id, tc.body, string(lang), "")
				if code != tc.status || out["code"] != tc.code || out["error"] != i18n.T(lang, "hub-choose."+tc.code) {
					t.Errorf("%s: %d %v", lang, code, out)
				}
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("an invalid choice reached the manager")
	}
}

func TestChooseAnswersAndReceipts(t *testing.T) {
	h := newTestHub(t)
	store := &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	h.store = store
	manager, _ := connectInboxManager(t, h)
	requests := make(chan assistantwire.HTTPRequest, 8)
	manager.mu.Lock()
	manager.http = func(_ context.Context, r assistantwire.HTTPRequest) (any, error) {
		requests <- r
		return assistantwire.HTTPResponse{Status: 200, Body: []byte(`{"ok":true}`)}, nil
	}
	manager.mu.Unlock()
	mux := h.routes()
	for _, multi := range []bool{false, true} {
		id, picks := "one", `"option":0`
		if multi {
			id, picks = "many", `"options":[2,0]`
		}
		card := chooseCard(id, multi)
		reportInteraction(t, h, card)
		body := `{` + picks + `,"rev":"` + askRev(card.Questions[0]) + `","client_id":"tap-` + id + `"}`
		code, out := chooseRequest(t, mux, h.cfg.Token, id, body, "en", "")
		if code != 200 || out["ok"] != true {
			t.Fatalf("choose: %d %v", code, out)
		}
		r := <-requests
		want := "The complete first option label"
		if multi {
			want += ", Third"
		}
		var resolution proto.Resolution
		if r.Path != "/api/v1/interactions/"+id+"/resolve" || json.Unmarshal(r.Body, &resolution) != nil || resolution.Answers["Which library?"] != want {
			t.Fatalf("forwarded %s %s", r.Path, r.Body)
		}
		reportResolved(t, h, id, "")
		// A lost response can replay even after the pending card has disappeared.
		code, out = chooseRequest(t, mux, h.cfg.Token, id, body, "en", "")
		if code != 200 || out["ok"] != true || len(requests) != 0 {
			t.Fatalf("receipt: %d %v, requests %d", code, out, len(requests))
		}
		changed := strings.Replace(body, `"rev":"`, `"rev":"different`, 1)
		if code, out := chooseRequest(t, mux, h.cfg.Token, id, changed, "en", ""); code != 409 || out["code"] != "op_id_reused" {
			t.Fatalf("reused receipt: %d %v", code, out)
		}
		fresh := strings.Replace(body, "tap-"+id, "next-"+id, 1)
		if code, out := chooseRequest(t, mux, h.cfg.Token, id, fresh, "en", ""); code != 409 || out["code"] != codeNotPending {
			t.Fatalf("already answered: %d %v", code, out)
		}
	}
	card := chooseCard("header", false)
	reportInteraction(t, h, card)
	body := `{"option":1,"rev":"` + askRev(card.Questions[0]) + `"}`
	for range 2 {
		if code, out := chooseRequest(t, mux, h.cfg.Token, card.ID, body, "en", "00000000-0000-4000-8000-000000000051"); code != 200 || out["ok"] != true {
			t.Fatalf("header receipt: %d %v", code, out)
		}
	}
	if len(requests) != 1 {
		t.Fatalf("header receipt executed %d times", len(requests))
	}
}

func TestChooseOfflineRetryAndEndedSnapshot(t *testing.T) {
	h := newTestHub(t)
	store := &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	h.store = store
	card := chooseCard("one", false)
	reportInteraction(t, h, card)
	body := `{"option":0,"rev":"` + askRev(card.Questions[0]) + `","client_id":"tap-offline"}`
	mux := h.routes()
	if code, out := chooseRequest(t, mux, h.cfg.Token, card.ID, body, "en", ""); code != 503 || out["code"] != assistantwire.CodeAssistantOffline {
		t.Fatalf("offline: %d %v", code, out)
	}
	store.ops.mu.Lock()
	kept := len(store.ops.ops)
	store.ops.mu.Unlock()
	if kept != 0 {
		t.Fatal("offline choice kept a receipt")
	}
	manager, _ := connectInboxManager(t, h)
	manager.mu.Lock()
	manager.http = func(context.Context, assistantwire.HTTPRequest) (any, error) {
		return assistantwire.HTTPResponse{Status: 200, Body: []byte(`{"ok":true}`)}, nil
	}
	manager.mu.Unlock()
	if code, out := chooseRequest(t, mux, h.cfg.Token, card.ID, body, "en", ""); code != 200 || out["ok"] != true {
		t.Fatalf("retry online: %d %v", code, out)
	}
	for _, result := range []assistantwire.HTTPResponse{
		{Status: 410, Body: []byte(`{"code":"gone"}`)},
		{Status: 200, Body: []byte(`{"ok":true,"already":true,"status":"resolved"}`)},
	} {
		manager.mu.Lock()
		manager.http = func(context.Context, assistantwire.HTTPRequest) (any, error) { return result, nil }
		manager.mu.Unlock()
		if code, out := chooseRequest(t, mux, h.cfg.Token, card.ID, strings.Replace(body, `,"client_id":"tap-offline"`, "", 1), "ja", ""); code != 409 || out["code"] != codeNotPending || out["error"] != i18n.T(i18n.Ja, "hub-choose.not_pending") {
			t.Fatalf("ended snapshot: %d %v", code, out)
		}
	}
}

func TestChooseAuthentication(t *testing.T) {
	h := newTestHub(t)
	h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}
	manager, _ := connectInboxManager(t, h)
	var calls atomic.Int32
	manager.mu.Lock()
	manager.http = func(context.Context, assistantwire.HTTPRequest) (any, error) {
		calls.Add(1)
		return assistantwire.HTTPResponse{Status: 200, Body: []byte(`{"ok":true}`)}, nil
	}
	manager.mu.Unlock()
	card := chooseCard("one", false)
	reportInteraction(t, h, card)
	body := `{"option":0,"rev":"` + askRev(card.Questions[0]) + `"}`
	mux := h.routes()
	if code, out := chooseRequest(t, mux, "jd_invalid", card.ID, body, "en", ""); code != 401 || calls.Load() != 0 {
		t.Fatalf("unauthorized: %d %v", code, out)
	}
	code, watch := call(t, mux, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"Watch","kind":"watch"}`)
	if code != 200 {
		t.Fatalf("create watch: %d %v", code, watch)
	}
	if code, out := chooseRequest(t, mux, watch["token"].(string), card.ID, body, "en", ""); code != 200 || out["ok"] != true || calls.Load() != 1 {
		t.Fatalf("answer-only choice: %d %v", code, out)
	}
}

// A disconnect after resolve reaches the manager must retain an unknown receipt.
// Reconnecting and retrying the same tap cannot answer the card a second time.
func TestChooseDroppedReplyKeepsReceipt(t *testing.T) {
	h := newTestHub(t)
	store := &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	h.store = store
	manager, link := connectInboxManager(t, h)
	arrived := make(chan struct{}, 1)
	manager.mu.Lock()
	manager.http = func(ctx context.Context, _ assistantwire.HTTPRequest) (any, error) {
		arrived <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	manager.mu.Unlock()
	go func() {
		<-arrived
		_ = link.ws.CloseNow()
	}()
	card := chooseCard("uncertain", false)
	reportInteraction(t, h, card)
	body := `{"option":0,"rev":"` + askRev(card.Questions[0]) + `","client_id":"tap-uncertain"}`
	mux := h.routes()
	if code, out := chooseRequest(t, mux, h.cfg.Token, card.ID, body, "en", ""); code != 502 || out["code"] != assistantwire.CodeAssistantInDoubt || out["delivered"] != "unknown" {
		t.Fatalf("dropped response: %d %v", code, out)
	}
	store.ops.mu.Lock()
	kept := len(store.ops.ops)
	for _, op := range store.ops.ops {
		if op.State != "unknown" {
			t.Errorf("receipt state = %s", op.State)
		}
	}
	store.ops.mu.Unlock()
	if kept != 1 {
		t.Fatalf("kept %d receipts", kept)
	}
	reconnected, _ := connectInboxManager(t, h)
	var calls atomic.Int32
	reconnected.mu.Lock()
	reconnected.http = func(context.Context, assistantwire.HTTPRequest) (any, error) {
		calls.Add(1)
		return assistantwire.HTTPResponse{Status: 200, Body: []byte(`{"ok":true}`)}, nil
	}
	reconnected.mu.Unlock()
	if code, out := chooseRequest(t, mux, h.cfg.Token, card.ID, body, "en", ""); code != 202 || out["state"] != "unknown" || calls.Load() != 0 {
		t.Fatalf("uncertain retry: %d %v, calls %d", code, out, calls.Load())
	}
}
