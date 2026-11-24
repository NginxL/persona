package imessage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// "Hand to Persona" (#528; the user, 2026-11-03: iMessage's texts may go to AI like Feishu's): while the switch is on, the
// Mac's real conversations (KeepContacts) go into the knowledge base (connectorwire.Archive, by ROWID), unread ones into
// the inbox (connectorwire.Inbox, triaged like Feishu's), and the assistant reads them (ReadTools). Codes are masked in
// all of it. Off: none of it — nothing is read for Persona, the tools are gone; what the hub kept stays until the
// user deletes it (extmgr/imessage.go).

// SetAI turns "Hand to Persona" on or off (the hub keeps the choice).
func (c *Remote) SetAI(on bool) {
	c.ai.Store(on)
	if !on {
		c.inboxMu.Lock()
		c.things, c.contexts = nil, map[string]string{}
		c.inboxMu.Unlock()
	}
}

// AI says whether the messages go to Persona.
func (c *Remote) AI() bool { return c.ai.Load() }

// zone is the user's time zone (the hub sets Zone); never the machine's, which for a hub is UTC.
func (c *Remote) zone() *time.Location {
	if c.Zone != nil {
		if z := c.Zone(); z != nil {
			return z
		}
	}
	return time.UTC
}

// changesPage is how many messages one connector.changes call asks for.
const changesPage = 300

// Changes is connectorwire.Archive: the new messages of the conversations that go to Persona, as knowledge-base chats
// "imessage-<12 hex>" (the conversation's id) with people "imessage-<hash of their number>".
func (c *Remote) Changes(ctx context.Context, cursor string, _ int) ([]connectorwire.ChatBatch, string, bool, error) {
	if !c.AI() {
		return nil, cursor, false, nil
	}
	var page ChangesPage
	if err := c.read(ctx, "connector.changes", map[string]any{"id": ID, "cursor": cursor, "limit": changesPage,
		"days": 30, "keep": KeepContacts}, &page); err != nil {
		return nil, cursor, false, err
	}
	out := make([]connectorwire.ChatBatch, 0, len(page.Batches))
	for _, b := range page.Batches {
		out = append(out, kbBatch(b))
	}
	return out, page.Next, page.More, nil
}

func kbBatch(b Batch) connectorwire.ChatBatch {
	kind := "dm"
	if b.Chat.Group {
		kind = "group"
	}
	out := connectorwire.ChatBatch{Chat: kbwire.SourceChat{ID: strings.TrimPrefix(b.Chat.ID, ID+"-"), Name: b.Chat.Name, Kind: kind,
		UpdatedAt: b.Chat.Last}}
	for _, m := range b.Messages {
		sm := kbwire.SourceMessage{ID: m.ID, Position: m.ID, Mine: m.FromMe, Type: "TEXT", Text: lineText(m), At: m.At}
		if m.Kind == "event" {
			sm.Type = "SYSTEM"
		}
		if m.Sender != nil {
			sm.SenderID, sm.SenderName = PersonKey(m.Sender.Handle), m.Sender.shown()
			sm.SenderHandles = []kbwire.Handle{handleOf(m.Sender.Handle)} // hashed by the knowledge base, never kept as it is
		}
		out.Messages = append(out.Messages, sm)
	}
	return out
}

// handleOf is a Messages handle as an identity hint: an Apple ID's address or a number.
func handleOf(h string) kbwire.Handle {
	if strings.Contains(h, "@") {
		return kbwire.Handle{Kind: kbwire.HandleEmail, Value: h}
	}
	return kbwire.Handle{Kind: kbwire.HandlePhone, Value: h}
}

// PersonKey is a person's id in the knowledge base: a hash of their number or address, never the number itself.
func PersonKey(handle string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(handle))))
	return hex.EncodeToString(sum[:6])
}

// lineText is a message as one line of text: what it says, what it answers, what it carries.
func lineText(m Message) string {
	switch {
	case m.Kind == "event":
		return m.Text
	case m.Unsent:
		return "（撤回了一条消息）"
	case m.Undecoded:
		return "[无法显示的消息]"
	}
	text := m.Text
	for _, a := range m.Attachments {
		text = strings.TrimSpace(text + " " + a.describe())
	}
	if m.ReplyTo != "" {
		text = "回复「" + m.ReplyTo + "」：" + text
	}
	return text
}

func (a Attachment) describe() string {
	switch a.Kind {
	case "image":
		return "[图片]"
	case "video":
		return "[视频]"
	case "audio":
		return "[语音]"
	}
	if a.Name != "" {
		return "[文件 " + a.Name + "]"
	}
	return "[文件]"
}

