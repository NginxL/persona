package extwire

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/lark"
	"github.com/nwverse/persona/server/internal/lazyre"
)

var larkMediaKey = lazyre.New(`^[a-zA-Z0-9_./:+-]{1,500}$`)

// LarkMediaKey accepts one gateway resource key, including slashes within the key.
func LarkMediaKey(key string) bool {
	if !larkMediaKey.MatchString(key) {
		return false
	}
	for part := range strings.SplitSeq(key, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// LarkAvatarURL keeps old gateway paths and signed CDN links usable after their original address expires.
// account is the gateway's account number, distinct from a signed-in user's Feishu identity.
// A zero account accepts the original path's account hint, then the primary account of older Inbox records.
func LarkAvatarURL(ref string, account int) string {
	value := ref
	switch {
	case strings.HasPrefix(ref, "/"):
		u, err := url.Parse(ref)
		if err != nil || u.Host != "" || u.Fragment != "" {
			return ""
		}
		value = ""
		for _, prefix := range []string{"/api/v1/lark/media/", "/agent/v1/media/", "/api/media/"} {
			if key, ok := strings.CutPrefix(u.Path, prefix); ok {
				value = key
				break
			}
		}
		if !LarkMediaKey(value) {
			return ""
		}
		if hint := u.Query().Get("ol_account"); hint != "" && account == 0 {
			account, err = strconv.Atoi(hint)
			if err != nil || account < 1 {
				return ""
			}
		}
	case AvatarAddress(ref):
		u, err := url.Parse(ref)
		if err != nil || u.Hostname() == "" || u.User != nil {
			return ""
		}
		host := strings.ToLower(u.Hostname())
		for _, domain := range []string{"feishucdn.com", "larksuitecdn.com", "feishu.cn", "larksuite.com", "larkoffice.com"} {
			if host != domain && !strings.HasSuffix(host, "."+domain) {
				continue
			}
			if _, tail, ok := strings.Cut(u.EscapedPath(), "/static-resource/v1/"); ok {
				part, _, _ := strings.Cut(tail, "/")
				part, _, _ = strings.Cut(part, "~")
				if key, err := url.PathUnescape(part); err == nil && LarkMediaKey(key) {
					value = key
				}
			}
			break
		}
	default:
		if !LarkMediaKey(value) {
			return ""
		}
	}
	if account == 0 {
		account = 1
	}
	path := AvatarURL("lark", value)
	if path == "" || account < 1 || account > lark.MaxAccount {
		return ""
	}
	return path + "?lark_account=" + strconv.Itoa(account)
}
