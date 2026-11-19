package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/prompts"
)

// snapshotWaitTest is a service with a snapshot taken lastRead ago from a read that took took, whose connectors
// answer with chat after delay; the func returns the lines it logged about the live snapshot.
func snapshotWaitTest(t *testing.T, took, lastRead, delay time.Duration, chat string) (*Service, *fakeHub, func() []string) {
	t.Helper()
	f := newFakeHub()
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	f.liveStates = []any{connector.SnapshotData{Chats: &connector.SnapshotChats{AtMe: []connector.SnapshotChat{{Name: chat, Unread: 1}}}}}
	f.liveDelay = delay
	s := newStorelessService(t, f)
	var mu sync.Mutex
	var lines []string
	s.logf = func(format string, args ...any) {
		if line := fmt.Sprintf(format, args...); strings.Contains(line, "live snapshot") {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, line)
		}
	}
	loc := s.location(context.Background())
	s.live.snap = &prompts.Snapshot{Now: time.Now().Add(-lastRead).In(loc), Zone: loc.String(),
		Chats: &prompts.SnapshotChats{AtMe: []prompts.SnapshotChat{{Name: "earlier-chat", Unread: 1}}}}
	s.live.at, s.live.took = time.Now().Add(-lastRead), took
	return s, f, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
}

// readIn waits for the read the last message started to be in: takeSnapshot stores it, then closes the channel that
// was in snapping.
func readIn(t *testing.T, s *Service) {
	t.Helper()
	s.live.mu.Lock()
	in := s.live.snapping
	s.live.mu.Unlock()
	if in == nil {
		return
	}
	select {
	case <-in:
	case <-time.After(snapshotSlowest + time.Second):
		t.Fatal("the snapshot read never came in")
	}
}

// While reads take longer than a message waits, a message no longer waits for one: it would wait the whole
// snapshotWait and still go with the last snapshot (#8948). The read it starts is there for the next message.
func TestSnapshotSlowReadsDoNotHoldTheMessage(t *testing.T) {
	slow := snapshotWait + 300*time.Millisecond
	s, _, logged := snapshotWaitTest(t, slow, 10*time.Minute, slow, "fresh-chat")
	ctx := context.Background()
	start := time.Now()
	got := s.contextSnapshot(ctx, "", false)
	if d := time.Since(start); d > snapshotWait/3 {
		t.Fatalf("waited %v for a read slower than snapshotWait", d)
	}
	if !strings.Contains(got, "earlier-chat") {
		t.Fatalf("the message did not go with the last snapshot:\n%s", got)
	}
	readIn(t, s)
	if got := s.contextSnapshot(ctx, "", false); !strings.Contains(got, "fresh-chat") {
		t.Fatalf("the read the message started was not there for the next one:\n%s", got)
	}
	if lines := logged(); len(lines) != 1 || !strings.Contains(lines[0], "live snapshot: the read took") || !strings.Contains(lines[0], "(connectors ") {
		t.Fatalf("a slow read was not logged with the connectors' share: %q", lines)
	}
}

// A message still waits for fresh data when the last read was quick, and with no usable snapshot (none yet, or one
// older than snapshotStale) it still waits up to snapshotSlowest, however slow the last read was.
func TestSnapshotStillWaitsWhenItShould(t *testing.T) {
	ctx := context.Background()
	t.Run("last read quick", func(t *testing.T) {
		s, _, logged := snapshotWaitTest(t, 50*time.Millisecond, 10*time.Minute, 300*time.Millisecond, "fresh-chat")
		if got := s.contextSnapshot(ctx, "", false); !strings.Contains(got, "fresh-chat") {
			t.Fatalf("a message did not wait for a read quicker than snapshotWait:\n%s", got)
		}
		if lines := logged(); len(lines) != 0 {
			t.Fatalf("a quick read was logged as slow: %q", lines)
		}
	})
	t.Run("nothing usable", func(t *testing.T) {
		for name, lastRead := range map[string]time.Duration{"stale": snapshotStale + time.Minute, "none": 0} {
			s, _, _ := snapshotWaitTest(t, snapshotSlowest, lastRead, snapshotWait+300*time.Millisecond, "fresh-chat")
			if name == "none" {
				s.live.snap = nil
			}
			if got := s.contextSnapshot(ctx, "", false); !strings.Contains(got, "fresh-chat") {
				t.Fatalf("%s: a message went without waiting for the only usable snapshot:\n%s", name, got)
			}
		}
	})
}
