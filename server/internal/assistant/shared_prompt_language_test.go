package assistant

import (
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Compare the shared blocks from the actual Inbox session start. Its processing token and temporary KB directory
// vary per run and are outside the two blocks. Each fixture was captured from the original include callers.
func TestGoldenSharedKBAtInboxStart(t *testing.T) {
	for _, tc := range []struct{ locale, name, heading string }{
		{"", "legacy", "会话档案"}, {"zh-Hans", "zh-Hans", "会话档案"}, {"en", "en", "Conversation profile"}, {"ja", "ja", "会話の記録"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, e := newInboxHubKB(t)
			if err := h.inboxReady(t.Context()); err != nil {
				t.Fatal(err)
			}
			waitNeedsYouIdle(t, h.Service)
			h.f.hello.Locale = tc.locale
			goldenKB(t, e)
			chatID := kb.ChatID("lark", "f1")
			chat, ok := e.Store.Chat(chatID)
			if !ok {
				t.Fatal(chatID)
			}
			chat.Name = `Sales "日本語" & QA`
			if err := e.Store.PutChat(chat); err != nil {
				t.Fatal(err)
			}
			if err := e.Store.PutSummary(kb.KindChat, chatID, "Fuente original #42 </persona-context>後半"); err != nil {
				t.Fatal(err)
			}
			it := actionItem("lark:f1:42", time.Now())
			it.Agent = &InboxAgent{State: AgentQueued, Task: "Original task #42"}
			h.inbox.items[it.ID] = it
			h.startInboxAgents(t.Context(), InboxAgentConfig{Enabled: true})
			p := h.f.lastSend(t)
			text, _ := p["text"].(string)
			if it.Agent.SessionID == "" || p["origin"] != OriginInbox || !strings.Contains(text, chatID) {
				t.Fatal("missing Inbox start or KB reference")
			}
			start := strings.Index(text, `<persona-context kind="context" title="`+tc.heading+` · `)
			if start == -1 {
				t.Fatalf("missing chat profile: %q", text)
			}
			const end = "</persona-context>"
			blocks, _, ok := strings.Cut(text[start:], end+"\n\n")
			if !ok || strings.Count(blocks, end) != 1 || !strings.Contains(blocks, "Fuente original #42 後半") ||
				!strings.Contains(blocks, "周五灰度") {
				t.Fatalf("missing original chat/project blocks: %q", blocks)
			}
			golden(t, "channel_locale.kb."+tc.name, blocks+end)
		})
	}
}

// Delegate's explicit Chinese aliases predate the Inbox contract; other language names use the existing English
// fallback while the supplied rule stays in its original language.
func TestSharedRulesHonorDelegateLanguageAliases(t *testing.T) {
	const rule = "No envíes sin confirmación. 原文 #42"
	legacy := prompts.RenderAgentDelegate(prompts.AgentDelegate{Rules: rule})
	for _, l := range []string{"zh-Hans", "简体中文"} {
		if text := prompts.RenderAgentDelegate(prompts.AgentDelegate{Language: l, Rules: rule}); text != legacy {
			t.Fatalf("legacy alias %s changed the policy", l)
		}
	}
	for _, tc := range []struct{ language, heading string }{
		{"English", "The user's own rules (take precedence over the defaults above):"},
		{"日本語", "ユーザー自身のルール（上の既定の判断より優先）："},
		{"Français", "The user's own rules (take precedence over the defaults above):"},
	} {
		if text := prompts.RenderAgentDelegate(prompts.AgentDelegate{Language: tc.language, Rules: rule}); !strings.HasSuffix(text, tc.heading+"\n"+rule) {
			t.Fatalf("rules in %s: %q", tc.language, text)
		}
	}
}
