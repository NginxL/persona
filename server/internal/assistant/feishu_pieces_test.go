package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// A long answer whose second card does not get out: the other cards go, and a line under the owner's message says
// which part is missing and that the whole answer is in the app. All cards out: no such line.
func TestFeishuAnswerSaysWhichPieceIsMissing(t *testing.T) {
	needsLarkLayer(t)
	for _, fail := range []bool{true, false} {
		s, posts, _ := cotHub(t, false)
		f := s.hub.(*fakeHub)
		n := 0
		f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
			n++
			if fail && n == 2 {
				return nil, errors.New("feishu reply: 99991400 rate limited")
			}
			var m map[string]any
			_ = json.Unmarshal(p, &m)
			*posts = append(*posts, m)
			return map[string]any{"message_id": "om_card"}, nil
		})
		var notes []string
		f.answerApp(AppFeishu, FeishuText, func(p json.RawMessage) (any, error) {
			var m map[string]any
			_ = json.Unmarshal(p, &m)
			notes = append(notes, m["reply_to"].(string)+" "+m["text"].(string))
			return map[string]any{}, nil
		})
		r, _ := cotRun(s)
		long := strings.Repeat("第一段的内容很多。\n\n", 700) + strings.Repeat("第二段的内容很多。\n\n", 700) + "最后一句"
		s.finishChannelRun(r, &channel.Result{Answer: long})
		if n < 3 {
			t.Fatalf("fail=%v: only %d cards for a long answer", fail, n)
		}
		if !fail {
			if len(notes) != 0 {
				t.Fatalf("a note with every card out: %v", notes)
			}
			continue
		}
		if len(*posts) != n-1 {
			t.Fatalf("the cards after the missing one: %d of %d", len(*posts), n)
		}
		if len(notes) != 1 || !strings.HasPrefix(notes[0], "om_m1 ") || !strings.Contains(notes[0], "第 2 段") || !strings.Contains(notes[0], "App") {
			t.Fatalf("notes %v", notes)
		}
	}
}

func TestMissingPiecesNote(t *testing.T) {
	if got := missingPiecesNote("en", []int{2, 4}); !strings.Contains(got, "2 parts") || !strings.Contains(got, "2, 4") {
		t.Fatalf("en %q", got)
	}
	if got := missingPiecesNote("zh-Hans", []int{3}); !strings.Contains(got, "1 段") || !strings.Contains(got, "第 3 段") {
		t.Fatalf("zh %q", got)
	}
	if got := missingPiecesNote("zh-Hans", []int{2, 4}); !strings.Contains(got, "第 2、4 段") {
		t.Fatalf("zh list %q", got)
	}
	if got := missingPiecesNote("ja", []int{2, 4}); !strings.Contains(got, "2、4 番目") || strings.Contains(got, "件") {
		t.Fatalf("ja %q", got)
	}
}

// failSecondPost makes f's second FeishuPost fail; it returns the count of posts tried and the chat lines said.
func failSecondPost(f *fakeHub) (*int, *[]string) {
	n := 0
	var says []string
	f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) {
		n++
		if n == 2 {
			return nil, errors.New("feishu send: 99991400 rate limited")
		}
		return map[string]any{"message_id": "om_card"}, nil
	})
	f.answerApp(AppFeishu, FeishuText, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		if r, _ := m["reply_to"].(string); r == "" {
			says = append(says, m["text"].(string))
		}
		return map[string]any{}, nil
	})
	return &n, &says
}

const longBody = "今天做完了很多事情，这一段很长。\n\n"

// A long notify_user push whose second card fails: the others go, and a line in the chat names the missing part.
func TestFeishuPushSaysWhichPieceIsMissing(t *testing.T) {
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), Features: FeishuFeatures{Main: true, Pushes: true}}, nil
	})
	f.setSessions("server", proto.Session{SessionID: "m1", Assistant: true})
	n, says := failSecondPost(f)
	s := newTestService(t, f)
	s.feishuPush("server", "m1", "巡检结果", strings.Repeat(longBody, 1500)+"最后一句")
	if *n < 3 {
		t.Fatalf("only %d cards", *n)
	}
	if len(*says) != 1 || !strings.Contains((*says)[0], "第 2 段") {
		t.Fatalf("says %v", *says)
	}
}

// The same for a synced turn of the main conversation (feishuMirror).
func TestFeishuMirrorSaysWhichPieceIsMissing(t *testing.T) {
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), Features: FeishuFeatures{Main: true, Sync: true}}, nil
	})
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.setSessions("server", proto.Session{SessionID: "m1", Assistant: true})
	f.setItems(proto.Item{Role: "user", Kind: proto.KindText, Text: "写晚报"},
		proto.Item{Role: "assistant", Kind: proto.KindText, Text: strings.Repeat(longBody, 1500) + "最后一句"})
	n, says := failSecondPost(f)
	s := newTestService(t, f)
	s.feishuMirror(context.Background(), "server", "m1")
	if *n < 3 {
		t.Fatalf("only %d cards", *n)
	}
	if len(*says) != 1 || !strings.Contains((*says)[0], "第 2 段") {
		t.Fatalf("says %v", *says)
	}
}
