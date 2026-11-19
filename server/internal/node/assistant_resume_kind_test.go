package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const (
	claudeKindLauncher = "ccx_kind"
	codexKindLauncher  = "cx_kind"
	claudeConversation = "5d0c1a52-3f0e-4b8a-9a51-6f2b7c9e0d14"
)

// probedLaunchers answers the launcher probe for these names (agent by name) for the test's length.
func probedLaunchers(t *testing.T, agents map[string]string) {
	t.Helper()
	probeCache.Lock()
	for name, agent := range agents {
		probeCache.at[name], probeCache.got[name] = time.Now(), proto.LauncherDetail{Agent: agent}
	}
	probeCache.Unlock()
	t.Cleanup(func() {
		probeCache.Lock()
		for name := range agents {
			delete(probeCache.at, name)
			delete(probeCache.got, name)
		}
		probeCache.Unlock()
	})
}

// kindTestNode is a node with a Codex thread (testThread) and a Claude transcript (claudeConversation) on disk.
func kindTestNode(t *testing.T) *Node {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	n := codexTestNode(t)
	probedLaunchers(t, map[string]string{claudeKindLauncher: "claude", codexKindLauncher: "codex", "codex": "codex"})
	rollouts := filepath.Join(n.home, ".codex", "sessions", "2026", "11", "05")
	projects := filepath.Join(n.home, ".claude", "projects", "-assistant")
	for _, d := range []string{rollouts, projects} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	rollout := filepath.Join(rollouts, "rollout-2026-11-05T19-12-28-"+testThread+".jsonl")
	if err := os.WriteFile(rollout, []byte(`{"type":"session_meta","payload":{"id":"`+testThread+`"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projects, claudeConversation+".jsonl"), []byte(`{"type":"user"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return n
}

// A conversation resumes on the kind of agent it was started on, whatever kind the launcher asked for runs: the
// main conversation follows "Model Settings › Main Chat" on every message, and a Codex main conversation with the setting moved
// to a Claude launcher failed every message on `claude --resume <thread>` (2026-11-05).
func TestAssistantResumeLauncherKeepsTheConversationsAgent(t *testing.T) {
	n := kindTestNode(t)
	for _, c := range []struct {
		name, id, asked, own string
		want                 string
		switched             bool
	}{
		{"codex thread, claude launcher asked: its own codex launcher", testThread, claudeKindLauncher, codexKindLauncher, codexKindLauncher, true},
		{"codex thread whose own was overwritten by a failed start: plain codex", testThread, claudeKindLauncher, claudeKindLauncher, "codex", true},
		{"codex thread, nothing remembered: plain codex", testThread, "claude", "", "codex", true},
		{"codex thread, another codex launcher: as asked", testThread, "codex", codexKindLauncher, "codex", false},
		{"claude transcript, codex launcher asked: its own claude launcher", claudeConversation, codexKindLauncher, claudeKindLauncher, claudeKindLauncher, true},
		{"claude transcript, nothing remembered: plain claude", claudeConversation, codexKindLauncher, "", "claude", true},
		{"claude transcript, another claude launcher: as asked", claudeConversation, claudeKindLauncher, "claude", claudeKindLauncher, false},
		{"no transcript yet: as asked", "8f7e6d5c-4b3a-4291-8f7e-6d5c4b3a2910", codexKindLauncher, claudeKindLauncher, codexKindLauncher, false},
		{"no launcher asked: its own as before", testThread, "", claudeKindLauncher, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, switched := n.assistantResumeLauncher(context.Background(), c.id, c.asked, sessionState{Launcher: c.own})
			if got != c.want || switched != c.switched {
				t.Fatalf("assistantResumeLauncher = %q, %v; want %q, %v", got, switched, c.want, c.switched)
			}
		})
	}
}

// Sent with a Codex launcher, a Claude conversation is resumed by Claude Code (`--resume <id>`) without the model
// picked for the other launcher, instead of a Codex thread/resume of an id Codex never had.
func TestAssistantSendResumesAClaudeConversationOnClaude(t *testing.T) {
	argv := filepath.Join(t.TempDir(), "argv")
	fakeClaudeHome(t, "printf '%s\\n' \"$@\" > "+argv+"\nread line\necho '{\"type\":\"system\",\"subtype\":\"init\"}'\nsleep 30\n")
	n := kindTestNode(t)
	withFakeHub(t, n)
	n.state.put(claudeConversation, sessionState{Launcher: "claude", Cwd: t.TempDir()})
	id, err := n.assistantSend(context.Background(), claudeConversation, "", codexKindLauncher, "hello", nil, "", "",
		modelExecution{Model: "gpt-model", Fingerprint: "codex-binding"})
	if err != nil {
		t.Fatal(err)
	}
	defer n.stopAssistant(id)
	raw, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	args := string(raw)
	if !strings.Contains(args, "--resume\n"+claudeConversation+"\n") {
		t.Fatalf("not resumed by claude: %q", args)
	}
	if strings.Contains(args, "gpt-model") {
		t.Fatalf("started with the other launcher's model: %q", args)
	}
	if st, _ := n.state.get(claudeConversation); st.Launcher != "claude" {
		t.Fatalf("its launcher is now %q", st.Launcher)
	}
}

// Launcher-kind normalization changes execution, not the message's audience: a public mention keeps its snapshot
// suppression when a Claude transcript is asked to resume through Codex (#11130).
func TestAssistantPublicSendSurvivesLauncherKindNormalization(t *testing.T) {
	fakeClaudeHome(t, "read line\necho '{\"type\":\"system\",\"subtype\":\"init\"}'\nsleep 30\n")
	n := kindTestNode(t)
	fh := withFakeHub(t, n)
	calls := 0
	fh.mu.Lock()
	fh.onContext = func(context.Context) { calls++ }
	fh.mu.Unlock()
	n.state.put(claudeConversation, sessionState{Launcher: "claude", Cwd: t.TempDir()})
	id, err := n.assistantSend(context.Background(), claudeConversation, "", codexKindLauncher, "public mention", nil, "", "", modelExecution{Public: true})
	if err != nil {
		t.Fatal(err)
	}
	defer n.stopAssistant(id)
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if calls != 0 {
		t.Fatalf("launcher-kind normalization requested %d owner snapshots for a public turn", calls)
	}
}

// Sent with a Claude launcher, a Codex thread never goes to claude (it answered "No conversation found" and the
// message was lost every time).
func TestAssistantSendNeverResumesACodexThreadOnClaude(t *testing.T) {
	ran := filepath.Join(t.TempDir(), "ran")
	fakeClaudeHome(t, "touch "+ran+"\nread line\necho '{\"type\":\"system\",\"subtype\":\"init\"}'\nsleep 30\n")
	n := kindTestNode(t)
	withFakeHub(t, n)
	n.state.put(testThread, sessionState{Launcher: "claude", Cwd: t.TempDir()})
	// There is no codex on this PATH: the start fails, but as Codex's.
	if id, err := n.assistantSend(context.Background(), testThread, "", "claude", "hello", nil, "", ""); err == nil {
		n.stopAssistant(id)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("a Codex thread was resumed by claude")
	}
}
