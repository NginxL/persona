package kbhistory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/kb"
)

// A manager that shuts down ends the run's context. A write or save the import makes then fails because of that,
// not because anything broke: the run ends as it does at the gate, with no STOP and nothing of the step saved, and
// the next start does the step again (#10975). A write that really fails, while the run goes on, still stops.

type historyWriteFunc = func(*kb.Engine, context.Context, *kb.Chat, []kb.SourceMessage, kb.HistoryWrite) (int, error)

// useWriteKB has the driver write through f for the rest of the test.
func useWriteKB(t *testing.T, f historyWriteFunc) {
	t.Helper()
	saved := historyWriteKB
	historyWriteKB = f
	t.Cleanup(func() { historyWriteKB = saved })
}

// directChat is the direct chat "a" of n messages, one an hour, read whole 15 a page: the first page leaves the
// cursor at n-15.
func directChat(n int) *fakeHistory {
	return &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}},
		convs: map[string][]kb.SourceMessage{"a": historyConv("a", n, recentStart(n, time.Hour), time.Hour)}}
}

// endedWithoutStop checks a run the shutdown ended: it returned the context's error, wrote no STOP, pushed nothing
// and left the source's status short of stopped.
func endedWithoutStop(t *testing.T, ht *historyTest, err error) {
	t.Helper()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run: %v", err)
	}
	if stop, stopped := ReadStop(ht.data); stopped {
		t.Fatalf("STOP %+v", stop)
	}
	if len(ht.notices) != 0 {
		t.Fatalf("notices %v", ht.notices)
	}
	if st := ReadStatus(ht.data)[testSource]; st.State == "stopped" || st.LastStop != nil {
		t.Fatalf("status %+v", st)
	}
}

// resumesAt checks the next start of chat "a": its first page is the one at cursor, and the whole chat is archived
// with no message twice.
func resumesAt(t *testing.T, ht *historyTest, cursor string, n int) {
	t.Helper()
	ht.src.hook = nil
	ht.d.instance = "test-2"
	before := ht.src.count("page")
	ht.run()
	if q := ht.src.queries[before]; q.Before != cursor {
		t.Fatalf("resumed from %+v, want before %s", q, cursor)
	}
	if got := ht.kept("a"); !slices.Equal(got, span(0, n-1)) {
		t.Fatalf("kept %v", got)
	}
	if st := ht.state("a"); st.State != kb.HistoryDone {
		t.Fatalf("state %+v", st)
	}
}

