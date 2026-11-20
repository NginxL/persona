package hub

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connectorwire"
)

// wireInboxEveryBlock adds inbox_every_block: one made-up item that carries every block an Inbox item has, as GET
// /api/v1/inbox lists it. inbox_list stays the realistic pair. This one is there so that a move of the item type
// (docs/plans/plan-inbox-vertical-slice.md, step S8) shows byte for byte that the list did not change; the keys
// listed() leaves out are pinned by internal/assistant's TestListedKeys.
func wireInboxEveryBlock(t *testing.T, add wireAdder, httpJSON func(any) json.RawMessage, now time.Time) {
	t.Helper()
	earlier := now.Add(-time.Hour)
	later := now.Add(time.Hour)
	it := assistant.InboxItem{
		ID: "lark:feed_blocks:20", Source: "lark", Kind: assistant.KindMention, Account: "feishu", AccountName: "Lark",
		Title: "Example discussion", Sender: "Example sender", Avatar: "https://example.com/avatar.png",
		Preview: "Please confirm the example.", Count: 2, StartedAt: earlier, UpdatedAt: now, Tier: assistant.TierAction,
		Summary: "An example asks for a confirmation.", Body: "**Example** body", Reason: "Someone asks you directly.",
		Lark: &assistant.InboxLark{AccountID: 2, OLAccount: 2, FeedID: "feed_blocks", IsGroup: true,
			IsTopic: true, StartPosition: "19", ReadPosition: "18", MentionPosition: "20", AtAll: true, AtMe: true,
			ReplyTo: "message_example", ThreadKind: assistant.LarkThreadTopic, SenderID: "sender_example"},
		Mail: &assistant.InboxMail{AccountID: "account_example", Mailbox: "INBOX", Generation: 3, UIDs: []uint32{21, 22},
			MessageID: "message_example", From: assistant.MailAddress{Name: "Example sender", Email: "sender@example.com"},
			Direct: true, Thread: "thread_example"},
		Doc: &assistant.InboxDoc{URL: "https://example.com/docx/doc_example", Link: "https://example.com/docx/doc_example",
			Type: "docx", Notice: "comment", NoticeID: "notice_example", Quote: "An example passage",
			Rows: []assistant.DocRow{{Ref: "tbl_example/rec_example", Table: "tbl_example", Title: "Example row",
				Fields: []assistant.DocField{{Name: "Owner", Value: "Example owner"}},
				Status: []assistant.DocStatus{{Field: "fld_example", Name: "Status", Value: "Open",
					Options: []assistant.DocOption{{Name: "Open", Color: "#336699"}, {Name: "Done"}}}}}},
			ReadAt: &earlier, Reply: &assistant.DocReply{Kind: "thread", Label: "Reply in the comment", Thread: "comment_example"}},
		Translations: map[string]*assistant.InboxTranslation{"en": {Subject: "Example subject", Preview: "Example preview",
			Text: "The whole example body", Clipped: true, ModelFingerprint: "model_example", Of: "20"}},
		Moment: &assistant.InboxMoment{PostID: "post_example", Notice: "comment", Notices: []string{"notice_a", "notice_b"},
			ReplyTo: "comment_example", MyPost: true, PostText: "An example post", Anonymous: true, ReadAt: &earlier},
		Connector: &assistant.InboxConnector{Thread: "thread_example", Label: "Review requested",
			URL: "https://example.com/pull/1", OpenURL: "sms:example", ReplyTo: "Example repository",
			CanReply: true, Avatar: "media_example", RuleTier: assistant.TierFYI,
			Tags: []connectorwire.Tag{{Key: connectorwire.TagRepo, Value: "example/repo", Name: "example/repo"}}},
		Agent: &assistant.InboxAgent{State: assistant.AgentDrafted, Task: "Draft a short confirmation.",
			Reason: "A direct question.", MachineID: "m1", SessionID: "s1", Draft: "Confirmed.", Note: "The example says so.",
			Of: "20", Changes: []assistant.DocChange{{Ref: "tbl_example/rec_example", Field: "fld_example", Value: "Done"}},
			TodoID: "todo_example", StartedAt: &earlier, DraftedAt: &now, Attempts: 1,
			RetryAfter: &later, Wake: true, SentOf: "19", Lost: []string{"s0"}, ProcessingTurn: "turn_example",
			DeferredAt: &earlier, WakeSlot: true},
		Where: "Example group", TitleBy: "user",
		Follow: &assistant.InboxFollow{Who: "Example sender", Next: "Check the answer.", CheckAt: &later, Checks: 1, Due: true},
		MergedFrom: []assistant.InboxMergeRef{{ID: "lark:feed_other:5", Source: "lark", AccountName: "Lark",
			Title: "Another example"}},
		PreMergeTier: assistant.TierFYI, PreMergeTitle: "Old title", PreMergeTitleBy: "model", MergeTitle: "Merged title",
		Parent: &assistant.InboxParent{Kind: assistant.ParentTodo, ID: "todo_example", By: assistant.ParentByUser},
		Folder: "work/examples", FolderBy: "user", Urgent: true, Recap: "The example waits for a confirmation.",
		Handling: &assistant.InboxHandling{Owner: assistant.HandlingOwner, State: assistant.HandlingDone, MachineID: "m1",
			SessionID: "s1", Since: &earlier},
		Suggestion: &assistant.InboxSuggestion{Version: 2, Kind: assistant.SuggestReply, Text: "Confirmed.",
			Note: "The example says so.", Handback: true, Due: "2026-01-03", MachineID: "m1",
			Changes: []assistant.DocChange{{Ref: "tbl_example/rec_example", Field: "fld_example", Value: "Done"}}, At: now,
			Cwd: "~/example", Conversation: &assistantwire.SessionRef{MachineID: "m1", SessionID: "s2"},
			Owner: &assistantwire.SessionRef{MachineID: "m1", SessionID: "s1"}, SourceOf: "20"},
		SuggestionAttempts: []*assistant.InboxSuggestionAttempt{{Version: 1, Status: "failed", Retryable: true,
			Code: "send_failed", Text: "Confirmed?", At: earlier,
			Conversation: &assistantwire.SessionRef{MachineID: "m1", SessionID: "s1"}, RetryAfter: 30,
			ClientID: "client_example", Target: "message_example"}},
		Chain: []assistant.InboxChainEntry{
			{ID: "c1", At: earlier, TurnAt: &earlier, Kind: assistant.ChainReceived, Text: "2 new messages", Count: 2,
				Sender: "Example sender", Messages: []assistant.InboxChainMessage{{Sender: "Example sender",
					Text: "Please confirm the example.", At: &earlier}},
				Link: &assistant.InboxChainLink{Type: "lark", Position: "19"}, Via: "lark"},
			{ID: "c2", At: now, Kind: assistant.ChainHandling, Text: "Looking into it", Edited: true, Detail: "A detail",
				Link: &assistant.InboxChainLink{Type: "session", MachineID: "m1", SessionID: "s1", ID: "item_example",
					UID: 21, URL: "https://example.com/item"}, Via: assistant.ViaAssistant},
			{ID: "c3", At: now, Kind: assistant.ChainDone, Text: "Marked as done", Via: assistant.ViaUser, By: "user"},
		},
		ChainCount: 3, Related: []assistant.InboxRelated{{ID: "mail:account_example:thread_other:9", Title: "A related mail",
			Source: "mail"}},
		Handoffs: []assistant.InboxHandoff{{MachineID: "m1", SessionID: "s2", At: now, Op: "op_example",
			PendingSuggestion: true, SourceOf: "19"}},
		Last: "20", Context: "Only the detail shows this.", TriagedAt: "20", BodyOf: "20", JudgedAt: "20", FolderOf: "20",
		Rules: "Example rules", TriageRev: 3, RuleOf: "19", SettledOf: "18", RepliedOf: "17", ArrivedOf: "20",
		ArrivedAt: now, Meta: json.RawMessage(`{"messages":["m19","m20"]}`),
		Group: &assistant.InboxGroup{Connector: "lark", Thread: "thread_example"}, ChainSeq: 2, SuggestSeq: 2,
	}
	add("inbox", "inbox_every_block", "Every block an Inbox item can carry, on one made-up item, as the list shows it: "+
		"the item's bookkeeping and each block's internal fields are left out.", "inbox",
		httpJSON(map[string]any{"items": []assistant.InboxItem{it.Listed()}}),
		map[string]any{"/items/0/id": "lark:feed_blocks:20", "/items/0/source": "lark", "/items/0/context": nil,
			"/items/0/lark/feed_id": "feed_blocks", "/items/0/mail/thread": "thread_example",
			"/items/0/moment/post_id": "post_example", "/items/0/agent/state": "drafted", "/items/0/status": "action",
			"/items/0/handling/state": "done", "/items/0/suggestion/kind": "reply", "/items/0/chain/0/kind": "received",
			"/items/0/related/0/id": "mail:account_example:thread_other:9", "/items/0/follow/who": "Example sender"},
		"server/internal/assistant/inbox.go: InboxItem.listed", "server/internal/assistant/inbox_listed_keys_test.go: TestListedKeys",
		"apple/Shared/InboxModels.swift: InboxItem")
}
