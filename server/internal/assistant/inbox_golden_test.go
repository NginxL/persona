package assistant

// Golden tests for the inbox's prompts (testdata/prompts/inbox_*.golden; docs/assistant/prompts.md): UPDATE_GOLDEN=1 writes them.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
)

func goldenCall(t *testing.T, name string, c capturedCall) {
	t.Helper()
	golden(t, name+".system", c.System)
	golden(t, name+".user", c.User)
}

// goldenInbox is an inbox whose sources describe Telegram's kinds in the extension's own words (kind_descriptions,
// #1704: the hub's template had the same).
func goldenInbox(t *testing.T) *inboxHub {
	t.Helper()
	h := agentHub(t)
	h.inbox.srcMu.Lock()
	for kind, d := range map[string]string{"dm": "Telegram 里有人私聊用户，还没读", "mention": "Telegram 群里 @ 了用户，或回复了用户的消息"} {
		h.inbox.kinds["telegram/"+kind] = d
	}
	h.inbox.srcMu.Unlock()
	return h
}

func goldenItems(t *testing.T) map[string]*InboxItem {
	t.Helper()
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	agent := &InboxAgent{State: AgentQueued, Task: "查清楚构建为什么挂了，回复对方结论"}
	return map[string]*InboxItem{
		"dm": {ID: "lark:f2:1", Kind: KindDM, Title: "张三", Context: "张三：1013 合了吗？", Summary: "张三问 1013 是否合入", UpdatedAt: at,
			Lark: &InboxLark{FeedID: "f2"}, Agent: agent},
		"mention": {ID: "lark:f1:1", Kind: KindMention, Title: "发布群", Context: "张三：@我 周五灰度能提前吗", UpdatedAt: at,
			Lark: &InboxLark{FeedID: "f1", IsGroup: true}, Agent: agent},
		"group": {ID: "lark:f3:1", Kind: KindGroup, Title: "闲聊群", Context: "李四：午饭吃啥", UpdatedAt: at, Lark: &InboxLark{FeedID: "f3"}},
		"bot":   {ID: "lark:f9:1", Kind: KindBot, Title: "告警机器人", Context: "告警：示例服务 5xx 升高", UpdatedAt: at, Lark: &InboxLark{FeedID: "f9"}},
		"doc_rows": {ID: "doc:1", Kind: KindDoc, Title: "排期表", Context: "表格「排期表」第 3 行 @我：状态 进行中", UpdatedAt: at,
			Doc: &InboxDoc{URL: "https://example.feishu.cn/sheets/AbC", Rows: []DocRow{{}}}, Agent: agent},
		"doc_plain": {ID: "doc:2", Kind: KindDoc, Title: "设计文档", Context: "评论：这里要改吗", UpdatedAt: at,
			Doc: &InboxDoc{URL: "https://example.feishu.cn/docx/AbC"}, Agent: agent},
		"moment": {ID: "moment:1", Kind: KindMoment, Title: "我的帖子", Context: "王五评论：写得好", UpdatedAt: at,
			Moment: &InboxMoment{PostID: "p1", Notice: "comment"}, Agent: agent},
		"mail_direct": {ID: "mail:1", Kind: KindMail, Title: "季度汇报", Context: "来信：请周五前提交", UpdatedAt: at,
			Mail: &InboxMail{AccountID: "acc1", Mailbox: "INBOX", UIDs: []uint32{7, 9}, Direct: true, From: MailAddress{Email: "Sender@Example.com"}}, Agent: agent},
		"mail_cc": {ID: "mail:2", Kind: KindMail, Title: "周报", Context: "抄送：本周进展", UpdatedAt: at,
			Mail: &InboxMail{AccountID: "acc1", Mailbox: "INBOX"}},
		"github_review": {ID: "github:1", Source: "github", Kind: "review_requested", Title: "Add the example",
			Context: "GitHub PR example-org/example-repo#12：Add the example\n状态：open · 作者 alice · 等这些人 review：me", UpdatedAt: at,
			Connector: &InboxConnector{Thread: "example-org/example-repo#12", Label: "请你 review", URL: "https://github.com/example-org/example-repo/pull/12",
				ReplyTo: "评论到 example-org/example-repo#12（仓库可见的人都能看到）", CanReply: true, RuleTier: TierAction}, Agent: agent},
		"imessage_dm": {ID: "imessage:1", Source: "imessage", Kind: "dm", Title: "张三", UpdatedAt: at,
			Context: "我（10-30 07:58）：在吗\n张三（10-30 07:59）：示例：周五下午三点见，地方你定",
			Connector: &InboxConnector{Thread: "imessage-0123456789ab", OpenURL: "sms:+15550100", ReplyTo: "在「信息」里回复（Persona 不替你发）",
				RuleTier: TierAction}, Agent: agent},
		"imessage_group": {ID: "imessage:2", Source: "imessage", Kind: "group", Title: "周末徒步", UpdatedAt: at,
			Context: "李四（10-30 08:01）：示例：明天十点出发", Connector: &InboxConnector{Thread: "imessage-ba9876543210", Label: "群聊", RuleTier: TierFYI}},
		"telegram_dm": {ID: "telegram:u1", Source: "telegram", Kind: "dm", Title: "示例用户", UpdatedAt: at,
			Context:   "示例用户（Telegram 私聊）\n2026-10-30 07:58 我：在吗\n2026-10-30 07:59 示例用户：示例：周五下午三点见",
			Connector: &InboxConnector{Thread: "u1", Label: "私聊", OpenURL: "tg://resolve?domain=example", RuleTier: TierAction}, Agent: agent},
		"telegram_mention": {ID: "telegram:c2", Source: "telegram", Kind: "mention", Title: "示例群", UpdatedAt: at,
			Context:   "示例群（Telegram 群）\n2026-10-30 08:01 另一个示例用户：@example 示例方案你怎么看？",
			Connector: &InboxConnector{Thread: "c2", Label: "@你", OpenURL: "https://t.me/c/2/12", RuleTier: TierAction}},
	}
}

