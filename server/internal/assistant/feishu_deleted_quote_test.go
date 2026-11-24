package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/botport"

	"github.com/nwverse/persona/server/internal/botcard"
)

// Serve synthetic readback through the production Open API client and fake hub boundary.
func quoteReadbackAPI(t *testing.T, f *fakeHub, items []map[string]any, code int) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "synthetic-token", "expire": 7200})
		case "/open-apis/im/v1/messages/om_parent":
			if r.Method != http.MethodGet || r.URL.Query().Get("user_id_type") != "open_id" {
				t.Errorf("unexpected readback request: %s %s", r.Method, r.URL)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": "synthetic readback", "data": map[string]any{"items": items}})
		default:
			t.Errorf("unexpected Open API request: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	t.Cleanup(api.Close)
	client := botport.New("cli_fixture", "synthetic-secret", api.URL)
	f.answerApp(AppFeishu, FeishuMessages, func(p json.RawMessage) (any, error) {
		var req struct {
			MessageID string `json:"message_id"`
		}
		if err := json.Unmarshal(p, &req); err != nil {
			return nil, err
		}
		items, err := client.Message(context.Background(), req.MessageID)
		return map[string]any{"items": items}, err
	})
}

// A deleted parent can still contain its old body and resource keys in a successful readback.
// Both the private handler and botSend must omit that quote while retaining the new message's media.
func TestFeishuReplyDeletedQuote(t *testing.T) {
	needsLarkModule(t)
	parents := []struct{ kind, body string }{
		{"text", `{"text":"old-quote-text"}`},
		{"post", `{"title":"old-quote-title","content":[[{"tag":"text","text":"old-quote-text"},{"tag":"img","image_key":"img_old"}]]}`},
		{"image", `{"image_key":"img_old"}`},
		{"file", `{"file_key":"file_old","file_name":"old-quote.txt"}`},
	}
	for _, parent := range parents {
		for _, deleted := range []bool{true, false} {
			state := "live"
			if deleted {
				state = "deleted"
			}
			for _, own := range []string{"post", "file"} {
				for _, entry := range []string{"private", "botSend"} {
					t.Run(parent.kind+"/"+state+"/"+own+"/"+entry, func(t *testing.T) {
						e := newThreadEnv(t, nil)
						quoteReadbackAPI(t, e.f, []map[string]any{{"message_id": "om_parent", "deleted": deleted, "msg_type": parent.kind,
							"sender": map[string]any{"id": "ou_old", "sender_type": "user"}, "body": map[string]any{"content": parent.body}}}, 0)
						e.f.answerApp(AppFeishu, FeishuUserName, func(json.RawMessage) (any, error) { return map[string]any{"name": "Old sender"}, nil })
						e.f.answerApp(AppFeishu, FeishuResource, func(p json.RawMessage) (any, error) {
							var req struct {
								Key string `json:"key"`
							}
							if err := json.Unmarshal(p, &req); err != nil {
								return nil, err
							}
							return map[string]any{"data": []byte(req.Key), "mime": "image/png"}, nil
						})
						e.f.answerApp(AppFeishu, FeishuSave, func(p json.RawMessage) (any, error) {
							var req struct {
								Key string `json:"key"`
							}
							if err := json.Unmarshal(p, &req); err != nil {
								return nil, err
							}
							return map[string]any{"path": "/tmp/quote-fixture/" + req.Key + ".txt"}, nil
						})
						ev := privateEvent("om_current", "")
						ev.Message.ParentID, ev.Message.RootID = "om_parent", "om_parent"
						ev.Message.MessageType = own
						ev.Message.Content = `{"title":"current reply","content":[[{"tag":"text","text":"current request"},{"tag":"img","image_key":"img_current"}]]}`
						if own == "file" {
							ev.Message.Content = `{"file_key":"file_current","file_name":"current-reply.txt"}`
						}
						if entry == "private" {
							e.deliver(ev)
						} else {
							content := botcard.ParseContent(ev.Message.MessageType, ev.Message.Content, nil)
							if err := e.s.botSend(context.Background(), &ev, content, botTarget{Kind: targetMain}); err != nil {
								t.Fatal(err)
							}
						}
						h := onlyHandoff(t, e.f)
						if h.machine != "server" || h.session != "m1" {
							t.Fatalf("reply target changed: %+v", h)
						}
						wantOwn := "current request"
						if own == "file" {
							wantOwn = "/tmp/quote-fixture/file_current.txt"
						}
						if !strings.Contains(h.text, wantOwn) {
							t.Errorf("new reply missing %q: %s", wantOwn, h.text)
						}
						hasQuote := strings.Contains(h.text, "om_parent")
						if hasQuote == deleted {
							t.Errorf("deleted=%v, parent quote present=%v: %s", deleted, hasQuote, h.text)
						}
						if deleted {
							for _, old := range []string{"old-quote", "Old sender", "img_old", "file_old"} {
								if strings.Contains(h.text, old) {
									t.Errorf("deleted parent leaked %q: %s", old, h.text)
								}
							}
							if names := e.f.appParams(AppFeishu, FeishuUserName); len(names) != 0 {
								t.Errorf("deleted parent caused contact lookup: %v", names)
							}
						} else if parent.kind == "text" || parent.kind == "post" {
							if !strings.Contains(h.text, "old-quote-text") {
								t.Errorf("live parent text lost: %s", h.text)
							}
						}
						wantImages, wantSaves := 0, 0
						if own == "post" {
							wantImages++
						} else {
							wantSaves++
						}
						if !deleted {
							if parent.kind == "post" || parent.kind == "image" {
								wantImages++
							}
							if parent.kind == "file" {
								wantSaves++
							}
						}
						images := sentImages(e.f)
						if len(images) != wantImages {
							t.Errorf("images sent=%v, want %d", images, wantImages)
						}
						if own == "post" && len(images) > 0 {
							img, _ := images[0].(map[string]any)
							if img["data"] != base64.StdEncoding.EncodeToString([]byte("img_current")) {
								t.Errorf("current picture changed: %v", img)
							}
						}
						for method, want := range map[string]int{FeishuMessages: 1, FeishuResource: wantImages, FeishuSave: wantSaves} {
							calls := e.f.appParams(AppFeishu, method)
							if len(calls) != want {
								t.Errorf("%s calls=%v, want %d", method, calls, want)
							}
							if deleted && method != FeishuMessages {
								for _, call := range calls {
									if call["message_id"] != "om_current" {
										t.Errorf("deleted resource requested: %s %v", method, call)
									}
								}
							}
						}
					})
				}
			}
		}
	}
}

func TestFeishuReplyUnavailableQuote(t *testing.T) {
	needsLarkModule(t)
	for _, tc := range []struct {
		name   string
		parent bool
		code   int
	}{
		{"no parent", false, 0}, {"empty readback", true, 0}, {"missing parent", true, 230011},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newThreadEnv(t, nil)
			quoteReadbackAPI(t, e.f, nil, tc.code)
			ev := privateEvent("om_current", "current request")
			if tc.parent {
				ev.Message.ParentID, ev.Message.RootID = "om_parent", "om_parent"
			}
			e.deliver(ev)
			h := onlyHandoff(t, e.f)
			if !strings.Contains(h.text, "current request") || strings.Contains(h.text, "om_parent") {
				t.Fatalf("reply changed: %+v", h)
			}
			wantReads := 0
			if tc.parent {
				wantReads = 1
			}
			if reads := e.f.appParams(AppFeishu, FeishuMessages); len(reads) != wantReads {
				t.Fatalf("quote reads=%v, want %d", reads, wantReads)
			}
			for _, method := range []string{FeishuUserName, FeishuResource, FeishuSave} {
				if calls := e.f.appParams(AppFeishu, method); len(calls) != 0 {
					t.Errorf("unavailable quote caused %s: %v", method, calls)
				}
			}
		})
	}
}
