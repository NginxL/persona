package extmgr

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/feishubot"
)

// Reading a group with /read (#11803, docs/extensions/bots.md "Reading a group with /read"): alert and problem groups are
// where questions come up, and the question is about what was said there. With the read_group switch on, the owner's
// "@bot /read <task>" in any group the bot is in (opted in or not) is handed to the assistant marked
// assistantwire.FeishuFromRead. The assistant reads the group as the owner (lark-cli on its own computer), so the bot
// needs no permission to read a group's history: the hub only lets the request through and keeps the message as a
// source the answer goes under (BotAISource with Kind botSourceRead).
//
// As with "/ai", nothing else of such a group gets through, and the group does not become one the bot sends into: the
// source opens replies under that one message (feishu.post and feishu.text, at most botReadReplies), nothing more.

const (
	// botSourceRead is BotAISource.Kind of a message taken as the owner's "/read".
	botSourceRead = "read"
	// botReadReplies is how many replies the bot may write under a /read message: an answer split into cards (at most
	// five), a note on the pieces that did not get out, and a note on the files left out.
	botReadReplies = 8
)

// repliesMax is how many replies the bot may write under the source.
func (s BotAISource) repliesMax() int {
	if s.Kind == botSourceRead {
		return botReadReplies
	}
	return botSourceReplies
}

// botReadGroup takes the owner's "@bot /read …" from a group chat (the caller checked the sender is the owner and the
// group's own mention policy): with read_group on, a text or rich-text message (not a merged forward) that @-mentions
// the bot (told by its own open id) and whose words, the bot's leading @ taken off, start with /read (or /读群) as a
// word. It is kept as a source, audited (never the words) and handed over. false leaves the message to the usual
// rules, which is all that happens with the switch off: /read is then a word like any other.
func (h *Manager) botReadGroup(api *feishubot.Client, set FeishuBotSettings, ev feishubot.MessageEvent, raw json.RawMessage, since time.Time, pass func()) bool {
	msg := ev.Message
	if !set.Features.ReadGroup || msg.MessageType != "text" && msg.MessageType != "post" || msg.MessageID == "" || msg.ChatID == "" {
		return false
	}
	content := feishubot.ParseContent(msg.MessageType, msg.Content, msg.Mentions)
	if content.Forward {
		return false
	}
	// Cheap first, from the text alone: a group's ordinary messages cost no call.
	if _, ok := feishubot.ReadCommand(feishubot.StripLeadingMentions(content.Text, msg.Mentions, nil)); !ok {
		return false
	}
	botID := h.botOpenID(api)
	if botID == "" {
		return false // the bot cannot tell which @ is its own: nothing is taken
	}
	isBot := func(m feishubot.Mention) bool { return m.ID.OpenID == botID }
	if !slices.ContainsFunc(msg.Mentions, isBot) {
		return false
	}
	if _, ok := feishubot.ReadCommand(feishubot.StripLeadingMentions(content.Text, msg.Mentions, isBot)); !ok {
		return false
	}
	h.takeAISource(msg.ChatID, msg.MessageID, botSourceRead)
	h.audit(context.Background(), channel.Feishu, auditBotReadTaken, botChatActor(set.Owner.OpenID), map[string]any{"chat_id": msg.ChatID})
	h.handToAssistant(api, ev, raw, "", assistantwire.FeishuFromRead, since, pass, msg.ThreadID != "")
	return true
}

// readSourceReply says whether a feishu.post / feishu.text replies to a message the hub took as the owner's "/read",
// and so may go into that message's group: found is false when replyTo is no such message (the usual rules decide).
// For one that is, the call must name no chat but the source's; the chat to send into is returned. A reply whose
// original is gone is lost, never sent into the group as a message of its own (the caller sends with no_detour).
func readSourceReply(set FeishuBotSettings, chatID, replyTo string, now time.Time) (chat string, found bool, err error) {
	src := set.aiSource(replyTo, now)
	if src == nil || set.Owner == nil || src.Kind != botSourceRead {
		return "", false, nil
	}
	if chatID != "" && chatID != src.ChatID {
		return "", true, errBotSourceReply
	}
	return src.ChatID, true, nil
}
