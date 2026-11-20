package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// signInCLI is resumeCLI that also answers `claude auth status`: signed in only in the user's own setup (no
// CLAUDE_CONFIG_DIR, ~/.claude), as plain claude signed in with a subscription is, and in the directory named in
// $CLI_FAKE_DIR/auth-dir, unless $CLI_FAKE_DIR/auth-everywhere says it is signed in everywhere (a gateway's token in its
// environment) or $CLI_FAKE_DIR/auth-unknown that it can't tell. Every status run appends a line to
// $CLI_FAKE_DIR/auth-runs.
const signInCLI = `case " $* " in
  *" auth status "*)
    echo "${CLAUDE_CONFIG_DIR-unset}" >> "$CLI_FAKE_DIR/auth-runs"
    if [ -e "$CLI_FAKE_DIR/auth-unknown" ]; then echo 'not json'; exit 1; fi
    if [ -e "$CLI_FAKE_DIR/auth-everywhere" ] || [ "${CLAUDE_CONFIG_DIR+set}" != set ] ||
      { [ -e "$CLI_FAKE_DIR/auth-dir" ] && [ "$CLAUDE_CONFIG_DIR" = "$(cat "$CLI_FAKE_DIR/auth-dir")" ]; }; then
      echo '{"loggedIn": true, "authMethod": "claude.ai"}'; exit 0
    fi
    echo '{"loggedIn": false, "authMethod": "none"}'; exit 1 ;;
esac
` + resumeCLI

// signInTest is resumeTest with signInCLI as the machine's claude, and a conversation id whose only transcript (and
// subagent file) is in Persona's private profile of a shell launcher, "gateway", which it last ran on.
func signInTest(t *testing.T) (f *cliFixture, real, id, cwd, private string) {
	t.Helper()
	f, real = resumeTest(t)
	if err := os.WriteFile(filepath.Join(real, "bin", "claude"), []byte("#!/bin/sh\n"+signInCLI), 0o700); err != nil {
		t.Fatal(err)
	}
	n := f.n
	n.claudeV.set("2.1.308")
	id = "a5500000-0000-4000-8000-000000011694"
	cwd = filepath.Join(n.dir, "assistant")
	private = filepath.Join(n.dir, "profiles", "gateway")
	writeProfileTranscript(t, private, cwd, id)
	writeFile(t, filepath.Join(private, "projects", "-assistant", id, "subagents", "agent-a.jsonl"), "{}\n")
	putSessionState(t, n, id, map[string]any{"launcher": "gateway", "cwd": cwd, "config_dir": private})
	return f, real, id, cwd, private
}

// Switched to plain claude, a conversation of a shell launcher's private profile, which plain claude is signed out of,
// resumes in the user's own setup where it is signed in: its transcript and subagents copied there, the original kept.
func TestAssistantSwitchResumesWhereTheLauncherIsSignedIn(t *testing.T) {
	f, real, id, cwd, private := signInTest(t)
	n := f.n
	params := map[string]any{"launcher": "claude", "cwd": cwd, "text": "hello", "session_id": id, "model_fingerprint": "signin-test"}
	if _, err := nodeCall(t, n, "assistant.send", params); err != nil {
		t.Fatalf("resume failed: %v (child saw %q)", err, f.file("cli-profile"))
	}
	if got := f.file("cli-profile"); got != "unset\n" {
		t.Fatalf("child CLAUDE_CONFIG_DIR = %q, want unset (the signed-in ~/.claude)", got)
	}
	moved := filepath.Join(real, ".claude", "projects", "-assistant")
	for _, p := range []string{filepath.Join(moved, id+".jsonl"), filepath.Join(moved, id, "subagents", "agent-a.jsonl"),
		filepath.Join(private, "projects", "-assistant", id+".jsonl")} {
		if !fileExists(p) {
			t.Fatalf("%s missing", p)
		}
	}
	if st, _ := n.state.get(id); st.ConfigDir != "default" || st.Launcher != "claude" {
		t.Fatalf("recorded %q on %q, want default on claude", st.ConfigDir, st.Launcher)
	}
}

