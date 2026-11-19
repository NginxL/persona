package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Exercise the RPC boundary and the real fixture process, not just a profile
// resolver. Old records deliberately contain no config_dir.
func TestAssistantProfileCompatibility(t *testing.T) {
	for _, scenario := range []string{"new-default", "new-explicit", "new-legacy", "new-machine-default", "new-shell-default", "old-service", "old-home", "old-home-with-service", "old-machine", "old-shell-default", "old-discovered", "old-missing", "old-recorded"} {
		t.Run(scenario, func(t *testing.T) {
			f := cliTest(t)
			n := f.n
			n.home = os.Getenv("HOME")
			n.dir = filepath.Join(n.home, ".persona")
			n.cdirs = newClaudeDirs()
			n.cdirs.scan = func() map[int]procEnv { return nil }
			service, other := t.TempDir(), t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", service)
			cwd := filepath.Join(n.dir, "assistant")
			params := map[string]any{"launcher": "claude", "cwd": cwd, "text": "hello", "model_fingerprint": "compat-test"}
			want := filepath.Join(n.dir, "profiles", "claude")
			profile, id := want, ""
			switch scenario {
			case "new-explicit":
				want, profile = other, other
				params["config_dir"] = other
			case "new-legacy":
				want, profile = service, service
				params["config_dir"] = "legacy"
			case "new-machine-default":
				n.claudeStartDir.Store(&other)
			case "new-shell-default":
				writeFile(t, filepath.Join(n.home, ".zshrc"), "export CLAUDE_CONFIG_DIR="+shellQuote(other)+"\n")
			case "old-service", "old-missing":
				want, profile = service, service
			case "old-home":
				t.Setenv("CLAUDE_CONFIG_DIR", "")
				want, profile = "", filepath.Join(n.home, ".claude")
			case "old-home-with-service":
				want, profile = "", filepath.Join(n.home, ".claude")
			case "old-machine":
				n.claudeStartDir.Store(&other)
				want, profile = other, other
			case "old-shell-default":
				writeFile(t, filepath.Join(n.home, ".zshrc"), "export CLAUDE_CONFIG_DIR="+shellQuote(other)+"\n")
				want, profile = other, other
			case "old-discovered", "old-recorded":
				want, profile = other, other
			}
			if strings.HasPrefix(scenario, "old-") {
				id = "a5500000-0000-4000-8000-000000000001"
				params["session_id"] = id
				raw, _ := json.Marshal(map[string]any{"launcher": "claude", "cwd": cwd, "config_dir": func() string {
					if scenario == "old-recorded" {
						return other
					}
					return ""
				}()})
				var st sessionState
				if err := json.Unmarshal(raw, &st); err != nil {
					t.Fatal(err)
				}
				n.state.put(id, st)
				if scenario != "old-missing" && scenario != "old-recorded" {
					writeFile(t, filepath.Join(profile, "projects", "-assistant", id+".jsonl"),
						`{"type":"user","cwd":`+jsonString(t, cwd)+`,"message":{"role":"user","content":"earlier"}}`+"\n")
				}
				n.cdirs.known = []string{other}
			}
			answer, err := nodeCall(t, n, "assistant.send", params)
			if err != nil {
				t.Fatal(err)
			}
			var res struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(answer, &res); err != nil {
				t.Fatal(err)
			}
			check := func() {
				t.Helper()
				p := f.proc(res.SessionID)
				if got := envValue(p.cmd.Environ(), "CLAUDE_CONFIG_DIR"); got != want {
					t.Fatalf("%s profile=%q, want %q", scenario, got, want)
				}
				if got := strings.TrimSuffix(f.file("cli-profile"), "\n"); got != want {
					t.Fatalf("CLI child profile=%q, want %q", got, want)
				}
			}
			check()
			if st, ok := n.state.get(res.SessionID); !ok || st.ConfigDir == "" {
				t.Fatalf("profile selection was not recorded: %+v", st)
			}
			f.idle(t, res.SessionID)
			n.stopAssistant(res.SessionID)
			// Changing a service default cannot move a recorded conversation.
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": res.SessionID, "text": "again"}); err != nil {
				t.Fatal(err)
			}
			check()
			n.stopAssistant(res.SessionID)
			t.Logf("%s: profile retained after process restart", scenario)
		})
	}
}

