package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// scopedAssistantDirs makes a dedicated manager's state root under PERSONA_HOME: its own assistant directory (the
// node's PERSONA_NODE_SESSION_DIR), the built-in assistant's directory beside it, which the scope leaves out, and its
// health space.
func scopedAssistantDirs(t *testing.T, n *Node, root string) (own, builtin, health string) {
	t.Helper()
	own = filepath.Join(root, identity.AssistantDirName("m-abcdef"))
	builtin = identity.AssistantDir(n.home)
	health = filepath.Join(root, identity.HealthDirName("m-abcdef"))
	for _, d := range []string{own, builtin, health} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	n.sessionScope = &nodeSessionScope{dir: own, claude: filepath.Join(root, "claude"), codex: filepath.Join(root, "codex")}
	return own, builtin, health
}

// On a node scoped to its manager's assistant directory, a conversation resumed without a folder runs in that
// directory, not in the built-in assistant's directory of the same root, which the node never lists. A record of the
// built-in one (what the fallback stored before it knew the scope) goes back to it too: resumed there, the main
// conversation was in no list and every channel message to it ended as "gone" after three minutes.
func TestStoredCwdOnAScopedNode(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PERSONA_HOME", root)
	n := codexTestNode(t)
	own, builtin, health := scopedAssistantDirs(t, n, root)
	if builtin != filepath.Join(root, "assistant") {
		t.Fatalf("the built-in assistant directory is %q", builtin)
	}
	gone := filepath.Join(t.TempDir(), ".previous")
	cases := []struct{ name, stored, want string }{
		{"none stored", "", own},
		{"its own directory", own, own},
		{"the built-in directory stored by the old fallback", builtin, own},
		{"gone, with the built-in directory's name", filepath.Join(gone, "assistant"), own},
		{"its own directory moved", filepath.Join(gone, identity.AssistantDirName("m-abcdef")), own},
		{"its health space", health, health},
	}
	for _, c := range cases {
		if got := n.storedCwd(c.stored); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}

	// A node without a scope keeps its data directory's assistant directory, as before.
	n.sessionScope = nil
	for _, stored := range []string{"", builtin} {
		if got := n.storedCwd(stored); got != builtin {
			t.Errorf("unscoped, stored %q: %q, want %q", stored, got, builtin)
		}
	}
}

// At rest, a scoped node lists the conversation whose record names the built-in directory where its transcript says
// it started, inside the scope: it was left out of the list before, so session.get answered that it was not there.
func TestScopedNodeListsAConversationRecordedInTheBuiltinDirectory(t *testing.T) {
	t.Setenv("PERSONA_HOME", "")
	n, _, home := herdrNode(t)
	root := identity.DataDir(home)
	own, builtin, _ := scopedAssistantDirs(t, n, root)
	n.sessionScope.claude = filepath.Join(home, ".claude")
	const id = "dddddddd-0000-0000-0000-0000000000a1"
	// Started in its own directory, resumed later in the built-in one (the transcript's lines say so, as the record).
	writeDrift(t, home, id, own, builtin)
	n.state.put(id, sessionState{Launcher: "claude", Cwd: builtin})
	listed := func() (proto.Session, bool) {
		t.Helper()
		offlineCache.Lock()
		offlineCache.at = time.Time{}
		offlineCache.Unlock()
		for _, s := range n.sessionList(context.Background(), false) {
			if s.SessionID == id {
				return s, true
			}
		}
		return proto.Session{}, false
	}
	s, ok := listed()
	if !ok {
		t.Fatal("the conversation is not listed")
	}
	if s.Cwd != own {
		t.Errorf("listed in %q, want its own directory %q", s.Cwd, own)
	}

	// A record of its own directory is kept as it is.
	n.state.put(id, sessionState{Launcher: "claude", Cwd: own})
	if s, ok := listed(); !ok || s.Cwd != own {
		t.Errorf("recorded in its own directory: listed %v in %q", ok, s.Cwd)
	}
}

// A message to the main conversation of a scoped node whose record names the built-in directory (a channel's message
// names no folder) starts its process in the node's own assistant directory and corrects the record, so the next list
// has it again.
func TestScopedAssistantResumesInItsOwnDirectory(t *testing.T) {
	fakeClaudeHome(t, "pwd -P > \"$HOME/cwd.txt\"\nread line\necho '{\"type\":\"system\",\"subtype\":\"init\"}'\nsleep 30\n")
	fake := os.Getenv("HOME")
	root := t.TempDir()
	t.Setenv("PERSONA_HOME", root)
	n := codexTestNode(t)
	withFakeHub(t, n)
	own, builtin, _ := scopedAssistantDirs(t, n, root)
	id := newUUID()
	n.state.put(id, sessionState{Launcher: "claude", Cwd: builtin})

	if _, err := n.assistantSend(context.Background(), id, "", "claude", "hello", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	defer n.stopAssistant(id)
	var ran string
	waitWithin(t, 10*time.Second, "the conversation's process to start", func() bool {
		b, err := os.ReadFile(filepath.Join(fake, "cwd.txt"))
		ran = strings.TrimSpace(string(b))
		return err == nil && ran != ""
	})
	want, _ := filepath.EvalSymlinks(own)
	if ran != want {
		t.Fatalf("the conversation ran in %q, want %q", ran, want)
	}
	if st, _ := n.state.get(id); st.Cwd != own {
		t.Fatalf("stored folder %q, want it corrected to %q", st.Cwd, own)
	}
}
