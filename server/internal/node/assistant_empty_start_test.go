package node

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

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

type emptyStartInput struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

func emptyStartUserInputs(t *testing.T, stdin string) []emptyStartInput {
	t.Helper()
	var inputs []emptyStartInput
	for line := range strings.SplitSeq(strings.TrimSpace(stdin), "\n") {
		if line == "" {
			continue
		}
		var input emptyStartInput
		if err := json.Unmarshal([]byte(line), &input); err != nil {
			t.Fatal(err)
		}
		if input.Type == "user" {
			inputs = append(inputs, input)
		}
	}
	return inputs
}

// Android allocates a conversation before draining its outbox. Allocation must not
// consume a snapshot, acknowledge view errors, or send a context-only model turn.
func TestAssistantEmptyStartWaitsForFirstMessage(t *testing.T) {
	for _, warm := range []bool{false, true} {
		for _, text := range []string{"", " \t\n"} {
			name := "cold"
			if warm {
				name = "warm"
			}
			if text != "" {
				name += "/whitespace"
			}
			t.Run(name, func(t *testing.T) {
				f := cliTest(t)
				cwd := t.TempDir()
				var snapshots atomic.Int32
				f.fh.mu.Lock()
				f.fh.context = viewErrorsAnswer
				f.fh.onContext = func(context.Context) { snapshots.Add(1) }
				f.fh.mu.Unlock()
				if warm {
					if err := f.n.assistantWarm(cwd, "claude", cliExec()); err != nil {
						t.Fatal(err)
					}
				}
				id, err := f.n.assistantSend(context.Background(), "", cwd, "claude", text, nil, "default", "", cliExec())
				if err != nil || id == "" {
					t.Fatalf("start: id=%q, error=%v", id, err)
				}
				// Acknowledging a later control proves the CLI consumed all earlier stdin.
				if err := f.n.setModes(context.Background(), id, "plan", ""); err != nil {
					t.Fatal(err)
				}
				if inputs := emptyStartUserInputs(t, f.file("stdin.jsonl")); len(inputs) != 0 {
					t.Fatalf("empty creation sent %d model inputs: %+v", len(inputs), inputs)
				}
				if snapshots.Load() != 0 || f.fh.ackCount() != 0 {
					t.Fatal("empty creation consumed context or acknowledged undelivered view errors")
				}
				if !f.turnOver(id) {
					t.Fatal("empty creation left the process busy")
				}
				if s := f.session(t, id); s.Status != proto.StatusIdle {
					t.Fatalf("empty creation was listed as %q", s.Status)
				}
				if st, ok := f.n.state.get(id); !ok || !st.AwaitingFirstMessage {
					t.Fatal("empty creation lost its pending first message")
				}
				const message = "The first real message"
				got, err := f.n.assistantSend(context.Background(), id, cwd, "claude", message, nil, "plan", "", cliExec())
				if err != nil || got != id {
					t.Fatalf("first message: id=%q, error=%v", got, err)
				}
				f.idle(t, id)
				inputs := emptyStartUserInputs(t, f.file("stdin.jsonl"))
				if len(inputs) != 1 {
					t.Fatalf("first message produced %d model inputs", len(inputs))
				}
				content := inputs[0].Message.Content
				if len(content) != 2 || !strings.Contains(content[0].Text, "<persona-context>") ||
					!strings.Contains(content[0].Text, "<persona-view-errors>") || content[1].Text != message {
					t.Fatalf("first message lost context or words: %+v", content)
				}
				cliWait(t, "the first message's context receipt", func() bool { return f.fh.ackCount() == 1 })
				if snapshots.Load() != 1 {
					t.Fatalf("read %d snapshots for one message", snapshots.Load())
				}
				if st, _ := f.n.state.get(id); st.AwaitingFirstMessage {
					t.Fatal("delivered message left the conversation waiting for its first input")
				}
			})
		}
	}
}

func TestAssistantImageOnlyStartStillSends(t *testing.T) {
	f := cliTest(t)
	images := []proto.Image{{Mime: "image/png", Data: "aW1hZ2U="}}
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "", images, "default", "", cliExec())
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	inputs := emptyStartUserInputs(t, f.file("stdin.jsonl"))
	if len(inputs) != 1 || len(inputs[0].Message.Content) != 2 || inputs[0].Message.Content[1].Type != "image" {
		t.Fatalf("image-only input was dropped or duplicated: %+v", inputs)
	}
}

