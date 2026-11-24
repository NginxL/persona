package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A track conversation's context (#13499): the time, the track and the owner's language, in each language.
func TestGoldenSpaceContext(t *testing.T) {
	now := time.Date(2026, 11, 22, 21, 30, 0, 0, time.UTC)
	for _, tc := range []struct{ name, language string }{{"zh-Hans", ""}, {"en", "English"}, {"ja", "日本語"}} {
		got := prompts.RenderSpaceContext(prompts.SpaceContext{Now: now, Zone: "UTC", Owner: "Example user", Title: "qa-plain", Language: tc.language})
		golden(t, "space_context."+tc.name, got)
	}
	golden(t, "space_context.health", prompts.RenderSpaceContext(prompts.SpaceContext{Now: now, Zone: "UTC", Health: true}))
}

// A conversation in the health space or another track gets its own short context instead of the main chat's
// snapshot: nothing of Agents, computers, unread Lark chats, tasks or the knowledge base, at most 600 characters. The
// main conversation and a side conversation of the main space keep the snapshot.
func TestContextOfATrackConversationIsItsOwn(t *testing.T) {
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.setSessions("server", proto.Session{SessionID: "m1", MachineID: "server", Assistant: true},
		proto.Session{SessionID: "side", MachineID: "server", Assistant: true},
		proto.Session{SessionID: "health1", MachineID: "server", Assistant: true, Space: SpaceHealth},
		proto.Session{SessionID: "plain1", MachineID: "server", Assistant: true, Space: "qa-plain"})
	s := newTestService(t, f)
	ctx := context.Background()
	text := func(id string) string {
		return s.Context(ctx, ContextParams{SessionID: id, MachineID: "server"}).Text
	}
	for id, head := range map[string]string{"health1": "**这是健康空间的对话**", "plain1": "**这是「qa-plain」空间的对话**"} {
		got := text(id)
		if !strings.HasPrefix(got, head) || !strings.Contains(got, "\n- 现在：") || !strings.Contains(got, "- 回答语言：简体中文") {
			t.Errorf("%s: not its track's context:\n%s", id, got)
		}
		if n := utf8.RuneCountInString(got); n > spaceContextMax {
			t.Errorf("%s: %d characters", id, n)
		}
		for _, leak := range []string{"Agents", "在线的电脑", "飞书", "待办", "知识库", "模型档位", "iPhone", "主对话"} {
			if strings.Contains(got, leak) {
				t.Errorf("%s: its context tells %q:\n%s", id, leak, got)
			}
		}
	}
	if got := text("m1"); !strings.HasPrefix(got, "**这是主对话**") || !strings.Contains(got, "- Agents：") {
		t.Errorf("the main conversation lost its snapshot:\n%s", got)
	}
	if got := text("side"); !strings.Contains(got, "- Agents：") || strings.Contains(got, "空间的对话") {
		t.Errorf("a side conversation of the main space lost its snapshot:\n%s", got)
	}
}
