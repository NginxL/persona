package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Exercise the real asynchronous feishu.message entry; wait for its turn before inspecting notes or calls.
func deliverImageFallback(t *testing.T, s *Service, msgType, content string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"sender":  map[string]any{"sender_id": map[string]string{"open_id": "ou_a"}, "sender_type": "user"},
		"message": map[string]any{"message_id": "om_image_fallback", "chat_id": "oc_1", "chat_type": "p2p", "message_type": msgType, "content": content},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FeishuMessage(context.Background(), FeishuMessage{Event: raw}); err != nil {
		t.Fatal(err)
	}
	s.fbot.mu.Lock()
	done := s.fbot.line
	s.fbot.mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("message turn did not finish")
	}
}

func TestFeishuImageFileFallbackEntry(t *testing.T) {
	f, s, _ := bigFileHub(t)
	resourceCalls, saves := 0, 0
	f.answerApp(AppFeishu, FeishuResource, func(p json.RawMessage) (any, error) {
		var a struct {
			MessageID string `json:"message_id"`
			Key, Kind string
			MachineID string `json:"machine_id"`
			AsFile    bool   `json:"as_file"`
		}
		if err := json.Unmarshal(p, &a); err != nil {
			return nil, err
		}
		resourceCalls++
		if a.MessageID != "om_image_fallback" || a.MachineID != "server" || a.Kind != "image" {
			t.Errorf("resource params %s", p)
		}
		if a.Key == "img_large" {
			if a.AsFile {
				t.Error("first image incorrectly forced to file")
			}
			return map[string]any{"path": "/tmp/img_large.jpg", "name": "img_large.jpg", "mime": "image/jpeg"}, nil
		}
		if a.Key == "img_5" {
			if !a.AsFile {
				t.Error("fifth small image must be a file")
			}
			return map[string]any{"path": "/tmp/img_5.gif", "name": "img_5.gif", "mime": "image/gif"}, nil
		}
		if a.AsFile {
			t.Error("small image incorrectly forced to file")
		}
		return map[string]any{"data": []byte("synthetic PNG"), "mime": "image/png"}, nil
	})
	f.answerApp(AppFeishu, FeishuSave, func(p json.RawMessage) (any, error) {
		saves++
		var a map[string]any
		_ = json.Unmarshal(p, &a)
		if a["key"] != "file_video" || a["kind"] != "file" || a["machine_id"] != "server" {
			t.Errorf("an image was downloaded again, or attachment routing changed: %s", p)
		}
		return map[string]any{"path": "/tmp/video.mp4"}, nil
	})
	deliverImageFallback(t, s, "post", `{"content":[[{"tag":"text","text":"Inspect these synthetic resources"},{"tag":"img","image_key":"img_large"},{"tag":"img","image_key":"img_1"},{"tag":"img","image_key":"img_2"},{"tag":"img","image_key":"img_3"},{"tag":"img","image_key":"img_4"},{"tag":"img","image_key":"img_5"},{"tag":"media","file_key":"file_video"}]]}`)
	if resourceCalls != 6 || saves != 1 {
		t.Fatalf("resource calls=%d saves=%d", resourceCalls, saves)
	}
	if images := sentImages(f); len(images) != botImagesMax {
		t.Fatalf("images %v", images)
	}
	for _, image := range sentImages(f) {
		img := image.(map[string]any)
		if img["mime"] != "image/png" || img["data"] != base64.StdEncoding.EncodeToString([]byte("synthetic PNG")) {
			t.Fatalf("small image changed: %v", img)
		}
	}

	text := sendOnceText(f)
	for _, part := range []string{"Inspect these synthetic resources", "/tmp/img_large.jpg", "/tmp/img_5.gif", "/tmp/video.mp4"} {
		if !strings.Contains(text, part) {
			t.Fatalf("missing %q in %q", part, text)
		}
	}
}

