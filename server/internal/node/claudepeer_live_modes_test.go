package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/tmux"
)

const (
	livePeerBypassHead = `<cross-session-message from-name="Persona" from-mode="bypass">`
	livePeerBareHead   = `<cross-session-message from-name="Persona">`
)

// livePeerCase describes the receiver-side evidence available when one message is sent. An empty transcriptMode
// writes a transcript with no recorded permission mode; nil argv makes the process arguments unreadable.
type livePeerCase struct {
	transcriptMode string
	argv           []string
	footerMode     string
	footerText     string
	receiverClass  string
	owner          bool
}

// sendLivePeer sends one message through the existing outside-Claude path and returns its envelope's opening tag.
// A nonempty footerMode is served by a fake tmux capture for the external terminal named in Claude's session file.
func sendLivePeer(t *testing.T, c livePeerCase) (string, error) {
	t.Helper()
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 500*time.Millisecond)
	oldArgv := processArgv
	processArgv = func(pid int) ([]string, error) {
		if pid == os.Getpid() && c.argv != nil {
			return c.argv, nil
		}
		return nil, errors.New("process arguments unavailable")
	}
	t.Cleanup(func() { processArgv = oldArgv })

	var transcript string
	if c.transcriptMode == "" {
		transcript = bgTranscript(t, home, home, sidA, "deploy")
	} else {
		transcript = modeTranscript(t, home, home, c.transcriptMode)
	}
	inbox := newFakeInbox(t, transcript)
	inbox.setMode(c.receiverClass)
	entrypoint := "claude-desktop"
	var extra map[string]any
	if c.footerMode != "" && runtime.GOOS == "windows" {
		t.Skip("the external tmux capture fixture is a POSIX shell script")
	}
	if c.footerMode != "" {
		footer := c.footerText
		if footer == "" {
			footer = map[string]string{
				"default":           "────\n❯ \n────\n  ? for shortcuts\n",
				"bypassPermissions": "────\n❯ \n────\n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n",
			}[c.footerMode]
		}
		if footer == "" {
			t.Fatalf("no footer fixture for mode %q", c.footerMode)
		}
		dir := t.TempDir()
		screenPath := filepath.Join(dir, "screen")
		if err := os.WriteFile(screenPath, []byte(footer), 0o600); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(dir, "tmux")
		script := "#!/bin/sh\n[ \"$1\" = capture-pane ] && /bin/cat " + shellQuote(screenPath) + "\nexit 0\n"
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		n.hosts.Set(append(n.hosts.All(), &tmuxHost{c: tmux.Client{Bin: bin}})...)
		entrypoint = "cli"
		extra = map[string]any{"tmux": "orc:@11.%11"}
	}
	writeOutsideClaude(t, home, sidA, entrypoint, "idle", inbox, extra)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	if c.footerMode != "" {
		proc, ok := n.outsideClaude(sidA)
		if !ok {
			t.Fatal("the external-terminal Claude is not live")
		}
		pane := outsidePane(proc)
		captured, err := n.readPane(context.Background(), pane, "visible", 60)
		if pane == "" || err != nil {
			t.Fatalf("external footer %q: pane %q, capture %q, %v", c.footerMode, pane, captured, err)
		}
		if c.footerText != "" {
			if captured != c.footerText {
				t.Fatalf("external capture = %q, want fixture %q", captured, c.footerText)
			}
		} else if tuiMode(captured) != c.footerMode {
			t.Fatalf("external footer %q: pane %q, capture %q", c.footerMode, pane, captured)
		}
	}

	if c.footerMode != "" {
		// Ordinary owner messages now use tmux's direct pane path. Forced priority models a card answer, which still
		// exercises the remaining inbox path and its receiver-class attestation.
		ctx := context.Background()
		if c.owner {
			ctx, _ = withOwnerDelivery(ctx)
		}
		handled, err := n.sendOutsidePriority(ctx, sidA, "ship it", nil, peerNow)
		if !handled {
			t.Fatal("forced outside-Claude send was not handled")
		}
		return envelopeHead(t, inbox, 0), err
	}

	params := map[string]any{"session_id": sidA, "text": "ship it", "client_msg_id": "live-peer-1"}
	if c.owner {
		params["owner_input"] = true
	}
	_, err := request(t, n, "session.send", params)
	return envelopeHead(t, inbox, 0), err
}

func checkLivePeer(t *testing.T, head string, err error, wantHead, wantCode string) {
	t.Helper()
	if head != wantHead {
		t.Errorf("envelope = %q, want %q", head, wantHead)
	}
	if wantCode == "" {
		if err != nil {
			t.Errorf("receiver did not take the message: %v (code %q)", err, refusalCode(err))
		}
	} else if err == nil || refusalCode(err) != wantCode || !maybeDelivered(err) {
		t.Errorf("send outcome = %v (code %q), want delivered-unknown %q", err, refusalCode(err), wantCode)
	}
}