// goldenKB is the knowledge base the golden prompts read (the assistant's own): a Feishu group with its summary and
// one project, a mail correspondent's chat without a summary.
func goldenKB(t *testing.T, e *kb.Engine) {
	t.Helper()
	now := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	for _, p := range []*kb.Project{
		{Slug: "demo-release", Name: "示例发布", Kind: "short", Status: "active", Tagging: kb.Tagging{Importance: "P0", Relevance: 95}, OneLine: "周五灰度", Updated: now},
		{Slug: "demo-infra", Name: "示例基建", Kind: "long", Status: "active", Tagging: kb.Tagging{Importance: "P2", Relevance: 40}, Updated: now},
	} {
		if err := e.Store.PutProject(p); err != nil {
			t.Fatal(err)
		}
	}
	_ = e.Store.PutSummary(kb.KindProject, "demo-release", "# 示例发布\n\n## 目标\n周五灰度")
	c := &kb.Chat{ID: kb.ChatID("lark", "f1"), Source: "lark", SourceID: "f1", Kind: "group", Name: "发布群", Created: now,
		LastAt: now, OneLine: "在推示例发布", Tagging: kb.Tagging{Importance: "P1", Relevance: 70}, Projects: []string{"demo-release", "missing"}}
	_ = e.Store.PutChat(c)
	_ = e.Store.PutSummary(kb.KindChat, c.ID, "# 发布群\n\n## 这是什么\n发布协调")
	_ = e.Store.PutChat(&kb.Chat{ID: kb.ChatID("mail", "acc1-sender@example.com"), Source: "mail", SourceID: "acc1-sender@example.com", Kind: "mail",
		Name: "示例来信", Created: now, LastAt: now, Tagging: kb.Tagging{Importance: "P3", Relevance: 10}})
}

func TestGoldenInboxTriage(t *testing.T) {
	ctx := context.Background()
	for name, it := range goldenItems(t) {
		h := goldenInbox(t)
		model, calls := captureModel(t, `{"tier":"action","summary":"s","reason":"r"}`)
		h.in.model = model
		if name == "dm" {
			h.f.hello.Owner = "示例用户"
			h.setConfig(func(c *assistantwire.InboxConfig) { c.Rules = strings.TrimSpace("  老板的消息一律需要我  ") }) // the hub trims them
		}
		h.inbox.items[it.ID] = it
		h.triageOne(ctx, it)
		goldenCall(t, "inbox_triage."+name, onlyCall(t, calls()))
	}
}

func TestGoldenInboxDraft(t *testing.T) {
	ctx := context.Background()
	items := goldenItems(t)
	for _, c := range []struct {
		name, item, owner, notes, instruction string
	}{
		{"plain", "dm", "", "", ""},
		{"owner_notes_instruction", "mail_direct", "示例用户", " 我在示例公司做后端 ", " 说周四给 "},
	} {
		h := goldenInbox(t)
		model, calls := captureModel(t, "好的")
		h.in.models[purposeDraft] = model
		h.f.hello.Owner, h.f.hello.OwnerNotes = c.owner, c.notes
		it := items[c.item]
		h.inbox.items[it.ID] = it
		if _, err := h.draftReply(ctx, it.ID, c.instruction); err != nil {
			t.Fatal(err)
		}
		goldenCall(t, "inbox_draft."+c.name, onlyCall(t, calls()))
	}
}

