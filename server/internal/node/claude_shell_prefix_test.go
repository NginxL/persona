package node

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// A Claude Code input line beginning with ! is a local shell command, not a
// model prompt. Messages delivered through Persona must keep their meaning
// without activating that input mode, both for a task's first message and for
// a later agent_send/session.send delivery.
func TestClaudeTerminalMessagesDoNotRunBangCommands(t *testing.T) {
	const bang = "!echo shell-prefix-fixture"
	for _, followUp := range []bool{false, true} {
		name := "first task message"
		if followUp {
			name = "follow-up message"
		}
		t.Run(name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			cwd := mkdir(t, filepath.Join(home, "proj"))
			startPane(t, f, screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi"))
			promptLands(t, f, home, cwd)

			startText := bang
			if followUp {
				startText = "initial task"
			}
			if _, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "workspace_id": "w1", "prompt": startText}); err != nil {
				t.Fatal(err)
			}
			n.firstWait.Wait()
			id := startedID(f)

			if followUp {
				f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(id, proto.StatusIdle, "w1:p2", "w1:t2", "w1", cwd, "")}, nil))
				f.reset()
				f.after(func(c herdrCall) {
					if c.Method == "agent.prompt" && (str(c.Params["text"]) == bang || str(c.Params["text"]) == " "+bang) {
						writeUserLine(t, home, cwd, id, str(c.Params["text"]))
					}
				})
				if _, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": bang}); err != nil {
					t.Fatal(err)
				}
			}

			want := call("agent.prompt", map[string]any{"target": "w1:p2", "text": " " + bang})
			if got := f.trace("agent.prompt"); len(got) != 1 || got[0] != want {
				t.Fatalf("terminal prompt = %v, want only %s", got, want)
			}
		})
	}
}

// An owner delivery proves both halves of the send: the terminal receives the
// escaped text, and the transcript watcher was started with that same text.
// Escaping only inside the final prompt call would leave localInput(raw) true,
// disable the watcher, and make this confirmed delivery fail.
func TestClaudeTerminalOwnerBangMessage(t *testing.T) {
	const bang = "!echo shell-prefix-fixture"
	plain, ansi := emptyOwnerTmuxScreens(t)
	env := newOwnerTmuxTestNode(t, "idle", plain, ansi, false, false)
	res, err := request(t, env.n, "session.send", map[string]any{
		"session_id": sidA, "text": bang, "client_msg_id": "claude-shell-prefix-owner", "owner_input": true,
	})
	if err != nil {
		t.Fatalf("confirmed owner delivery: %v", err)
	}
	if res["delivered_as"] != deliveredUserInput {
		t.Fatalf("delivery = %v, want %s", res["delivered_as"], deliveredUserInput)
	}
	got := env.pane.submitted()
	if len(got) != 1 || got[0].text != " "+bang {
		t.Fatalf("pane submissions = %+v, want one escaped model prompt", got)
	}
	assertOwnerTmuxTranscript(t, env.transcript, " "+bang)
}

// The generic terminal prompt is shared with Codex, TRAE and other agents;
// only Claude's queue/retry wrapper applies Claude Code's input-mode escaping.
func TestClaudeTerminalPromptScope(t *testing.T) {
	pane := &ownerTmuxPane{}
	n := &Node{hosts: host.NewRegistry(pane)}
	const raw = "!status"
	if err := n.prompt(context.Background(), "tmux:orc:@11.%11", raw); err != nil {
		t.Fatal(err)
	}
	if err := n.promptClaude(context.Background(), "tmux:orc:@11.%11", raw); err != nil {
		t.Fatal(err)
	}
	got := pane.submitted()
	if len(got) != 2 || got[0].text != raw || got[1].text != " "+raw {
		t.Fatalf("generic then Claude prompts = %+v, want %q then %q", got, raw, " "+raw)
	}
}

func TestClaudeTerminalMessageText(t *testing.T) {
	const raw = "!echo shell-prefix-fixture"
	if got := claudeMessageText(raw); got != " "+raw {
		t.Fatalf("escaped text = %q", got)
	}
	if got := claudeMessageText(" " + raw); got != " "+raw {
		t.Fatalf("escaping is not idempotent: %q", got)
	}
	if localInput(" " + raw) {
		t.Fatal("an escaped shell prefix is still treated as local input")
	}
	if !localInput(raw) || !localInput(" /compact") {
		t.Fatal("existing local-command recognition changed")
	}
}