// blockFile puts a directory where path is, so a file renamed over it fails (for root too, and on Windows). The
// returned func puts back what was there.
func blockFile(t *testing.T, path string) func() {
	t.Helper()
	raw, err := os.ReadFile(path)
	had := err == nil
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "blocker"), 0o700); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
		if had {
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// The second page's answer comes while the manager shuts down, and its write is cut off: whether the knowledge base
// says context.Canceled, wraps it, or says something else, the run ends without a stop, the page's state is not
// saved, and the next start reads that page again.
func TestHistoryShutdownCutsAWriteShort(t *testing.T) {
	real := historyWriteKB
	for name, write := range map[string]historyWriteFunc{
		"canceled": real, // kb.Engine.WriteHistory's own answer to a context that ended
		"wrapped": func(e *kb.Engine, ctx context.Context, c *kb.Chat, msgs []kb.SourceMessage, opt kb.HistoryWrite) (int, error) {
			if err := ctx.Err(); err != nil {
				return 0, fmt.Errorf("history: a day file: %w", err)
			}
			return real(e, ctx, c, msgs, opt)
		},
		"other error": func(e *kb.Engine, ctx context.Context, c *kb.Chat, msgs []kb.SourceMessage, opt kb.HistoryWrite) (int, error) {
			if ctx.Err() != nil {
				return 0, errors.New("the file system went away")
			}
			return real(e, ctx, c, msgs, opt)
		},
	} {
		t.Run(name, func(t *testing.T) {
			useWriteKB(t, write)
			const n = 60
			src := directChat(n)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			src.hook = func(k int, _ connectorwire.HistoryQuery) (connectorwire.HistoryPage, error, bool) {
				if k == 2 {
					cancel() // the call itself is never cancelled: its answer comes, and is to be written
				}
				return connectorwire.HistoryPage{}, nil, false
			}
			ht := newHistoryTest(t, src)
			ht.plan(true, nil)
			endedWithoutStop(t, ht, ht.d.runOnce(ctx))
			if got := src.count("page"); got != 2 {
				t.Fatalf("%d pages", got)
			}
			// The first page is written and saved; nothing of the second: the cursor is where the first left it.
			if st := ht.state("a"); st.Cursor != "45" || st.Read != 15 || st.State != kb.HistoryRunning {
				t.Fatalf("state %+v", st)
			}
			if got := ht.kept("a"); !slices.Equal(got, span(45, n-1)) {
				t.Fatalf("kept %v", got)
			}
			resumesAt(t, ht, "45", n)
		})
	}
}

// A write that fails while the run goes on stops the import with write_failed, as before: also when the error wraps
// a cancellation of something else, since only the run's own context ending is a shutdown.
func TestHistoryWriteFailureStillStops(t *testing.T) {
	real := historyWriteKB
	for name, c := range map[string]struct {
		err   error
		class string
	}{
		"permission":     {&os.PathError{Op: "open", Path: "day.md", Err: os.ErrPermission}, "permission"},
		"wraps a cancel": {fmt.Errorf("a step of its own: %w", context.Canceled), "error"},
	} {
		t.Run(name, func(t *testing.T) {
			writes := 0
			useWriteKB(t, func(e *kb.Engine, ctx context.Context, ch *kb.Chat, msgs []kb.SourceMessage, opt kb.HistoryWrite) (int, error) {
				if writes++; writes == 2 {
					return 0, c.err
				}
				return real(e, ctx, ch, msgs, opt)
			})
			ht := newHistoryTest(t, directChat(60))
			ht.plan(true, nil)
			ht.run()
			stop, stopped := ReadStop(ht.data)
			if !stopped || stop.Code != "write_failed" || stop.Args["class"] != c.class || stop.By != "driver" {
				t.Fatalf("STOP %+v %v", stop, stopped)
			}
			if len(ht.notices) != 1 || !strings.Contains(ht.notices[0], "write_failed") {
				t.Fatalf("notices %v", ht.notices)
			}
			if st := ReadStatus(ht.data)[testSource]; st.State != "stopped" {
				t.Fatalf("status %+v", st)
			}
			if st := ht.state("a"); st.Cursor != "45" || st.Read != 15 {
				t.Fatalf("state %+v", st)
			}
		})
	}
}

// The import's other saves a shutdown can reach end the run the same way when they fail then: the chat's state, the
// anchors the search found, and the plan (its approval taken back).
func TestHistoryShutdownCutsASaveShort(t *testing.T) {
	t.Run("state", func(t *testing.T) {
		const n = 60
		src := directChat(n)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ht := newHistoryTest(t, src)
		ht.plan(true, nil)
		armed := false
		src.hook = func(k int, _ connectorwire.HistoryQuery) (connectorwire.HistoryPage, error, bool) {
			armed = k == 1
			return connectorwire.HistoryPage{}, nil, false
		}
		var restore func()
		ht.d.sleep = func(ctx context.Context, d time.Duration) error {
			if armed { // the wait before the second page: the manager shuts down, and the chat's state cannot be saved
				armed = false
				restore = blockFile(t, StatePathFor(ht.data, kb.ChatID(testSource, "a")))
				cancel()
			}
			return ht.clock.Sleep(ctx, d)
		}
		err := ht.d.runOnce(ctx)
		if restore == nil {
			t.Fatalf("no wait before the second page: %v", err)
		}
		restore() // what a failed save leaves: the state the first page saved
		endedWithoutStop(t, ht, err)
		if got := src.count("page"); got != 1 {
			t.Fatalf("%d pages", got)
		}
		ht.d.sleep = ht.clock.Sleep
		resumesAt(t, ht, "45", n)
	})

	t.Run("anchors", func(t *testing.T) {
		const n = 200
		msgs := historyConv("g", n, recentStart(n, 10*time.Minute), 10*time.Minute, 100)
		src := positionalSource("g", msgs, 100)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ht := newHistoryTest(t, src)
		ht.plan(true, nil)
		var restore func()
		src.anchorHook = func(connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error, bool) {
			if restore == nil { // the manager shuts down during the first search: what it finds cannot be saved
				restore = blockFile(t, historyAnchorsPath(ht.data, testSource))
				cancel()
			}
			return connectorwire.HistoryAnchors{}, nil, false
		}
		err := ht.d.runOnce(ctx)
		if restore == nil {
			t.Fatalf("no search: %v", err)
		}
		restore()
		endedWithoutStop(t, ht, err)
		if got := src.count("anchors"); got != 1 || src.count("page") != 0 {
			t.Fatalf("calls %v", src.calls)
		}
		// The next start searches again and reads the window around the owner's message.
		src.anchorHook = nil
		ht.d.instance = "test-2"
		ht.run()
		if got := ht.kept("g"); !slices.Equal(got, span(50, 150)) {
			t.Fatalf("kept %v", got)
		}
		if cache, err := loadHistoryAnchors(ht.data, testSource); err != nil || !cache.Done {
			t.Fatalf("anchors %+v %v", cache, err)
		}
		if _, stopped := ReadStop(ht.data); stopped {
			t.Fatal("STOP after the next start")
		}
	})

	// The plan reads but cannot be saved: only a directory its owner cannot write to does that.
	readOnlyDirs := func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs a directory its owner cannot write to")
		}
	}

	t.Run("plan, unlisted chats", func(t *testing.T) {
		readOnlyDirs(t)
		const n = 200
		msgs := historyConv("g", n, recentStart(n, 10*time.Minute), 10*time.Minute, 100)
		src := positionalSource("g", msgs, 100)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ht := newHistoryTest(t, src)
		ht.plan(true, nil)
		// A first run gathers the anchors and is cut off at its first page.
		src.hook = func(int, connectorwire.HistoryQuery) (connectorwire.HistoryPage, error, bool) {
			cancel()
			return connectorwire.HistoryPage{}, nil, false
		}
		endedWithoutStop(t, ht, ht.d.runOnce(ctx))
		cache, err := loadHistoryAnchors(ht.data, testSource)
		if err != nil || !cache.Done {
			t.Fatalf("anchors %+v %v", cache, err)
		}
		// What the search found includes a chat the plan does not list: the next start adds it to the plan first.
		cache.Anchors = append(cache.Anchors, connectorwire.HistoryAnchor{Chat: "u", Position: "3", At: msgs[n-1].At, Mine: true})
		if err := writeJSONFile(historyAnchorsPath(ht.data, testSource), cache); err != nil {
			t.Fatal(err)
		}
		// That start comes as the manager shuts down, and the plan cannot be saved.
		dir := Dir(ht.data)
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		src.hook = nil
		ht.d.instance = "test-2"
		err = ht.d.runOnce(ctx)
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		endedWithoutStop(t, ht, err)
		if p, _, _ := LoadPlan(ht.data, testSource); p == nil || p.chat("u") != nil {
			t.Fatalf("plan %+v", p)
		}
		// The next start adds it, without a stop.
		ht.d.instance = "test-3"
		ht.run()
		if p, _, _ := LoadPlan(ht.data, testSource); p == nil || p.chat("u") == nil {
			t.Fatalf("plan %+v", p)
		}
		if _, stopped := ReadStop(ht.data); stopped {
			t.Fatal("STOP after the next start")
		}
	})

	t.Run("plan, the gate", func(t *testing.T) {
		readOnlyDirs(t)
		const n = 300
		src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "g", Kind: "group"}},
			convs: map[string][]kb.SourceMessage{"g": historyConv("g", n, recentStart(n, time.Hour), time.Hour, 250)}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ht := newHistoryTest(t, src)
		dir := Dir(ht.data)
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		armed := false
		src.hook = func(k int, _ connectorwire.HistoryQuery) (connectorwire.HistoryPage, error, bool) {
			if k == 2 { // the group rule turns on: the next gate takes the approval back
				ht.selection(func(s *kb.Selection) {
					r := s.Messages.Rules[kb.RuleGroup]
					r.On = true
					s.Messages.Rules[kb.RuleGroup] = r
				})
				armed = true
			}
			return connectorwire.HistoryPage{}, nil, false
		}
		cfg := ht.d.cfg
		ht.d.cfg = func(ctx context.Context) *assistantwire.KBHistory {
			if armed { // in that gate the manager shuts down, and the plan cannot be saved
				armed = false
				cancel()
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
			}
			return cfg(ctx)
		}
		ht.plan(true, nil)
		err := ht.d.runOnce(ctx)
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		endedWithoutStop(t, ht, err)
		if p, _, _ := LoadPlan(ht.data, testSource); p == nil || p.ApprovedAt == nil {
			t.Fatalf("plan %+v", p)
		}
		// The next start takes the approval back and tells the owner, without a stop.
		ht.d.instance = "test-2"
		ht.run()
		if p, _, _ := LoadPlan(ht.data, testSource); p == nil || p.ApprovedAt != nil {
			t.Fatalf("approval kept: %+v", p)
		}
		if len(ht.notices) != 1 || !strings.Contains(ht.notices[0], "plan_changed") {
			t.Fatalf("notices %v", ht.notices)
		}
		if _, stopped := ReadStop(ht.data); stopped {
			t.Fatal("STOP after the next start")
		}
	})
}
