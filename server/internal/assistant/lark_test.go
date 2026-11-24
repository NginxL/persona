package assistant

import (
	"context"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/larkhost"
)

// The outbound channel port is the lark module's layer in a build with the module and larkhost.None without it: a
// bot switched off, whose status reads without an error, that is never ready and refuses a post with ErrNoChannel.
func TestChatPortFollowsTheLarkModule(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, newFakeHub())
	_, none := s.chat().(larkhost.None)
	if none == larkhost.InBuild() {
		t.Fatalf("port %T with the module in the build: %v", s.chat(), larkhost.InBuild())
	}
	if !none {
		return
	}
	if st, err := s.chat().Status(ctx); err != nil || st.Ready || st.Enabled {
		t.Fatalf("status %+v %v", st, err)
	}
	if _, ready := s.chat().Features(ctx); ready {
		t.Fatal("ready without a channel")
	}
	if _, err := s.chat().Post(ctx, larkhost.Target{}, "", botcard.NewCard("", "", "x"), "x", ""); !errors.Is(err, larkhost.ErrNoChannel) {
		t.Fatalf("post %v", err)
	}
	s.chat().Note(ctx, larkhost.Target{}, "om_1", "x")
	s.chat().ThreadPost("todo_1", larkhost.ThreadEvent{Kind: larkhost.ThreadTodoDone})
}