func TestFeishuOversizeImageEntry(t *testing.T) {
	for _, only := range []bool{false, true} {
		name := "with text"
		if only {
			name = "image only"
		}
		t.Run(name, func(t *testing.T) {
			f, s, notes := bigFileHub(t)
			f.answerApp(AppFeishu, FeishuResource, func(json.RawMessage) (any, error) {
				return map[string]any{"too_large": true, "name": "img_large.png"}, nil
			})
			msgType, content := "post", `{"content":[[{"tag":"text","text":"Keep these accompanying words"},{"tag":"img","image_key":"img_large"}]]}`
			if only {
				msgType, content = "image", `{"image_key":"img_large"}`
			}
			deliverImageFallback(t, s, msgType, content)
			if len(f.appParams(AppFeishu, FeishuSave)) != 0 {
				t.Fatal("oversize image downloaded again")
			}
			if len(*notes) != 1 || !strings.Contains((*notes)[0], "img_large.png") {
				t.Fatalf("notes %v", *notes)
			}
			if only {
				if sendOnceText(f) != "" {
					t.Fatal("oversize-only image started a run")
				}
				want := i18n.T("zh-Hans", "assistant-feishu.file_too_large_only", i18n.Args{"files": "img_large.png"})
				if !strings.Contains((*notes)[0], want) {
					t.Fatalf("note %q, want %q", (*notes)[0], want)
				}
			} else {
				if !strings.Contains(sendOnceText(f), "Keep these accompanying words") {
					t.Fatalf("text lost: %q", sendOnceText(f))
				}
				want := i18n.T("zh-Hans", "assistant-feishu.file_too_large", i18n.Args{"files": "img_large.png"})
				if (*notes)[0] != want {
					t.Fatalf("note %q, want %q", (*notes)[0], want)
				}
			}
		})
	}
}

func TestFeishuImageFailureMessages(t *testing.T) {
	for _, saveFailure := range []bool{false, true} {
		name, key := "download", "download_image"
		if saveFailure {
			name, key = "save", "save_file"
		}
		t.Run(name, func(t *testing.T) {
			f, s, _ := bigFileHub(t)
			f.answerApp(AppFeishu, FeishuResource, func(json.RawMessage) (any, error) {
				if saveFailure {
					return nil, &rpc.Error{Code: assistantwire.CodeFeishuSave, Message: "synthetic failure"}
				}
				return nil, errors.New("synthetic failure")
			})
			ev := &botcard.MessageEvent{}
			ev.Message.MessageID, ev.Message.ChatType = "om_image", "p2p"
			content := botcard.ParseContent("image", `{"image_key":"img_failure"}`, nil)
			err := s.botSend(context.Background(), ev, content, botTarget{Kind: targetMain})
			rt, resolveErr := s.resolveBotTarget(context.Background(), botTarget{Kind: targetMain})
			if resolveErr != nil {
				t.Fatal(resolveErr)
			}
			prefix := i18n.T("zh-Hans", "assistant-feishu."+key, i18n.Args{"machine": rt.machineName})
			if err == nil || !strings.HasPrefix(err.Error(), prefix) || !strings.Contains(err.Error(), "synthetic failure") {
				t.Fatalf("error %v, want prefix %q", err, prefix)
			}
			if sendOnceText(f) != "" {
				t.Fatal("failed image started a run")
			}
		})
	}
}

func TestFeishuImageFallbackEmptySavedPath(t *testing.T) {
	f, s, notes := bigFileHub(t)
	f.answerApp(AppFeishu, FeishuResource, func(json.RawMessage) (any, error) {
		return map[string]any{"path": "", "name": "img_empty.jpg", "mime": "image/jpeg"}, nil
	})
	deliverImageFallback(t, s, "post", `{"content":[[{"tag":"text","text":"Keep these words"},{"tag":"img","image_key":"img_empty"}]]}`)
	if !strings.Contains(sendOnceText(f), "Keep these words") || len(sentImages(f)) != 0 || len(*notes) != 1 || !strings.Contains((*notes)[0], "img_empty.jpg") {
		t.Fatalf("sent=%q images=%v notes=%v", sendOnceText(f), sentImages(f), *notes)
	}
}

func TestFeishuQuotedImageFileFallback(t *testing.T) {
	f, s := quoteHub(t, func(p json.RawMessage) (any, error) {
		var a map[string]any
		_ = json.Unmarshal(p, &a)
		if a["message_id"] != "om_pic" || a["key"] != "img_q" || a["machine_id"] != "server" {
			t.Errorf("quoted resource identity changed: %s", p)
		}
		return map[string]any{"path": "/tmp/img_q.webp", "name": "img_q.webp", "mime": "image/webp"}, nil
	})
	ev := quoteReply()
	content := botcard.ParseContent(ev.Message.MessageType, ev.Message.Content, nil)
	if err := s.botSend(context.Background(), ev, content, botTarget{Kind: targetMain}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sendOnceText(f), "/tmp/img_q.webp") || len(sentImages(f)) != 0 || len(f.appParams(AppFeishu, FeishuSave)) != 0 {
		t.Fatalf("sent=%q images=%v extra saves=%d", sendOnceText(f), sentImages(f), len(f.appParams(AppFeishu, FeishuSave)))
	}
}
