package extmgr

import (
	"context"

	"github.com/nwverse/persona/server/internal/channel"
)

// What happened to the Feishu bot, as plugin_audit records it (#5353): the plugin column holds the channel's id
// (channel.Feishu) and the actions a bot_ prefix. Who did it is the device (actorOf) for what the app changed, and the
// Feishu account (botChatActor) for what happened in the chat.
const (
	auditBotCredentials = "bot_credentials"  // {app_id, domain?, cleared?, owner_reset?}: never the secret
	auditBotEnabled     = "bot_enabled"      // {enabled}: only when it changes
	auditBotFeatures    = "bot_features"     // {features}: the switches, only when they change
	auditBotNudgeAfter  = "bot_nudge_after"  // {minutes}: nudge_after, only when it changes
	auditBotPairCode    = "bot_pair_code"    // a new pairing code was issued (never the code)
	auditBotPaired      = "bot_paired"       // {open_id}: whoever sent the code is the owner now
	auditBotPairGuessed = "bot_pair_guessed" // {fails}: the pairing was dropped after too many wrong codes
	auditBotUnpaired    = "bot_unpaired"     // the owner was removed

	auditBotSourceReportDelay     = "bot_source_report_delay"      // {minutes}: source-group preview window
	auditBotSourceReportFullReply = "bot_source_report_full_reply" // {enabled}: forward the final answer

	// {chat_id}: a group chat the owner opted in, or took out again (the /topics command, or the app's list).
	auditBotTopicChatAdded   = "bot_topic_chat_added"
	auditBotTopicChatRemoved = "bot_topic_chat_removed"
	// {chat_id}: the group chat that is the home topic group now (/topics home, or the app's list).
	auditBotTopicHome = "bot_topic_home"
	// {chat_id}: the owner's "@bot /ai …" was taken from a group chat that is not opted in (#6146). Never the words.
	auditBotAITaken = "bot_ai_taken"
	// {chat_id}: the owner's "@bot /read …" was taken from a group chat (#11803). Never the words.
	auditBotReadTaken = "bot_read_taken"
)

// botRecord is an audit record of the bot's, written once its lock is let go.
type botRecord struct {
	action string
	detail map[string]any
}

// auditBot records actions on the Feishu bot by whoever made the request.
func (h *Manager) auditBot(ctx context.Context, records ...botRecord) {
	for _, r := range records {
		h.audit(ctx, channel.Feishu, r.action, actorOf(ctx), r.detail)
	}
}

// botChatActor is the Feishu account that did something in the bot's chat.
func botChatActor(openID string) string { return "feishu:" + openID }
