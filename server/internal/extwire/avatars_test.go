package extwire

import (
	"strings"
	"testing"
)

// An avatar value names a picture (#10923): a source's media ref or an https address, at most AvatarValueMost bytes;
// its hub path holds it whole, so the route reads it back without a table.
func TestAvatarValuesAndPaths(t *testing.T) {
	for _, v := range []string{"a.u1.501", "avatar:octocat:4", "media/key_1", "https://avatars.example.com/u/1?v=4", "HTTPS://Example.com/a.png", "示例"} {
		p := AvatarURL("telegram", v)
		if !strings.HasPrefix(p, "/api/v1/avatars/telegram/") || strings.ContainsAny(strings.TrimPrefix(p, "/api/v1/avatars/telegram/"), "/?#%+=") {
			t.Errorf("%q: path %q", v, p)
			continue
		}
		if back, ok := AvatarValue(strings.TrimPrefix(p, "/api/v1/avatars/telegram/")); !ok || back != v {
			t.Errorf("%q: read back %q, %v", v, back, ok)
		}
	}
	for _, v := range []string{"", "http://avatars.example.com/u/1", "HTTP://example.com/a.png", strings.Repeat("x", AvatarValueMost+1)} {
		if AvatarUsable(v) || AvatarURL("telegram", v) != "" {
			t.Errorf("%.40q names a picture", v)
		}
	}
	for _, source := range []string{"", "a/b", "a?b", "a#b", "a%2fb"} {
		if p := AvatarURL(source, "a.u1.501"); p != "" {
			t.Errorf("source %q: %q", source, p)
		}
	}
	if !AvatarAddress("https://example.com/a.png") || !AvatarAddress("HTTPS://example.com/a.png") || AvatarAddress("a.u1.501") || AvatarAddress("avatar:octocat:4") {
		t.Fatal("addresses and refs told apart wrongly")
	}
	if _, ok := AvatarValue("not*base64"); ok {
		t.Fatal("a segment that is not base64url read as a value")
	}
	if _, ok := AvatarValue(strings.TrimPrefix(AvatarURL("x", "a."+strings.Repeat("y", 10)), "/api/v1/avatars/x/") + "!"); ok {
		t.Fatal("a damaged segment read as a value")
	}
}
