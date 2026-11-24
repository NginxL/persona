package larkcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
	adapter "github.com/nwverse/persona/server/internal/modules/lark/larkcli"
)

const parkReplyPages = 4

// ObserveReply reads only the exact bound topic and returns identity/time receipts, never message content.
func (c *Connector) ObserveReply(ctx context.Context, watch connectorwire.LarkReplyWatch) (connectorwire.LarkReplyObservation, error) {
	var out connectorwire.LarkReplyObservation
	if c == nil || c.runner == nil {
		return out, errors.New("lark reply reader unavailable")
	}
	if err := validateReplyWatch(watch); err != nil {
		return out, err
	}
	before, err := c.replyAuth(ctx, watch)
	if err != nil {
		return out, err
	}
	seenTokens := map[string]bool{}
	seenMessages := map[string]bool{}
	var request, latest connectorwire.LarkReplyMessage
	var replies []connectorwire.LarkReplyMessage
	token := ""
	for pageNo := 0; pageNo < parkReplyPages; pageNo++ {
		page, err := c.runner.ListMessages(ctx, watch.ChatID, watch.ThreadID, token, 50)
		if err != nil {
			return out, fmt.Errorf("read bound Lark thread: %w", err)
		}
		for _, msg := range page.Messages {
			if msg.MessageID == "" || seenMessages[msg.MessageID] {
				continue
			}
			seenMessages[msg.MessageID] = true
			verified, ok := verifyReplyScope(msg, watch)
			if !ok || msg.Deleted || (msg.MsgType != "text" && msg.MsgType != "post") || forwardedContent(msg.Content) {
				continue
			}
			stamp, ok := parseReplyTime(msg.CreateTime, c.currentTime())
			if !ok {
				// An unparseable timestamp on a candidate identity cannot safely establish ordering.
				if msg.MessageID == watch.RequestMessageID || isReplySender(msg.Sender, watch) {
					return out, errors.New("Lark message time is unknown or in the future")
				}
				continue
			}
			receipt := connectorwire.LarkReplyMessage{
				ID: msg.MessageID, SenderID: msg.Sender.ID, SenderType: msg.Sender.SenderType,
				IDType: msg.Sender.IDType, ChatID: verified.chat,
				ThreadID: verified.thread, URL: msg.MessageAppLink, At: stamp.at, Before: stamp.before,
			}
			if msg.Sender.SenderType == "app" {
				receipt.AppID = msg.Sender.AppID
			}
			if msg.MessageID == watch.RequestMessageID {
				if !isRequestSender(msg.Sender, watch) {
					return out, errors.New("bound Lark request message sender does not match its account")
				}
				request = receipt
			}
			if isReplySender(msg.Sender, watch) {
				if !validOpaqueID(msg.MessageID) || !validOpaqueID(msg.Sender.ID) {
					return out, errors.New("Lark reply has an invalid source identity")
				}
				replies = append(replies, receipt)
			}
		}
		if !page.HasMore {
			break
		}
		if page.PageToken == "" || seenTokens[page.PageToken] {
			return out, errors.New("Lark thread pagination did not advance")
		}
		seenTokens[page.PageToken] = true
		token = page.PageToken
		if pageNo == parkReplyPages-1 {
			return out, errors.New("Lark thread exceeds bounded read")
		}
	}
	after, err := c.replyAuth(ctx, watch)
	if err != nil {
		return out, err
	}
	if before != after {
		return out, errors.New("Lark account identity changed during read")
	}
	checked := c.currentTime().UTC()
	if !request.At.IsZero() && request.Before.After(checked) {
		return out, errors.New("bound Lark request time is not safely in the past")
	}
	for _, reply := range replies {
		if reply.Before.After(checked) {
			return out, errors.New("Lark reply time is not safely in the past")
		}
	}
	latest = selectReplyCandidate(replies)
	out = connectorwire.LarkReplyObservation{Watch: watch, Request: request, Reply: latest, CheckedAt: checked}
	return out, nil
}

