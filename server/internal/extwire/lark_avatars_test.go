package extwire

import (
	"strings"
	"testing"
)

func TestLarkAvatarPaths(t *testing.T) {
	for _, test := range []struct {
		ref, key string
		account  int
		wantSlot string
	}{
		{"avatar_1", "avatar_1", 0, "1"},
		{"/api/v1/lark/media/old%2Fone?size=96&ol_account=7", "old/one", 0, "7"},
		{"/agent/v1/media/old:one?ol_account=7", "old:one", 2, "2"},
		{"/api/media/old+one", "old+one", 4, "4"},
		{"https://s.larksuitecdn.com/static-resource/v1/old%2Fone~ab?expires=123", "old/one", 1, "1"},
		{"https://images.example.com/u/p?v=4", "https://images.example.com/u/p?v=4", 2, "2"},
		{"https://feishu.cn.example.com/static-resource/v1/p", "https://feishu.cn.example.com/static-resource/v1/p", 2, "2"},
	} {
		if got, want := LarkAvatarURL(test.ref, test.account), AvatarURL("lark", test.key)+"?lark_account="+test.wantSlot; got != want {
			t.Errorf("%q: got %q, want %q", test.ref, got, want)
		}
	}
	for _, ref := range []string{"", "/not-a-picture", "/api/media/a/%2e%2e/x", "/agent/v1/media/p?ol_account=-1", "/api/media/p?ol_account=no", "http://example.com/p", "https://u@example.com/p", "portrait//p", "x?size=64", strings.Repeat("x", 501)} {
		if got := LarkAvatarURL(ref, 0); got != "" {
			t.Errorf("invalid key %q produced %q", ref, got)
		}
	}
}
