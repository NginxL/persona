package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A transient turn owned by automatic continuation is retry state, not a final answer for every connected channel.
// The terminal turn after the chain still goes out once through the ordinary mirror paths.
func TestTransientRetryTurnsAreNotMirrored(t *testing.T) {
	s, f := channelPushService(t, false)
	enableChannelPushFeishu(f)
	s.work.switches = &TriageSwitches{} // transient_retry's default is on
	ctx := context.Background()
	for _, id := range []string{"a1", "a2", "a3"} {
		const failure = "API Error: 503 temporarily unavailable"
		f.setSessions("server", proto.Session{SessionID: "s1", Assistant: true, Status: proto.StatusDone,
			TurnError: failure, TurnErrorKind: proto.TurnErrorTransient, UpdatedAt: time.Unix(200, 0)})
		f.setItems(proto.Item{ID: "q" + id, Role: "user", Kind: proto.KindText, Text: "Continue"},
			proto.Item{ID: id, Role: "assistant", Kind: proto.KindText, Text: failure})
		s.feishuMirror(ctx, "server", "s1")
		s.extensionMirror(ctx, "server", "s1")
	}
	if got := f.appParams(AppFeishu, FeishuPost); len(got) != 0 {
		t.Fatalf("mirrored %d intermediate retry turns to Feishu: %v", len(got), got)
	}
	if got := f.appParams(AppChannels, ChannelsSend); len(got) != 0 {
		t.Fatalf("mirrored %d intermediate retry turns to extension channels: %v", len(got), got)
	}

	const terminal = "API Error: 401 authentication required"
	f.setSessions("server", proto.Session{SessionID: "s1", Assistant: true, Status: proto.StatusDone,
		TurnError: terminal, TurnErrorKind: proto.TurnErrorUser, UpdatedAt: time.Unix(300, 0)})
	f.setItems(proto.Item{ID: "q4", Role: "user", Kind: proto.KindText, Text: "Continue"},
		proto.Item{ID: "a4", Role: "assistant", Kind: proto.KindText, Text: terminal})
	s.feishuMirror(ctx, "server", "s1")
	s.extensionMirror(ctx, "server", "s1")
	if got := f.appParams(AppFeishu, FeishuPost); len(got) != 1 || !strings.Contains(fmt.Sprint(got[0]), terminal) {
		t.Fatalf("terminal Feishu mirrors = %v, want exactly the terminal failure", got)
	}
	if got := f.appParams(AppChannels, ChannelsSend); len(got) != 2 || !strings.Contains(fmt.Sprint(got), terminal) {
		t.Fatalf("terminal extension mirrors = %v, want exactly one terminal failure per channel", got)
	}
}

// With automatic continuation off, the same transient error is already terminal and keeps its existing mirror behavior.
func TestTransientErrorIsMirroredWhenRetryIsDisabled(t *testing.T) {
	s, f := channelPushService(t, false)
	enableChannelPushFeishu(f)
	off := false
	s.work.switches = &TriageSwitches{TransientRetry: &off}
	const failure = "API Error: 503 temporarily unavailable"
	f.setSessions("server", proto.Session{SessionID: "s1", Assistant: true, Status: proto.StatusDone,
		TurnError: failure, TurnErrorKind: proto.TurnErrorTransient})
	f.setItems(proto.Item{ID: "q1", Role: "user", Kind: proto.KindText, Text: "Check status"},
		proto.Item{ID: "a1", Role: "assistant", Kind: proto.KindText, Text: failure})
	s.feishuMirror(context.Background(), "server", "s1")
	s.extensionMirror(context.Background(), "server", "s1")
	if got := f.appParams(AppFeishu, FeishuPost); len(got) != 1 {
		t.Fatalf("Feishu mirrors with retry disabled = %d, want 1", len(got))
	}
	if got := f.appParams(AppChannels, ChannelsSend); len(got) != 2 {
		t.Fatalf("extension mirrors with retry disabled = %d, want one per channel", len(got))
	}
}
