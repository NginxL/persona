package lark

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
)

// outboundHost records the outbound port's calls the layer hands to the core; the rest is the fake core's.
type outboundHost struct {
	*Service
	calls []any
}

func (h *outboundHost) Status(context.Context) (assistantwire.FeishuStatus, error) {
	h.calls = append(h.calls, "status")
	return assistantwire.FeishuStatus{Ready: true, Owner: "ou_1"}, nil
}

func (h *outboundHost) Features(context.Context) (assistantwire.FeishuFeatures, bool) {
	h.calls = append(h.calls, "features")
	return assistantwire.FeishuFeatures{Reports: true}, true
}

func (h *outboundHost) Post(_ context.Context, to assistantwire.ChannelTarget, replyTo string, card *botcard.Card, fallback, key string) (string, error) {
	h.calls = append(h.calls, []any{"post", to, replyTo, card, fallback, key})
	return "om_1", nil
}

func (h *outboundHost) Note(_ context.Context, to assistantwire.ChannelTarget, replyTo, text string) {
	h.calls = append(h.calls, []any{"note", to, replyTo, text})
}

func (h *outboundHost) Reply(_ context.Context, to assistantwire.ChannelTarget, replyTo, text, key string, full bool) error {
	h.calls = append(h.calls, []any{"reply", to, replyTo, text, key, full})
	return errors.New("refused")
}

func (h *outboundHost) UpdateCard(_ context.Context, messageID string, card *botcard.Card) error {
	h.calls = append(h.calls, []any{"update", messageID, card})
	return nil
}

func (h *outboundHost) ThreadPost(todoID string, ev assistantwire.ThreadEvent) {
	h.calls = append(h.calls, []any{"thread", todoID, ev})
}

// The layer's outbound port hands each call to the core's host unchanged and gives back what it answers.
func TestLayerForwardsTheOutboundPort(t *testing.T) {
	h := &outboundHost{Service: newTestService(t, newFakeHub())}
	l := New(h)
	ctx := context.Background()
	card := &botcard.Card{}
	to := assistantwire.ChannelTarget{ChatID: "oc_1", InThread: true, NoDetour: true}
	if st, err := l.Status(ctx); err != nil || !st.Ready || st.Owner != "ou_1" {
		t.Fatalf("status %+v %v", st, err)
	}
	if f, ready := l.Features(ctx); !ready || !f.Reports {
		t.Fatalf("features %+v %v", f, ready)
	}
	if mid, err := l.Post(ctx, to, "om_0", card, "fb", "k1"); mid != "om_1" || err != nil {
		t.Fatalf("post %q %v", mid, err)
	}
	l.Note(ctx, to, "om_2", "a note")
	if err := l.Reply(ctx, to, "om_3", "a reply", "k2", true); err == nil || err.Error() != "refused" {
		t.Fatalf("reply %v", err)
	}
	if err := l.UpdateCard(ctx, "om_4", card); err != nil {
		t.Fatal(err)
	}
	ev := assistantwire.ThreadEvent{Kind: assistantwire.ThreadTodoDone, Text: "done"}
	l.ThreadPost("todo_1", ev)
	want := []any{"status", "features", []any{"post", to, "om_0", card, "fb", "k1"}, []any{"note", to, "om_2", "a note"},
		[]any{"reply", to, "om_3", "a reply", "k2", true}, []any{"update", "om_4", card}, []any{"thread", "todo_1", ev}}
	if !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls %v, want %v", h.calls, want)
	}
}