// Collect is connectorwire.Inbox: the conversations Watch last saw (unread ones open or add to an item; one read
// elsewhere or answered closes it).
func (c *Remote) Collect(context.Context) ([]connectorwire.InboxThing, error) {
	if !c.AI() {
		return nil, nil
	}
	c.inboxMu.Lock()
	defer c.inboxMu.Unlock()
	return slices.Clone(c.things), nil
}

var errNoReply = errors.New("在「信息」里回复：Persona 不替你发信息")

// errNotHanded answers a read while the switch is off, for the assistant's tools and the subscription catalog alike.
var errNotHanded = errors.New("「信息」没有交给 Persona（设置 › 连接器 › 信息）")

// Reply: Persona does not send messages (the item's "Reply in Messages" opens Messages with the number).
func (c *Remote) Reply(context.Context, connectorwire.InboxReply) error { return errNoReply }

// How often Watch looks at the Mac for new messages, how many conversations it looks at, and how many of a
// conversation's last messages go to the inbox (triage, the AI twin).
var (
	watchPoll    = 2 * time.Minute
	watchChats   = 30
	contextLines = 15
)

// Watch is connectorwire.Events: every couple of minutes it lists the newest conversations, keeps the inbox's things
// from them and kicks the knowledge base when anything came in. A Mac that does not answer is waited for (the
// status says why), so the hub does not restart and log this every few minutes.
func (c *Remote) Watch(ctx context.Context, changed func(connectorwire.Scope)) error {
	for {
		if c.AI() && c.Status(ctx).State == connectorwire.StateOK {
			c.look(ctx, changed)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(watchPoll):
		}
	}
}

func (c *Remote) look(ctx context.Context, changed func(connectorwire.Scope)) {
	things, newest, err := c.rows(ctx)
	if err != nil {
		return
	}
	c.inboxMu.Lock()
	moved := !newest.Equal(c.newest)
	same := slices.EqualFunc(things, c.things, func(a, b connectorwire.InboxThing) bool {
		return a.Thread == b.Thread && a.Last == b.Last && a.Read == b.Read && a.Done == b.Done
	})
	c.newest = newest
	if c.AI() {
		c.things = things
	}
	c.inboxMu.Unlock()
	if moved {
		changed(connectorwire.ScopeChats)
	}
	if !same {
		changed(connectorwire.ScopeInbox)
	}
}

// rows are the inbox's things of the newest conversations, those of the last week, and when the newest one moved:
// what the watch keeps for Collect, and what inbox_things answers a things subscription, one computation (#6317).
func (c *Remote) rows(ctx context.Context) ([]connectorwire.InboxThing, time.Time, error) {
	var page ChatsPage
	if err := c.read(ctx, "connector.chats", map[string]any{"id": ID, "limit": watchChats, "keep": KeepContacts}, &page); err != nil {
		return nil, time.Time{}, err
	}
	var newest time.Time
	var things []connectorwire.InboxThing
	for _, chat := range page.Chats {
		if chat.Last.After(newest) {
			newest = chat.Last
		}
		if time.Since(chat.Last) > 7*24*time.Hour {
			continue
		}
		things = append(things, c.thing(ctx, chat))
	}
	return things, newest, nil
}

// thing is a conversation as the inbox sees it; an unread one carries its last messages for triage.
func (c *Remote) thing(ctx context.Context, chat Chat) connectorwire.InboxThing {
	last := strconv.FormatInt(chat.Last.UnixNano(), 10)
	t := connectorwire.InboxThing{Thread: chat.ID, Kind: "dm", Title: chat.Name, Sender: chat.Name, Preview: MaskCodes(chat.Preview),
		At: chat.Last, Last: last, ReplyTo: "在「信息」里回复（Persona 不替你发）", Tier: connectorwire.TierAction,
		Tags: []connectorwire.Tag{{Key: connectorwire.TagChat, Value: chat.ID, Name: chat.Name}}}
	if chat.Group {
		t.Kind, t.Label, t.Tier = "group", "群聊", connectorwire.TierFYI
		if s, _, ok := strings.Cut(t.Preview, "："); ok {
			t.Sender = s
		}
	}
	if !chat.Group && len(chat.Members) == 1 {
		t.OpenURL = "sms:" + chat.Members[0].Handle
	}
	switch {
	case chat.FromMe:
		t.Done = connectorwire.DoneReplied
	case chat.Unread == 0:
		t.Read = true
	default:
		t.Context = c.context(ctx, chat.ID, last)
	}
	return t
}