func TestAssistantNewPrivateProfileMustBeSignedIn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		loggedIn bool
		explicit bool
	}{
		{name: "signed out"},
		{name: "signed in", loggedIn: true},
		{name: "explicit legacy is unchanged", explicit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cliTest(t)
			seen := filepath.Join(t.TempDir(), "auth-profile")
			fakeClaudeHome(t, `case " $* " in
  *" auth status "*)
    printf '%s\n' "$CLAUDE_CONFIG_DIR" > `+shellQuote(seen)+`
    echo '{"loggedIn": `+fmt.Sprintf("%t", tc.loggedIn)+`, "authMethod": "claude.ai"}'
    exit 1 ;;
esac
`+fakeCLI)
			n := f.n
			n.home = os.Getenv("HOME")
			n.dir = filepath.Join(n.home, ".persona")
			n.claudeV.set("2.1.308")
			params := map[string]any{"launcher": "claude", "cwd": t.TempDir(), "text": "hello"}
			if tc.explicit {
				params["config_dir"] = "legacy"
			}
			answer, err := nodeCall(t, n, "assistant.send", params)
			if !tc.loggedIn && !tc.explicit {
				var re *rpc.Error
				if !errors.As(err, &re) || re.Code != proto.TurnErrorNotLoggedIn ||
					!strings.Contains(string(re.Data), `"delivered":"none"`) {
					t.Fatalf("signed-out start = %v", err)
				}
				if got := f.file("cli-argv") + f.file("start.json"); got != "" {
					t.Fatalf("a model process started before sign-in: %q", got)
				}
			} else if err != nil {
				t.Fatal(err)
			} else {
				var res struct {
					SessionID string `json:"session_id"`
				}
				if err := json.Unmarshal(answer, &res); err != nil {
					t.Fatal(err)
				}
				n.stopAssistant(res.SessionID)
			}
			profile, readErr := os.ReadFile(seen)
			if tc.explicit {
				if !os.IsNotExist(readErr) {
					t.Fatalf("an explicit legacy profile was preflighted: %q, %v", profile, readErr)
				}
			} else if got, want := strings.TrimSpace(string(profile)), filepath.Join(n.dir, "profiles", "claude"); got != want {
				t.Fatalf("auth checked profile %q, want %q (%v)", got, want, readErr)
			}
		})
	}
}

func TestAssistantNewProfileAuthFailsOpen(t *testing.T) {
	for _, tc := range []struct {
		name, launcher, answer, wrapper string
		managed, checked                bool
	}{
		{name: "unreadable status", launcher: "claude", answer: "not a status", checked: true},
		{name: "managed provider", launcher: "gw", answer: `{"loggedIn":false}`, managed: true},
		{name: "launcher-owned profile", launcher: "work", answer: `{"loggedIn":false}`, wrapper: "/tmp/wrapper-profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cliTest(t)
			seen := filepath.Join(t.TempDir(), "auth-ran")
			fakeClaudeHome(t, `touch `+shellQuote(seen)+"\necho '"+tc.answer+"'\nexit 1\n")
			n := f.n
			n.home = os.Getenv("HOME")
			n.dir = filepath.Join(n.home, ".persona")
			n.claudeV.set("2.1.308")
			selected := n.assistantConfigDir(tc.launcher, "")
			if tc.managed {
				n.setManaged([]proto.ManagedLauncher{{Name: tc.launcher, BaseURL: "https://gw.example.com", APIKey: "test-key"}})
			}
			if tc.wrapper != "" {
				selected = tc.wrapper
				writeFile(t, filepath.Join(n.home, ".zshrc"), `function work { CLAUDE_CONFIG_DIR=`+shellQuote(tc.wrapper)+` command claude "$@"; }`+"\n")
			}
			if err := n.assistantNewProfileAuth(context.Background(), tc.launcher, "", selected); err != nil {
				t.Fatalf("start refused: %v", err)
			}
			_, err := os.Stat(seen)
			if tc.checked && err != nil {
				t.Fatalf("auth status did not run: %v", err)
			}
			if !tc.checked && !os.IsNotExist(err) {
				t.Fatal("an unchanged profile was preflighted")
			}
		})
	}
}