// A draft uses About me as the user edited it in the app, not the config file's notes it replaced (#5769).
func TestInboxDraftUsesEditedAboutMe(t *testing.T) {
	ctx := context.Background()
	h := goldenInbox(t)
	model, calls := captureModel(t, "好的")
	h.in.models[purposeDraft] = model
	h.f.hello.OwnerNotes = "config file notes"
	edited := "  notes edited in the app  "
	setSettings(t, h.Service, func(v *Settings) { v.Notes = &edited })
	it := goldenItems(t)["dm"]
	h.inbox.items[it.ID] = it
	if _, err := h.draftReply(ctx, it.ID, ""); err != nil {
		t.Fatal(err)
	}
	system := onlyCall(t, calls()).System
	if !strings.Contains(system, "notes edited in the app") || strings.Contains(system, "config file notes") {
		t.Fatalf("the draft should know About me as edited in the app:\n%s", system)
	}
}

func TestGoldenInboxJudge(t *testing.T) {
	ctx := context.Background()
	items := goldenItems(t)
	for _, c := range []struct{ name, item, owner, rules string }{
		{"summary_rules", "dm", "示例用户", " 告警都不用交 "},
		{"plain", "dm", "", ""}, // the direct human Lark message remains eligible
	} {
		h := goldenInbox(t)
		model, calls := captureModel(t, `{"handle":false,"reason":"r"}`)
		h.in.model = model
		h.f.hello.Owner = c.owner
		h.setConfig(func(cfg *assistantwire.InboxConfig) { cfg.AgentRules = strings.TrimSpace(c.rules) }) // the hub trims them
		it := *items[c.item]
		h.inbox.items[it.ID] = &it
		it.Agent = &InboxAgent{State: AgentQueued}
		h.linkTask(ctx, &it, []prompts.InboxTask{{ID: "todo_1", Title: "修登录崩溃", State: "在做", Last: "测试通过",
			Notes: "客户端发版群反馈登录后崩溃，修好后回复陈晓。"}})
		goldenCall(t, "inbox_judge."+c.name, onlyCall(t, calls()))
	}
}

func TestGoldenInboxAgentPrompt(t *testing.T) {
	items := goldenItems(t)
	ctx := context.Background()
	for _, name := range []string{"dm", "doc_rows", "doc_plain", "moment", "mail_direct", "github_review", "imessage_dm"} {
		h := goldenInbox(t)
		h.f.hello.Owner = "示例用户"
		golden(t, "inbox_agent."+name, h.inbox.inboxAgentPrompt(items[name], h.owner(ctx), h.kbChatContext(ctx, h.inbox.kbChatOf(items[name]))))
	}
	// With the knowledge base (the assistant's own): the chat's summary and projects follow.
	h, e := newInboxHubKB(t)
	h.inboxReady(ctx)
	goldenKB(t, e)
	it := *items["mention"]
	golden(t, "inbox_agent.mention_kb", h.inbox.inboxAgentPrompt(&it, h.owner(ctx), h.kbChatContext(ctx, h.inbox.kbChatOf(&it))))
	// A chat in another Feishu account than lark.account: the prompt names it for lark_messages.
	it = *items["dm"]
	it.Lark = &InboxLark{AccountID: 2, FeedID: "f2"}
	golden(t, "inbox_agent.dm_second_account", h.inbox.inboxAgentPromptFor(&it, "示例用户", nil, nil, 1, time.UTC))
	it = *items["mail_direct"]
	it.Mail = &InboxMail{AccountID: "acc1", Mailbox: "INBOX", UIDs: []uint32{3}, From: MailAddress{Email: "Sender@Example.com"}}
	golden(t, "inbox_agent.mail_kb", h.inbox.inboxAgentPrompt(&it, "", h.kbChatContext(ctx, h.inbox.kbChatOf(&it))))

	// A subscription's row (#6222): the subscription and the connector it reads, its link, and no reply line when it
	// can't be replied to; its knowledge-base chat while its knowledge base is on.
	h = goldenInbox(t)
	h.f.hello.Owner = "示例用户"
	h.inbox.srcMu.Lock()
	h.inbox.names["sub_x"], h.inbox.names["sub_gh"] = "学校通知", "我的 PR"
	h.inbox.vias["sub_x"] = inboxVia{connector: "example", source: "通知", chats: map[string]string{"sub:sub_x:t1": "tabc"}}
	h.inbox.vias["sub_gh"] = inboxVia{connector: "github", source: "通知"}
	h.inbox.srcMu.Unlock()
	it = *items["github_review"]
	it.ID, it.Source, it.Kind, it.Title, it.Context = "sub_x:1", "sub_x", "sub_notice", "下周的答疑改到周三", "【下周的答疑改到周三】\n示例：地点不变"
	it.Connector = &InboxConnector{Thread: "sub:sub_x:t1", Label: "学校通知", URL: "https://notices.example.com/n1", RuleTier: TierFYI}
	golden(t, "inbox_agent.subscription", h.inbox.inboxAgentPrompt(&it, h.owner(ctx), nil))
	if got := h.inbox.kbChatOf(&it); got != kb.ChatID("sub_x", "tabc") {
		t.Errorf("a subscription's chat: %q", got)
	}
	gh := *items["github_review"]
	gh.ID, gh.Source = "sub_gh:1", "sub_gh"
	gh.Connector = &InboxConnector{Thread: "sub:sub_gh:example-org/example-repo#12", URL: gh.Connector.URL, ReplyTo: gh.Connector.ReplyTo, CanReply: true}
	golden(t, "inbox_agent.subscription_github", h.inbox.inboxAgentPrompt(&gh, h.owner(ctx), nil))
	if got := h.inbox.kbChatOf(&gh); got != "" {
		t.Errorf("a subscription whose knowledge base is off has a chat: %q", got)
	}
	// A connector's row that says nothing of replies: no empty reply line.
	quiet := *items["imessage_dm"]
	quiet.Connector = &InboxConnector{Thread: quiet.Connector.Thread, OpenURL: quiet.Connector.OpenURL}
	golden(t, "inbox_agent.connector_no_reply", h.inbox.inboxAgentPrompt(&quiet, h.owner(ctx), nil))
}