// context is a conversation's last messages as text, read once for each newest message.
func (c *Remote) context(ctx context.Context, chat, last string) string {
	key := chat + "\n" + last
	c.inboxMu.Lock()
	text, ok := c.contexts[key]
	c.inboxMu.Unlock()
	if ok {
		return text
	}
	var page MessagesPage
	if err := c.read(ctx, "connector.older", map[string]any{"id": ID, "chat": chat, "limit": contextLines, "keep": KeepContacts}, &page); err != nil {
		return ""
	}
	text = c.transcript(page.Messages)
	c.inboxMu.Lock()
	if len(c.contexts) > 200 {
		c.contexts = map[string]string{}
	}
	c.contexts[key] = text
	c.inboxMu.Unlock()
	return text
}

// transcript is messages as the models read them: "Name (10-30 08:00): …" with full-width punctuation, codes masked.
func (c *Remote) transcript(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		who := "我"
		if !m.FromMe {
			who = "有人"
			if m.Sender != nil {
				who = m.Sender.shown()
			}
		}
		if m.Kind == "event" {
			who = "（群）"
		}
		fmt.Fprintf(&b, "%s（%s）：%s\n", who, m.At.In(c.zone()).Format("01-02 15:04"), MaskCodes(lineText(m)))
	}
	return strings.TrimSpace(b.String())
}

var readTools = []string{"imessage_chats", "imessage_read", "imessage_search"}

// inboxThings is the catalog's tool a things subscription reads the Inbox's rows with (#6317): not one of the
// assistant's tools (Tools), which read conversations.
const inboxThings = "inbox_things"

var _ connectorwire.Catalog = (*Remote)(nil)