func TestAssistantProfileSelectionBoundaries(t *testing.T) {
	t.Run("scope cannot escape", func(t *testing.T) {
		n := scopedTestNode(t)
		if _, err := n.assistantProfileSelection(context.Background(), "", "claude", t.TempDir(), false); err == nil {
			t.Fatal("an external profile escaped the node's session scope")
		}
		if got, err := n.assistantProfileSelection(context.Background(), "", "claude", "", false); err != nil || got != n.sessionScope.claude {
			t.Fatalf("scoped default=%q: %v", got, err)
		}
		if got, err := n.assistantProfileSelection(context.Background(), "", "claude", "legacy", false); err != nil || got != n.sessionScope.claude {
			t.Fatalf("legacy did not resolve to the node's scoped profile: got %q, want %q (error %v)", got, n.sessionScope.claude, err)
		}
	})
	t.Run("wrapper keeps its own directory", func(t *testing.T) {
		f := cliTest(t)
		profile := t.TempDir()
		writeFile(t, filepath.Join(os.Getenv("HOME"), ".zshrc"), `function work { CLAUDE_CONFIG_DIR=`+shellQuote(profile)+` command claude "$@"; }`+"\n")
		if got, err := f.n.assistantProfileSelection(context.Background(), "", "work", "", false); err != nil || got != profile {
			t.Fatalf("wrapper default=%q: %v", got, err)
		}
		if _, err := f.n.assistantProfileSelection(context.Background(), "", "work", t.TempDir(), false); err == nil {
			t.Fatal("a conflicting wrapper was accepted")
		}
	})
	t.Run("warm spare cannot select another profile", func(t *testing.T) {
		f := cliTest(t)
		cwd, first, second := t.TempDir(), t.TempDir(), t.TempDir()
		if err := f.n.assistantWarm(cwd, "claude", modelExecution{ConfigDir: first}); err != nil {
			t.Fatal(err)
		}
		f.n.mu.Lock()
		p := f.n.spare
		f.n.mu.Unlock()
		t.Cleanup(p.stop)
		if got := f.n.adoptSpare(cwd, "claude", modelExecution{ConfigDir: second}); got != nil {
			t.Fatal("a new conversation adopted another profile's warm process")
		}
		if got := f.n.adoptSpare(cwd, "claude", modelExecution{ConfigDir: first}); got != p {
			t.Fatal("a matching warm process was not reused")
		}
	})
}

