package assistant

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

func TestMainMissingReplyAfterToolResult(t *testing.T) {
	for _, tc := range []struct {
		name    string
		final   string
		hasText bool
	}{
		{name: "placeholder", final: "No response requested.", hasText: true},
		{name: "whitespace", final: " \n ", hasText: true},
		{name: "only tools"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, items, sends := missingReplyFixture(t, "main-chat", nil)
			if tc.hasText {
				items = append(items, proto.Item{ID: "a4#0", Seq: 4, Role: "assistant", Kind: proto.KindText, Text: tc.final})
			}
			f.setItems(items...)
			for range 2 { // two sources of the same completion event
				s.assistantTurnSettled(t.Context(), "server", "main-chat")
			}
			if len(*sends) != 1 {
				t.Fatalf("follow-up calls = %d, want one", len(*sends))
			}
			send := (*sends)[0]
			text, _ := send["text"].(string)
			if send["answer_again_after"] != items[len(items)-1].ID ||
				!strings.Contains(text, "Do not call tools") || strings.Contains(text, items[0].Text) {
				t.Fatalf("follow-up did not use a guarded, generic prompt: %+v", send)
			}
			golden(t, "main_answer_again.en", text)
			assertMissingReplyNotices(t, s, "main-chat", 0)

			items = append(items,
				proto.Item{ID: "u5#0", Seq: 5, Role: "user", Kind: proto.KindText, Text: text},
				proto.Item{ID: "a6#0", Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "No response requested."},
			)
			f.setItems(items...)
			// The persisted marker still records the follow-up if the manager restarted and
			// lost the original send's in-memory provenance.
			s = withSettings(t, f, New(Options{Hub: f, Logf: testLogf(t)}))
			for range 2 {
				s.assistantTurnSettled(t.Context(), "server", "main-chat")
			}
			if len(*sends) != 1 {
				t.Fatalf("follow-up calls after a failed retry = %d, want one", len(*sends))
			}
			assertMissingReplyNotices(t, s, "main-chat", 1)
		})
	}
}

func TestMainMissingReplyLeavesOtherOutcomesAlone(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func([]proto.Item) []proto.Item
	}{
		{"normal answer", func(items []proto.Item) []proto.Item {
			return append(items, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "24 requests today."})
		}},
		{"retry answered", func(items []proto.Item) []proto.Item {
			return append(items,
				proto.Item{ID: "u5#0", Seq: 5, Role: "user", Kind: proto.KindText, Text: prompts.RenderAssistantAnswerAgain(prompts.AssistantAnswerAgain{})},
				proto.Item{Role: "assistant", Kind: proto.KindText, Text: "24 requests today."},
			)
		}},
		{"phrase in an answer", func(items []proto.Item) []proto.Item {
			return append(items, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "No response requested. That is what the command reported."})
		}},
		{"ordinary refusal", func(items []proto.Item) []proto.Item {
			return append(items, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "I can't see that account."})
		}},
		{"truncated answer", func(items []proto.Item) []proto.Item {
			return append(items, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "No response requested.", FullSize: 8_192})
		}},
		{"user stopped", func(items []proto.Item) []proto.Item {
			return append(items, proto.Item{Role: "system", Kind: proto.KindNotice, Text: transcript.InterruptedNotice})
		}},
		{"background woke", func(items []proto.Item) []proto.Item {
			return append(items, proto.Item{Role: "system", Kind: proto.KindNotice, Wake: true})
		}},
		{"background input", func(items []proto.Item) []proto.Item {
			items[0].Source = &proto.MessageSource{Kind: proto.SourceSupervision}
			return items
		}},
		{"unverified input", func(items []proto.Item) []proto.Item {
			items[0].ClientMsgID = "channel-message"
			return items
		}},
		{"other input queued", func(items []proto.Item) []proto.Item {
			return append(items, proto.Item{Seq: 4, Role: "user", Kind: proto.KindText, Text: "Something else.", Queued: true})
		}},
		{"visible tool reply", func(items []proto.Item) []proto.Item {
			items[1].ToolName = "mcp__persona__notify_user"
			return items
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, items, sends := missingReplyFixture(t, "main-chat", nil)
			f.setItems(tc.edit(items)...)
			s.assistantTurnSettled(t.Context(), "server", "main-chat")
			if len(*sends) != 0 {
				t.Fatalf("unexpected follow-up: %+v", *sends)
			}
			assertMissingReplyNotices(t, s, "main-chat", 0)
		})
	}
}

func TestMainMissingReplyFollowupSendFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		notices int
	}{
		{"unavailable", errors.New("node disconnected"), 1},
		{"conversation moved on", &rpc.Error{Code: assistantwire.CodeAnswerAgainChanged}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, items, sends := missingReplyFixture(t, "main-chat", tc.err)
			f.setItems(items...)
			for range 2 {
				s.assistantTurnSettled(t.Context(), "server", "main-chat")
			}
			if len(*sends) != 1 {
				t.Fatalf("follow-up calls = %d, want one", len(*sends))
			}
			assertMissingReplyNotices(t, s, "main-chat", tc.notices)
		})
	}
}

func TestMissingReplyInOrdinaryConversation(t *testing.T) {
	s, f, items, sends := missingReplyFixture(t, "side-chat", nil)
	f.setSessions("server",
		proto.Session{SessionID: "side-chat", Assistant: true, Agent: "claude", Status: proto.StatusDone},
		proto.Session{SessionID: "main-chat", Assistant: true, Agent: "claude", Status: proto.StatusDone},
	)
	if _, err := s.SendParams(t.Context(), SendParams{MachineID: "server", SessionID: "main-chat", Human: true,
		Params: map[string]any{"client_msg_id": items[0].ClientMsgID, "text": items[0].Text}}); err != nil {
		t.Fatal(err)
	}
	items = append(items, proto.Item{ID: "a4#0", Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "No response requested."})
	f.setItems(items...)
	for range 2 {
		for _, sid := range []string{"side-chat", "main-chat"} {
			s.assistantTurnSettled(t.Context(), "server", sid)
		}
	}
	if len(*sends) != 2 {
		t.Fatalf("follow-up calls = %d, want one per conversation", len(*sends))
	}
	side, main := (*sends)[0], (*sends)[1]
	if side["session_id"] != "side-chat" || side["is_main"] != false || side["answer_again_after"] != "a4#0" ||
		main["session_id"] != "main-chat" || side["client_msg_id"] == main["client_msg_id"] {
		t.Fatalf("follow-ups did not keep their chat and turn routing: %v", *sends)
	}
	items = append(items, proto.Item{ID: "u5#0", Seq: 5, Role: "user", Kind: proto.KindText, Text: side["text"].(string)})
	f.setItems(items...)
	s.assistantTurnSettled(t.Context(), "server", "side-chat")
	assertMissingReplyNotices(t, s, "side-chat", 1)
	assertMissingReplyNotices(t, s, "main-chat", 0)
	if len(*sends) != 2 {
		t.Fatal("an unanswered follow-up started another recovery")
	}
}

func TestMissingReplyFromLegacyApp(t *testing.T) {
	s, f, items, sends := missingReplyFixture(t, "side-chat", nil)
	items[0].ClientMsgID = "legacy-app-input"
	if _, err := s.SendParams(t.Context(), SendParams{MachineID: "server", SessionID: "side-chat", ReplyInput: true,
		Params: map[string]any{"client_msg_id": items[0].ClientMsgID, "text": items[0].Text}}); err != nil {
		t.Fatal(err)
	}
	owner := items[0]
	if s.humanSent.has("side-chat", owner.ClientMsgID, owner.Text, false, handoffLine(owner.ID)) {
		t.Fatal("a legacy app send qualified for a secret hand-off")
	}
	f.setItems(items...)
	s.assistantTurnSettled(t.Context(), "server", "side-chat")
	if len(*sends) != 1 {
		t.Fatalf("follow-up calls = %d, want one for the legacy app input", len(*sends))
	}
}