func TestAssistantEmptyStartSurvivesNodeRestart(t *testing.T) {
	f := cliTest(t)
	cwd := filepath.Join(f.n.home, ".persona", "assistant")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := nodeCall(t, f.n, "assistant.send", map[string]any{
		"cwd": cwd, "launcher": "claude", "text": "", "permission_mode": "default", "model_fingerprint": "fp",
	})
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.SessionID == "" {
		t.Fatalf("empty start: %s, error=%v", raw, err)
	}
	id := created.SessionID
	previous, _ := f.n.state.get(id)
	wire, err := json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	var stored db.NodeSession
	if err := json.Unmarshal(wire, &stored); err != nil {
		t.Fatal(err)
	}
	wire, err = json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var restored sessionState
	if err := json.Unmarshal(wire, &restored); err != nil {
		t.Fatal(err)
	}
	if left := stopAndWait([]*assistantProc{f.n.takeAssistant(id)}); left != 0 {
		t.Fatal("the first node's empty process did not stop")
	}
	next := codexTestNode(t)
	next.home, next.dir = f.n.home, t.TempDir()
	withFakeHub(t, next)
	next.state.merge(map[string]sessionState{id: restored})
	t.Cleanup(func() {
		if p := next.takeAssistant(id); p != nil {
			stopAndWait([]*assistantProc{p})
		}
	})
	if !next.isAssistantSession(id) || next.transcriptPath(id) != "" {
		t.Fatal("the empty session must be recognized from its record before any transcript exists")
	}
	const message = "First message after node restart"
	if _, err := nodeCall(t, next, "session.send", map[string]any{"session_id": id, "text": message}); err != nil {
		t.Fatal(err)
	}
	f.n = next
	f.idle(t, id)
	inputs := emptyStartUserInputs(t, f.file("stdin.jsonl"))
	if len(inputs) != 1 || len(inputs[0].Message.Content) == 0 || inputs[0].Message.Content[len(inputs[0].Message.Content)-1].Text != message {
		t.Fatalf("restart did not preserve exactly one first message: %+v", inputs)
	}
}

func TestAssistantEmptyCodexStartWaitsForFirstMessage(t *testing.T) {
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	const launcher = "cx_empty_start"
	probeCache.Lock()
	probeCache.at[launcher], probeCache.got[launcher] = time.Now(), proto.LauncherDetail{Agent: "codex"}
	probeCache.Unlock()
	t.Cleanup(func() {
		probeCache.Lock()
		delete(probeCache.at, launcher)
		delete(probeCache.got, launcher)
		probeCache.Unlock()
	})
	cwd := t.TempDir()
	p, c, f := newFakeCodex(t, n, cwd)
	p.launcher = launcher
	p.fingerprint = n.executionFingerprint(launcher, modelExecution{})
	f.results["thread/start"] = map[string]any{"thread": map[string]any{"id": testThread}}
	f.results["turn/start"] = map[string]any{"turn": map[string]any{"id": "first-turn", "status": "inProgress"}}
	if err := c.start(context.Background(), "", cwd, false); err != nil {
		t.Fatal(err)
	}
	n.spare = p
	var snapshots atomic.Int32
	fh.mu.Lock()
	fh.onContext = func(context.Context) { snapshots.Add(1) }
	fh.mu.Unlock()
	id, err := n.assistantSend(context.Background(), "", cwd, launcher, "", nil, "", "")
	if err != nil || id != testThread {
		t.Fatalf("start: id=%q, error=%v", id, err)
	}
	if turns := f.sent("turn/start"); len(turns) != 0 {
		t.Fatalf("empty creation started %d Codex turns: %s", len(turns), turns[0].Params)
	}
	if snapshots.Load() != 0 {
		t.Fatal("empty creation read a snapshot")
	}
	if st, _ := n.state.get(id); !st.AwaitingFirstMessage {
		t.Fatal("empty Codex creation lost its pending first message")
	}
	const message = "The first real message"
	if _, err := n.assistantSend(context.Background(), id, cwd, launcher, message, nil, "", ""); err != nil {
		t.Fatal(err)
	}
	turns := f.sent("turn/start")
	if len(turns) != 1 || len(f.sent("turn/steer")) != 0 || snapshots.Load() != 1 {
		t.Fatal("the first real message did not start exactly one turn")
	}
	var turn struct {
		Input []struct{ Text string } `json:"input"`
	}
	if err := json.Unmarshal(turns[0].Params, &turn); err != nil {
		t.Fatal(err)
	}
	if len(turn.Input) != 2 || !strings.Contains(turn.Input[0].Text, "<persona-context>") || turn.Input[1].Text != message {
		t.Fatalf("first Codex turn lost context or words: %+v", turn.Input)
	}
	if st, _ := n.state.get(id); st.AwaitingFirstMessage {
		t.Fatal("delivered Codex message left the conversation waiting for its first input")
	}
}

