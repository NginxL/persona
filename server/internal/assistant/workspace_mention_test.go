package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// workspaceTag is the block the web's @ menu writes for a workspace (web/src/lib/mentions.ts workspaceContext).
const workspaceTag = `<persona-context kind="workspace" title="weather" url="" machine="box" cwd="/code/weather" branch="main">
Workspace: weather
Computer: box
Folder: /code/weather
</persona-context>`

func mentionEnv(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub().withMachine()
	now := time.Now().UTC()
	f.setSessions("box",
		proto.Session{SessionID: "old", Title: "Old work", Cwd: "/code/weather", Agent: "claude", Status: proto.StatusIdle, UpdatedAt: now.Add(-time.Hour)},
		proto.Session{SessionID: "ios", Title: "Fix login", Cwd: "/code/weather/ios", Agent: "codex", Status: proto.StatusWorking, UpdatedAt: now},
		proto.Session{SessionID: "other", Title: "Elsewhere", Cwd: "/code/weatherman", Status: proto.StatusIdle, UpdatedAt: now},
		proto.Session{SessionID: "me", Title: "Assistant", Cwd: "/code/weather", Assistant: true, UpdatedAt: now})
	s := newKBService(t, f)
	e := kbEng(t, s)
	if err := e.Store.PutProject(&kb.Project{
		Slug: "weather-app", Name: "Weather App", Kind: "short", Status: "active",
		Dirs: []string{"box:/code/weather"}, OneLine: "The weather app for iOS and Android",
	}); err != nil {
		t.Fatal(err)
	}
	return s, f
}

// A mentioned workspace becomes a block that names where to dispatch, the project there and the agents that worked in
// the folder (newest first, the assistant's own and look-alike folders left out); the user's words stay after it.
func TestWithWorkspacesFillsTheBlock(t *testing.T) {
	s, _ := mentionEnv(t)
	got := s.withWorkspaces(context.Background(), workspaceTag+"\n\nAdd a dark mode")
	for _, want := range []string{
		`<persona-context kind="workspace" title="weather" url="" machine="box" cwd="/code/weather">`,
		"machine 填 box、cwd 填 /code/weather",
		"分支：main",
		"Weather App（weather-app）：The weather app for iOS and Android",
		"「Fix login」codex，working",
		"session ios，在 /code/weather/ios",
		"session old",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Index(got, "session ios") > strings.Index(got, "session old") {
		t.Errorf("newest first:\n%s", got)
	}
	for _, not := range []string{"Elsewhere", "Assistant", "Computer: box"} {
		if strings.Contains(got, not) {
			t.Errorf("%q should not be there:\n%s", not, got)
		}
	}
	if !strings.HasSuffix(got, "</persona-context>\n\nAdd a dark mode") {
		t.Errorf("the user's words after the block:\n%s", got)
	}
}

// Only blocks in front count, a computer the hub doesn't know keeps the client's block, and other contexts pass through.
func TestWithWorkspacesLeavesTheRest(t *testing.T) {
	s, _ := mentionEnv(t)
	ctx := context.Background()
	unknown := strings.Replace(workspaceTag, `machine="box"`, `machine="nowhere"`, 1)
	if got := s.withWorkspaces(ctx, unknown+"\n\nhi"); got != unknown+"\n\nhi" {
		t.Errorf("unknown computer changed:\n%s", got)
	}
	quote := `<persona-context kind="context" title="Another session" url="persona://session/box/s1">` + "\nFolder: /x\n</persona-context>"
	got := s.withWorkspaces(ctx, quote+"\n"+workspaceTag+"\n\nhi")
	if !strings.HasPrefix(got, quote+"\n") || !strings.Contains(got, "cwd 填 /code/weather") {
		t.Errorf("a quote then a workspace:\n%s", got)
	}
	later := "look at this: " + workspaceTag
	if got := s.withWorkspaces(ctx, later); got != later {
		t.Errorf("a block inside the user's words was filled:\n%s", got)
	}
}

// Messages to the assistant's conversations go through SendParams: the block there is filled in before the node gets it.
func TestSendParamsFillsWorkspaces(t *testing.T) {
	s, f := mentionEnv(t)
	f.setSessions("server", proto.Session{SessionID: "side", Assistant: true})
	p, err := s.SendParams(context.Background(), SendParams{MachineID: "server", SessionID: "side", Params: map[string]any{"text": workspaceTag + "\n\nhi"}})
	if err != nil {
		t.Fatal(err)
	}
	if text, _ := p["text"].(string); !strings.Contains(text, "cwd 填 /code/weather") || !strings.HasSuffix(text, "\n\nhi") {
		t.Fatalf("send params text:\n%s", text)
	}
}

// The block for a mentioned workspace (prompts/assistant/workspace_mention.md), every part present.
func TestGoldenWorkspaceMention(t *testing.T) {
	golden(t, "workspace_mention", prompts.RenderWorkspaceMention(prompts.WorkspaceMention{
		Title: "weather", Machine: "box", MachineName: "Work laptop", Cwd: "/code/weather", Branch: "main", Online: false,
		Projects: []prompts.WorkspaceProject{{Name: "Weather App", Slug: "weather-app", OneLine: "The weather app for iOS and Android"}},
		Sessions: []prompts.WorkspaceSession{
			{Title: "Fix login", Agent: "codex", Status: "working", When: "11-06 09:30", ID: "ios", Cwd: "/code/weather/ios"},
			{Title: "Old work", Agent: "claude", Status: "idle", When: "11-05 18:02", ID: "old"},
		},
	}))
}
