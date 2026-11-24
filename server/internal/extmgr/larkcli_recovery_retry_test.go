package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/cliport"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/lark"
)

// Registry.Remove/Add is an asynchronous replacement; polling may observe the gap.
func currentRecoveryCLI(h *Manager) cliport.Connector {
	c, _ := h.connectors.Get("larkcli").(cliport.Connector)
	return c
}

func recoveryCLI(t *testing.T, w *botLoadWorld) string {
	t.Helper()
	bin, err := filepath.Abs(filepath.Join("..", "modules", "lark", "larkcli", "testdata", "fake-lark-cli.sh"))
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("LARKCLI_FAKE_LOG", log)
	t.Setenv("LARKCLI_FAKE_SCENARIO", "success")
	w.h.cfg.Lark = lark.Config{CLI: &lark.CLIConfig{Bin: bin}}
	w.h.connectors = w.h.newConnectors(nil)
	return log
}

func TestLarkCLIManagedRecoveryRetriesTransientSyncFailure(t *testing.T) {
	needsLarkCLI(t)
	w := newBotLoadWorld(t, true, false)
	recoveryCLI(t, w)
	real := w.h.cfg.Lark.CLI.Bin
	dir := t.TempDir()
	allow := filepath.Join(dir, "allow")
	attempts := filepath.Join(dir, "attempts")
	release := filepath.Join(dir, "release")
	wrapper := filepath.Join(dir, "fake-cli")
	if err := os.WriteFile(wrapper, []byte(`#!/bin/sh
case "$*" in
 *"config init"*)
  printf 'attempt\n' >> "$LARKCLI_RECOVERY_ATTEMPTS"
  if [ ! -f "$LARKCLI_RECOVERY_ALLOW" ]; then exit 17; fi
  while [ ! -f "$LARKCLI_RECOVERY_RELEASE" ]; do sleep 0.02; done
  ;;
esac
exec "$LARKCLI_RECOVERY_REAL" "$@"
`), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LARKCLI_RECOVERY_REAL", real)
	t.Setenv("LARKCLI_RECOVERY_ALLOW", allow)
	t.Setenv("LARKCLI_RECOVERY_ATTEMPTS", attempts)
	t.Setenv("LARKCLI_RECOVERY_RELEASE", release)
	t.Setenv("LARKCLI_FAKE_SCENARIO", "config_different")
	w.h.cfg.Lark.CLI.Bin = wrapper
	sealed, err := w.h.sealKey("synthetic-sync-retry-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.botStore.PutSetting(w.ctx, botSettingsKey, FeishuBotSettings{AppID: "cli_recovered", SecretEnc: sealed}); err != nil {
		t.Fatal(err)
	}
	w.h.startConnectors(w.ctx)
	w.store.recover()
	w.advance()
	w.h.botAPI()
	count := func() int { raw, _ := os.ReadFile(attempts); return strings.Count(string(raw), "attempt") }
	eventually(t, 2*time.Second, "first managed profile sync failure", func() bool { return count() == 1 })
	if err := os.WriteFile(allow, []byte("synthetic recovery"), 0600); err != nil {
		t.Fatal(err)
	}
	// Calls on the old live connector request the second attempt, even though bot settings are cached.
	eventually(t, 8*time.Second, "ordinary CLI calls retry managed profile synchronization", func() bool {
		c := currentRecoveryCLI(w.h)
		if c == nil {
			return false
		}
		_, _ = c.Call(w.ctx, "larkcli_chats", json.RawMessage(`{}`))
		return count() == 2
	})
	responsive := make(chan struct{})
	go func() {
		for range 100 {
			w.h.botAPI()
		}
		close(responsive)
	}()
	select {
	case <-responsive:
	case <-time.After(time.Second):
		t.Fatal("CLI synchronization held the bot settings lock")
	}
	if count() != 2 {
		t.Fatal("parallel reads duplicated an in-flight synchronization")
	}
	if err := os.WriteFile(release, []byte("synthetic release"), 0600); err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "retried managed synchronization completes", func() bool {
		c := currentRecoveryCLI(w.h)
		return c != nil && c.Profile() == managedLarkCLIProfile
	})

	if got := count(); got != 2 {
		t.Fatalf("sync retries were not coalesced/cooled down: %d", got)
	}
}

type dualLarkReadStore struct {
	Store
	failed         atomic.Bool
	botReads       atomic.Int32
	connectorReads atomic.Int32
}

func (s *dualLarkReadStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == botSettingsKey {
		s.botReads.Add(1)
	}
	if key == connectorSettingsKey {
		s.connectorReads.Add(1)
	}
	if s.failed.Load() && (key == botSettingsKey || key == connectorSettingsKey) {
		return false, errors.New("synthetic dual-key storage outage")
	}
	return s.Store.Setting(ctx, key, out)
}

