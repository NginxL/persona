package larkcli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	adapter "github.com/nwverse/persona/server/internal/larkcli"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Thread preserves the source's message boundaries. Inbox context is a legacy
// text projection and cannot supply identity, direction or pagination for this page.
func (c *Connector) Thread(ctx context.Context, thread, before string, limit int) (connector.ThreadPage, error) {
	chatID, topic, err := conversationIDs(thread)
	if err != nil {
		return connector.ThreadPage{}, conversationError(err)
	}
	if limit <= 0 || limit > connector.ThreadMost {
		limit = connector.ThreadMost
	}
	auth, err := c.runner.AuthStatus(ctx)
	if err != nil {
		return connector.ThreadPage{}, conversationError(err)
	}
	if !auth.LoggedIn || auth.Expired {
		return connector.ThreadPage{}, conversationError(&adapter.Error{Kind: adapter.KindNotLoggedIn})
	}
	// The chat's own record is read beside the messages: a first open waits for one of the two reads, not both.
	record := make(chan chatRecord, 1)
	go func() { record <- c.conversationChat(ctx, chatID) }()
	source, err := c.runner.ListMessages(ctx, chatID, topic, before, limit)
	if err != nil {
		return connector.ThreadPage{}, conversationError(err)
	}
	page := connector.ThreadPage{Chat: connector.ThreadChat{ID: thread, Kind: ""}, Messages: []connector.ThreadMessage{}}
	seen := make(map[string]bool, len(source.Messages))
	for _, m := range source.Messages {
		if m.MessageID == "" || seen[m.MessageID] {
			continue
		}
		seen[m.MessageID] = true
		// Never display a response belonging to a different chat or topic.
		if m.ChatID != "" && m.ChatID != chatID {
			continue
		}
		if topic != "" && m.ThreadID != "" && m.ThreadID != topic {
			continue
		}
		if m.ChatType == "p2p" {
			page.Chat.Kind = "dm"
		} else if m.ChatType == "group" {
			page.Chat.Kind = "group"
		}
		if m.ChatName != "" {
			page.Chat.Name = m.ChatName
		}
		mine := auth.OpenID != "" && (m.Sender.IDType == "" || m.Sender.IDType == "open_id") && m.Sender.ID == auth.OpenID
		text := conversationText(m.Content)
		format := ""
		if m.MsgType == "post" || m.MsgType == "interactive" {
			format = "markdown"
		}
		out := connector.ThreadMessage{ID: m.MessageID, At: conversationTime(m.CreateTime), Sender: connector.ThreadPerson{ID: m.Sender.ID, Name: m.Sender.Name}, Mine: mine, Text: text, Format: format, Edited: m.Updated}
		// Existing Inbox items may predate stored AppLinks. Their loaded original
		// message can still supply its source link without recollecting history.
		link, linkErr := url.Parse(m.MessageAppLink)
		if linkErr == nil && link.Scheme == "https" && link.User == nil &&
			(link.Host == "applink.feishu.cn" || link.Host == "applink.larksuite.com") &&
			(link.Path == "/client/chat/open" || link.Path == "/client/thread/open") {
			out.URL = m.MessageAppLink
		}
		if m.Deleted {
			out.Text = ""
			out.Format = ""
			out.Event = true
		}
		if m.ParentID != "" {
			out.ReplyTo = &connector.ThreadQuote{ID: m.ParentID}
		}
		page.Messages = append(page.Messages, out)
	}
	if source.HasMore {
		page.Before = source.PageToken
	}
	r := <-record
	page.Chat.Kind = cmp.Or(page.Chat.Kind, r.kind)
	page.Chat.Name = cmp.Or(page.Chat.Name, r.name)
	if page.Chat.Kind == "dm" && page.Chat.Name == "" {
		// A direct chat's record has no name: it is the other person's, as the item's title is.
		for _, m := range page.Messages {
			if !m.Mine && !m.Event && m.Sender.Name != "" {
				page.Chat.Name = m.Sender.Name
				break
			}
		}
	}
	return page, nil
}