// An item about one of the user's tasks: the judge's input lists the tasks, the AI twin is told the linked one.
func TestGoldenInboxTask(t *testing.T) {
	items := goldenItems(t)
	b := newInbox()
	task := prompts.InboxTask{ID: "todo_1", Title: "修登录崩溃", State: "在做", Last: "测试通过 42/42"}
	golden(t, "inbox_judge_input.with_task", prompts.RenderInboxJudgeInput(prompts.InboxJudgeInput{Summary: "张三问修复进度",
		Item: b.promptItem(items["dm"]), Tasks: []prompts.InboxTask{task}}))
	golden(t, "inbox_agent.with_task", b.inboxAgentPromptFor(items["dm"], "示例用户", nil, &task, 1, time.UTC))
}

// Relating a batch to events: the first call (rules, the connector's meta description, the events) and the call after
// one tool call.
func TestGoldenInboxGroup(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
	model, client := newGroupModel(t, `{"tool":"messages_before","args":{"message":"m1060","n":5}}`, groupAnswerOK)
	h, _ := groupHub(t, g, client)
	h.f.hello.Owner = "示例用户"
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()
	calls := model.asked()
	if len(calls) != 2 {
		t.Fatalf("%d calls", len(calls))
	}
	goldenCall(t, "inbox_group.first", calls[0])
	golden(t, "inbox_group.after_tool.user", calls[1].User)
}

func TestGoldenInboxFolders(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"first", "tree"} {
		h := goldenInbox(t)
		model, calls := captureModel(t, `{"folders":{}}`)
		h.in.model = model
		h.inbox.folderCfg = InboxFolderConfig{Enabled: true}
		at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
		h.inbox.items["mail:1"] = &InboxItem{ID: "mail:1", Kind: KindMail, Title: "示例服务告警：5xx 升高", Sender: "alerts@example.com",
			Summary: "示例服务 5xx 升高，值班请看", Preview: "告警：示例服务 5xx 升高\n详情见面板", Last: "9", UpdatedAt: at}
		if name == "tree" {
			h.f.hello.Owner = "示例用户"
			h.inbox.folderCfg.Rules = "构建机器人的消息放到 work/builds"
			h.setConfig(func(c *assistantwire.InboxConfig) { c.Rules = "老板的消息一律需要我" })
			h.inbox.items["lark:f1:1"] = &InboxItem{ID: "lark:f1:1", Kind: KindGroup, Title: "发布群", Last: "1", UpdatedAt: at,
				Folder: "projects/example/releases", FolderBy: FolderByUser}
			h.inbox.items["lark:f2:1"] = &InboxItem{ID: "lark:f2:1", Kind: KindBot, Title: "告警机器人", Last: "1", UpdatedAt: at,
				Folder: "work/alerts", FolderBy: FolderByModel}
		}
		h.fileFolders(ctx)
		goldenCall(t, "inbox_folders."+name, onlyCall(t, calls()))
	}
}