func TestLarkCLIDualSettingsOutageRestoresDurableSwitches(t *testing.T) {
	needsLarkCLI(t)
	for _, tc := range []struct {
		name, override, chosen               string
		enabled, missing, missingPreferences bool
	}{
		{name: "manual_disabled", chosen: "other"},
		{name: "override_disabled", override: "fixed", chosen: "other"},
		{name: "known_empty_disabled", missing: true},
		{name: "manual_enabled", chosen: "other", enabled: true},
		{name: "both_records_missing", enabled: true, missing: true, missingPreferences: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newBotLoadWorld(t, true, false)
			log := recoveryCLI(t, w)
			w.h.cfg.Lark.CLI.Profile = tc.override
			options := json.RawMessage(`{"inbox_all_chats":["oc_saved"],"documents":["doc_saved"],"wiki_spaces":[],"triage_rules":"Keep saved preferences"}`)
			cs := connectorSettings{Switch: map[string]bool{"larkcli": tc.enabled}, Inbox: map[string]bool{"larkcli": false}, Options: map[string]json.RawMessage{"larkcli": options}}
			if !tc.missing {
				if err := w.store.botStore.PutSetting(w.ctx, botSettingsKey, FeishuBotSettings{LarkCLIProfile: tc.chosen}); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.missingPreferences {
				if err := w.store.botStore.PutSetting(w.ctx, connectorSettingsKey, cs); err != nil {
					t.Fatal(err)
				}
			}
			st := &dualLarkReadStore{Store: w.store}
			st.failed.Store(true)
			w.h.store = st
			w.h.startConnectors(w.ctx)
			initialReads := st.connectorReads.Load()
			if w.h.connectors.Enabled("larkcli") || w.h.connectors.Feeds("larkcli", connector.FeedInbox) {
				t.Error("unread connector preferences started as enabled/feed-on")
			}
			current := w.h.connectors.Get("larkcli").(cliport.Connector)
			if _, err := current.Call(w.ctx, "larkcli_chats", json.RawMessage(`{}`)); !errors.Is(err, errBotSettingsUnread) {
				t.Fatalf("unknown preferences ran CLI: %v", err)
			}
			if err := w.h.setConnectorEnabled(w.ctx, "larkcli", true); !errors.Is(err, errBotSettingsUnread) {
				t.Fatalf("unknown preferences accepted an enabled write: %v", err)
			}
			if err := w.h.setConnectorFeed(w.ctx, "larkcli", connector.FeedInbox, true); !errors.Is(err, errBotSettingsUnread) {
				t.Fatalf("unknown preferences accepted a feed write: %v", err)
			}
			if err := w.h.applyConnectorOptions(w.ctx, "larkcli", json.RawMessage(`{}`)); !errors.Is(err, errBotSettingsUnread) {
				t.Fatalf("unknown preferences accepted an options write: %v", err)
			}
			if raw, _ := os.ReadFile(log); len(raw) > 0 {
				t.Fatalf("unread identity/preferences started CLI: %s", raw)
			}
			st.failed.Store(false)
			w.store.recover()
			w.advance()
			want := tc.chosen
			if tc.override != "" {
				want = tc.override
			}
			eventually(t, 8*time.Second, "dual-key recovery restores durable connector state", func() bool {
				w.h.listedConnectors(w.ctx, true)
				c := currentRecoveryCLI(w.h)
				if c == nil {
					return false
				}
				return c.Profile() == want && (strings.Contains(string(c.Options()), "oc_saved") != tc.missingPreferences) && w.h.connectors.Enabled("larkcli") == tc.enabled && !w.h.larkCLIRecovery.preferencesUnread.Load()
			})
			if w.h.connectors.Enabled("larkcli") != tc.enabled || w.h.connectors.Feeds("larkcli", connector.FeedInbox) != tc.missingPreferences {
				t.Error("recovery changed durable connector/feed switches")
			}
			if !tc.enabled {
				if raw, _ := os.ReadFile(log); len(raw) > 0 {
					t.Fatalf("disabled connector ran during preference recovery: %s", raw)
				}
			} else {
				eventually(t, time.Second, "enabled recovered connector resumes", func() bool {
					raw, _ := os.ReadFile(log)
					if want == "" {
						return len(raw) > 0 && !strings.Contains(string(raw), "--profile ")
					}
					return strings.Contains(string(raw), "--profile "+want+" ")
				})
			}
			if st.connectorReads.Load() != initialReads+1 {
				t.Fatalf("connector preference recovery reads: initial=%d final=%d", initialReads, st.connectorReads.Load())
			}
			var durable connectorSettings
			found, err := w.store.botStore.Setting(w.ctx, connectorSettingsKey, &durable)
			if err != nil {
				t.Fatal(err)
			}
			if tc.missingPreferences {
				if found {
					t.Fatal("missing preferences were persisted as a quarantine")
				}
			} else if durable.Switch["larkcli"] != tc.enabled || durable.Inbox["larkcli"] || !strings.Contains(string(durable.Options["larkcli"]), "oc_saved") {
				t.Fatal("recovery rewrote durable preferences")
			}
		})
	}
}