func TestAssistantEmptyRuntimeStartWaitsForFirstMessage(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "same node"
		if restart {
			name = "after node restart"
		}
		t.Run(name, func(t *testing.T) {
			n, hub := runtimeNode(t)
			assistantHub(hub)
			host := dialRuntime(t, n, assistantRuntimeHello())
			const id = "session-empty-start"
			cwd := filepath.Join(n.home, ".persona", "assistant")
			const remainder = "The generic runtime must retain this split instruction section.\n"
			rules := filepath.Join(cwd, ".claude", "rules")
			if err := os.MkdirAll(rules, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(rules, testInstructionRuleName(2)), []byte(testInstructionRuleMarker+remainder), 0o600); err != nil {
				t.Fatal(err)
			}
			host.mu.Lock()
			host.answer = func(method string, _ json.RawMessage) (any, error) {
				if method == "session.start" {
					return map[string]any{"session": proto.Session{SessionID: id, Cwd: cwd, Status: proto.StatusIdle}}, nil
				}
				return map[string]any{"ok": true}, nil
			}
			host.mu.Unlock()
			if _, err := nodeCall(t, n, "assistant.send", map[string]any{"launcher": "dsh", "cwd": cwd, "text": " \t\n"}); err != nil {
				t.Fatal(err)
			}
			if len(host.got("session.start")) != 1 || len(host.got("session.send")) != 0 {
				t.Fatal("empty runtime creation sent a model message")
			}
			if st, _ := n.state.get(id); !st.AwaitingFirstMessage {
				t.Fatal("empty runtime creation lost its pending instructions")
			}
			if restart {
				// A new node loads the serialized record from the hub, with no in-memory context.
				previous, _ := n.state.get(id)
				wire, err := json.Marshal(previous)
				if err != nil {
					t.Fatal(err)
				}
				var restored sessionState
				var stored db.NodeSession
				if err := json.Unmarshal(wire, &stored); err != nil {
					t.Fatal(err)
				}
				wire, err = json.Marshal(stored)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(wire, &restored); err != nil {
					t.Fatal(err)
				}
				next, nextHub := runtimeNode(t)
				next.home = n.home
				next.state.merge(map[string]sessionState{id: restored})
				assistantHub(nextHub)
				n, host = next, dialRuntime(t, next, assistantRuntimeHello())
			}
			var refuse atomic.Bool
			refuse.Store(true)
			host.mu.Lock()
			host.answer = func(method string, _ json.RawMessage) (any, error) {
				if method == "session.send" && refuse.Swap(false) {
					return nil, errors.New("fixture refused input")
				}
				return map[string]any{"ok": true}, nil
			}
			host.mu.Unlock()
			message := map[string]any{"session_id": id, "launcher": "dsh", "text": "The first runtime message"}
			if _, err := nodeCall(t, n, "assistant.send", message); err == nil {
				t.Fatal("the runtime's refusal was ignored")
			}
			if st, _ := n.state.get(id); !st.AwaitingFirstMessage {
				t.Fatal("a refused first message consumed the runtime instructions")
			}
			if _, err := nodeCall(t, n, "assistant.send", message); err != nil {
				t.Fatal(err)
			}
			sends := host.got("session.send")
			if len(sends) != 2 {
				t.Fatalf("first runtime message: %d send attempts", len(sends))
			}
			var sent struct{ Text, Context string }
			if err := json.Unmarshal(sends[1], &sent); err != nil {
				t.Fatal(err)
			}
			if sent.Text != message["text"] || !strings.Contains(sent.Context, "MEMORY.md") || !strings.Contains(sent.Context, "<persona-context>") ||
				!strings.Contains(sent.Context, remainder) {
				t.Fatalf("first runtime message lost instructions or words: %+v", sent)
			}
			if st, _ := n.state.get(id); st.AwaitingFirstMessage {
				t.Fatal("delivered runtime message kept its pending instructions")
			}
			message["text"] = "The next message"
			if _, err := nodeCall(t, n, "assistant.send", message); err != nil {
				t.Fatal(err)
			}
			sends = host.got("session.send")
			if len(sends) != 3 {
				t.Fatalf("runtime send count = %d, want 3", len(sends))
			}
			var next struct{ Context string }
			if err := json.Unmarshal(sends[2], &next); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(sends[2]), "MEMORY.md") || !strings.Contains(next.Context, remainder) {
				t.Fatal("runtime initialization instructions were repeated, or a later turn lost split instructions")
			}
		})
	}
}
