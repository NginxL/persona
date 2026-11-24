package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
)

func (s *botStore) AddExtensionAudit(_ context.Context, id, action, actor string, detail json.RawMessage) error {
	var d map[string]any
	if err := json.Unmarshal(detail, &d); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, auditRow{ID: id, Action: action, Actor: actor, Detail: d})
	return nil
}

// takeAudit gives the rows recorded since the last take.
func (s *botStore) takeAudit() []auditRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.audit
	s.audit = nil
	return out
}

// doAs is do as the device id (the hub's principal), as a request relayed from that device.
func (w *botWorld) doAs(t *testing.T, device, method, path, body string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(withPrincipal(req.Context(), principal{ID: device, Role: roleFull}))
	w.mux.ServeHTTP(rec, req)
	return rec.Code
}

// rowsWith is the rows whose action is action.
func rowsWith(rows []auditRow, action string) []auditRow {
	var out []auditRow
	for _, r := range rows {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

// Every change to the Feishu bot is recorded with who made it: credentials (never the secret), the switch and the
// features only when they change, a pairing code issued, the owner paired (by the Feishu account that sent the code)
// and removed, and the owner dropped by a different app; the assistant's own switch calls are the assistant's (#5353).
func TestBotChangesAreAudited(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	st := w.h.store.(*botStore)
	rows := st.takeAudit()
	if c := rowsWith(rows, auditBotCredentials); len(c) != 1 || c[0].ID != channel.Feishu || c[0].Detail["app_id"] != "cli_test" ||
		c[0].Actor != "hub token" {
		t.Fatalf("setting the credentials: %v", rows)
	}
	if e := rowsWith(rows, auditBotEnabled); len(e) != 1 || e[0].Detail["enabled"] != true {
		t.Fatalf("turning it on: %v", rows)
	}

	// The same credentials again (the app resends them), the same switch and features: nothing changed.
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", fmt.Sprintf(`{"enabled":true,"app_id":"cli_test","app_secret":"s3cret","domain":%q}`,
		w.api.srv.URL)); code != 200 {
		t.Fatal(code)
	}
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"app_id":"cli_test"}`); code != 200 {
		t.Fatal(code)
	}
	if rows := st.takeAudit(); len(rows) != 0 {
		t.Fatalf("nothing changed, recorded %v", actions(rows))
	}

	// Agents driven from Feishu, turned on from a device; again, no change.
	if code := w.doAs(t, "dev_phone", "PUT", "/api/v1/bots/feishu", `{"features":{"agents":true}}`); code != 200 {
		t.Fatal(code)
	}
	if code := w.doAs(t, "dev_phone", "PUT", "/api/v1/bots/feishu", `{"features":{"agents":true}}`); code != 200 {
		t.Fatal(code)
	}
	rows = st.takeAudit()
	if !sameActions(rows, auditBotFeatures) || rows[0].Actor != "device:dev_phone" {
		t.Fatalf("features: %v", rows)
	}
	if f, _ := rows[0].Detail["features"].(map[string]any); f["agents"] != true {
		t.Fatalf("features recorded %v", rows[0].Detail)
	}

	// Pairing: the code issued from the app, the owner by the Feishu account that sent it.
	w.pair(t)
	eventually(t, 5*time.Second, "the pairing recorded", func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return len(rowsWith(st.audit, auditBotPaired)) == 1
	})
	rows = st.takeAudit()
	if !sameActions(rows, auditBotPairCode, auditBotPaired) || rows[1].Actor != botChatActor("ou_me") ||
		rows[1].Detail["open_id"] != "ou_me" {
		t.Fatalf("pairing: %v", rows)
	}

	// Another app: the owner goes with the old one, and the record says so.
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", fmt.Sprintf(`{"app_id":"cli_other","app_secret":"s3cret-2","domain":%q}`,
		w.api.srv.URL)); code != 200 {
		t.Fatal(code)
	}
	rows = st.takeAudit()
	if !sameActions(rows, auditBotCredentials) || rows[0].Detail["app_id"] != "cli_other" || rows[0].Detail["owner_reset"] != true {
		t.Fatalf("another app: %v", rows)
	}

	// Paired again (w.pair waits for the assistant's first hello only), then the owner removed from a device; removing
	// nobody records nothing.
	_, out := w.do(t, "POST", "/api/v1/bots/feishu/pair", "")
	if _, err := w.h.botEvent(w.ctx, botMessageEvent("om_pair2", "ou_me", "text",
		textContent(out["pairing"].(map[string]any)["code"].(string)), nil)); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "the second pairing recorded", func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return len(rowsWith(st.audit, auditBotPaired)) == 1
	})
	st.takeAudit()
	if code := w.doAs(t, "dev_mac", "DELETE", "/api/v1/bots/feishu/owner", ""); code != 200 {
		t.Fatal(code)
	}
	if code := w.doAs(t, "dev_mac", "DELETE", "/api/v1/bots/feishu/owner", ""); code != 200 {
		t.Fatal(code)
	}
	rows = st.takeAudit()
	if !sameActions(rows, auditBotUnpaired) || rows[0].Actor != "device:dev_mac" {
		t.Fatalf("unpairing: %v", rows)
	}

	// The assistant's own call (feishu.configure) is the assistant's.
	w.feishu(t, assistantwire.FeishuConfigure, map[string]any{"features": map[string]bool{"agents": false}})
	rows = st.takeAudit()
	if !sameActions(rows, auditBotFeatures) || rows[0].Actor != actorAssistant {
		t.Fatalf("the assistant's switch: %v", rows)
	}

	// Clearing the credentials: back to hub.json's.
	if code := w.doAs(t, "dev_phone", "PUT", "/api/v1/bots/feishu", `{"app_id":""}`); code != 200 {
		t.Fatal(code)
	}
	rows = st.takeAudit()
	if c := rowsWith(rows, auditBotCredentials); len(c) != 1 || c[0].Detail["cleared"] != true {
		t.Fatalf("clearing: %v", rows)
	}

	// Never the secret, nor a pairing code.
	st.mu.Lock()
	raw, _ := json.Marshal(st.audit)
	st.mu.Unlock()
	if strings.Contains(string(raw), "s3cret") {
		t.Fatalf("the secret is in the audit: %s", raw)
	}
}

// Codes guessed until the pairing is dropped are recorded, by the Feishu account that guessed.
func TestBotPairGuessesAreAudited(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	st := w.h.store.(*botStore)
	if code, out := w.do(t, "POST", "/api/v1/bots/feishu/pair", ""); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	_, out := w.do(t, "GET", "/api/v1/bots/feishu", "")
	code := out["pairing"].(map[string]any)["code"].(string)
	st.takeAudit()
	for i := 0; i < botPairTries; i++ {
		_, _ = w.h.botEvent(w.ctx, botMessageEvent(fmt.Sprintf("om_g%d", i), "ou_x", "text", textContent(fmt.Sprintf("%06d", i)), nil))
	}
	eventually(t, 5*time.Second, "the guesses recorded", func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return len(rowsWith(st.audit, auditBotPairGuessed)) == 1
	})
	rows := rowsWith(st.takeAudit(), auditBotPairGuessed)
	if rows[0].Actor != botChatActor("ou_x") || rows[0].Detail["fails"] != float64(botPairTries) {
		t.Fatalf("guesses: %v", rows)
	}
	raw, _ := json.Marshal(rows)
	if strings.Contains(string(raw), code) {
		t.Fatalf("the pairing code is in the audit: %s", raw)
	}
}
