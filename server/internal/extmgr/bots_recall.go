package extmgr

import (
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/botport"
)

// A message belongs to the bot and owner that received it, in one chat.
// The same id in another chat or after replacing the bot cannot recall it.
type botMessageRef struct {
	bot, domain     string
	owner, chat, id string
}

// The client pointer identifies the active connection generation. Header AppID
// is optional in older events, but when supplied must agree with that source.
// Message references themselves remain stable across same-app refreshes.
func (b *botHub) acceptBotEventLocked(api botport.Client, appID string) bool {
	return api != nil && b.set.Enabled && b.api == api && (appID == "" || appID == api.AppID())
}

// Credentials may be refreshed without changing the bot. Keep the identity
// stable across those reconnects, while separating apps and API domains.
func botMessageReference(api botport.Client, owner, chat, id string) botMessageRef {
	if api == nil {
		return botMessageRef{}
	}
	return botMessageRef{bot: api.AppID(), domain: api.Domain(), owner: owner, chat: chat, id: id}
}

type botQueuedMessage struct {
	ref      botMessageRef
	recalled bool
	handed   bool
}

// Unknown DM recalls still reach the assistant (they may precede delivery),
// but a known message from another context must not use that fallback: the
// assistant's recall payload carries only the message id.
func (b *botHub) messageInOtherContextLocked(ref botMessageRef) bool {
	for known := range b.queuedMessages {
		if known.id == ref.id && known != ref {
			return true
		}
	}
	for known := range b.recentMessages {
		if known.id == ref.id && known != ref {
			return true
		}
	}
	return false
}

// queueMessageLocked records ownership before the goroutine waits in line.
// This is recall evidence only: botMessage still applies the current receive
// policy after the wait, including topic commands and required mentions.
func (b *botHub) queueMessageLocked(ev botcard.MessageEvent) *botQueuedMessage {
	msg := ev.Message
	if b.api == nil || b.set.Owner == nil || ev.Sender.SenderType != "user" ||
		ev.Sender.SenderID.OpenID != b.set.Owner.OpenID ||
		!botChatAllowed(b.set, msg.ChatID) || msg.ChatType != "p2p" && msg.ThreadID == "" {
		return nil
	}
	ref := botMessageReference(b.api, b.set.Owner.OpenID, msg.ChatID, msg.MessageID)
	queued := &botQueuedMessage{ref: ref}
	if b.queuedMessages == nil {
		b.queuedMessages = make(map[botMessageRef]*botQueuedMessage)
	}
	b.queuedMessages[ref] = queued
	return queued
}

// beginBotHandoff and botRecalled share the lock: a recall either cancels a
// pending handoff or is forwarded to the assistant's existing recall handling.
func (h *Manager) beginBotHandoff(api botport.Client, ev botcard.MessageEvent, inThread bool) bool {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	ref := botMessageReference(api, ev.Sender.SenderID.OpenID, ev.Message.ChatID, ev.Message.MessageID)
	if b.api == nil || b.set.Owner == nil || ref != botMessageReference(b.api, b.set.Owner.OpenID, ev.Message.ChatID, ev.Message.MessageID) || b.recentMessages[ref] == botMessageCanceled {
		return false
	}
	if queued := b.queuedMessages[ref]; queued != nil {
		if queued.recalled {
			return false
		}
		queued.handed = true
	}
	if inThread {
		b.rememberBotMessageSourceLocked(ref, botMessageHandedGroup)
	} else if ev.Message.ChatType == "p2p" {
		b.rememberBotMessageSourceLocked(ref, botMessageHandedDM)
	}
	return true
}

// Canceled source evidence never vouches for an assistant handoff or delivery.
// All completed source evidence shares the existing bounded history, so normal
// later completions eventually evict canceled entries as well.
type botMessageState uint8

const (
	botMessageCanceled botMessageState = iota + 1
	botMessageHandedDM
	botMessageHandedGroup
)

func (b *botHub) rememberBotMessageSourceLocked(ref botMessageRef, state botMessageState) {
	if previous := b.recentMessages[ref]; state == botMessageCanceled && (previous == botMessageHandedDM || previous == botMessageHandedGroup) {
		return // canceling a duplicate cannot undo the original message's handoff
	}
	if b.recentMessages == nil {
		b.recentMessages = make(map[botMessageRef]botMessageState)
	}
	if _, exists := b.recentMessages[ref]; !exists {
		b.recentMessageOrder = append(b.recentMessageOrder, ref)
		if len(b.recentMessageOrder) > botMsgChatKeep {
			delete(b.recentMessages, b.recentMessageOrder[0])
			b.recentMessageOrder = b.recentMessageOrder[1:]
		}
	}
	b.recentMessages[ref] = state
}
