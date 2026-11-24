package feishubot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
)

// API readbacks use a scalar id plus id_type, unlike received events' id object.
// Exercise the production decoder and projection before rendering quote/forward content.
func TestItemMentionReadback(t *testing.T) {
	for _, tc := range []struct {
		name   string
		idType string
		id     string
		wantID UserID
	}{
		{"open ID", "open_id", "ou_member", UserID{OpenID: "ou_member"}},
		{"union ID", "union_id", "on_member", UserID{UnionID: "on_member"}},
		{"user ID", "user_id", "member", UserID{UserID: "member"}},
		{"unknown type", "future_id", "ou_member", UserID{}},
		{"missing type", "", "ou_member", UserID{}},
		{"missing ID", "open_id", "", UserID{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, upperID := range []string{"", "om_forward"} {
				t.Run("upper="+upperID, func(t *testing.T) {
					var item Item
					payload := fmt.Sprintf(`{"message_id":"om_source","upper_message_id":%q,"msg_type":"post","mentions":[{"key":"@_user_1","id":%q,"id_type":%q,"name":"Member One"}]}`, upperID, tc.id, tc.idType)
					if err := json.Unmarshal([]byte(payload), &item); err != nil {
						t.Fatal(err)
					}
					mentions := item.MentionList()
					want := []Mention{{Key: "@_user_1", ID: tc.wantID, Name: "Member One"}}
					if !reflect.DeepEqual(mentions, want) {
						t.Errorf("MentionList = %+v, want %+v", mentions, want)
					}
					for _, render := range []struct {
						name string
						node string
						want string
					}{
						{"real ID", fmt.Sprintf(`{"tag":"at","user_id":%q}`, tc.id), ""},
						{"legacy key", `{"tag":"at","user_id":"@_user_1"}`, "@Member One"},
						{"unmatched ID", `{"tag":"at","user_id":"ou_other"}`, "@ou_other"},
						{"embedded name", `{"tag":"at","user_id":"ou_other","user_name":"Inline Member"}`, "@Inline Member"},
					} {
						t.Run(render.name, func(t *testing.T) {
							want := render.want
							if want == "" {
								switch {
								case tc.id == "":
									want = "@某人" // i18n-ignore: existing parser fallback
								case tc.wantID == (UserID{}):
									want = "@" + tc.id
								default:
									want = "@Member One"
								}
							}
							// Readbacks may wrap a post in its language; received posts do not.
							item.Body.Content = `{"en_us":{"title":"","content":[[` + render.node + `]]}}`
							if got := ParseContent(item.MsgType, item.Body.Content, mentions).Text; got != want {
								t.Errorf("ParseContent = %q, want %q", got, want)
							}
						})
					}
					item.MsgType = "text"
					item.Body.Content = `{"text":"hello @_user_1; keep @_user_10"}`
					if got := ParseContent(item.MsgType, item.Body.Content, mentions).Text; got != "hello @Member One; keep @_user_10" {
						t.Errorf("legacy text = %q", got)
					}
				})
			}
		})
	}
}

func TestMessageMentionReadback(t *testing.T) {
	// The quote and the child of a merged forward share the real Message API decoder.
	content := `{"en_us":{"content":[[{"tag":"at","user_id":"ou_member"}]]}}`
	c, _, hits, _ := retryAPIClient(t, func(w http.ResponseWriter, r *http.Request, _ *atomic.Int32) {
		if r.Method != http.MethodGet || r.URL.Path != "/open-apis/im/v1/messages/om_source" || r.URL.Query().Get("user_id_type") != "open_id" {
			t.Errorf("unexpected API request: %s %s", r.Method, r.URL)
		}
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"items":[
			{"message_id":"om_quote","msg_type":"post","body":{"content":%q},"mentions":[{"key":"@_user_1","id":"ou_member","id_type":"open_id","name":"Member One"}]},
			{"message_id":"om_child","upper_message_id":"om_source","msg_type":"post","body":{"content":%q},"mentions":[{"key":"@_user_1","id":"ou_member","id_type":"open_id","name":"Member One"}]}
		]}}`, content, content)
	})
	items, err := c.Message(context.Background(), "om_source")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].UpperMessageID != "" || items[1].UpperMessageID != "om_source" {
		t.Fatalf("unexpected quote/forward readback: %+v", items)
	}
	for _, item := range items {
		if got := ParseContent(item.MsgType, item.Body.Content, item.MentionList()).Text; got != "@Member One" {
			t.Errorf("%s content = %q, want @Member One", item.MessageID, got)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("API calls = %d, want one message read with no contact lookup", got)
	}
}

func TestItemMentionReadbackFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mentions string
		userID   string
		want     string
	}{
		{"legacy key only", `[{"key":"@_user_1","name":"Member One"}]`, "@_user_1", "@Member One"},
		{"ID without key", `[{"id":"ou_member","id_type":"open_id","name":"Member One"}]`, "ou_member", "@Member One"},
		{"ID without name", `[{"key":"@_user_1","id":"ou_member","id_type":"open_id"}]`, "ou_member", "@ou_member"},
		{"key without name", `[{"key":"@_user_1","id":"ou_member","id_type":"open_id"}]`, "@_user_1", "@_user_1"},
		{"no mentions", `[]`, "ou_member", "@ou_member"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := fmt.Sprintf(`{"content":[[{"tag":"at","user_id":%q}]]}`, tc.userID)
			payload := fmt.Sprintf(`{"msg_type":"post","body":{"content":%q},"mentions":%s}`, content, tc.mentions)
			var item Item
			if err := json.Unmarshal([]byte(payload), &item); err != nil {
				t.Fatal(err)
			}
			if got := ParseContent(item.MsgType, item.Body.Content, item.MentionList()).Text; got != tc.want {
				t.Errorf("ParseContent = %q, want %q", got, tc.want)
			}
		})
	}
}
