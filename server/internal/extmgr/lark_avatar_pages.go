package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/extwire"
)

// Only the public Lark API's typed JSON pages have known avatar fields. Media, raw gateway
// responses and arbitrary rich message parts keep their normal streaming representation.
func larkAvatarPageShape(path string) string {
	path, ok := strings.CutPrefix(path, "/api/v1/lark/")
	if !ok {
		return ""
	}
	s := strings.Split(path, "/")
	switch {
	case path == "accounts":
		return "accounts"
	case path == "conversations" || path == "pinned":
		return "conversations"
	case path == "flags":
		return "flags"
	case len(s) == 2 && s[0] == "users" && s[1] != "":
		return "person"
	case len(s) == 3 && s[0] == "conversations" && s[1] != "":
		switch s[2] {
		case "members", "messages", "pins":
			return s[2]
		}
	case len(s) == 3 && s[0] == "messages" && s[1] != "" && s[2] == "thread":
		return "messages"
	case len(s) == 4 && s[0] == "conversations" && s[1] != "" && s[2] == "pin-messages" && s[3] != "":
		return "pinTarget"
	}
	return ""
}

type larkAvatarPage struct {
	shape   string
	account int
}
type larkAvatarPageKey struct{}

func withLarkAvatars(r *http.Request, hp *hubExtension) *http.Request {
	if r.Method != http.MethodGet || r.Header.Get("Range") != "" {
		return r
	}
	shape := larkAvatarPageShape(r.URL.EscapedPath())
	if shape == "" {
		return r
	}
	q := r.URL.Query()
	account := larkAccountNumber(q.Get("ol_account"))
	if shape != "accounts" && account == 0 && !q.Has("ol_account") {
		account = defaultLarkAvatarAccount(r.Context(), hp)
	}
	if shape != "accounts" && account == 0 {
		return r // The plugin still answers older gateways and invalid account requests as usual.
	}
	r = r.Clone(context.WithValue(r.Context(), larkAvatarPageKey{}, larkAvatarPage{shape, account}))
	if shape != "accounts" && !q.Has("ol_account") {
		q.Set("ol_account", strconv.Itoa(account))
		r.URL.RawQuery = q.Encode()
	}
	r.Header.Set("Accept-Encoding", "identity")
	return r
}

var larkAvatarChildren = map[string]map[string]string{
	"accounts":      {"accounts": "account"},
	"conversations": {"conversations": "chat"},
	"members":       {"members": "person"},
	"messages":      {"messages": "message", "root": "message"},
	"pins":          {"items": "pin"},
	"pin":           {"message": "message"},
	"pinTarget":     {"target": "target"},
	"target":        {"message": "message", "root": "message", "page": "messages"},
	"flags":         {"conversations": "chat", "messages": "flag"},
	"flag":          {"message": "message"},
}

// add visits only documented row types and retains unknown fields, including the legacy avatar.
func (p larkAvatarPage) add(value any, shape string) bool {
	if list, ok := value.([]any); ok {
		changed := false
		for _, row := range list {
			if p.add(row, shape) {
				changed = true
			}
		}
		return changed
	}
	row, ok := value.(map[string]any)
	if !ok {
		return false
	}
	account := p.account
	to, ref := "avatar_url", ""
	switch shape {
	case "account":
		id, _ := row["id"].(json.Number)
		account = larkAccountNumber(string(id))
		ref, _ = row["avatar"].(string)
	case "chat", "person":
		ref, _ = row["avatar"].(string)
		if ref == "" && shape == "person" {
			ref, _ = row["avatarUrl"].(string)
		}
	case "message":
		to = "sender_avatar_url"
		ref, _ = row["sender_avatar"].(string)
	case "flag":
		to = "chat_avatar_url"
		ref, _ = row["chat_avatar"].(string)
	}
	changed := false
	if ref != "" && account > 0 {
		if path := extwire.LarkAvatarURL(ref, account); path != "" {
			row[to] = path
			changed = true
		}
	}
	for field, child := range larkAvatarChildren[shape] {
		if p.add(row[field], child) {
			changed = true
		}
	}
	return changed
}

type larkPageBody struct {
	io.Reader
	io.Closer
}

func addLarkAvatarResponse(res *http.Response) error {
	if res.Request == nil || res.StatusCode != http.StatusOK {
		return nil
	}
	page, ok := res.Request.Context().Value(larkAvatarPageKey{}).(larkAvatarPage)
	if !ok || (res.Header.Get("Content-Encoding") != "" && res.Header.Get("Content-Encoding") != "identity") {
		return nil
	}
	const limit = 4 << 20
	if res.ContentLength > limit {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return err
	}
	// A newer, oversized or unexpected page passes through byte-for-byte.
	res.Body = larkPageBody{io.MultiReader(bytes.NewReader(data), res.Body), res.Body}
	if len(data) > limit || !json.Valid(data) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var body map[string]any
	if dec.Decode(&body) != nil || !page.add(body, page.shape) {
		return nil
	}
	updated, err := json.Marshal(body)
	if err != nil {
		return err
	}
	res.Body = larkPageBody{bytes.NewReader(updated), res.Body}
	res.ContentLength = int64(len(updated))
	res.Header.Set("Content-Length", strconv.Itoa(len(updated)))
	res.Header.Del("ETag")
	res.Header.Del("Content-MD5")
	res.Header.Del("Digest")
	res.Header.Del("Content-Digest")
	return nil
}
