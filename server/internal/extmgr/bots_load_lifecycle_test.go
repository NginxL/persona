package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/cliport"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/lark"
)

type botBlockedReadStore struct {
	Store
	entered chan bool
	release chan struct{}
	once    atomic.Bool
	calls   atomic.Int32
}

func (s *botBlockedReadStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == botSettingsKey {
		s.calls.Add(1)
	}
	if key == botSettingsKey && s.once.CompareAndSwap(false, true) {
		_, bounded := ctx.Deadline()
		s.entered <- bounded
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-s.release:
			return false, errors.New("synthetic release")
		}
	}
	return s.Store.Setting(ctx, key, out)
}

func TestBotLoadTransportReadIsBounded(t *testing.T) {
	needsLarkModule(t)
	w := newBotLoadWorld(t, true, true)
	st := &botBlockedReadStore{Store: w.store, entered: make(chan bool, 1), release: make(chan struct{})}
	w.h.store = st
	w.store.recover()
	w.advance()
	done := make(chan struct{})
	go func() { w.h.botAPI(); close(done) }()
	bounded := <-st.entered
	if !bounded {
		close(st.release)
		<-done
		t.Fatal("transport retry passed an unbounded background context to storage")
	}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch i % 3 {
			case 0:
				w.h.botAPI()
			case 1:
				if _, err := w.status(); !errors.Is(err, errBotSettingsUnread) {
					t.Errorf("pending status: %v", err)
				}
			case 2:
				if code, _ := call(t, w.routes, "GET", "/api/v1/bots/feishu", w.token, ""); code != 503 {
					t.Errorf("pending settings: HTTP %d", code)
				}
			}
		}()
	}
	followersDone := make(chan struct{})
	go func() { wg.Wait(); close(followersDone) }()
	select {
	case <-followersDone:
	case <-time.After(time.Second):
		close(st.release)
		<-done
		t.Fatal("concurrent lookups waited behind storage while holding the bot settings lifecycle")
	}
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		close(st.release)
		t.Fatal("transport read did not time out")
	}
	wg.Wait()
	if st.calls.Load() != 1 {
		t.Fatal("overlapping transport reads")
	}
	if api, _ := w.h.botAPI(); api != nil {
		t.Fatal("timed-out read published settings")
	}
	w.advance()
	if api, set := w.h.botAPI(); api == nil || set.Owner == nil {
		t.Fatal("next eligible read did not restore transport")
	}
}

func TestBotLoadReadKeepsEarlierCallerDeadline(t *testing.T) {
	needsLarkModule(t)
	w := newBotLoadWorld(t, true, true)
	st := &botBlockedReadStore{Store: w.store, entered: make(chan bool, 1), release: make(chan struct{})}
	w.h.store = st
	w.store.recover()
	w.advance()
	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := w.h.loadBotSettings(ctx); err == nil {
		t.Fatal("canceled read succeeded")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("earlier caller deadline lost: %v", elapsed)
	}
	w.h.botAPI()
	if st.calls.Load() != 1 {
		t.Fatal("cancellation bypassed cooldown")
	}
	w.advance()
	if api, _ := w.h.botAPI(); api == nil {
		t.Fatal("canceled read did not recover on retry")
	}
}

// fakeCLIRunsRecorded has every run of the fake lark CLI record its process id in a directory of the test's
// (LARKCLI_FAKE_PIDS, larkcli/testdata/fake-lark-cli.sh) and returns that directory, for waitFakeCLIRunsExited.
func fakeCLIRunsRecorded(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LARKCLI_FAKE_PIDS", dir)
	return dir
}

// fakeCLIRunning lists the recorded runs still going: a recorded process id counts only while its process runs
// fake-lark-cli.sh, so an id the system gave to another process since is not waited for.
func fakeCLIRunning(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var running []string
	for _, e := range entries {
		out, err := exec.Command("ps", "-p", e.Name(), "-o", "command=").Output()
		if err == nil && strings.Contains(string(out), "fake-lark-cli.sh") {
			running = append(running, e.Name())
		}
	}
	return running
}

// waitFakeCLIRunsExited waits, five seconds at most, until no recorded run of the fake CLI is still going.
func waitFakeCLIRunsExited(t *testing.T, dir string) {
	t.Helper()
	eventually(t, 5*time.Second, "every fake lark CLI run to exit", func() bool { return len(fakeCLIRunning(dir)) == 0 })
}