// chatRecord is what a chat's own record says of it: its kind as a conversation page has it ("dm", "group"; "" when
// it could not be read) and its name, until when that holds.
type chatRecord struct {
	kind, name string
	until      time.Time
}

// How long a chat's record is kept: its kind never changes, a renamed group shows its new name within the hour. A
// record that could not be read is asked for again a minute later.
const (
	chatRecordKeep  = time.Hour
	chatRecordRetry = time.Minute
)

// conversationChat is the chat's kind and name from its own record (lark-cli `im chats get`). The message list carries
// neither (lark-cli 1.0.98 to 1.0.100), and the apps show a page whose kind is not a chat's as the item's plain text:
// a Lark conversation opened on iOS and Android as all of its messages run together.
func (c *Connector) conversationChat(ctx context.Context, chatID string) chatRecord {
	now := c.currentTime()
	c.recordsMu.Lock()
	r, ok := c.chatRecords[chatID]
	c.recordsMu.Unlock()
	if ok && now.Before(r.until) {
		return r
	}
	r = chatRecord{until: now.Add(chatRecordRetry)}
	if chat, err := c.runner.GetChat(ctx, chatID); err == nil {
		switch chat.ChatMode {
		case "p2p":
			r.kind = "dm"
		case "group", "topic":
			r.kind = "group"
		}
		r.name = strings.TrimSpace(chat.Name)
		if r.kind != "" {
			r.until = now.Add(chatRecordKeep)
		}
	} else if ctx.Err() != nil {
		return chatRecord{} // the page was given up on: nothing learned, nothing kept
	}
	c.recordsMu.Lock()
	if c.chatRecords == nil {
		c.chatRecords = map[string]chatRecord{}
	}
	c.chatRecords[chatID] = r
	c.recordsMu.Unlock()
	return r
}

// Media is deliberately unavailable until the CLI attachment contract is wired.
// No media references are advertised by these text/card pages.
func (c *Connector) Media(context.Context, string) (connector.Media, error) {
	return connector.Media{}, errors.New("lark-cli conversation attachments are not available")
}

func conversationIDs(thread string) (chat, topic string, err error) {
	rest, ok := strings.CutPrefix(thread, "larkcli:chat:")
	if !ok {
		return "", "", fmt.Errorf("invalid lark-cli conversation")
	}
	chat, topic, split := strings.Cut(rest, ":thread:")
	if chat == "" || strings.ContainsAny(chat, ":\r\n\t ") || split && (topic == "" || strings.ContainsAny(topic, ":\r\n\t ")) {
		return "", "", fmt.Errorf("invalid lark-cli conversation")
	}
	return chat, topic, nil
}

func conversationTime(value string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t
	}
	// CLI 1.0.99's common.FormatTime emits local wall time without an offset.
	// Keep the CLI process's zone (cliLocation); interpreting it as UTC shifts the chat.
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, value, cliLocation()); err == nil {
			return t
		}
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	if n >= 1e12 {
		return time.UnixMilli(n).UTC()
	}
	return time.Unix(n, 0).UTC()
}

// The shortcut may nest a JSON string around {text: ...}. Retain unknown
// structures literally rather than flattening arbitrary object fields out of order.
func conversationText(raw json.RawMessage) string {
	var object struct {
		Text *string `json:"text"`
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if json.Unmarshal([]byte(text), &object) == nil && object.Text != nil {
			return *object.Text
		}
		return text
	}
	if json.Unmarshal(raw, &object) == nil && object.Text != nil {
		return *object.Text
	}
	return string(raw)
}

// Preserve a failed read's category even when the next auth-status call succeeds.
func conversationError(err error) error {
	code := ""
	switch adapter.KindOf(err) {
	case adapter.KindRateLimited:
		code = "rate_limited"
	case adapter.KindMissingPermission:
		code = "no_permission"
	case adapter.KindExpired, adapter.KindNotLoggedIn:
		code = "not_signed_in"
	}
	if code != "" {
		return &rpc.Error{Code: code, Message: err.Error()}
	}
	return err
}
