package node

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// The user's /compact to a Codex on its app-server (#10188, codex_remote_compact.go): thread/compact/start while the
// thread rests, a refusal with nothing sent while a turn runs, and anything more than the bare command a message as
// before. A Codex reached through its terminal still has it typed. Against the synthetic app-server of
// codex_remote_test.go.

// compactRemote is a node joined to nothing yet, with one thread on a connected synthetic app-server, and that
// thread's session id.
func compactRemote(t *testing.T) (*Node, *remoteFixture, string) {
	t.Helper()
	n := remoteTestNode(t)
	home := t.TempDir()
	f, _ := connectRemoteFixture(t, n, home, "one", remoteRaw)
	return n, f, n.registerCodex(home, remoteRaw, "")
}

// nothingSent: err refused the message before anything reached Codex, with code.
func nothingSent(t *testing.T, err error, code string) {
	t.Helper()
	if refusalCode(err) != code {
		t.Fatalf("want a %s refusal, got %v", code, err)
	}
	var data struct {
		Delivered string `json:"delivered"`
	}
	_ = json.Unmarshal(errorData(err), &data)
	if data.Delivered != deliveredNone {
		t.Fatalf("a refusal that sent nothing says delivered %q", data.Delivered)
	}
}

func TestCodexRemoteCompactWhileIdle(t *testing.T) {
	for _, text := range []string{"/compact", "  /compact\n"} {
		t.Run(text, func(t *testing.T) {
			n, f, id := compactRemote(t)
			if err := n.send(context.Background(), id, text, nil, sendOpts{}); err != nil {
				t.Fatal(err)
			}
			compact := f.sent("thread/compact/start")
			if len(compact) != 1 || compact[0]["threadId"] != remoteRaw || len(compact[0]) != 1 {
				t.Fatalf("thread/compact/start %v", compact)
			}
			if starts, steers := f.count("turn/start"), f.count("turn/steer"); starts+steers != 0 {
				t.Fatalf("the command went to the model too: %d turn/start, %d turn/steer", starts, steers)
			}
			if f.count("thread/resume") != 1 { // joined, so the compaction's turn and items reach the node
				t.Fatalf("thread/resume calls = %d, want 1", f.count("thread/resume"))
			}
		})
	}
}

func TestCodexRemoteCompactRefusedWhileATurnRuns(t *testing.T) {
	t.Run("a turn the node started", func(t *testing.T) {
		n, f, id := compactRemote(t)
		if err := n.send(context.Background(), id, "合成请求", nil, sendOpts{}); err != nil {
			t.Fatal(err)
		}
		nothingSent(t, n.send(context.Background(), id, "/compact", nil, sendOpts{}), "codex_compact_busy")
		if f.count("thread/compact/start") != 0 || f.count("turn/steer") != 0 || f.count("turn/start") != 1 {
			t.Fatalf("compact %d, steer %d, start %d", f.count("thread/compact/start"), f.count("turn/steer"), f.count("turn/start"))
		}
	})
	// The node last saw the thread rest; a turn began since (typed at the computer): the app-server's word decides.
	t.Run("a turn begun since the node last looked", func(t *testing.T) {
		n, f, id := compactRemote(t)
		f.mu.Lock()
		f.active = true
		f.mu.Unlock()
		nothingSent(t, n.send(context.Background(), id, "/compact", nil, sendOpts{}), "codex_compact_busy")
		if got := f.count("thread/compact/start") + f.count("turn/steer") + f.count("turn/start"); got != 0 {
			t.Fatalf("%d calls reached the model, want none", got)
		}
	})
	t.Run("the thread could not be read", func(t *testing.T) {
		n, f, id := compactRemote(t)
		f.mu.Lock()
		f.reject = "thread/read"
		f.mu.Unlock()
		nothingSent(t, n.send(context.Background(), id, "/compact", nil, sendOpts{}), "codex_read_only")
		if got := f.count("thread/compact/start") + f.count("turn/steer") + f.count("turn/start"); got != 0 {
			t.Fatalf("%d calls reached the model, want none", got)
		}
	})
}

// An app-server without thread/compact/start refuses it: the refusal is passed on, and the command is not sent to the
// model instead.
func TestCodexRemoteCompactUnsupported(t *testing.T) {
	n, f, id := compactRemote(t)
	f.mu.Lock()
	f.reject, f.rejectCode = "thread/compact/start", -32601
	f.mu.Unlock()
	nothingSent(t, n.send(context.Background(), id, "/compact", nil, sendOpts{}), "codex_refused")
	if f.count("thread/compact/start") != 1 || f.count("turn/start") != 0 {
		t.Fatalf("compact %d, start %d", f.count("thread/compact/start"), f.count("turn/start"))
	}
}

// Only the bare command is Codex's: with a photo, with words after it, or as part of a word, it is a message.
func TestCodexRemoteCompactOnlyAlone(t *testing.T) {
	photo := []proto.Image{{Mime: "image/png", Data: "iVBORw0KGgo="}}
	for _, c := range []struct {
		name, text string
		images     []proto.Image
	}{
		{"with a photo", "/compact", photo},
		{"with words after it", "/compact now", nil},
		{"part of a word", "/compacting", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			n, f, id := compactRemote(t)
			if err := n.send(context.Background(), id, c.text, c.images, sendOpts{}); err != nil {
				t.Fatal(err)
			}
			starts := f.sent("turn/start")
			if len(starts) != 1 || inputText(starts[0]) != c.text || f.count("thread/compact/start") != 0 {
				t.Fatalf("turn/start %v, compact %d", starts, f.count("thread/compact/start"))
			}
			if in, _ := starts[0]["input"].([]any); len(in) != 1+len(c.images) {
				t.Fatalf("input %v", in)
			}
		})
	}
}

// A Codex pane joined on its shared app-server (the route a 0.169 daemon puts every message on) compacts the same way.
func TestCodexDaemonPaneCompacts(t *testing.T) {
	n, f, _, _ := daemonPane(t)
	n.joinCodexPanes(context.Background())
	waitFor(t, "joined", func() bool { return n.codexJoined(remoteRaw) })
	if err := n.send(context.Background(), remoteRaw, "/compact", nil, sendOpts{}); err != nil {
		t.Fatal(err)
	}
	if f.count("thread/compact/start") != 1 || f.count("turn/start") != 0 {
		t.Fatalf("compact %d, start %d", f.count("thread/compact/start"), f.count("turn/start"))
	}
}

// A Codex reached through its terminal gets /compact typed, as before: its TUI runs the command.
func TestCodexTerminalCompactStillTyped(t *testing.T) {
	n, f, _ := codexNode(t, proto.StatusIdle)
	f.setScreen(cxPane, cxEmpty)
	if _, err := request(t, n, "session.send", map[string]any{"session_id": cxID, "text": "/compact"}); err != nil {
		t.Fatal(err)
	}
	var typed []string
	for _, c := range f.calls() {
		if c.Method == "agent.prompt" {
			typed = append(typed, str(c.Params["text"]))
		}
	}
	if len(typed) != 1 || typed[0] != "/compact" {
		t.Fatalf("typed %q, want the command as it was sent", typed)
	}
}
