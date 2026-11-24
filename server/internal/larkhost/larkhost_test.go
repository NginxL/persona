package larkhost

import (
	"context"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// Without a module provided there is no layer; with one, New makes it for each host; providing twice panics.
func TestProvide(t *testing.T) {
	saved := provided
	t.Cleanup(func() { provided = saved })
	provided = nil
	if InBuild() {
		t.Fatal("in the build without a module")
	}
	if _, ok := New(nil); ok {
		t.Fatal("a layer without a module")
	}
	made := 0
	Provide(func(Host) Layer { made++; return None{} })
	if _, ok := New(nil); !ok || made != 1 || !InBuild() {
		t.Fatalf("made %d", made)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("provided twice without a panic")
		}
	}()
	Provide(func(Host) Layer { return nil })
}

// None, the port without a channel, is a bot switched off: its status reads (no error) as neither on nor ready, it
// supports nothing, refuses every send with ErrNoChannel and drops notes and thread lines.
func TestNone(t *testing.T) {
	ctx := context.Background()
	var o Outbound = None{}
	if st, err := o.Status(ctx); err != nil || st.Ready || st.Enabled {
		t.Fatalf("status %+v %v", st, err)
	}
	if f, ready := o.Features(ctx); ready || f != (assistantwire.FeishuFeatures{}) {
		t.Fatalf("features %+v %v", f, ready)
	}
	if mid, err := o.Post(ctx, Target{}, "", nil, "", ""); mid != "" || !errors.Is(err, ErrNoChannel) {
		t.Fatalf("post %q %v", mid, err)
	}
	if err := o.Reply(ctx, Target{}, "om_1", "x", "", false); !errors.Is(err, ErrNoChannel) {
		t.Fatalf("reply %v", err)
	}
	if err := o.UpdateCard(ctx, "om_1", nil); !errors.Is(err, ErrNoChannel) {
		t.Fatalf("update %v", err)
	}
	o.Note(ctx, Target{}, "om_1", "x")
	o.ThreadPost("todo_1", ThreadEvent{Kind: ThreadTodoDone})
}
