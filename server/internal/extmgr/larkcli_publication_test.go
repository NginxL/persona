package extmgr

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
)

func TestLarkCLIRecoveryPublicationRejectsCallsAndRetiredInstances(t *testing.T) {
	needsLarkCLI(t)
	for _, profile := range []string{"fixed", ""} {
		t.Run("profile_"+profile, func(t *testing.T) {
			w := newBotLoadWorld(t, true, false)
			log := recoveryCLI(t, w)
			w.h.cfg.Lark.CLI.Profile = profile
			options := json.RawMessage(`{"inbox_all_chats":["oc_saved"],"triage_rules":"Keep saved preferences"}`)
			cs := connectorSettings{Switch: map[string]bool{"larkcli": false}, Inbox: map[string]bool{"larkcli": false}, Options: map[string]json.RawMessage{"larkcli": options}}
			if err := w.store.botStore.PutSetting(w.ctx, connectorSettingsKey, cs); err != nil {
				t.Fatal(err)
			}
			st := &dualLarkReadStore{Store: w.store}
			st.failed.Store(true)
			w.h.store = st
			w.h.startConnectors(w.ctx)
			old := currentRecoveryCLI(w.h)
			if old == nil {
				t.Fatal("placeholder missing")
			}
			st.failed.Store(false)
			w.store.recover()
			w.advance()
			published, resume := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() { done <- w.h.refreshLarkCLIConnectorAtPublication(w.ctx, func() { close(published); <-resume }) }()
			select {
			case <-published:
			case err := <-done:
				t.Fatalf("recovery skipped publication boundary: %v", err)
			}
			// This is the real concurrent caller, after Add exposed the new pointer and before its switch is applied.
			result := w.h.connectors.Call(w.ctx, "larkcli_chats", json.RawMessage(`{}`), nil)
			if !result.IsError {
				t.Error("Registry.Call crossed the disabled replacement publication boundary")
			}
			if _, err := old.Call(w.ctx, "larkcli_chats", json.RawMessage(`{}`)); !errors.Is(err, errBotSettingsUnread) {
				t.Errorf("retired same-profile instance crossed publication: %v", err)
			}
			raw, _ := os.ReadFile(log)
			if len(raw) > 0 {
				t.Errorf("publication quarantine executed CLI: %s", raw)
			}
			close(resume)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if result := w.h.connectors.Call(w.ctx, "larkcli_chats", json.RawMessage(`{}`), nil); !result.IsError {
				t.Error("durably disabled connector ran after publication")
			}
			if w.h.connectors.Enabled("larkcli") || w.h.connectors.Feeds("larkcli", connector.FeedInbox) {
				t.Fatal("publication changed durable off/feed policy")
			}
			if c := currentRecoveryCLI(w.h); c == nil || !strings.Contains(string(c.Options()), "oc_saved") {
				t.Fatal("publication lost durable options")
			}
			if err := w.h.setConnectorEnabled(w.ctx, "larkcli", true); err != nil {
				t.Fatal(err)
			}
			if result := w.h.connectors.Call(w.ctx, "larkcli_chats", json.RawMessage(`{}`), nil); result.IsError {
				t.Fatalf("normal enabled call: %+v", result)
			}
			// The newly enabled Watch may log concurrently; only this retired request uses page size 37.
			if _, err := old.Call(w.ctx, "larkcli_chats", json.RawMessage(`{"limit":37}`)); !errors.Is(err, errBotSettingsUnread) {
				t.Errorf("retired same-profile instance ran after successful enable: %v", err)
			}
			after, _ := os.ReadFile(log)
			if strings.Contains(string(after), "--page-size 37") {
				t.Error("retired instance launched a CLI process")
			}
		})
	}
}