// Catalog is connectorwire.Catalog: the read tools as the derived catalog had them (the same names, descriptions and
// schemas, so a subscription keeps its tool), and inbox_things. None while the switch is off.
func (c *Remote) Catalog(context.Context) ([]connectorwire.CatalogSource, error) {
	src := connectorwire.CatalogSource{ID: ID, Name: c.Name(), Tools: []connectorwire.CatalogTool{}}
	tools := c.Tools()
	if len(tools) > 0 {
		t := prompts.Tool("imessage_inbox_things")
		tools = append(tools, proto.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	for _, t := range tools {
		src.Tools = append(src.Tools, connectorwire.CatalogTool{Name: strings.TrimPrefix(t.Name, ID+"_"), Description: t.Description,
			InputSchema: t.InputSchema, ReadOnly: true})
	}
	return []connectorwire.CatalogSource{src}, nil
}

// CatalogCall is connectorwire.Catalog: the read tools' JSON answers (CallJSON, as before), and inbox_things' rows.
func (c *Remote) CatalogCall(ctx context.Context, q connectorwire.CatalogQuery) (string, error) {
	if q.Tool == inboxThings {
		if !c.AI() {
			return "", errNotHanded
		}
		things, _, err := c.rows(ctx)
		if err != nil {
			return "", errors.New(c.explain(err))
		}
		if things == nil {
			things = []connectorwire.InboxThing{}
		}
		out, err := json.Marshal(map[string]any{"things": things})
		return string(out), err
	}
	out, ok, err := c.CallJSON(ctx, ID+"_"+q.Tool, q.Args)
	if !ok {
		return "", fmt.Errorf("unknown tool %s", q.Tool)
	}
	return string(out), err
}

// Tools is connectorwire.ReadTools: none while the switch is off.
func (c *Remote) Tools() []proto.Tool {
	if !c.AI() {
		return nil
	}
	out := make([]proto.Tool, 0, len(readTools))
	for _, n := range readTools {
		t := prompts.Tool(n)
		out = append(out, proto.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

// Call runs one of the read tools on the Mac's messages (the conversations that go to Persona, codes masked).
func (c *Remote) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	v, err := c.answer(ctx, name, args)
	if err != nil {
		return "", err
	}
	switch v := v.(type) {
	case ChatsPage:
		return c.chatsText(v), nil
	case MessagesPage:
		return c.messagesText(v), nil
	case hits:
		return c.hitsText(v.query, v.list), nil
	}
	return "", fmt.Errorf("unknown tool %s", name)
}

var _ connectorwire.ReadJSON = (*Remote)(nil)

// CallJSON is connectorwire.ReadJSON: the same answers for subscriptions, as JSON with an id on each item
// (persona_json.go).
func (c *Remote) CallJSON(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, bool, error) {
	if !slices.Contains(readTools, name) {
		return nil, false, nil
	}
	v, err := c.answer(ctx, name, args)
	if err != nil {
		return nil, true, err
	}
	var data any
	switch v := v.(type) {
	case ChatsPage:
		data = chatsData(v)
	case MessagesPage:
		data = messagesData(v)
	case hits:
		data = hitsData(v.list)
	}
	out, err := json.Marshal(data)
	return out, true, err
}

// hits is what imessage_search found.
type hits struct {
	query string
	list  []Hit
}

// answer is what a read tool found: a ChatsPage, a MessagesPage or hits.
func (c *Remote) answer(ctx context.Context, name string, args json.RawMessage) (any, error) {
	if !c.AI() {
		return nil, errNotHanded
	}
	var a struct {
		Chat   string `json:"chat"`
		Before string `json:"before"`
		Query  string `json:"query"`
		Limit  int    `json:"limit"`
		Days   int    `json:"days"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("arguments: %w", err)
		}
	}
	switch name {
	case "imessage_chats":
		var page ChatsPage
		if err := c.read(ctx, "connector.chats", map[string]any{"id": ID, "limit": clamp(a.Limit, 30, 100), "before": a.Before,
			"keep": KeepContacts}, &page); err != nil {
			return nil, errors.New(c.explain(err))
		}
		return page, nil
	case "imessage_read":
		chat := strings.TrimSpace(a.Chat)
		if !ValidChat(chat) {
			return nil, errors.New("chat must be a conversation id from imessage_chats or imessage_search (imessage-<12 hex>)")
		}
		var page MessagesPage
		if err := c.read(ctx, "connector.older", map[string]any{"id": ID, "chat": chat, "limit": clamp(a.Limit, 40, 200),
			"before": a.Before, "keep": KeepContacts}, &page); err != nil {
			return nil, errors.New(c.explain(err))
		}
		return page, nil
	case "imessage_search":
		if strings.TrimSpace(a.Query) == "" {
			return nil, errors.New("query is empty")
		}
		var res struct {
			Hits []Hit `json:"hits"`
		}
		if err := c.read(ctx, "connector.search", map[string]any{"id": ID, "query": a.Query, "limit": clamp(a.Limit, 20, 50),
			"days": clamp(a.Days, 365, 3650), "keep": KeepContacts}, &res); err != nil {
			return nil, errors.New(c.explain(err))
		}
		return hits{query: a.Query, list: res.Hits}, nil
	}
	return nil, fmt.Errorf("unknown tool %s", name)
}

func clamp(n, def, most int) int {
	if n <= 0 {
		return def
	}
	return min(n, most)
}

func (c *Remote) when(t time.Time) string { return t.In(c.zone()).Format("2006-01-02 15:04") }

func (c *Remote) chatsText(page ChatsPage) string {
	if len(page.Chats) == 0 {
		return "没有会话。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s 上「信息」的会话，最新的在前：\n", c.machine)
	for _, ch := range page.Chats {
		kind := "私聊"
		if ch.Group {
			var who []string
			for _, p := range ch.Members {
				who = append(who, p.shown())
			}
			kind = "群聊（" + strings.Join(who, "、") + "）"
		} else if len(ch.Members) == 1 && ch.Members[0].Name != "" {
			kind = "私聊 " + ch.Members[0].Handle
		}
		fmt.Fprintf(&b, "- %s [%s] · %s · %s", ch.Name, ch.ID, kind, c.when(ch.Last))
		if ch.Unread > 0 {
			fmt.Fprintf(&b, " · 未读 %d", ch.Unread)
		}
		if ch.Preview != "" {
			prefix := ""
			if ch.FromMe {
				prefix = "我："
			}
			fmt.Fprintf(&b, "：%s%s", prefix, MaskCodes(ch.Preview))
		}
		b.WriteString("\n")
	}
	if page.More {
		fmt.Fprintf(&b, "（还有更早的：before=%s）\n", page.Before)
	}
	return strings.TrimSpace(b.String())
}

func (c *Remote) messagesText(page MessagesPage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "会话「%s」[%s]，旧的在前：\n", page.Chat.Name, page.Chat.ID)
	if page.More {
		fmt.Fprintf(&b, "（还有更早的：before=%s）\n", page.Before)
	}
	for _, m := range page.Messages {
		who := "我"
		switch {
		case m.Kind == "event":
			who = "（群）"
		case !m.FromMe && m.Sender != nil:
			who = m.Sender.shown()
		case !m.FromMe:
			who = "对方"
		}
		fmt.Fprintf(&b, "[%s] %s：%s\n", c.when(m.At), who, MaskCodes(lineText(m)))
	}
	if len(page.Messages) == 0 {
		b.WriteString("（没有消息）\n")
	}
	return strings.TrimSpace(b.String())
}

func (c *Remote) hitsText(query string, hits []Hit) string {
	if len(hits) == 0 {
		return "没有找到含「" + query + "」的消息。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "含「%s」的消息，最新的在前：\n", query)
	for _, h := range hits {
		who := "我"
		if !h.Message.FromMe && h.Message.Sender != nil {
			who = h.Message.Sender.shown()
		}
		fmt.Fprintf(&b, "- [%s] 「%s」[%s] %s：%s\n", c.when(h.Message.At), h.Chat.Name, h.Chat.ID, who, MaskCodes(lineText(h.Message)))
	}
	return strings.TrimSpace(b.String())
}
