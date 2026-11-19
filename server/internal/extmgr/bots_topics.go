package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Topic group chats (#5279, docs/extensions/bots.md "Topic group chats"): the bot's chat with the owner is the private one, and
// anyone in a group could write to a bot in it, so a group is talked to only where the owner said so. The owner opts a
// group in (the /topics command in it, or the bot's settings), and from then on, in that group:
//
//   - a message reaches the assistant when five things all hold: the bot is paired, the sender is the owner, the chat
//     is on the list, the message is in a topic (it has a thread_id), and it is not the command below. Any other
//     message is dropped without a word: nobody is told the bot is there, and nothing is said in the group.
//   - the bot may send into the chat (botChatAllowed).
//
// A group message never changes where the owner's pushes go (BotOwner.ChatID), which stays the private chat.
//
// One more thing is taken from a group that is not on the list (#6146): the owner's "@bot /ai …". It is handed to the
// assistant marked assistantwire.FeishuFromElsewhere, and the message is kept as a source (BotAISource). The bot may
// then reply to that message, and write nothing else in that group: botChatAllowed is unchanged, the group does not
// become a chat the bot may send into, and the reply is a separate rule of feishu.text (aiSourceReply).

// BotChat is a group chat the owner opted in.
type BotChat struct {
	ChatID string `json:"chat_id"`
	Name   string `json:"name,omitempty"` // a label the app may set; the command in the chat leaves it empty
	// Home marks the home topic group (#6146): where a request from a group that is not opted in opens its topic. At
	// most one chat has it; with none marked, the first legacy enrollment is the home (homeChat).
	Home                bool `json:"home,omitempty"`
	RequireOwnerMention bool `json:"require_owner_mention,omitempty"`
	// Strict enrollments never acquire an implicit home, even after their policy is reversed.
	ExplicitHomeOnly bool      `json:"explicit_home_only,omitempty"`
	At               time.Time `json:"at"`
}

type botChatView struct {
	ChatID              string `json:"chat_id"`
	Name                string `json:"name,omitempty"`
	AddedAt             int64  `json:"added_at"`
	Home                bool   `json:"home,omitempty"` // the effective home topic group
	RequireOwnerMention bool   `json:"require_owner_mention"`
}

// BotAISource is a message the hub took as the owner's "@bot /ai …" in a group chat that is not opted in (#6146).
type BotAISource struct {
	ChatID    string    `json:"chat_id"`
	MessageID string    `json:"message_id"`
	At        time.Time `json:"at"`
	// Replies is how many replies the bot has been let write under it (botSourceReplies at most).
	Replies int `json:"replies,omitempty"`
}

const (
	// botAISourcesKeep bounds the list; the oldest go first. botAISourcesKeepFor drops the ones nobody replied to for good.
	botAISourcesKeep    = 500
	botAISourcesKeepFor = 90 * 24 * time.Hour
	// botSourceTextMax is the longest reply the bot may write under such a message: one paragraph, not a document.
	botSourceTextMax = 2000
	// botSourceReplies is how many replies the bot may write under one source: the one thing it has to say (a hint, a
	// failure note or the outcome) and one more for a try that must be made again.
	botSourceReplies = 2
)

// botTopicChatsMax bounds the list: it is the owner's own, so this only stops a runaway client.
const botTopicChatsMax = 50

// topicChat says whether chatID is a group chat the owner opted in.
func (s FeishuBotSettings) topicChat(chatID string) bool {
	return chatID != "" && slices.ContainsFunc(s.TopicChats, func(c BotChat) bool { return c.ChatID == chatID })
}

// homeChat uses an explicit home, else the first legacy enrollment. A strict enrollment alone never redirects other groups.
func (s FeishuBotSettings) homeChat() string {
	for _, c := range s.TopicChats {
		if c.Home {
			return c.ChatID
		}
	}
	for _, c := range s.TopicChats {
		if !c.ExplicitHomeOnly {
			return c.ChatID
		}
	}
	return ""
}

// aiSource is the source with that message id, nil when the hub took no such message or took it longer ago than
// botAISourcesKeepFor. A message id is unique, but the chat is kept with it: the reply goes into that chat and no other.
func (s FeishuBotSettings) aiSource(messageID string, now time.Time) *BotAISource {
	if messageID == "" {
		return nil
	}
	for i := range s.AISources {
		if s.AISources[i].MessageID == messageID && now.Sub(s.AISources[i].At) <= botAISourcesKeepFor {
			return &s.AISources[i]
		}
	}
	return nil
}

// checkTopicChats says whether the list a client sent is one the hub keeps: ids that look like a chat id, no repeats.
func checkTopicChats(list []BotChat) error {
	if len(list) > botTopicChatsMax {
		return i18n.Err("hub-bots.topic_chats_many", i18n.Args{"n": botTopicChatsMax})
	}
	seen := map[string]bool{}
	homes := 0
	for _, c := range list {
		id := strings.TrimSpace(c.ChatID)
		if id == "" || len(id) > 100 || strings.ContainsFunc(id, unicode.IsSpace) || seen[id] {
			return i18n.Err("hub-bots.topic_chat_bad")
		}
		seen[id] = true
		if c.Home {
			homes++
		}
	}
	if homes > 1 {
		return i18n.Err("hub-bots.topic_chat_home_many")
	}
	return nil
}

var errNoTopicOwner = i18n.Err("hub-bots.topic_needs_owner")

// topicChatsPossible says whether a PUT that carries a list can keep it: the bot has an owner now, and the same request
// does not change the App ID (which unpairs the owner, and the list with it).
func (h *Manager) topicChatsPossible(ctx context.Context, appID *string) error {
	b := &h.bots
	b.mu.Lock()
	h.loadBotsLocked(ctx)
	owner, saved := b.set.Owner != nil, b.set.AppID
	b.mu.Unlock()
	if !owner {
		return errNoTopicOwner
	}
	if appID != nil && strings.TrimSpace(*appID) != saved {
		return i18n.Err("hub-bots.topic_chats_new_app")
	}
	return nil
}

// setTopicChats replaces the owner's list (PUT /api/v1/bots/feishu): the chats that stay keep the time they were
// added, new ones get now. keepHome: the request said nothing about the home group, so the one the list had stays
// (when it is still in the list). A bot with no owner has no list to keep.
func (h *Manager) setTopicChats(ctx context.Context, want []BotChat, keepHome bool, mentionEdits map[string]bool) error {
	b := &h.bots
	b.mu.Lock()
	h.loadBotsLocked(ctx)
	if b.set.Owner == nil {
		nothing := len(want) == 0 && len(b.set.TopicChats) == 0 // read under the lock
		b.mu.Unlock()
		if nothing {
			return nil
		}
		return errNoTopicOwner
	}
	before := b.set.TopicChats
	next := make([]BotChat, 0, len(want))
	for _, c := range want {
		c.ChatID, c.Name = strings.TrimSpace(c.ChatID), strings.TrimSpace(c.Name)
		c.At = b.clock().UTC()
		existing := slices.IndexFunc(before, func(o BotChat) bool { return o.ChatID == c.ChatID })
		if i := existing; i >= 0 {
			c.At = before[i].At
			c.RequireOwnerMention = before[i].RequireOwnerMention
			c.ExplicitHomeOnly = before[i].ExplicitHomeOnly
			if keepHome {
				c.Home = before[i].Home
			}
		}
		if required, ok := mentionEdits[c.ChatID]; ok {
			c.RequireOwnerMention = required
		}
		if existing < 0 {
			c.ExplicitHomeOnly = c.RequireOwnerMention
		}
		next = append(next, c)
	}
	records := topicRecords(before, next)
	if was, now := markedHome(before), markedHome(next); now != "" && now != was {
		records = append(records, botRecord{auditBotTopicHome, map[string]any{"chat_id": now}})
	}
	b.set.TopicChats = next
	if len(records) > 0 || !slices.EqualFunc(before, next, func(a, b BotChat) bool { return a == b }) {
		if err := h.saveBotSettingsLocked(); err != nil {
			b.set.TopicChats = before
			b.mu.Unlock()
			return err
		}
	}
	b.mu.Unlock()
	h.auditBot(ctx, records...)
	return nil
}

// markedHome is the chat marked home in the list, "" for none.
func markedHome(list []BotChat) string {
	for _, c := range list {
		if c.Home {
			return c.ChatID
		}
	}
	return ""
}

// topicRecords are the audit records for the chats that came and went between two lists.
func topicRecords(before, after []BotChat) []botRecord {
	var out []botRecord
	for _, c := range after {
		if !slices.ContainsFunc(before, func(o BotChat) bool { return o.ChatID == c.ChatID }) {
			out = append(out, botRecord{auditBotTopicChatAdded, map[string]any{"chat_id": c.ChatID}})
		}
	}
	for _, c := range before {
		if !slices.ContainsFunc(after, func(o BotChat) bool { return o.ChatID == c.ChatID }) {
			out = append(out, botRecord{auditBotTopicChatRemoved, map[string]any{"chat_id": c.ChatID}})
		}
	}
	return out
}

// The commands /topics takes: it opts the group in, out, or makes it the home topic group (opting it in if it is not).
const (
	topicsOn   = "on"
	topicsOff  = "off"
	topicsHome = "home"
)

// topicsCmd reads the owner's message as the command that opts a group in or out: /topics or /话题群 (a full-width
// slash too), alone or with "on" / "off" (关) or "home" (主群 in Chinese), after the @ of the bot that a group
// message may start with. ok is false for any other text, which is then an ordinary message.
func topicsCmd(text string, mentions []feishubot.Mention) (cmd string, ok bool) {
	t := strings.TrimSpace(text)
	for stripped := true; stripped; {
		stripped = false
		for _, m := range mentions {
			for _, tok := range []string{m.Key, "@" + m.Name} {
				if tok != "" && tok != "@" && strings.HasPrefix(t, tok) {
					t, stripped = strings.TrimSpace(strings.TrimPrefix(t, tok)), true
				}
			}
		}
	}
	if after, found := strings.CutPrefix(t, "／"); found { // i18n-ignore: the full-width slash
		t = "/" + after
	}
	word, ok := strings.CutPrefix(t, "/")
	if !ok {
		return "", false
	}
	fields := strings.Fields(word)
	if len(fields) == 0 || len(fields) > 2 {
		return "", false
	}
	switch strings.ToLower(fields[0]) {
	case "topics", "话题群": // i18n-ignore: the command's own name
	default:
		return "", false
	}
	switch arg := strings.ToLower(strings.Join(fields[1:], "")); arg {
	case "", "on", "开": // i18n-ignore: the command's own word
		return topicsOn, true
	case "off", "关", "关闭": // i18n-ignore: the command's own words
		return topicsOff, true
	case "home", "主群": // i18n-ignore: the command's own words
		return topicsHome, true
	}
	return "", false
}

// botGroupMessage takes a message from a group chat (not the bot's private chat): see the five conditions above, and
// the owner's "@bot /ai …" from a group that is not on the list (botAIElsewhere). Every one that fails is a plain return:
// no note, no reaction, nothing in the chat.
func (h *Manager) botGroupMessage(api *feishubot.Client, set FeishuBotSettings, ev feishubot.MessageEvent, raw json.RawMessage, since time.Time, pass func()) {
	msg := ev.Message
	if set.Owner == nil || ev.Sender.SenderID.OpenID != set.Owner.OpenID || msg.ChatID == "" {
		return // not paired, or not the owner: in a group anyone could write to the owner's Persona
	}
	// Apply the group's current policy before settings commands or any topic side effect.
	if i := slices.IndexFunc(set.TopicChats, func(c BotChat) bool { return c.ChatID == msg.ChatID }); i >= 0 && set.TopicChats[i].RequireOwnerMention {
		botID := h.botOpenID(api)
		if botID == "" || !slices.ContainsFunc(msg.Mentions, func(m feishubot.Mention) bool { return m.ID.OpenID == botID }) {
			return
		}
	}
	if msg.MessageType == "text" || msg.MessageType == "post" {
		content := feishubot.ParseContent(msg.MessageType, msg.Content, msg.Mentions)
		if cmd, ok := topicsCmd(content.Text, msg.Mentions); ok && !content.Forward {
			// The list is changed before the next message is let go: the one right after /topics off must not pass the
			// gate, and the one right after /topics must.
			key, recs := h.applyTopicsCommand(set.Owner.OpenID, msg.ChatID, cmd)
			pass()
			if key == "" {
				return // unpaired meanwhile
			}
			for _, rec := range recs {
				h.audit(context.Background(), channel.Feishu, rec.action, botChatActor(set.Owner.OpenID), rec.detail)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			h.botNoteIn(ctx, api, msg.ChatID, msg.MessageID, i18n.T(h.userLang(), key), msg.ThreadID != "")
			return // the hub's own: it never goes to the assistant
		}
	}
	if !set.topicChat(msg.ChatID) {
		h.botAIElsewhere(api, set, ev, raw, since, pass)
		return
	}
	if msg.ThreadID == "" {
		return
	}
	// No action: a message in a topic answers no card (the assistant does not look for one), so none is issued.
	h.handToAssistant(api, ev, raw, "", "", since, pass, true)
}

// botAIElsewhere takes the owner's "@bot /ai …" from a group chat that is not on the list (#6146, docs/extensions/bots.md "Asking
// from any group"): the one thing such a group gets through. The owner's message (the caller checked), a text or
// rich-text message (a merged forward is nothing to run), that @-mentions the bot, whose words, the leading @ of the bot
// taken off, start with /ai. It is kept as a source (the bot may reply to it, aiSourceReply), audited, and handed to the
// assistant marked FeishuFromElsewhere. Anything else is dropped without a word, as every message of such a group was.
func (h *Manager) botAIElsewhere(api *feishubot.Client, set FeishuBotSettings, ev feishubot.MessageEvent, raw json.RawMessage, since time.Time, pass func()) {
	msg := ev.Message
	if msg.MessageType != "text" && msg.MessageType != "post" || msg.MessageID == "" {
		return
	}
	content := feishubot.ParseContent(msg.MessageType, msg.Content, msg.Mentions)
	if content.Forward {
		return
	}
	// Cheap first, from the text alone: what does not start with /ai (after the @s it starts with) is not asked about the
	// bot at all, so a group's ordinary messages cost no call.
	if _, ok := feishubot.AICommand(feishubot.StripLeadingMentions(content.Text, msg.Mentions, nil)); !ok {
		return
	}
	botID := h.botOpenID(api)
	if botID == "" {
		return // the bot cannot tell which @ is its own: nothing is taken
	}
	isBot := func(m feishubot.Mention) bool { return m.ID.OpenID == botID }
	if !slices.ContainsFunc(msg.Mentions, isBot) {
		return
	}
	if _, ok := feishubot.AICommand(feishubot.StripLeadingMentions(content.Text, msg.Mentions, isBot)); !ok {
		return
	}
	h.takeAISource(msg.ChatID, msg.MessageID)
	h.audit(context.Background(), channel.Feishu, auditBotAITaken, botChatActor(set.Owner.OpenID), map[string]any{"chat_id": msg.ChatID})
	h.handToAssistant(api, ev, raw, "", assistantwire.FeishuFromElsewhere, since, pass, false)
}

// botOpenID is the bot's own open id, which tells its @ from anyone else's: read at start-up, and once more here when
// that did not work.
func (h *Manager) botOpenID(api *feishubot.Client) string {
	b := &h.bots
	b.mu.Lock()
	id := b.info.OpenID
	b.mu.Unlock()
	if id != "" {
		return id
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := api.BotInfo(ctx)
	if err != nil {
		h.logf("bots: bot info: %v", err)
		return ""
	}
	b.mu.Lock()
	if b.api == api {
		b.info = info
	}
	b.mu.Unlock()
	return info.OpenID
}

// useAISourceReply counts a reply under the source (at most botSourceReplies) before it is made: false when it has had
// them all (or is gone), and then nothing is sent. A reply that fails still counts: the bot does not keep writing under
// a message in a group it is not in charge of.
func (h *Manager) useAISourceReply(messageID string, fullReply bool) bool {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	src := b.set.aiSource(messageID, b.clock().UTC())
	if b.set.Owner == nil || src == nil || src.Replies >= botSourceReplies || fullReply && !b.set.SourceReportFullReply {
		return false
	}
	src.Replies++
	h.saveBotSettingsLocked()
	return true
}

// takeAISource keeps the message as one the bot may reply to (the same message again changes nothing), dropping the
// oldest past botAISourcesKeep and the ones older than botAISourcesKeepFor.
func (h *Manager) takeAISource(chatID, messageID string) {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.set.Owner == nil || b.set.aiSource(messageID, b.clock().UTC()) != nil {
		return
	}
	now := b.clock().UTC()
	kept := slices.DeleteFunc(slices.Clone(b.set.AISources), func(s BotAISource) bool { return now.Sub(s.At) > botAISourcesKeepFor })
	kept = append(kept, BotAISource{ChatID: chatID, MessageID: messageID, At: now})
	if len(kept) > botAISourcesKeep {
		kept = kept[len(kept)-botAISourcesKeep:]
	}
	b.set.AISources = kept
	h.saveBotSettingsLocked()
}

// errBotSourceReply: a reply under a message taken through /ai that the bot may not write.
var errBotSourceReply = &rpc.Error{Code: assistantwire.CodeChatDenied, Message: "feishu: the bot may only reply to the original /ai message in its chat using the configured reply format"}

// aiSourceReply says whether a feishu.text is a reply to a message the hub took as the owner's "/ai" in a group that is
// not on the list, and so may go there: found is false when replyTo is no such message (the usual rules decide). For one
// that is, the reply must name no chat but its message's, respect the owner's full answer preference, and never detour
// into the group if the original is gone. This is all that is open in such a group: a send into
// it, a card, a reply to any other message, or a reply with a chat of its own is refused as ever.
func aiSourceReply(set FeishuBotSettings, chatID, replyTo, text string, now time.Time, fullReply bool) (chat string, found bool, err error) {
	src := set.aiSource(replyTo, now)
	if src == nil || set.Owner == nil {
		return "", false, nil
	}
	if chatID != "" && chatID != src.ChatID || !fullReply && utf8.RuneCountInString(text) > botSourceTextMax ||
		fullReply && !set.SourceReportFullReply {
		return "", true, errBotSourceReply
	}
	return src.ChatID, true, nil
}

// applyTopicsCommand opts the group chat in or out for the owner, or makes it the home topic group (whose open id it
// is, checked by the caller) and returns the key of the line that says what came of it, with the audit records when the
// list changed; "" when the owner is gone or another by now.
func (h *Manager) applyTopicsCommand(ownerID, chatID, cmd string) (key string, recs []botRecord) {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	cur := &b.set
	if cur.Owner == nil || cur.Owner.OpenID != ownerID {
		return "", nil
	}
	has := cur.topicChat(chatID)
	added := botRecord{auditBotTopicChatAdded, map[string]any{"chat_id": chatID}}
	home := botRecord{auditBotTopicHome, map[string]any{"chat_id": chatID}}
	switch {
	case cmd == topicsOn && has:
		return "hub-bots_chat.topics_already", nil
	case (cmd == topicsOn || cmd == topicsHome) && !has && len(cur.TopicChats) >= botTopicChatsMax:
		return "hub-bots_chat.topics_full", nil
	case cmd == topicsOn:
		cur.TopicChats = append(slices.Clone(cur.TopicChats), BotChat{ChatID: chatID, At: b.clock().UTC()})
		h.saveBotSettingsLocked()
		return "hub-bots_chat.topics_on", []botRecord{added}
	case cmd == topicsHome && has && markedHome(cur.TopicChats) == chatID:
		return "hub-bots_chat.topics_home_already", nil
	case cmd == topicsHome:
		// Opted in with it when it was not on: the owner said so in so many words, and the group is the owner's own pick.
		list := slices.Clone(cur.TopicChats)
		for i := range list {
			list[i].Home = list[i].ChatID == chatID
		}
		key, recs = "hub-bots_chat.topics_home", []botRecord{home}
		if !has {
			list = append(list, BotChat{ChatID: chatID, Home: true, At: b.clock().UTC()})
			key, recs = "hub-bots_chat.topics_on_home", []botRecord{added, home}
		}
		cur.TopicChats = list
		h.saveBotSettingsLocked()
		return key, recs
	case has: // off
		cur.TopicChats = slices.DeleteFunc(slices.Clone(cur.TopicChats), func(c BotChat) bool { return c.ChatID == chatID })
		h.saveBotSettingsLocked()
		return "hub-bots_chat.topics_off", []botRecord{{auditBotTopicChatRemoved, map[string]any{"chat_id": chatID}}}
	}
	return "hub-bots_chat.topics_not_on", nil
}

// ---- which chat a message is in ----

// botMsgChatKeep is how many message ids the hub remembers the chat of.
const botMsgChatKeep = 500

// idRing is a bounded set of message ids with what is known of each, oldest dropped first. Held under botHub.mu.
type idRing struct {
	m     map[string]string
	order []string
}

func (r *idRing) put(id, v string) {
	if r.m == nil {
		r.m = map[string]string{}
	}
	if _, ok := r.m[id]; !ok {
		r.order = append(r.order, id)
		if len(r.order) > botMsgChatKeep {
			delete(r.m, r.order[0])
			r.order = r.order[1:]
		}
	}
	r.m[id] = v
}

// rememberMessageChat notes the chat a message the bot may use is in (the owner's own messages in the DM and in a
// listed group, and what the bot itself sent there), for checkReplyParent. Messages of anyone else, and of chats the
// bot may not use, are not kept: they would only push out the ones a reply goes to.
func (h *Manager) rememberMessageChat(id, chatID string) {
	if id == "" || chatID == "" {
		return
	}
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	b.msgChat.put(id, chatID)
}

func (h *Manager) messageChat(id string) string {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.msgChat.m[id]
}

// rememberHandedGroup notes a group message handed to the assistant: only a recall of one of these is passed on.
func (h *Manager) rememberHandedGroup(id string) {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handedGroup.put(id, "")
}

func (h *Manager) handedGroupMessage(id string) bool {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.handedGroup.m[id]
	return ok
}

var errBotReplyChat = &rpc.Error{Code: assistantwire.CodeChatDenied, Message: "feishu: a reply goes into the chat of the message it answers, and that chat is not one the bot may send into"}

// checkReplyParent refuses a reply whose parent message is in a chat the bot may not send into: a reply lands in the
// parent's chat whatever chat_id says. It returns that chat. The chat is the one remembered for the message, else the
// open platform's answer (read again by the client when it is busy or the network blips; then remembered, if it is a
// chat the bot may use). A parent that is withdrawn or deleted cannot be replied to at all, so the send falls back to
// the chat the call names (replyOrSend), which botSendChat checked: the chat returned is "" then. A chat that cannot be
// told is refused. With no parent there is nothing to check.
func (h *Manager) checkReplyParent(ctx context.Context, api *feishubot.Client, set FeishuBotSettings, replyTo string) (string, error) {
	if replyTo == "" {
		return "", nil
	}
	chat := h.messageChat(replyTo)
	if chat == "" {
		items, err := api.Message(ctx, replyTo)
		if feishubot.MessageGone(err) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("feishu: could not tell which chat message %s is in: %w", replyTo, err)
		}
		for _, it := range items {
			if it.MessageID == replyTo {
				chat = it.ChatID
			}
		}
		if chat == "" {
			return "", fmt.Errorf("feishu: could not tell which chat message %s is in", replyTo)
		}
		if botChatAllowed(set, chat) {
			h.rememberMessageChat(replyTo, chat)
		}
	}
	if !botChatAllowed(set, chat) {
		return "", errBotReplyChat
	}
	return chat, nil
}