func validateReplyWatch(w connectorwire.LarkReplyWatch) error {
	if w.Source != "larkcli" || (w.Brand != "feishu" && w.Brand != "lark") || !validOpaqueID(w.AppID) || !validOpaqueID(w.AccountOpenID) ||
		!validOpaqueID(w.PersonOpenID) || !validOpaqueID(w.ChatID) || !validOpaqueID(w.ThreadID) || !validOpaqueID(w.RequestMessageID) ||
		w.AccountOpenID == w.PersonOpenID {
		return errors.New("incomplete or invalid Lark reply scope")
	}
	return nil
}

func validOpaqueID(value string) bool {
	if len(value) == 0 || len(value) > 200 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

type replyIdentity struct{ brand, appID, account string }

func (c *Connector) replyAuth(ctx context.Context, watch connectorwire.LarkReplyWatch) (replyIdentity, error) {
	auth, err := c.runner.AuthStatus(ctx)
	if err != nil {
		return replyIdentity{}, fmt.Errorf("read Lark account identity: %w", err)
	}
	got := replyIdentity{brand: auth.Brand, appID: auth.AppID, account: auth.OpenID}
	if !auth.LoggedIn || auth.Expired || got.brand != watch.Brand || got.appID != watch.AppID || got.account != watch.AccountOpenID {
		return replyIdentity{}, errors.New("Lark account is not signed in with the bound identity")
	}
	return got, nil
}

type verifiedReplyScope struct{ chat, thread string }

func verifyReplyScope(m adapter.Message, w connectorwire.LarkReplyWatch) (verifiedReplyScope, bool) {
	chat, thread := m.ChatID, m.ThreadID
	if (chat != "" && chat != w.ChatID) || (thread != "" && thread != w.ThreadID) {
		return verifiedReplyScope{}, false
	}
	linkChat, linkThread, linkOK := replyAppLink(m.MessageAppLink, w.Brand)
	if m.MessageAppLink != "" && !linkOK {
		return verifiedReplyScope{}, false
	}
	if m.MessageID == w.RequestMessageID && !linkOK {
		return verifiedReplyScope{}, false
	}
	if linkOK && (linkChat != w.ChatID || linkThread != w.ThreadID) {
		return verifiedReplyScope{}, false
	}
	if chat == "" {
		if !linkOK {
			return verifiedReplyScope{}, false
		}
		chat = linkChat
	}
	if thread == "" {
		if !linkOK {
			return verifiedReplyScope{}, false
		}
		thread = linkThread
	}
	return verifiedReplyScope{chat: chat, thread: thread}, true
}

func replyAppLink(raw, brand string) (string, string, bool) {
	u, err := url.Parse(raw)
	host := ""
	switch brand {
	case "feishu":
		host = "applink.feishu.cn"
	case "lark":
		host = "applink.larksuite.com"
	}
	if err != nil || host == "" || u.Scheme != "https" || u.User != nil || u.Fragment != "" ||
		u.Host != host || u.Path != "/client/thread/open" {
		return "", "", false
	}
	query := u.Query()
	if len(query["open_chat_id"]) != 1 || len(query["open_thread_id"]) != 1 {
		return "", "", false
	}
	chat, thread := query.Get("open_chat_id"), query.Get("open_thread_id")
	return chat, thread, chat != "" && thread != ""
}

func isRequestSender(s adapter.Sender, w connectorwire.LarkReplyWatch) bool {
	if s.SenderType == "user" && s.IDType == "open_id" && s.ID == w.AccountOpenID && s.AppID == "" && validOpaqueID(s.ID) {
		return true
	}
	return s.SenderType == "app" && validOpaqueID(s.ID) &&
		((s.IDType == "app_id" && s.ID == w.AppID) || (validOpaqueID(s.AppID) && s.AppID == w.AppID))
}

func isReplySender(s adapter.Sender, w connectorwire.LarkReplyWatch) bool {
	return s.SenderType == "user" && s.IDType == "open_id" && s.ID == w.PersonOpenID && s.ID != w.AccountOpenID && s.AppID == "" && validOpaqueID(s.ID)
}

func forwardedContent(raw json.RawMessage) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	// CLI shortcuts also encode their content object inside one JSON string.
	if encoded, ok := value.(string); ok {
		var content any
		if json.Unmarshal([]byte(encoded), &content) == nil {
			if _, ambiguous := content.(string); ambiguous {
				return true
			}
			value = content
		}
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for key, child := range x {
				k := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
				if k == "forward" || k == "merge_forward" || k == "forwarded_message" || walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range x {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	return walk(value)
}

type replyStamp struct{ at, before time.Time }

func parseReplyTime(raw string, now time.Time) (replyStamp, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return replyStamp{}, false
	}
	var at time.Time
	var width time.Duration
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		at = t.UTC()
		fraction := 0
		if i := strings.IndexByte(s, '.'); i >= 0 {
			for j := i + 1; j < len(s) && s[j] >= '0' && s[j] <= '9'; j++ {
				fraction++
			}
		}
		width = time.Second
		if fraction > 0 {
			width = time.Duration(1) * time.Nanosecond * time.Duration(pow10(9-fraction))
		}
	} else if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		if len(s) == 13 {
			at, width = time.UnixMilli(n).UTC(), time.Millisecond
		} else if len(s) == 10 {
			at, width = time.Unix(n, 0).UTC(), time.Second
		} else {
			return replyStamp{}, false
		}
	} else {
		for _, layout := range []struct {
			value string
			width time.Duration
		}{{"2006-01-02 15:04:05", time.Second}, {"2006-01-02 15:04", time.Minute}} {
			if t, err := time.ParseInLocation(layout.value, s, cliLocation()); err == nil {
				if !validLocalWallTime(s, layout.value, t, cliLocation()) {
					return replyStamp{}, false
				}
				at, width = t.UTC(), layout.width
				break
			}
		}
	}
	if at.IsZero() || width <= 0 {
		return replyStamp{}, false
	}
	before := at.Add(width)
	if !before.After(at) || before.After(now.UTC()) {
		return replyStamp{}, false
	}
	return replyStamp{at: at, before: before}, true
}

