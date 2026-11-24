package assistant

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// elapse moves the bubble's clock on by d (no real wait) and lets what it woke run.
func elapse(d time.Duration) {
	<-time.After(d)
	synctest.Wait()
}

// The turn ends a list shows wait out their windows on timers, not goroutines asleep through them (#2472): a hundred
// pending ends hold no goroutine each; none is pushed before its delay, and each is pushed once after it.
func TestTurnEndsWaitOnTimers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTestNoStore(t)
		t.Cleanup(func() {
			if q := ft.s.fleet.q.ch; q != nil {
				close(q) // the in-order worker ends with the bubble
			}
		})
		const n, delay = 100, time.Minute
		ft.f.mu.Lock()
		ft.f.hello.DoneDelayMS = delay.Milliseconds() // the tasks' and the launchers' window too
		ft.f.hello.Notify = assistantwire.NotifyTimings{DoneDelayMS: delay.Milliseconds(), LongTurnMS: 60_000}
		ft.f.mu.Unlock()
		work, done := make([]proto.Session, n), make([]proto.Session, n)
		for i := range work {
			work[i] = proto.Session{SessionID: fmt.Sprintf("s%d", i), Title: "修登录", Agent: "claude", Status: proto.StatusWorking,
				UpdatedAt: time.Now()}
			done[i] = work[i]
			done[i].Status, done[i].LastText = proto.StatusDone, "改好了"
		}
		ft.sessions("mac", work...)
		synctest.Wait()
		before := runtime.NumGoroutine()
		ft.sessions("mac", done...)
		synctest.Wait()
		if grown := runtime.NumGoroutine() - before; grown > 4 {
			t.Fatalf("%d pending turn ends hold %d more goroutines", n, grown)
		}
		pushed := func() map[string]int { // pushesOf would wait, the bubble's clock moving on meanwhile
			ft.f.mu.Lock()
			defer ft.f.mu.Unlock()
			out := map[string]int{}
			for _, p := range ft.f.pushes {
				if p.Kind == assistantwire.NotifyDone {
					out[p.SessionID]++
				}
			}
			return out
		}
		elapse(delay - time.Millisecond)
		if got := pushed(); len(got) != 0 {
			t.Fatalf("pushed before the delay: %v", got)
		}
		elapse(time.Millisecond)
		got := pushed()
		for _, w := range work {
			if got[w.SessionID] != 1 {
				t.Fatalf("%s pushed %d times: %v", w.SessionID, got[w.SessionID], got)
			}
		}
	})
}

// A task's receipt, and the retry of an answer read too early, come once the turn stayed still, once per turn end:
// none within the window, one after it.
func TestTaskReceiptAndRetryAfterTheWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeHub()
		f.sessions["box"] = nil
		f.ownModels = true
		f.settings = &Settings{Timezone: "Asia/Shanghai"}
		s := newStorelessService(t, f)
		t.Cleanup(func() {
			if q := s.fleet.q.ch; q != nil {
				close(q)
			}
		})
		startedTask(t, s, f)
		const delay = time.Minute
		f.mu.Lock()
		f.hello.DoneDelayMS = delay.Milliseconds()
		f.mu.Unlock()
		ctx := context.Background()
		ref := SessionRef{MachineID: "box", SessionID: "s1"}
		receipts := func() int { return strings.Count(feedKinds(s, "conv1"), "receipt") }
		end := func(text string, retry bool) {
			elapse(time.Second) // more work, after the last receipt
			s.taskWorking(ctx, "box", "s1", startRow{})
			done := proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
				LastText: text, UpdatedAt: time.Now()}
			f.setSessions("box", done)
			if !retry {
				s.TurnEnded(ctx, ref)
			} else { // the answer was read before it landed: the list that shows it has the turn looked at again
				s.tasks.mu.Lock()
				s.tasks.awaitingAnswer = map[SessionRef]bool{ref: true}
				s.tasks.mu.Unlock()
				s.setSessions("box", []proto.Session{done})
			}
			synctest.Wait()
		}
		for i, retry := range []bool{false, true} {
			end(fmt.Sprintf("修好了第 %d 处", i+1), retry)
			elapse(delay - time.Millisecond)
			if n := receipts(); n != i {
				t.Fatalf("turn end %d (retry %v): %d receipts within the window", i+1, retry, n)
			}
			elapse(time.Millisecond)
			if n := receipts(); n != i+1 {
				t.Fatalf("turn end %d (retry %v): %d receipts after the window", i+1, retry, n)
			}
		}
	})
}
