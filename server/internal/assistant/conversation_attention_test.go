package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func attentionTranscript(t0 time.Time, promoted, laterInput bool) []proto.Item {
	items := []proto.Item{
		{Seq: 1, Role: "user", Kind: proto.KindText, Text: "Fix the example", TS: t0},
		{Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "Fixed", TS: t0.Add(time.Minute)},
		{Seq: 3, Role: "user", Kind: proto.KindText, Text: "Check the task", Source: &proto.MessageSource{Kind: proto.SourceSupervision}, TS: t0.Add(2 * time.Minute)},
	}
	if promoted {
		items = append(items,
			proto.Item{Seq: 4, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "notify", ToolName: "mcp__persona__notify_user", TS: t0.Add(3 * time.Minute)},
			proto.Item{Seq: 5, Role: "user", Kind: proto.KindToolResult, ToolUseID: "notify", Text: "sent", TS: t0.Add(3 * time.Minute)})
	}
	items = append(items,
		proto.Item{Seq: 6, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "finish", ToolName: "mcp__persona__supervision_finish", TS: t0.Add(4 * time.Minute)},
		proto.Item{Seq: 7, Role: "user", Kind: proto.KindToolResult, ToolUseID: "finish", Text: `{"visibility":"silent","summary":"Handled."}`, TS: t0.Add(4 * time.Minute)},
		proto.Item{Seq: 8, Kind: "turn_duration", TS: t0.Add(5 * time.Minute)})
	if laterInput {
		items = append(items,
			proto.Item{Seq: 9, Role: "user", Kind: proto.KindText, Text: "Actually, one more thing", TS: t0.Add(6 * time.Minute)},
			proto.Item{Seq: 10, Role: "assistant", Kind: proto.KindText, Text: "On it", TS: t0.Add(7 * time.Minute)})
	}
	return items
}

func attentionReads(f *fakeHub) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.itemReads
}

func TestMainConversationSilentRoundUnreadAndCache(t *testing.T) {
	ctx := context.Background()
	st := newFleetMem()
	s, f := newQuietServiceOn(t, st)
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	f.setItems(attentionTranscript(t0, false, false)...)
	row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 9, UpdatedAt: t0.Add(5 * time.Minute), SeenAt: t0.Add(2 * time.Minute)}
	s.decorateMainConversationAttention(&row, s.settings(ctx))
	if row.State != proto.StateIdle {
		t.Fatalf("read earlier visible round + latest silent = %s, want idle", row.State)
	}
	if got := attentionReads(f); got != 1 {
		t.Fatalf("first classification used %d transcript reads, want 1", got)
	}
	row.State = proto.StateUnread
	s.decorateMainConversationAttention(&row, s.settings(ctx))
	if row.State != proto.StateIdle || attentionReads(f) != 1 {
		t.Fatalf("same revision was not cached: state=%s reads=%d", row.State, attentionReads(f))
	}

	// A manager restarted on the same store uses the persisted session+Rev entry.
	again := New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t)})
	withSettings(t, f, again)
	setSettings(t, again, func(v *Settings) { v.Main = "conv1" })
	row.State = proto.StateUnread
	again.decorateMainConversationAttention(&row, again.settings(ctx))
	if row.State != proto.StateIdle || attentionReads(f) != 1 {
		t.Fatalf("restart reread the transcript: state=%s reads=%d", row.State, attentionReads(f))
	}

	// seen_at before the earlier visible answer preserves that answer's unread mark.
	row.SeenAt = t0.Add(-time.Minute)
	row.State = proto.StateUnread
	s.decorateMainConversationAttention(&row, s.settings(ctx))
	if row.State != proto.StateUnread {
		t.Fatalf("earlier visible unread was hidden by the later silent round: %s", row.State)
	}
}

func TestMainConversationAttentionPromotionsInputAndFailuresStayVisible(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		promoted   bool
		laterInput bool
		fail       bool
		zeroRev    bool
		working    bool
		pending    int
		wantReads  int
	}{
		{name: "promotion", promoted: true, wantReads: 1},
		{name: "later genuine input", laterInput: true, wantReads: 1},
		{name: "read failure fails open", fail: true, wantReads: 1},
		{name: "no revision fails open", zeroRev: true},
		{name: "working wins", working: true},
		{name: "needs you wins", pending: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newQuietService(t)
			f.setItems(attentionTranscript(t0, tc.promoted, tc.laterInput)...)
			if tc.fail {
				f.itemsFail = 1
			}
			row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 9, UpdatedAt: t0.Add(8 * time.Minute)}
			if tc.zeroRev {
				row.Rev = 0
			}
			if tc.working {
				row.Status, row.State = proto.StatusWorking, proto.StateWorking
			}
			row.PendingInteractions = tc.pending
			s.decorateMainConversationAttention(&row, s.settings(ctx))
			want := proto.StateUnread
			if tc.working {
				want = proto.StateWorking
			}
			if row.State != want {
				t.Fatalf("state=%s, want %s", row.State, want)
			}
			if got := attentionReads(f); got != tc.wantReads {
				t.Fatalf("transcript reads=%d, want %d", got, tc.wantReads)
			}
		})
	}
}

func TestMainConversationSilentRoundWithoutEarlierWork(t *testing.T) {
	s, f := newQuietService(t)
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	items := attentionTranscript(t0, false, false)
	f.setItems(items[2:]...) // a complete synthetic history containing only the silent supervision round
	row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 9}
	s.decorateMainConversationAttention(&row, s.settings(context.Background()))
	if row.State != proto.StateIdle {
		t.Fatalf("silent-only complete history remained unread: %s", row.State)
	}
}