// Persona's own profile of plain claude is where its new conversations start by default. Signed out there (the
// default: nobody signed in to it), the conversation goes to the user's own setup; signed in there, it goes there.
func TestAssistantSignedInResumeTriesPersonasOwnProfileFirst(t *testing.T) {
	for _, signedIn := range []bool{false, true} {
		name := "own-profile-signed-out"
		if signedIn {
			name = "own-profile-signed-in"
		}
		t.Run(name, func(t *testing.T) {
			f, real, id, _, _ := signInTest(t)
			own := filepath.Join(f.n.dir, "profiles", "claude")
			if err := os.MkdirAll(own, 0o700); err != nil {
				t.Fatal(err)
			}
			want, at := "default", filepath.Join(real, ".claude")
			if signedIn {
				writeFile(t, filepath.Join(os.Getenv("CLI_FAKE_DIR"), "auth-dir"), own)
				want, at = own, own
			}
			got, err := f.n.assistantProfileSelection(context.Background(), id, "claude", "", true)
			if err != nil || got != want {
				t.Fatalf("got %q %v, want %q", got, err, want)
			}
			if !fileExists(filepath.Join(at, "projects", "-assistant", id+".jsonl")) {
				t.Fatalf("not copied to %s", at)
			}
			if !signedIn && fileExists(filepath.Join(own, "projects", "-assistant", id+".jsonl")) {
				t.Fatal("copied to the signed-out profile")
			}
		})
	}
}

// What keeps the resume where it is: the launcher signed in there, a state Claude Code can't tell, no profile it is
// signed in to, its own private profile. Answers are kept: asked again, nothing runs again.
func TestAssistantSignedInResumeBoundaries(t *testing.T) {
	selection := func(t *testing.T, n *Node, id, launcher string) string {
		t.Helper()
		got, err := n.assistantProfileSelection(context.Background(), id, launcher, "", true)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	for _, c := range []struct{ name, marker string }{{"signed-in-there", "auth-everywhere"}, {"unknown", "auth-unknown"}} {
		t.Run(c.name, func(t *testing.T) {
			f, real, id, _, private := signInTest(t)
			writeFile(t, filepath.Join(os.Getenv("CLI_FAKE_DIR"), c.marker), "")
			if got := selection(t, f.n, id, "claude"); got != private {
				t.Fatalf("got %q, want %q", got, private)
			}
			if fileExists(filepath.Join(real, ".claude", "projects", "-assistant", id+".jsonl")) {
				t.Fatal("copied")
			}
			runs := f.file("auth-runs")
			selection(t, f.n, id, "claude")
			if f.file("auth-runs") != runs {
				t.Fatalf("asked again: %q", f.file("auth-runs"))
			}
		})
	}
	t.Run("signed-in-nowhere", func(t *testing.T) {
		f, real, id, _, private := signInTest(t)
		f.n.claudeStartDir.Store(ptr(filepath.Join(real, "elsewhere"))) // plain claude's own setup is signed out too
		if got := selection(t, f.n, id, "claude"); got != private {
			t.Fatalf("got %q, want %q", got, private)
		}
		if runs := strings.Count(f.file("auth-runs"), "\n"); runs != 2 {
			t.Fatalf("%d status runs, want the profile's and the user's setup's", runs)
		}
	})
	t.Run("own-profile", func(t *testing.T) {
		f, _, id, _, private := signInTest(t)
		if got := selection(t, f.n, id, "gateway"); got != private {
			t.Fatalf("got %q, want %q", got, private)
		}
		if f.file("auth-runs") != "" {
			t.Fatal("asked for the launcher's own profile")
		}
	})
}

func ptr[T any](v T) *T { return &v }
