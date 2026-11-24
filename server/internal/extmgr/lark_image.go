package extmgr

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/lark"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	larkImageTool     = "lark_image"
	larkImagePage     = 10
	larkImageAttempts = 3
)

// The companion reads the same extension that supplies the assistant's message reader.
// A connector's own tool of this name always keeps its normal dispatch and access policy.
func (h *Manager) larkImageSource() (*hubExtension, bool) {
	if h.connectors == nil || !h.connectors.ReadOnly("lark_messages") || h.connectors.Offers(larkImageTool) {
		return nil, false
	}
	hp, source := h.extensions.messagingOwner(connector.Lark)
	if hp == nil || !h.extensions.servesConnector(hp, connector.Lark) {
		return nil, false
	}
	return hp, slices.Contains(source.Capabilities, "accounts")
}

func (h *Manager) callLarkImage(ctx context.Context, args json.RawMessage) proto.ToolResult {
	hp, multiple := h.larkImageSource()
	if hp == nil || connector.StatusOf(ctx, h.connectors.Get(connector.Lark)).State != connector.StateOK {
		return proto.TextResult("The Lark message reader is unavailable.", true)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	end, err := hp.proc.BeginHTTP(ctx)
	if err != nil {
		return proto.TextResult("The Lark messaging source is unavailable.", true)
	}
	defer end()
	return readLarkImages(ctx, hp, multiple, args)
}

// All reads use the registered process transport. Keep both declared and streamed bodies bounded.
func larkImageGet(ctx context.Context, hp *hubExtension, path string, query url.Values, limit int64) ([]byte, error) {
	res, err := getMessagingResource(ctx, hp, connector.Lark, path, query.Encode())
	if err != nil {
		return nil, errors.New("source unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source returned HTTP %d", res.StatusCode)
	}
	if res.ContentLength > limit {
		return nil, errors.New("size limit exceeded")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, errors.New("source read failed")
	}
	if int64(len(body)) > limit {
		return nil, errors.New("size limit exceeded")
	}
	return body, nil
}

func larkImageID(id string) bool {
	return id != "" && id != "." && id != ".." && len(id) <= 512 && !strings.ContainsAny(id, "/\\%?#") && strings.IndexFunc(id, unicode.IsControl) < 0
}

func readLarkImages(ctx context.Context, hp *hubExtension, multiple bool, args json.RawMessage) proto.ToolResult {
	var a struct {
		Message string `json:"message"`
		Chat    string `json:"chat_id"`
		Thread  string `json:"thread"`
		Before  string `json:"before"`
		Account *int   `json:"account"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return proto.TextResult("Invalid arguments for lark_image.", true)
	}
	q := url.Values{}
	if a.Account != nil {
		if *a.Account < 1 || *a.Account > lark.MaxAccount {
			return proto.TextResult("Invalid Lark account number.", true)
		}
		q.Set("account", strconv.Itoa(*a.Account))
	}
	var path string
	switch {
	case larkImageID(a.Message) && a.Chat == "" && a.Thread == "" && a.Before == "":
		path = "messages/" + url.PathEscape(a.Message)
	case larkImageID(a.Chat) && a.Thread == "" && a.Message == "":
		path = "conversations/" + url.PathEscape(a.Chat) + "/messages"
	case larkImageID(a.Thread) && a.Chat == "" && a.Message == "":
		path = "messages/" + url.PathEscape(a.Thread) + "/thread"
	default:
		return proto.TextResult("Give one message, chat_id or thread. Only a page takes before.", true)
	}
	if a.Message == "" {
		q.Set("limit", strconv.Itoa(larkImagePage))
		if a.Before != "" {
			q.Set("before", a.Before)
		}
	}
	// Pin the source default before reading either messages or media. Older pages
	// omit their account, and the user may change the default between two GETs.
	if multiple && a.Account == nil {
		raw, err := larkImageGet(ctx, hp, "accounts", nil, larkDefaultMost)
		id, ok := larkDefaultOf(raw)
		if err != nil || !ok {
			return proto.TextResult("Could not determine the default Lark account. Give the item's account number.", true)
		}
		q.Set("account", strconv.Itoa(id))
	}
	raw, err := larkImageGet(ctx, hp, path, q, 2<<20)
	if err != nil {
		return proto.TextResult("Could not read Lark messages: "+err.Error(), true)
	}
	var page wire.MessagingMessagePage
	if a.Message != "" {
		var answer wire.MessagingMessageAnswer
		if json.Unmarshal(raw, &answer) != nil || answer.Message.ID != a.Message {
			return proto.TextResult("The source did not return the requested message.", true)
		}
		page.Account, page.Messages = answer.Account, []wire.MessagingMessage{answer.Message}
	} else if json.Unmarshal(raw, &page) != nil || page.Messages == nil {
		return proto.TextResult("Invalid Lark message page.", true)
	}
	// Thread pages can carry the root separately from the replies. It is the
	// oldest message and shares their page and image limits.
	if root := page.Root; root != nil && root.ID == a.Thread && a.Thread != "" && len(page.Messages) < larkImagePage &&
		!slices.ContainsFunc(page.Messages, func(m wire.MessagingMessage) bool { return m.ID == root.ID }) {
		page.Messages = slices.Insert(page.Messages, 0, *root)
	}
	account := q.Get("account")
	if account != "" && page.Account != "" && page.Account != account {
		return proto.TextResult("The source returned a different Lark account.", true)
	}
	if account == "" {
		account = page.Account
	}
	if !multiple && a.Account != nil && page.Account == "" {
		return proto.TextResult("The source cannot confirm the requested Lark account.", true)
	}
	return larkImageResult(ctx, hp, page, account)
}

func larkImageResult(ctx context.Context, hp *hubExtension, page wire.MessagingMessagePage, account string) proto.ToolResult {
	var text strings.Builder
	if account != "" {
		fmt.Fprintf(&text, "Account %s\n", account)
	}
	if page.HasOlder && page.OlderCursor != "" {
		fmt.Fprintf(&text, "Older messages: before=%q\n", page.OlderCursor)
	}
	msgs := page.Messages
	if len(msgs) > larkImagePage {
		msgs = msgs[len(msgs)-larkImagePage:]
	}
	var images []connector.ToolImage
	attempted, skipped := 0, 0
	for _, m := range slices.Backward(msgs) {
		if m.Recalled || m.Private || !larkImageID(m.ID) {
			continue
		}
		fmt.Fprintf(&text, "Message %q (%.200s): %.500s\n", m.ID, m.Sender.Name, m.Text)
		index := 0
		for _, p := range m.Parts {
			if p.Kind != wire.MessagingPartImage {
				continue
			}
			index++
			if attempted == larkImageAttempts {
				skipped++
				continue
			}
			attempted++
			caption := fmt.Sprintf("Message %q, image %d", m.ID, index)
			body, err := larkImagePart(ctx, hp, m.ID, p.Media, account)
			if err != nil {
				fmt.Fprintf(&text, "%s unavailable: %s\n", caption, err)
				continue
			}
			images = append(images, connector.ToolImage{Caption: caption, Data: base64.StdEncoding.EncodeToString(body)})
		}
	}
	if attempted == 0 {
		text.WriteString("No readable image parts in these messages.\n")
	}
	if skipped > 0 {
		fmt.Fprintf(&text, "%d more image(s) omitted (three per call).\n", skipped)
	}
	return connector.ToolResult(connector.ToolAnswer{Text: strings.TrimSpace(text.String()), Images: images})
}

func larkImagePart(ctx context.Context, hp *hubExtension, message, ref, account string) ([]byte, error) {
	// Only source-local media on this part, including the message-specific form.
	prefix := "messages/" + url.PathEscape(message) + "/"
	local, scoped := strings.CutPrefix(ref, prefix)
	u, ok := messagingAvatarMedia(local)
	if !ok || strings.Contains(u.Path, "%") {
		return nil, errors.New("unsupported media reference")
	}
	path := u.EscapedPath()
	if scoped {
		path = prefix + path
	}
	q := u.Query()
	for _, key := range []string{"account", "ol_account", "token", "manager"} {
		q.Del(key)
	}
	if account != "" {
		q.Set("account", account)
	}
	return larkImageGet(ctx, hp, path, q, connector.ToolImageBytesMost)
}
