package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/remoteimage"
)

// Only these fields of checked messaging answers hold people or conversations.
// A card's declarative view, attachments, covers and extension-specific fields are opaque.
var messagingAvatarFields = map[string]map[string]string{
	"Accounts":         {"accounts": "Account"},
	"ConversationPage": {"conversations": "Conversation"},
	"Members":          {"members": "Person"},
	"Message":          {"sender": "Person", "parts": "Part", "edit_history": "EditVersion"},
	"EditVersion":      {"parts": "Part"},
	"MessagePage":      {"messages": "Message", "root": "Message"},
	"MessageAnswer":    {"message": "Message"},
	"Part":             {"messages": "Message"}, // forward parts only
	"ChatPin":          {"message": "Message"},
	"ChatPins":         {"items": "ChatPin"},
	"PinTargetAnswer":  {"target": "PinTarget"},
	"PinTarget":        {"message": "Message", "root": "Message", "page": "MessagePage"},
	"URLPreview":       {"author": "Person"},
	"Document":         {"blocks": "DocBlock"},
	"DocBlock":         {"parts": "Part", "items": "DocBlock", "rows": "DocBlock"},
	"CommentThreads":   {"threads": "CommentThread"},
	"CommentThread":    {"replies": "CommentReply"},
	"CommentReply":     {"author": "Person", "parts": "Part"},
	"Post":             {"author": "Person", "parts": "Part"},
	"PostComments":     {"comments": "PostComment"},
	"PostComment":      {"author": "Person", "parts": "Part"},
}

// addMessagingAvatars decorates the manager's copy after schema validation. Retain the
// source fields and large numeric values in a newer extension's unknown fields.
func addMessagingAvatars(rec *bufferedAnswer, answer, source, account string, multiple bool) bool {
	shape := strings.TrimPrefix(answer, "Messaging")
	if shape != "Profile" && messagingAvatarFields[shape] == nil {
		return false
	}
	if !bytes.Contains(rec.body.Bytes(), []byte("avatar")) && !bytes.Contains(rec.body.Bytes(), []byte(`\u`)) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(rec.body.Bytes()))
	d.UseNumber()
	var body map[string]any
	if d.Decode(&body) != nil {
		return false
	}
	if account == "" {
		account, _ = body["account"].(string)
	}
	page := messagingAvatarPage{source: source, multiple: multiple}
	if !page.visit(body, shape, account) {
		return page.needsAccount
	}
	b, err := json.Marshal(body)
	if err == nil {
		rec.body.Reset()
		rec.body.Write(b)
	}
	return page.needsAccount
}

type messagingAvatarPage struct {
	source       string
	multiple     bool
	needsAccount bool
}

func (p *messagingAvatarPage) visit(value any, shape, account string) bool {
	if list, ok := value.([]any); ok {
		changed := false
		for _, item := range list {
			if p.visit(item, shape, account) {
				changed = true
			}
		}
		return changed
	}
	row, ok := value.(map[string]any)
	if !ok {
		return false
	}
	switch shape {
	case "Account", "Profile", "Conversation", "Person":
		if shape == "Account" {
			account, _ = row["id"].(string)
		}
		ref, _ := row["avatar"].(string)
		path := messagingAvatarURL(p.source, ref, account)
		// An unidentified default must not create a permanently cacheable media URL.
		if p.multiple && account == "" && path != "" && !extwire.AvatarAddress(ref) {
			p.needsAccount = true
			path = ""
		}
		if path == "" {
			_, had := row["avatar_url"]
			delete(row, "avatar_url")
			return had
		}
		if row["avatar_url"] == path {
			return false
		}
		row["avatar_url"] = path
		return true
	case "Part":
		if row["kind"] != "forward" {
			return false
		}
	}
	changed := false
	for field, child := range messagingAvatarFields[shape] {
		if p.visit(row[field], child, account) {
			changed = true
		}
	}
	return changed
}

func messagingAvatarURL(source, ref, account string) string {
	path := extwire.AvatarURL(source, ref)
	if path == "" {
		return ""
	}
	// A connector can share this source ID. Keep the registered origin for
	// public pictures too, including after the source has been disabled.
	path += "?messaging=1"
	if extwire.AvatarAddress(ref) {
		if _, err := remoteimage.CheckURL(ref); err != nil {
			return ""
		}
		return path
	}
	if _, ok := messagingAvatarMedia(ref); !ok {
		return ""
	}
	if account != "" {
		path += "&account=" + strings.ReplaceAll(url.QueryEscape(account), "+", "%20")
	}
	return path
}

// A messaging source and a connector may share an ID and can be removed
// independently. An extension's public ID cannot start with an underscore.
func messagingAvatarCache(source string) string { return "_messaging-" + source }

// messagingAvatarMedia accepts a path within a source's media endpoint. Keep its
// escaped key and version query. A messaging avatar cannot name other source APIs.
func messagingAvatarMedia(ref string) (*url.URL, bool) {
	if !strings.HasPrefix(ref, "media/") {
		return nil, false
	}
	u, err := url.Parse(ref)
	if err != nil || u.Fragment != "" || strings.Contains(u.Path, "\\") {
		return nil, false
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return nil, false
		}
	}
	return u, true
}

// A multi-account source's GET me is the messaging protocol's current identity.
// If unavailable, the original request still gets its own response or error.
func defaultMessagingAccount(ctx context.Context, hp *hubExtension, source string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := getMessagingResource(ctx, hp, source, "me", "")
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var me wire.MessagingMe
	if err != nil || wire.ValidateJSON("MessagingMe", raw) != nil || json.Unmarshal(raw, &me) != nil {
		return ""
	}
	return me.Account
}

func getMessagingResource(ctx context.Context, hp *hubExtension, source, path, query string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://plugin/api/v1/messaging/"+url.PathEscape(source)+"/"+path, nil)
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = query
	who := principalOf(ctx)
	req.Header.Set("X-Persona-Device", who.ID)
	req.Header.Set("X-Persona-Role", who.Role)
	return hp.proxy.Transport.RoundTrip(req) // fixed extension transport, no redirects
}

// messagingAvatar reads the unscaled original once from the registered process's
// transport. The caller's requested drawing sizes share that original.
func messagingAvatar(ctx context.Context, hp *hubExtension, source string, media *url.URL, account string) ([]byte, error) {
	end, err := hp.proc.BeginHTTP(ctx)
	if err != nil {
		return nil, err
	}
	defer end()
	q := media.Query()
	q.Del("token")
	q.Del("manager")
	q.Del("account")
	if account != "" {
		q.Set("account", account)
	}
	res, err := getMessagingResource(ctx, hp, source, media.EscapedPath(), q.Encode())
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("messaging media returned HTTP %d", res.StatusCode)
	}
	limit := int64(remoteimage.Avatars.MaxSource)
	if res.ContentLength > limit {
		return nil, &remoteimage.Error{Code: remoteimage.CodeTooLarge, Status: http.StatusRequestEntityTooLarge}
	}
	return io.ReadAll(io.LimitReader(res.Body, limit+1))
}
