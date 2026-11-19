package hub

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/db/dbtest"
)

// Persist production sendOnce outcomes across OS process exits, with a real
// database and a fake node. Unknown delivery must never become permission to send.
func TestSendOnceProcessRestart(t *testing.T) {
	if os.Getenv(dbtest.Env) == "" {
		t.Skip(dbtest.Env + " not set")
	}
	for _, outcome := range []string{"delivered", "unknown", "sending"} {
		t.Run(outcome, func(t *testing.T) {
			url, done, err := dbtest.Own(t.Context(), os.Getenv(dbtest.Env))
			if err != nil {
				t.Fatal(err)
			}
			defer done()
			effects := filepath.Join(t.TempDir(), "effects")
			for _, phase := range []string{"first", "again", "again"} {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSendOnceProcessHelper$", "-test.v")
				cmd.Env = append(os.Environ(), dbtest.Env+"="+url, "PERSONA_SEND_PHASE="+phase, "PERSONA_SEND_OUTCOME="+outcome, "PERSONA_SEND_EFFECTS="+effects)
				out, err := cmd.CombinedOutput()
				cancel()
				if phase == "first" && outcome == "sending" {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 23 {
						t.Fatalf("crash: %v\n%s", err, out)
					}
				} else if err != nil {
					t.Fatalf("%s: %v\n%s", phase, err, out)
				}
			}
			b, err := os.ReadFile(effects)
			if err != nil || string(b) != "typed\n" {
				t.Fatalf("side effects: %q %v", b, err)
			}
		})
	}
}

func TestSendOnceProcessHelper(t *testing.T) {
	phase, outcome := os.Getenv("PERSONA_SEND_PHASE"), os.Getenv("PERSONA_SEND_OUTCOME")
	if phase == "" {
		t.Skip("subprocess helper")
	}
	ctx := context.Background()
	database, err := db.Open(ctx, os.Getenv(dbtest.Env), db.HubSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	h := newTestHub(t)
	h.store = database
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	peer := pipeNode(t, h, "box", func(context.Context) error {
		f, err := os.OpenFile(os.Getenv("PERSONA_SEND_EFFECTS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.WriteString("typed\n")
		_ = f.Close()
		if err != nil {
			return err
		}
		if outcome == "sending" {
			os.Exit(23)
		}
		if outcome == "unknown" {
			close(started)
			<-release
			return io.ErrUnexpectedEOF
		}
		return nil
	})
	// Break the RPC after the side effect but before its acknowledgement.
	if phase == "first" && outcome == "unknown" {
		go func() { <-started; peer.Close(io.ErrUnexpectedEOF) }()
	}
	err = h.sendOnce(ctx, h.byID["box"], map[string]any{"session_id": "s1", "client_msg_id": "model-retry:s1:1:1", "text": "Continue"})
	if outcome == "delivered" && err != nil {
		t.Fatal(err)
	}
	if outcome != "delivered" && err == nil {
		t.Fatal("uncertain delivery reported success")
	}
	state, claimErr := database.ClaimSend(ctx, "model-retry:s1:1:1", "box", "s1")
	if claimErr != nil || state != outcome {
		t.Fatalf("receipt=%q want %q: %v", state, outcome, claimErr)
	}
}
