package extmgr

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
)

func TestMessagingAvatarFields(t *testing.T) {
	const person = `{"name":"Avery","avatar":"media/portrait?v=2"@}`
	const message = `{"id":"m","position":"1","sender":` + person + `,"mine":false,"text":"hello","parts":[]}`
	const forward = `{"kind":"forward","title":"Earlier","messages":[` + message + `]}`
	const edit = `{"text":"old","parts":[` + forward + `]}`
	const plain = `{"kind":"text","text":"hello","vendor":{"avatar":"media/private","count":9007199254740993}}`
	const msg = `{"id":"m","position":"2","sender":` + person + `,"mine":false,"text":"hello","parts":[` + plain + `],"edit_history":[` + edit + `]}`
	const part = `{"kind":"paragraph","parts":[` + forward + `]}`
	for _, tc := range []struct{ shape, json string }{
		{"MessagePage", `{"messages":[` + msg + `],"root":` + message + `,"has_older":false}`},
		{"Members", `{"members":[` + person + `,{"name":"Robin","avatar_url":"https://pictures.example.invalid/ignored"!}]}`},
		{"Document", `{"title":"page","url":"https://docs.example.invalid/1","blocks":[{"kind":"table","rows":[[` + part + `]]}]}`},
		{"CommentThreads", `{"threads":[{"id":"t","resolved":false,"replies":[{"id":"r","author":` + person + `,"parts":[]}]}]}`},
		{"Post", `{"id":"p","author":` + person + `,"parts":[]}`},
		{"URLPreview", `{"url":"https://docs.example.invalid/1","author":` + person + `,"image":{"media":"media/cover"}}`},
	} {
		t.Run(tc.shape, func(t *testing.T) {
			raw := strings.NewReplacer("@", "", "!", "").Replace(tc.json)
			if err := wire.ValidateJSON("Messaging"+tc.shape, []byte(raw)); err != nil {
				t.Fatal(err)
			}
			rec := &bufferedAnswer{}
			rec.body.WriteString(raw)
			addMessagingAvatars(rec, "Messaging"+tc.shape, "studio", "2", true)
			path, _ := json.Marshal(messagingAvatarURL("studio", "media/portrait?v=2", "2"))
			expected := strings.NewReplacer("@", `,"avatar_url":`+string(path), `,"avatar_url":"https://pictures.example.invalid/ignored"!`, "").Replace(tc.json)
			read := func(s string) any {
				t.Helper()
				d := json.NewDecoder(strings.NewReader(s))
				d.UseNumber()
				var v any
				if err := d.Decode(&v); err != nil {
					t.Fatal(err)
				}
				return v
			}
			if !reflect.DeepEqual(read(rec.body.String()), read(expected)) {
				t.Fatalf("got %s, want %s", rec.body.String(), expected)
			}
		})
	}
}