// AC1: plan is a bypass-class session when its live process was started with bypass available. Without that process
// evidence, plan keeps its prior unattested behavior rather than guessing bypass.
func TestOutsideClaudeOwnerPlanWithBypassArgv(t *testing.T) {
	t.Run("bypass available", func(t *testing.T) {
		head, err := sendLivePeer(t, livePeerCase{
			transcriptMode: "plan",
			argv:           []string{"claude", "--dangerously-skip-permissions"},
			receiverClass:  peerModeBypass,
			owner:          true,
		})
		checkLivePeer(t, head, err, livePeerBypassHead, "")
	})
	t.Run("bypass unavailable", func(t *testing.T) {
		head, err := sendLivePeer(t, livePeerCase{
			transcriptMode: "plan",
			argv:           []string{"claude", "--resume", sidA},
			receiverClass:  peerModeBypass,
			owner:          true,
		})
		checkLivePeer(t, head, err, livePeerBareHead, peerHeld)
	})
}

// AC2: immediately after default becomes bypass, the live footer is newer evidence than the transcript. Cover both a
// stale default record and a transcript that has not recorded a mode yet.
func TestOutsideClaudeOwnerLiveFooterDefaultToBypass(t *testing.T) {
	for _, mode := range []string{"default", ""} {
		name := mode
		if name == "" {
			name = "no recorded mode"
		}
		t.Run(name, func(t *testing.T) {
			head, err := sendLivePeer(t, livePeerCase{
				transcriptMode: mode,
				footerMode:     "bypassPermissions",
				receiverClass:  peerModeBypass,
				owner:          true,
			})
			checkLivePeer(t, head, err, livePeerBypassHead, "")
		})
	}
}

// AC3: immediately after bypass becomes default, the live footer must replace the stale bypass attestation. The
// process's bypass-enabling flag says only that bypass is available, not that it is still the current mode.
func TestOutsideClaudeOwnerLiveFooterBypassToDefault(t *testing.T) {
	head, err := sendLivePeer(t, livePeerCase{
		transcriptMode: "bypassPermissions",
		argv:           []string{"claude", "--allow-dangerously-skip-permissions"},
		footerMode:     "default",
		receiverClass:  peerModePrompting,
		owner:          true,
	})
	checkLivePeer(t, head, err, livePeerBareHead, "")
}

func peerScreenFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "screen", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A draft or dialog can hide default's idle hint, and dontAsk has different wording. The visible composer is still
// newer prompting evidence than a stale bypass transcript. Without a composer, transcript fallback remains intact.
func TestOutsideClaudeOwnerLivePromptingComposerOverridesStaleBypass(t *testing.T) {
	cases := []struct {
		name       string
		footerMode string
		footerText string
		receiver   string
		wantHead   string
		wantCode   string
	}{
		{"default draft", "default", "────\n❯ half-typed draft\n────\n", peerModePrompting, livePeerBareHead, ""},
		{"default dialog", "default", "────\n❯ \n────\n  Settings  Status  Config\n", peerModePrompting, livePeerBareHead, ""},
		{"dontAsk", "dontAsk", "────\n❯ \n────\n  ⏵⏵ don't ask on (shift+tab to cycle)\n", peerModePrompting, livePeerBareHead, ""},
		{"question dialog keeps bypass", "bypassPermissions", peerScreenFixture(t, "claude-2.1.303-questions-preview-198.txt"), peerModeBypass, livePeerBypassHead, ""},
		{"permission dialog keeps bypass", "bypassPermissions", "────────────────────────────────────────────────\n Bash command\n\n   git push origin main\n   Push the release commit\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. Yes, and don't ask again for git push commands in /code/app\n   3. No, and tell Claude what to do differently (esc)\n", peerModeBypass, livePeerBypassHead, ""},
		{"no composer falls back", "bypassPermissions", "no composer here\n", peerModeBypass, livePeerBypassHead, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			head, err := sendLivePeer(t, livePeerCase{
				transcriptMode: "bypassPermissions",
				footerMode:     tc.footerMode,
				footerText:     tc.footerText,
				receiverClass:  tc.receiver,
				owner:          true,
			})
			checkLivePeer(t, head, err, tc.wantHead, tc.wantCode)
		})
	}
}

// AC4: receiver-side evidence never authorizes somebody else's words. With no readable footer or bypass-enabling
// argv, the existing fresh-transcript classification remains, including no guess when the transcript states no mode.
func TestOutsideClaudePeerEvidenceOwnerOnlyAndFallback(t *testing.T) {
	t.Run("non-owner with live bypass evidence", func(t *testing.T) {
		head, err := sendLivePeer(t, livePeerCase{
			transcriptMode: "default",
			argv:           []string{"claude", "--dangerously-skip-permissions"},
			footerMode:     "bypassPermissions",
			receiverClass:  peerModeBypass,
		})
		checkLivePeer(t, head, err, livePeerBareHead, peerHeld)
	})
	t.Run("fresh bypass transcript", func(t *testing.T) {
		head, err := sendLivePeer(t, livePeerCase{
			transcriptMode: "bypassPermissions",
			receiverClass:  peerModeBypass,
			owner:          true,
		})
		checkLivePeer(t, head, err, livePeerBypassHead, "")
	})
	t.Run("fresh default transcript", func(t *testing.T) {
		head, err := sendLivePeer(t, livePeerCase{
			transcriptMode: "default",
			receiverClass:  peerModePrompting,
			owner:          true,
		})
		checkLivePeer(t, head, err, livePeerBareHead, "")
	})
	t.Run("no recorded mode never guesses bypass", func(t *testing.T) {
		head, err := sendLivePeer(t, livePeerCase{
			receiverClass: peerModeBypass,
			owner:         true,
		})
		checkLivePeer(t, head, err, livePeerBareHead, peerHeld)
	})
}