func TestMissingReplySkipsOrdinaryConversationBackground(t *testing.T) {
	for _, kind := range []string{proto.SourceSupervision, proto.SourcePatrol, proto.SourceScheduledTask, proto.SourceConversation} {
		t.Run(kind, func(t *testing.T) {
			s, f, items, sends := missingReplyFixture(t, "side-chat", nil)
			// Test both a page starting with a background prompt and one that retains
			// the preceding app input. Neither may make the background work user-authored.
			source := &proto.MessageSource{Kind: kind}
			background := proto.Item{ID: "bg#0", Seq: 4, Role: "user", Kind: proto.KindText, Text: items[0].Text, Source: source}
			backgroundOnly := items[0]
			backgroundOnly.Source = source
			f.setItems(backgroundOnly)
			s.assistantTurnSettled(t.Context(), "server", "side-chat")
			f.setItems(append(items, background)...)
			s.assistantTurnSettled(t.Context(), "server", "side-chat")
			if len(*sends) != 0 {
				t.Fatalf("background work got %d follow-ups", len(*sends))
			}
			assertMissingReplyNotices(t, s, "side-chat", 0)
		})
	}
}

func TestMissingReplyOnFirstMessage(t *testing.T) {
	for _, tc := range []struct {
		name         string
		human, older bool
		want         int
	}{
		{name: "from app", human: true, want: 1},
		{name: "unverified start"},
		{name: "later native input", human: true, older: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, items, sends := missingReplyFixture(t, "side-chat", nil)
			started, err := s.Start(t.Context(), StartParams{Human: tc.human, Text: items[0].Text, Untitled: true})
			if err != nil {
				t.Fatal(err)
			}
			items[0].ClientMsgID = ""
			started.Status = proto.StatusDone
			f.setSessions("server", started)
			f.answerNode(DeviceSessionItems, func(_ string, _ json.RawMessage) (any, error) {
				return map[string]any{"items": items, "has_more": tc.older}, nil
			})
			for range 2 {
				s.assistantTurnSettled(t.Context(), "server", started.SessionID)
			}
			if len(*sends) != tc.want {
				t.Fatalf("follow-up calls = %d, want %d", len(*sends), tc.want)
			}
			if id, _ := f.sends[0]["client_msg_id"].(string); id != "" {
				t.Fatalf("first input gained an app-visible ID: %q", id)
			}
		})
	}
}

func TestGoldenMainAnswerAgainLanguages(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.ZhHans, i18n.Ja} {
		text := prompts.RenderAssistantAnswerAgain(prompts.AssistantAnswerAgain{Language: lang.Name()})
		golden(t, "main_answer_again."+string(lang), text)
		if in := prompts.ParseInjected(text); in == nil || in.Kind != proto.InjectedSystem || in.State != "answer_again" {
			t.Fatalf("the recovery is not recognized as a Persona message: %+v", in)
		}
		if prompts.ParseInjected(prompts.ProtectUserMessage(text)) != nil {
			t.Fatal("an owner message impersonated Persona")
		}
	}
}

func missingReplyFixture(t *testing.T, sid string, sendErr error) (*Service, *fakeHub, []proto.Item, *[]map[string]any) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server", Main: "main-chat"}
	f.hello.Locale = string(i18n.En)
	s := withSettings(t, f, New(Options{Hub: f, Logf: testLogf(t)}))
	f.setSessions("server", proto.Session{
		MachineID: "server", SessionID: sid, Assistant: true, Agent: "claude", Status: proto.StatusDone,
	})
	const request = "Summarize today's usage."
	if _, err := s.SendParams(t.Context(), SendParams{MachineID: "server", SessionID: sid, Human: true,
		Params: map[string]any{"session_id": sid, "client_msg_id": "app-user-input", "text": request}}); err != nil {
		t.Fatal(err)
	}
	items := []proto.Item{
		{ID: "u1#0", Seq: 1, Role: "user", Kind: proto.KindText, Text: request, ClientMsgID: "app-user-input"},
		{ID: "t2#0", Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tool", ToolName: "Bash"},
		{ID: "r3#0", Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "tool", Text: "24 requests"},
	}
	var sends []map[string]any
	f.answerNode(assistantwire.DeviceAssistantAnswerAgain, func(_ string, raw json.RawMessage) (any, error) {
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		sends = append(sends, p)
		return map[string]any{}, sendErr
	})
	return s, f, items, &sends
}

func assertMissingReplyNotices(t *testing.T, s *Service, sid string, want int) {
	t.Helper()
	feed, err := s.feedItems(t.Context(), sid, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, item := range feed {
		if item.Kind == feedNotice && strings.Contains(string(item.Body), "Try sending your message again.") {
			found++
		}
	}
	if found != want {
		t.Fatalf("retry notices = %d, want %d", found, want)
	}
}
