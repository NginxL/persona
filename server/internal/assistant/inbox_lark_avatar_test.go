package assistant

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/extwire"
)

func TestListedFeishuAvatarKeepsTheStoredPictureAndAccount(t *testing.T) {
	for _, tt := range []struct {
		name, ref, key string
		lark           InboxLark
		want           string
	}{
		{"old gateway item", "/agent/v1/media/default-avatar_1", "default-avatar_1", InboxLark{}, "1"},
		{"second account", "/api/v1/lark/media/image-v2", "image-v2", InboxLark{AccountID: 2}, "2"},
		{"gateway account hint", "/api/media/image-v2?ol_account=3", "image-v2", InboxLark{}, "3"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			it := InboxItem{ID: "lark:feed", Source: "lark", Avatar: tt.ref, Lark: &tt.lark}
			raw, err := json.Marshal(it.Listed())
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			want := extwire.AvatarURL("lark", tt.key) + "?lark_account=" + tt.want
			if fields["avatar_url"] != want || fields["avatar"] != tt.ref {
				t.Fatalf("listed avatar = %v, original = %v; want %s, %s", fields["avatar_url"], fields["avatar"], want, tt.ref)
			}
			stored, err := json.Marshal(it)
			if err != nil {
				t.Fatal(err)
			}
			var record map[string]any
			if err := json.Unmarshal(stored, &record); err != nil {
				t.Fatal(err)
			}
			if record["avatar_url"] != nil || record["avatar"] != tt.ref {
				t.Fatalf("stored original changed: %v", record)
			}
		})
	}
}