func TestMainConversationInternalAcceptedRoundUnread(t *testing.T) {
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		verdict string
		receipt string
		want    string
	}{
		{name: "internal accepted", verdict: reviewAccepted, receipt: `{"background":true,"message":"accepted"}`, want: proto.StateIdle},
		{name: "user requested accepted", verdict: reviewAccepted, receipt: `{"background":false,"message":"accepted"}`, want: proto.StateUnread},
		{name: "needs user", verdict: reviewNeedsUser, receipt: `{"background":true,"message":"card sent"}`, want: proto.StateUnread},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newQuietService(t)
			round := attentionTranscript(t0, false, false)[2:]
			items := append([]proto.Item{
				round[0],
				{Seq: 4, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "review", ToolName: "mcp__persona__task_review", ToolInput: json.RawMessage(`{"verdict":"` + tc.verdict + `"}`), TS: t0.Add(3 * time.Minute)},
				{Seq: 5, Role: "user", Kind: proto.KindToolResult, ToolUseID: "review", Text: tc.receipt, TS: t0.Add(3 * time.Minute)},
			}, round[1:]...)
			f.setItems(items...)
			row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 9, UpdatedAt: t0.Add(5 * time.Minute)}
			s.decorateMainConversationAttention(&row, s.settings(context.Background()))
			if row.State != tc.want {
				t.Fatalf("state=%s, want %s", row.State, tc.want)
			}
		})
	}
}

func TestMainConversationInternalAcceptedInvalidatesV1Cache(t *testing.T) {
	ctx := context.Background()
	st := newFleetMem()
	s, f := newQuietServiceOn(t, st)
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	round := attentionTranscript(t0, false, false)[2:]
	items := append([]proto.Item{
		round[0],
		{Seq: 4, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "review", ToolName: "mcp__persona__task_review", ToolInput: json.RawMessage(`{"verdict":"accepted"}`), TS: t0.Add(3 * time.Minute)},
		{Seq: 5, Role: "user", Kind: proto.KindToolResult, ToolUseID: "review", Text: `{"background":true,"message":"accepted"}`, TS: t0.Add(3 * time.Minute)},
	}, round[1:]...)
	f.setItems(items...)
	if err := st.PutSetting(ctx, "assistant.conversation_attention.v1", map[string]conversationAttentionEntry{
		conversationAttentionCacheKey("server", "conv1"): {Rev: 9, HasVisible: true, VisibleAt: t0.Add(3 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 9, UpdatedAt: t0.Add(5 * time.Minute)}
	s.decorateMainConversationAttention(&row, s.settings(ctx))
	if row.State != proto.StateIdle || attentionReads(f) != 1 {
		t.Fatalf("old visible cache survived the policy change: state=%s reads=%d", row.State, attentionReads(f))
	}
}

func TestMainConversationAttentionPagesForEarlierVisible(t *testing.T) {
	s, f := newQuietService(t)
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	items := attentionTranscript(t0, false, false)
	f.nodes = map[string]nodeAnswer{DeviceSessionItems: func(_ string, raw json.RawMessage) (any, error) {
		var p struct {
			Before int `json:"before_seq"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.Before == 0 {
			return map[string]any{"items": items[2:], "more_before": true}, nil
		}
		if p.Before == 3 {
			return map[string]any{"items": items[:2]}, nil
		}
		return map[string]any{"items": []proto.Item{}}, nil
	}}
	row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 9, SeenAt: t0.Add(2 * time.Minute)}
	s.decorateMainConversationAttention(&row, s.settings(context.Background()))
	if row.State != proto.StateIdle || attentionReads(f) != 2 {
		t.Fatalf("older visible page: state=%s reads=%d, want idle after two reads", row.State, attentionReads(f))
	}
}

func TestMainConversationAttentionPageCapFailsOpen(t *testing.T) {
	s, f := newQuietService(t)
	t0 := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	base := attentionTranscript(t0, false, false)[2:]
	calls := 0
	f.nodes = map[string]nodeAnswer{DeviceSessionItems: func(_ string, raw json.RawMessage) (any, error) {
		calls++
		page := append([]proto.Item(nil), base...)
		for i := range page {
			page[i].Seq += 97 - 10*(calls-1)
		}
		return map[string]any{"items": page, "more_before": true}, nil
	}}
	row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 9}
	s.decorateMainConversationAttention(&row, s.settings(context.Background()))
	if row.State != proto.StateUnread || calls != conversationAttentionScan {
		t.Fatalf("unbounded or fail-closed scan: state=%s calls=%d", row.State, calls)
	}
}

func TestDirectUserInteraction(t *testing.T) {
	for _, tc := range []struct {
		name string
		ix   proto.Interaction
		want bool
	}{
		{name: "question", ix: proto.Interaction{Kind: proto.IxQuestion}, want: true},
		{name: "elicitation", ix: proto.Interaction{Kind: proto.IxElicitation}, want: true},
		{name: "owner proposal", ix: proto.Interaction{Kind: proto.IxPermission, OwnerProposal: true}, want: true},
		{name: "agent permission", ix: proto.Interaction{Kind: proto.IxPermission}},
		{name: "plan", ix: proto.Interaction{Kind: proto.IxPlan}},
		{name: "terminal dialog", ix: proto.Interaction{Kind: proto.IxScreen}},
		{name: "delegating question", ix: proto.Interaction{Kind: proto.IxQuestion, Delegating: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := directUserInteraction(tc.ix); got != tc.want {
				t.Fatalf("directUserInteraction=%v, want %v", got, tc.want)
			}
		})
	}
}
