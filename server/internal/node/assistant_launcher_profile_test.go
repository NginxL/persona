package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Refuse before replacing a process or delivering input in another launcher's
// private profile, including a request carrying the old provider's model.
func TestAssistantLauncherSwitchRefusesAnotherPrivateProfile(t *testing.T) {
	for _, live := range []bool{false, true} {
		name := "cold"
		if live {
			name = "live"
		}
		t.Run(name, func(t *testing.T) {
			f := cliTest(t)
			n := f.n
			n.home = os.Getenv("HOME")
			n.dir = filepath.Join(n.home, ".persona")
			n.cdirs = newClaudeDirs()
			n.cdirs.scan = func() map[int]procEnv { return nil }
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			os.Unsetenv("CLAUDE_CONFIG_DIR")
			f.fakeLaunch(t)
			n.setManaged([]proto.ManagedLauncher{{Name: "gateway", Kind: "api", API: "messages", Model: "gateway-model"}})
			id := "a5500000-0000-4000-8000-000000000453"
			cwd := filepath.Join(n.dir, "assistant")
			profile := filepath.Join(n.dir, "profiles", "gateway")
			writeProfileTranscript(t, profile, cwd, id)
			putSessionState(t, n, id, map[string]any{"launcher": "gateway", "cwd": cwd, "config_dir": profile, "model": "gateway-model"})
			if live {
				if _, err := nodeCall(t, n, "assistant.send", map[string]any{"session_id": id, "launcher": "gateway", "text": "first", "model_fingerprint": "gateway"}); err != nil {
					t.Fatal(err)
				}
				f.idle(t, id)
			}
			before, _ := n.state.get(id)
			old := f.proc(id)
			input, launches := f.file("stdin.jsonl"), f.file("cli-argv")
			for _, model := range []string{"", "gateway-model"} {
				_, err := nodeCall(t, n, "assistant.send", map[string]any{"session_id": id, "launcher": "claude", "text": "do not deliver", "model": model, "model_fingerprint": "plain"})
				var message *rpc.Error
				if !errors.As(err, &message) || message.Code != "assistant_launcher_profile_switch" {
					t.Fatalf("model %q: got %v, want actionable profile refusal", model, err)
				}
			}
			after, _ := n.state.get(id)
			if after.Launcher != before.Launcher || after.ConfigDir != before.ConfigDir || after.Model != before.Model {
				t.Fatal("refusal changed saved selection")
			}
			if f.proc(id) != old || old != nil && !old.alive() {
				t.Fatal("refusal replaced or stopped original process")
			}
			if f.file("stdin.jsonl") != input || f.file("cli-argv") != launches {
				t.Fatal("refusal started a CLI or delivered input")
			}
		})
	}
}

func TestAssistantLauncherProfileSwitchBoundaries(t *testing.T) {
	f, _ := resumeTest(t)
	n := f.n
	const id = "a5500000-0000-4000-8000-000000000454"
	cwd := filepath.Join(n.dir, "assistant")
	private := filepath.Join(n.dir, "profiles", "gateway")
	writeProfileTranscript(t, private, cwd, id)
	putSessionState(t, n, id, map[string]any{"launcher": "gateway", "cwd": cwd, "config_dir": private})
	if got, err := n.assistantProfileSelection(context.Background(), id, "gateway", "", true); err != nil || got != private {
		t.Fatalf("same launcher: %q %v", got, err)
	}
	if _, err := n.assistantProfileSelection(context.Background(), id, "claude", "default", true); err == nil {
		t.Fatal("explicit directory bypassed profile ownership")
	}
	ageTranscript(t, private, id)
	writeProfileTranscript(t, filepath.Join(n.home, ".claude"), cwd, id)
	n.mu.Lock()
	delete(n.pathsAt, id)
	delete(n.paths, id)
	n.mu.Unlock()
	if got, err := n.assistantProfileSelection(context.Background(), id, "claude", "", true); err != nil || got != "default" {
		t.Fatalf("moved transcript: %q %v", got, err)
	}
}

// Older or previously misrecorded session metadata must not bypass the actual
// managed profile's ownership.
func TestAssistantLauncherProfileOwnershipWithoutCurrentLauncher(t *testing.T) {
	for _, recorded := range []string{"", "claude", "gateway"} {
		t.Run("recorded-"+recorded, func(t *testing.T) {
			f, _ := resumeTest(t)
			n := f.n
			n.setManaged([]proto.ManagedLauncher{{Name: "gateway", Kind: "api", API: "messages"}})
			id := "a5500000-0000-4000-8000-000000000455"
			cwd := filepath.Join(n.dir, "assistant")
			profile := filepath.Join(n.dir, "profiles", "gateway")
			writeProfileTranscript(t, profile, cwd, id)
			putSessionState(t, n, id, map[string]any{"launcher": recorded, "cwd": cwd, "config_dir": profile})
			for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
				_, err := n.assistantProfileSelection(i18n.With(context.Background(), lang), id, "claude", "", true)
				var message *rpc.Error
				if !errors.As(err, &message) || message.Code != "assistant_launcher_profile_switch" || message.Message != i18n.T(lang, "rpc-errors.assistant_launcher_profile_switch") {
					t.Fatalf("locale %s: got %v, want localized profile refusal", lang, err)
				}
			}
		})
	}
}