// The hub warms the spare in the main chat's profile choice for new conversations, and sends a start that profile, or
// none for Persona's own (#4633). When the choice changes between the two, either way, or a start names this computer's
// profile itself (a meeting's follow-up, a scheduled task), the spare from before is not adopted: the conversation
// starts in its own profile. Through the RPC boundary, as the hub calls it.
func TestAssistantSpareIsAdoptedOnlyInTheStartsProfile(t *testing.T) {
	// Answers each control request, as an adopted spare is told the start's permission mode, and each message.
	const cli = `echo '{"type":"system","subtype":"init"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"control_request"'*)
      rid=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}\n' "$rid" ;;
    *'"type":"user"'*) echo '{"type":"result","subtype":"success"}' ;;
  esac
done
`
	for _, tc := range []struct {
		name, warm, start string
		adopted           bool
	}{
		{"changed to Persona's own", "legacy", "", false},
		{"changed to this computer's", "", "legacy", false},
		{"Persona's own both times", "", "", true},
		{"this computer's both times", "legacy", "legacy", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cliTest(t)
			if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+cli), 0o700); err != nil {
				t.Fatal(err)
			}
			n := f.n
			n.home = os.Getenv("HOME")
			n.dir = filepath.Join(n.home, ".persona")
			n.cdirs = newClaudeDirs()
			n.cdirs.scan = func() map[int]procEnv { return nil }
			service := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", service) // this computer's profile: what "legacy" resolves to
			profile := map[string]string{"": filepath.Join(n.dir, "profiles", "claude"), "legacy": service}
			cwd := filepath.Join(n.dir, "assistant")
			params := func(dir string) map[string]any {
				p := map[string]any{"launcher": "claude", "cwd": cwd, "model_fingerprint": "spare-profile-test"}
				if dir != "" {
					p["config_dir"] = dir
				}
				return p
			}
			if _, err := nodeCall(t, n, "assistant.warm", params(tc.warm)); err != nil {
				t.Fatal(err)
			}
			n.mu.Lock()
			spare := n.spare
			n.mu.Unlock()
			if spare == nil {
				t.Fatal("nothing warmed")
			}
			t.Cleanup(spare.stop)
			if got := envValue(spare.cmd.Environ(), "CLAUDE_CONFIG_DIR"); got != profile[tc.warm] {
				t.Fatalf("warmed in %q, want %q", got, profile[tc.warm])
			}
			send := params(tc.start)
			send["text"], send["permission_mode"] = "hello", "default" // a bypass start never takes a Claude spare
			answer, err := nodeCall(t, n, "assistant.send", send)
			if err != nil {
				t.Fatal(err)
			}
			var res struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(answer, &res); err != nil {
				t.Fatal(err)
			}
			if adopted := res.SessionID == spare.id; adopted != tc.adopted {
				t.Fatalf("adopted the spare: %v, want %v", adopted, tc.adopted)
			}
			if got := envValue(f.proc(res.SessionID).cmd.Environ(), "CLAUDE_CONFIG_DIR"); got != profile[tc.start] {
				t.Fatalf("the conversation runs in %q, want %q", got, profile[tc.start])
			}
			if st, _ := n.state.get(res.SessionID); !sameAssistantProfile(st.ConfigDir, profile[tc.start]) {
				t.Fatalf("recorded profile %q, want %q", st.ConfigDir, profile[tc.start])
			}
			f.idle(t, res.SessionID)
		})
	}
	t.Run("a start naming no profile adopts no Claude spare in one", func(t *testing.T) {
		f := cliTest(t)
		cwd := t.TempDir()
		if err := f.n.assistantWarm(cwd, "claude", modelExecution{ConfigDir: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
		f.n.mu.Lock()
		p := f.n.spare
		f.n.mu.Unlock()
		t.Cleanup(p.stop)
		if got := f.n.adoptSpare(cwd, "claude"); got != nil {
			t.Fatal("a start with no resolved profile adopted a profiled spare")
		}
	})
}

func TestAssistantProfileCannotMoveExistingConversation(t *testing.T) {
	f := cliTest(t)
	first, second := t.TempDir(), t.TempDir()
	answer, err := nodeCall(t, f.n, "assistant.send", map[string]any{"cwd": t.TempDir(), "text": "hello", "config_dir": first})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(answer, &res); err != nil {
		t.Fatal(err)
	}
	f.idle(t, res.SessionID)
	f.n.stopAssistant(res.SessionID)
	_, err = nodeCall(t, f.n, "assistant.send", map[string]any{"session_id": res.SessionID, "text": "again", "config_dir": second})
	if err == nil || !strings.Contains(err.Error(), "config_dir") {
		t.Fatalf("moving a conversation was not refused: %v", err)
	}
}

func TestAssistantProfileRollbackKeepsTheOriginalTranscript(t *testing.T) {
	f := cliTest(t)
	n := f.n
	id := "a5500000-0000-4000-8000-000000000002"
	profile := filepath.Join(n.dir, "profiles", "claude")
	path := filepath.Join(profile, "projects", "-assistant", id+".jsonl")
	writeFile(t, path, `{"type":"user","message":{"role":"user","content":"keep this history"}}`+"\n")
	// The documented downgrade step selects the saved directory using the old
	// service/launcher environment. Old readers need not understand new metadata.
	t.Setenv("CLAUDE_CONFIG_DIR", profile)
	cmd := loginShell(context.Background(), `cat "$CLAUDE_CONFIG_DIR/projects/-assistant/`+id+`.jsonl"`)
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), "keep this history") {
		t.Fatalf("old env-only lookup lost the transcript: %q, %v", out, err)
	}
	if got := n.transcriptPath(id); got != path {
		t.Fatalf("old directory discovery cannot open %q: %q", path, got)
	}
}