func TestBotLoadLiveCLIProfileRecovery(t *testing.T) {
	needsLarkModule(t)
	w := newBotLoadWorld(t, true, false)
	bin, err := filepath.Abs(filepath.Join("..", "modules", "lark", "larkcli", "testdata", "fake-lark-cli.sh"))
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "calls.log")
	runs := fakeCLIRunsRecorded(t)
	// The live connector and the CLI recovery run the CLI in the background too, and every run appends to calls.log.
	// Cancelling the world only has those runs killed, from os/exec's own goroutine; one still going could write
	// calls.log back while the temporary directories are removed. Cleanups run last added, first called, and the
	// directories' removal was added by the world's first t.TempDir: this one stops the world and waits for every run
	// to exit before that (#14235).
	t.Cleanup(func() {
		w.cancel()
		waitFakeCLIRunsExited(t, runs)
	})
	t.Setenv("LARKCLI_FAKE_SCENARIO", "success")
	t.Setenv("LARKCLI_FAKE_LOG", log)
	w.h.cfg.Lark = lark.Config{CLI: &lark.CLIConfig{Bin: bin}}
	if err := w.store.botStore.PutSetting(w.ctx, botSettingsKey, FeishuBotSettings{Enabled: false, LarkCLIProfile: "other"}); err != nil {
		t.Fatal(err)
	}
	w.h.connectors = w.h.newConnectors(nil)
	w.h.startConnectors(w.ctx)
	c := w.h.connectors.Get("larkcli").(cliport.Connector)
	if _, err := c.Call(w.ctx, "larkcli_chats", json.RawMessage(`{}`)); err == nil {
		t.Error("live connector executed CLI while saved identity was unread")
	}
	if raw, _ := os.ReadFile(log); len(raw) > 0 {
		t.Errorf("unknown identity invoked CLI: %s", raw)
	}
	if _, err := w.h.assistantLark(w.ctx); !errors.Is(err, errBotSettingsUnread) {
		t.Errorf("assistant exposed an unknown CLI profile: %v", err)
	}
	w.store.recover()
	w.advance()
	w.h.botAPI()
	eventually(t, time.Second, "live connector profile replacement", func() bool { c := currentRecoveryCLI(w.h); return c != nil && c.Profile() == "other" })
	if cfg, err := w.h.assistantLark(w.ctx); err != nil || cfg.CLI == nil || cfg.CLI.Profile != "other" {
		t.Fatalf("assistant profile did not recover: %+v %v", cfg, err)
	}
	if _, err := c.Call(w.ctx, "larkcli_chats", json.RawMessage(`{}`)); err == nil {
		t.Error("retained placeholder ran with the recovered account and old Inbox cache")
	}
	for _, tool := range []struct{ name, args string }{{"larkcli_chats", `{}`}, {"larkcli_messages", `{"chat_id":"oc_example"}`}, {"larkcli_search", `{"query":"example","kind":"messages"}`}} {
		current := w.h.connectors.Get("larkcli").(connector.ReadTools)
		if _, err := current.Call(w.ctx, tool.name, json.RawMessage(tool.args)); err != nil {
			t.Errorf("%s: %v", tool.name, err)
		}
	}
	raw, _ := os.ReadFile(log)
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if !strings.HasPrefix(line, "--profile other ") {
			t.Errorf("recovered CLI invocation used the wrong profile: %q", line)
		}
	}
	eventually(t, time.Second, "live connector cache to refresh to the saved profile", func() bool {
		c := currentRecoveryCLI(w.h)
		return c != nil && c.Profile() == "other"
	})
}

