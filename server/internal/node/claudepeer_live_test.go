//go:build peerlive

package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
)

// TestLivePeerSend sends one message through the peer inbox of a claude started for the test (never a session the user
// has open): PERSONA_LIVE_PID names that claude, PERSONA_LIVE_DIR its CLAUDE_CONFIG_DIR, PERSONA_LIVE_TEXT the message,
// PERSONA_LIVE_MODE the from-mode to state (bypass or prompting, as peerAttest would; unset states none). With
// PERSONA_LIVE_CLASSIFY set, peerAttest chooses the mode as an owner's message, using the real tmux host.
// go test -tags peerlive -run TestLivePeerSend ./internal/node/ (docs/lessons/windows.md, "Claude Code's peer inbox").
func TestLivePeerSend(t *testing.T) {
	pid, _ := strconv.Atoi(os.Getenv("PERSONA_LIVE_PID"))
	dir := os.Getenv("PERSONA_LIVE_DIR")
	if pid <= 0 || dir == "" {
		t.Skip("PERSONA_LIVE_PID / PERSONA_LIVE_DIR not set")
	}
	b, err := os.ReadFile(filepath.Join(dir, "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	c := claudeProc{pid: pid, dir: filepath.Join(dir, "sessions")}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if !sameProcess(pid, claudeFileStart(c.ProcStart)) {
		t.Fatalf("pid %d is not the claude that wrote the file", pid)
	}
	p, ok := peerOf(c)
	if !ok || !peerReachable(p) {
		t.Fatalf("no usable inbox: %+v", c)
	}
	find := func() string {
		matches, _ := filepath.Glob(filepath.Join(dir, "projects", "*", c.SessionID+".jsonl"))
		if len(matches) == 0 {
			return ""
		}
		return matches[0]
	}
	var offset int64
	if st, err := os.Stat(find()); err == nil {
		offset = st.Size()
	}
	text := os.Getenv("PERSONA_LIVE_TEXT")
	if text == "" {
		text = "reply with OK"
	}
	p.fromMode = os.Getenv("PERSONA_LIVE_MODE")
	if os.Getenv("PERSONA_LIVE_CLASSIFY") != "" {
		n := &Node{hosts: host.NewRegistry(newTmuxHost()), bgScans: map[string]*bgScan{}, metas: map[string]quickMeta{}}
		ownerCtx, _ := withOwnerDelivery(context.Background())
		p.fromMode = n.peerAttest(ownerCtx, c, c.SessionID)
	}
	start := time.Now()
	err = peerSend(context.Background(), p, c.SessionID, text)
	t.Logf("peerSend from-mode %q: %v (%s)", p.fromMode, err, time.Since(start))
	taken := peerTaken(context.Background(), find, offset, text, 30*time.Second)
	t.Logf("taken: %v (%s) transcript found: %v", taken, time.Since(start), find() != "")
	if !taken {
		t.Logf("held notice in the transcript (peer_held): %v", peerHeldSeen(find(), offset))
	}
}
