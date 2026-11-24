package extmgr

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extwire"
)

func assertBotIdentities(t *testing.T, got, want []extwire.BotIdentity) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("bot identities = %+v, want %+v", got, want)
	}
}

func latestBotIdentities(t *testing.T, h *Manager) []extwire.BotIdentity {
	t.Helper()
	hub := hubOf(h)
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.reports) == 0 {
		t.Fatal("no extension report sent")
	}
	return hub.reports[len(hub.reports)-1].BotIdentities
}

func TestFeishuBotIdentityReport(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	want := []extwire.BotIdentity{{Kind: "feishu", ID: "cli_test"}}
	assertBotIdentities(t, latestBotIdentities(t, w.h), want)
	raw, err := json.Marshal(w.h.report())
	if err != nil || strings.Contains(string(raw), "s3cret") || strings.Contains(string(raw), "secret_enc") {
		t.Fatalf("report must contain only the bot identity: %s, %v", raw, err)
	}
	if code, out := w.do(t, "PUT", "/api/v1/bots/feishu", `{"enabled":false}`); code != 200 {
		t.Fatalf("disable: %d %v", code, out)
	}
	assertBotIdentities(t, latestBotIdentities(t, w.h), nil)
	if code, out := w.do(t, "PUT", "/api/v1/bots/feishu", `{"enabled":true}`); code != 200 {
		t.Fatalf("enable: %d %v", code, out)
	}
	assertBotIdentities(t, latestBotIdentities(t, w.h), want)
	if code, out := w.do(t, "PUT", "/api/v1/bots/feishu", `{"app_id":"cli_other"}`); code != 200 {
		t.Fatalf("change identity: %d %v", code, out)
	}
	assertBotIdentities(t, latestBotIdentities(t, w.h), []extwire.BotIdentity{{Kind: "feishu", ID: "cli_other"}})
	if code, out := w.do(t, "PUT", "/api/v1/bots/feishu", `{"app_id":""}`); code != 200 {
		t.Fatalf("clear: %d %v", code, out)
	}
	assertBotIdentities(t, latestBotIdentities(t, w.h), nil)
}

func TestFeishuBotIdentityConfigFallback(t *testing.T) {
	needsLarkModule(t)
	h := newTestManager(t)
	h.cfg.Bots.Feishu.AppID, h.cfg.Bots.Feishu.AppSecret = "cli_config", "config-secret"
	h.bots.loaded = true
	h.bots.set.Enabled = true
	want := []extwire.BotIdentity{{Kind: "feishu", ID: "cli_config"}}
	assertBotIdentities(t, h.report().BotIdentities, want)
	h.bots.set.AppID = "cli_settings"
	assertBotIdentities(t, h.report().BotIdentities, want) // an incomplete settings override still uses config
	h.bots.set.SecretEnc = "sealed-secret"
	assertBotIdentities(t, h.report().BotIdentities, []extwire.BotIdentity{{Kind: "feishu", ID: "cli_settings"}})
	h.isMemberNow.Store(true)
	assertBotIdentities(t, h.report().BotIdentities, nil)
	h.isMemberNow.Store(false)
	h.bots.set.AppID, h.bots.set.SecretEnc = "", ""
	h.cfg.Bots.Feishu.AppSecret = ""
	assertBotIdentities(t, h.report().BotIdentities, nil)
}

type identityWeChatConnector struct{}

func (*identityWeChatConnector) ID() string   { return "wechat" }
func (*identityWeChatConnector) Name() string { return "WeChat" }
func (*identityWeChatConnector) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapStatus}}
}
func (*identityWeChatConnector) Status(context.Context) connector.Status {
	return connector.Status{State: connector.StateOK, Account: "WeChat 123456"}
}

func TestWeChatBotIdentityReport(t *testing.T) {
	needsLarkModule(t)
	h := newTestManager(t)
	h.store = newKeepStore()
	h.connectors = h.newConnectors(nil)
	if err := h.addConnector(&identityWeChatConnector{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sec := h.secretsFor("wechat")
	save := func(raw string) {
		t.Helper()
		if err := sec.Save(ctx, []byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	assertBotIdentities(t, h.report().BotIdentities, nil)
	save(`{"creds":{"user_id":"example-user-123456","bot_id":"example-bot","token":"example-token"},"context":"context-token"}`)
	want := []extwire.BotIdentity{{Kind: "wechat", ID: "example-user-123456"}}
	assertBotIdentities(t, latestBotIdentities(t, h), want)
	hub := hubOf(h)
	hub.mu.Lock()
	reports := len(hub.reports)
	hub.mu.Unlock()
	save(`{"creds":{"user_id":"example-user-123456","token":"rotated-token"},"cursor":"next","context":"next-context"}`)
	hub.mu.Lock()
	unchanged := len(hub.reports) == reports
	hub.mu.Unlock()
	if !unchanged {
		t.Fatal("cursor, context and token changes must not resend an unchanged bot identity")
	}
	raw, err := json.Marshal(h.report())
	if err != nil || strings.Contains(string(raw), "example-token") || strings.Contains(string(raw), "context-token") || strings.Contains(string(raw), "example-bot") {
		t.Fatalf("report leaked more than the bound account identity: %s, %v", raw, err)
	}
	if err := h.setConnectorEnabled(ctx, "wechat", false); err != nil {
		t.Fatal(err)
	}
	assertBotIdentities(t, latestBotIdentities(t, h), nil)
	if err := h.setConnectorEnabled(ctx, "wechat", true); err != nil {
		t.Fatal(err)
	}
	assertBotIdentities(t, latestBotIdentities(t, h), want)
	// Another account with the same displayed suffix must still be a different identity.
	save(`{"creds":{"user_id":"other-user-123456","token":"new-token"}}`)
	assertBotIdentities(t, latestBotIdentities(t, h), []extwire.BotIdentity{{Kind: "wechat", ID: "other-user-123456"}})
	// Unbinding keeps old tokens for iLink, but must immediately remove the reported identity.
	save(`{"old":["example-token","new-token"]}`)
	assertBotIdentities(t, latestBotIdentities(t, h), nil)
	save(`{"creds":{"user_id":"example-user-123456"}}`)
	assertBotIdentities(t, latestBotIdentities(t, h), want)
	if err := sec.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	assertBotIdentities(t, latestBotIdentities(t, h), nil)
	save(`{"creds":{}}`)
	assertBotIdentities(t, latestBotIdentities(t, h), nil)
	save(`invalid state`)
	assertBotIdentities(t, latestBotIdentities(t, h), nil)
}