func TestBotLoadCLIRecoveryPreservesConnectorSettings(t *testing.T) {
	needsLarkModule(t)
	for _, tc := range []struct {
		name, override, chosen, want string
		off, feedOff, missing        bool
	}{
		{name: "disabled_manual_profile", chosen: "other", want: "other", off: true, feedOff: true},
		{name: "enabled_manual_profile", chosen: "other", want: "other"},
		{name: "manager_override", override: "fixed", chosen: "other", want: "fixed", off: true, feedOff: true},
		{name: "actual_empty_config", want: "", missing: true, off: true, feedOff: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newBotLoadWorld(t, true, false)
			bin, err := filepath.Abs(filepath.Join("..", "modules", "lark", "larkcli", "testdata", "fake-lark-cli.sh"))
			if err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(t.TempDir(), "calls.log")
			t.Setenv("LARKCLI_FAKE_LOG", log)
			t.Setenv("LARKCLI_FAKE_SCENARIO", "success")
			w.h.cfg.Lark = lark.Config{CLI: &lark.CLIConfig{Bin: bin, Profile: tc.override}}
			if !tc.missing {
				if err := w.store.botStore.PutSetting(w.ctx, botSettingsKey, FeishuBotSettings{Enabled: false, LarkCLIProfile: tc.chosen}); err != nil {
					t.Fatal(err)
				}
			}
			options := json.RawMessage(`{"inbox_all_chats":["oc_saved"],"documents":["doc_saved"],"wiki_spaces":[],"triage_rules":"Keep saved preferences"}`)
			cs := connectorSettings{Switch: map[string]bool{"larkcli": !tc.off}, Inbox: map[string]bool{"larkcli": !tc.feedOff}, Options: map[string]json.RawMessage{"larkcli": options}}
			if err := w.store.botStore.PutSetting(w.ctx, connectorSettingsKey, cs); err != nil {
				t.Fatal(err)
			}
			w.h.connectors = w.h.newConnectors(nil)
			w.h.startConnectors(w.ctx)
			old := w.h.connectors.Get("larkcli").(cliport.Connector)
			before := string(old.Options())
			if tc.override != "" {
				if _, err := old.Call(w.ctx, "larkcli_chats", json.RawMessage(`{}`)); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := old.Call(w.ctx, "larkcli_chats", json.RawMessage(`{}`)); err == nil {
					t.Fatal("unknown profile ran CLI")
				}
				if raw, _ := os.ReadFile(log); len(raw) > 0 {
					t.Fatalf("unknown profile invoked CLI: %s", raw)
				}
			}
			w.store.recover()
			w.advance()
			w.h.botAPI()
			eventually(t, time.Second, "recovered connector profile", func() bool { c := currentRecoveryCLI(w.h); return c != nil && c.Profile() == tc.want })
			current := w.h.connectors.Get("larkcli").(cliport.Connector)
			if string(current.Options()) != before {
				t.Fatalf("options lost: before=%s after=%s", before, current.Options())
			}
			if w.h.connectors.Enabled("larkcli") == tc.off || w.h.connectors.Feeds("larkcli", connector.FeedInbox) == tc.feedOff {
				t.Fatal("recovery changed connector enabled/feed switches")
			}
			if tc.off && connector.StatusOf(w.ctx, current).State != connector.StateDisabled {
				t.Fatal("replacement lost the connector instance's disabled status")
			}

			// Disabled connectors stay stopped throughout replacement. A direct capability call below probes the selected identity independently of that switch.
			if tc.off {
				raw, _ := os.ReadFile(log)
				if tc.override == "" && len(raw) > 0 {
					t.Fatalf("disabled connector started during recovery: %s", raw)
				}
			}
			if _, err := current.Call(w.ctx, "larkcli_chats", json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
			_, _ = current.(interface {
				Poll(context.Context) (bool, error)
			}).Poll(w.ctx) // the connector's Inbox poll (connector/larkcli)
			raw, _ := os.ReadFile(log)
			if len(raw) == 0 {
				t.Fatal("fake CLI was not invoked")
			}
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				prefix := "--profile " + tc.want + " "
				if tc.want != "" && !strings.HasPrefix(line, prefix) {
					t.Errorf("wrong CLI profile: %s", line)
				}
				if tc.want == "" && strings.HasPrefix(line, "--profile ") {
					t.Errorf("known default acquired a profile: %s", line)
				}
			}
		})
	}
}

func TestBotLoadManagedCLIRecoverySynchronizes(t *testing.T) {
	needsLarkModule(t)
	w := newBotLoadWorld(t, true, false)
	bin, err := filepath.Abs(filepath.Join("..", "modules", "lark", "larkcli", "testdata", "fake-lark-cli.sh"))
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "init.log")
	t.Setenv("LARKCLI_FAKE_SCENARIO", "config_different")
	t.Setenv("LARKCLI_FAKE_INIT_LOG", log)
	w.h.cfg.Lark = lark.Config{CLI: &lark.CLIConfig{Bin: bin}}
	sealed, err := w.h.sealKey("synthetic-recovery-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.botStore.PutSetting(w.ctx, botSettingsKey, FeishuBotSettings{Enabled: false, AppID: "cli_recovered", SecretEnc: sealed}); err != nil {
		t.Fatal(err)
	}
	w.h.connectors = w.h.newConnectors(nil)
	w.h.startConnectors(w.ctx)
	if _, err := os.Stat(log); err == nil {
		t.Fatal("unknown identity initialized the managed CLI")
	}
	w.store.recover()
	w.advance()
	w.h.botAPI()
	eventually(t, 2*time.Second, "managed CLI synchronization and replacement", func() bool {
		c := currentRecoveryCLI(w.h)
		return c != nil && c.Profile() == managedLarkCLIProfile
	})
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "--app-id cli_recovered") || !strings.Contains(string(raw), "stdin=synthetic-recovery-secret") || strings.Contains(strings.SplitN(string(raw), "|", 2)[0], "synthetic-recovery-secret") {
		t.Fatalf("managed profile not synchronized safely: %s", raw)
	}
	if api, _ := w.h.botAPI(); api != nil {
		t.Fatal("CLI recovery enabled the disabled bot")
	}
}