// validLocalWallTime rejects skipped and repeated local clock readings: ParseInLocation alone chooses one side of a DST fold.
func validLocalWallTime(raw, layout string, parsed time.Time, loc *time.Location) bool {
	if parsed.In(loc).Format(layout) != raw {
		return false
	}
	year, month, day := parsed.In(loc).Date()
	hour, minute, second := parsed.In(loc).Clock()
	wallUTC := time.Date(year, month, day, hour, minute, second, 0, time.UTC)
	_, chosenOffset := parsed.In(loc).Zone()
	offsets := map[int]struct{}{chosenOffset: {}}
	for delta := -36 * time.Hour; delta <= 36*time.Hour; delta += 30 * time.Minute {
		_, offset := parsed.Add(delta).In(loc).Zone()
		offsets[offset] = struct{}{}
	}
	matches := 0
	for offset := range offsets {
		candidate := wallUTC.Add(-time.Duration(offset) * time.Second)
		if candidate.In(loc).Format(layout) == raw {
			matches++
		}
	}
	return matches == 1
}

func pow10(n int) int64 {
	v := int64(1)
	for i := 0; i < n; i++ {
		v *= 10
	}
	return v
}

// Select deterministically by the timestamp lower bound and then ID. This is a stable choice, not an ordering claim:
// the caller independently applies the park fence to the selected source interval.
func selectReplyCandidate(replies []connectorwire.LarkReplyMessage) connectorwire.LarkReplyMessage {
	var newest connectorwire.LarkReplyMessage
	for _, candidate := range replies {
		if newest.At.IsZero() || candidate.At.After(newest.At) || (candidate.At.Equal(newest.At) && candidate.ID > newest.ID) {
			newest = candidate
		}
	}
	return newest
}
