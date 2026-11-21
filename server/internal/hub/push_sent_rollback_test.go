package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/db/dbtest"
	"github.com/nwverse/persona/server/internal/update"
)

// sentRecordStore is what the delivered pushes use of the hub's store: its settings and the hub_push_sent table.
type sentRecordStore interface {
	Store
	pushSentStore
}

// oldBuild is the hub from before hub_push_sent as far as the delivered pushes go (push_grading.go on main before #8257):
// at start it reads only the hub.push_sent setting and keeps the entries inside the 14 days (loadPushGradingState), and every
// delivered push writes the whole list back (addSentLocked, saveIntercepts: PutSetting). It knows nothing of the
// table. Its pusher has no store, so the list is in memory and the daily counts are the real reader's.
type oldBuild struct {
	p     *Pusher
	store sentRecordStore
}

func startOldBuild(t *testing.T, store sentRecordStore, now time.Time) *oldBuild {
	t.Helper()
	p := newPusher(t.TempDir(), nil, t.Logf)
	p.now = func() time.Time { return now }
	var delivered []pushSent
	if _, err := store.Setting(t.Context(), pushSentSettingKey, &delivered); err != nil {
		t.Fatalf("the old build reads %s: %v", pushSentSettingKey, err)
	}
	cutoff := now.Add(-heldPushMaxAge)
	for _, e := range delivered {
		if e.At.After(cutoff) {
			p.sent = append(p.sent, e)
		}
	}
	return &oldBuild{p: p, store: store}
}

func (o *oldBuild) deliver(t *testing.T, manager, key string, at time.Time) {
	t.Helper()
	o.p.recordSent(pushPolicy{manager: manager, relatedKey: key}, "p1", at)
	o.p.gradingMu.Lock()
	list := slices.Clone(o.p.sent)
	o.p.gradingMu.Unlock()
	if err := o.store.PutSetting(t.Context(), pushSentSettingKey, list); err != nil {
		t.Fatal(err)
	}
}

// startNewBuild is this build starting on store: newPusher's loadSent, on the clock now.
func startNewBuild(t *testing.T, store sentRecordStore, now time.Time) *Pusher {
	t.Helper()
	p := newPusher(t.TempDir(), nil, t.Logf)
	p.now = func() time.Time { return now }
	p.store = store
	p.loadSent()
	return p
}

// history is a pusher's delivered pushes as what they hold, in order: the time to the microsecond the table keeps.
func history(p *Pusher) []string {
	p.gradingMu.Lock()
	defer p.gradingMu.Unlock()
	out := make([]string, len(p.sent))
	for i, e := range p.sent {
		out[i] = fmt.Sprintf("%s %s %s %q", e.At.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), e.Level, e.Manager, e.RelatedKey)
	}
	return out
}

func tableRows(t *testing.T, store sentRecordStore) int {
	t.Helper()
	rows, err := store.PushSentAfter(t.Context(), time.Time{}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// sentStores runs a test on the in-memory store and, with a test database, on Postgres (a database of its own: the
// table and the setting are the whole hub's).
func sentStores(t *testing.T, run func(t *testing.T, store sentRecordStore)) {
	t.Run("memory", func(t *testing.T) { run(t, newGradingStore()) })
	t.Run("postgres", func(t *testing.T) {
		base := os.Getenv(dbtest.Env)
		if base == "" {
			t.Skip(dbtest.Env + " not set")
		}
		url, done, err := dbtest.Own(context.Background(), base)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(done)
		d, err := db.Open(context.Background(), url, db.HubSet)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Close)
		run(t, d)
	})
}

// Upgrade → deliver → switch back to the old build → forward again: the old build, which reads only the
// hub.push_sent setting, finds the history and the daily counts as the new build had them, pushes it delivers
// meanwhile are there after the next upgrade, and no push is counted twice at any stage. The new build stops the way
// the hub does (Hub.closeDatabaseAfterWorkers: mirrorSent), which is where the setting is written; delivering a push
// never writes it.
func TestPushSentSurvivesSwitchBackAndForward(t *testing.T) {
	sentStores(t, func(t *testing.T, store sentRecordStore) {
		// Times finer than the table keeps, over three UTC days, as the hub's clock gives them.
		now := time.Date(2026, 11, 15, 10, 0, 0, 123456789, time.UTC)
		ctx := t.Context()
		check := func(stage string, p *Pusher, want map[string][]pushDayStats, wantHistory []string) {
			t.Helper()
			if got := statsViews(p); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: daily counts differ:\n got %+v\nwant %+v", stage, got, want)
			}
			if got := history(p); !slices.Equal(got, wantHistory) {
				t.Fatalf("%s: delivered history differs:\n got %q\nwant %q", stage, got, wantHistory)
			}
		}

		// 1. The old build, before the upgrade: pushes on three days, two of them equal, one past the 14 days.
		old := startOldBuild(t, store, now)
		old.deliver(t, "manager-a", "old-1", now.Add(-50*time.Hour))
		old.deliver(t, "manager-b", "old-2", now.Add(-26*time.Hour))
		old.deliver(t, "", "old-twin", now.Add(-3*time.Hour))
		old.deliver(t, "", "old-twin", now.Add(-3*time.Hour))
		old.deliver(t, "manager-a", "expired", now.Add(-heldPushMaxAge-time.Hour))
		old = startOldBuild(t, store, now) // as it starts again: the expired entry is not read
		wantOld, historyOld := statsViews(old.p), history(old.p)
		if len(historyOld) != 4 {
			t.Fatalf("the old build holds %d pushes, want 4", len(historyOld))
		}

		// 2. Upgrade: the same counts, read from the table.
		p := startNewBuild(t, store, now)
		check("after the upgrade", p, wantOld, historyOld)
		if n := tableRows(t, store); n != 4 {
			t.Fatalf("%d rows after the upgrade, want the 4 inside the 14 days", n)
		}

		// 3. The new build delivers: rows only; one push equal to an earlier one is still its own push.
		recordAndSave(t, p, "manager-a", "p1", "new-1", now.Add(-2*time.Hour))
		recordAndSave(t, p, "manager-b", "p0", "new-2", now.Add(-time.Hour))
		recordAndSave(t, p, "", "p1", "old-twin", now.Add(-3*time.Hour))
		wantNew, historyNew := statsViews(p), history(p)
		if len(historyNew) != 7 || tableRows(t, store) != 7 {
			t.Fatalf("the new build holds %d pushes in %d rows, want 7", len(historyNew), tableRows(t, store))
		}
		var stale []pushSent
		if _, err := store.Setting(ctx, pushSentSettingKey, &stale); err != nil || len(stale) != 5 {
			t.Fatalf("delivering wrote the setting: %d entries (%v), want the old build's 5 untouched", len(stale), err)
		}

		// 4. Switch back: the new build stops, the old one starts and reads only the setting.
		if err := p.mirrorSent(ctx); err != nil {
			t.Fatal(err)
		}
		old = startOldBuild(t, store, now)
		check("the old build after the switch back", old.p, wantNew, historyNew)

		// 5. The old build delivers, rewriting the whole setting each time as it always did.
		old.deliver(t, "manager-a", "back-1", now.Add(-30*time.Minute))
		old.deliver(t, "manager-a", "new-1", now.Add(-2*time.Hour)) // equal to one the new build delivered
		wantBack, historyBack := statsViews(old.p), history(old.p)

		// 6. Forward again: what the old build delivered is added, nothing twice.
		p = startNewBuild(t, store, now)
		check("after the second upgrade", p, wantBack, historyBack)
		if n := tableRows(t, store); n != 9 {
			t.Fatalf("%d rows after the second upgrade, want 9: each push once", n)
		}

		// 7. One more start, and one more switch back without a push in between: all the same.
		p = startNewBuild(t, store, now)
		check("after a restart", p, wantBack, historyBack)
		if n := tableRows(t, store); n != 9 {
			t.Fatalf("%d rows after a restart, want 9", n)
		}
		if err := p.mirrorSent(ctx); err != nil {
			t.Fatal(err)
		}
		check("the old build after a second switch back", startOldBuild(t, store, now).p, wantBack, historyBack)
	})
}

// A hub that was killed never ran its stop: the old build then finds the setting as of the last hourly write, short
// of the pushes since (at most an hour's), and the next upgrade still has every push, from the table, with the old
// build's added. The running hub writes the setting at most once an hour however many pushes go out.
func TestPushSentSwitchBackAfterKill(t *testing.T) {
	sentStores(t, func(t *testing.T, store sentRecordStore) {
		now := time.Date(2026, 11, 15, 10, 0, 0, 123456789, time.UTC)
		ctx := t.Context()
		settingEntries := func() int {
			t.Helper()
			var list []pushSent
			if _, err := store.Setting(ctx, pushSentSettingKey, &list); err != nil {
				t.Fatal(err)
			}
			return len(list)
		}
		p := startNewBuild(t, store, now)
		clock := now
		p.now = func() time.Time { return clock }
		for i := range 3 {
			recordAndSave(t, p, "manager-a", "p1", fmt.Sprintf("first-%d", i), clock)
		}
		if n := settingEntries(); n != 0 {
			t.Fatalf("the pushes wrote %d entries to the setting, want none before a maintenance pass", n)
		}
		p.mirrorSentIfDue(ctx) // the first pass after the start
		if n := settingEntries(); n != 3 {
			t.Fatalf("the setting holds %d entries after the first pass, want 3", n)
		}
		clock = now.Add(pushSentMirrorEvery - time.Minute)
		for i := range 2 {
			recordAndSave(t, p, "manager-b", "p1", fmt.Sprintf("second-%d", i), clock)
		}
		p.mirrorSentIfDue(ctx)
		if n := settingEntries(); n != 3 {
			t.Fatalf("the setting was rewritten within the hour: %d entries, want 3", n)
		}
		want := statsViews(p)

		// Killed here. The old build reads the hour-old setting and delivers one push of its own.
		old := startOldBuild(t, store, clock)
		if n := len(history(old.p)); n != 3 {
			t.Fatalf("the old build after the kill holds %d pushes, want the 3 of the last write", n)
		}
		old.deliver(t, "manager-a", "back-1", clock)

		// Forward again: the two pushes the old build never saw are still in the table, and its own is added.
		p = startNewBuild(t, store, clock)
		if n := tableRows(t, store); n != 6 || len(history(p)) != 6 {
			t.Fatalf("after the upgrade: %d rows, %d pushes, want 6", n, len(history(p)))
		}
		after := statsViews(p)
		for view, days := range want {
			for i, day := range days {
				if got := after[view][i]; got.Sent < day.Sent {
					t.Fatalf("%s %s: sent %d after the round trip, was %d before the kill", view, day.Date, got.Sent, day.Sent)
				}
			}
		}
		// The table is now ahead of the setting, so the next pass writes it without waiting for a push.
		clock = clock.Add(pushSentMirrorEvery)
		p.mirrorSentIfDue(ctx)
		if n := settingEntries(); n != 6 {
			t.Fatalf("the setting holds %d entries after the next pass, want all 6", n)
		}
	})
}

// failingAdopt is a store whose start could not read the old setting into the table.
type failingAdopt struct{ *gradingStore }

func (failingAdopt) AdoptPushSentSetting(context.Context, string, time.Time, int) (int, bool, error) {
	return 0, false, errors.New("database away")
}

// A hub that could not add the setting's entries to the table at start holds less than the setting may: it leaves
// the setting alone rather than write its shorter list over what an older build delivered.
func TestPushSentKeepsSettingItCouldNotRead(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	store := newGradingStore()
	old := startOldBuild(t, store, now)
	old.deliver(t, "manager-a", "old-1", now.Add(-time.Hour))
	old.deliver(t, "manager-a", "old-2", now.Add(-time.Minute))

	p := startNewBuild(t, failingAdopt{store}, now)
	recordAndSave(t, p, "manager-a", "p1", "new-1", now)
	if err := p.mirrorSent(t.Context()); err != nil {
		t.Fatal(err)
	}
	p.mirrorSentIfDue(t.Context())
	if got := history(startOldBuild(t, store, now).p); len(got) != 2 {
		t.Fatalf("the setting after a start that could not read it = %q, want the old build's two entries", got)
	}
	// The next start reads it, and then the setting gets all three.
	p = startNewBuild(t, store, now)
	if err := p.mirrorSent(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := history(startOldBuild(t, store, now).p); len(got) != 3 {
		t.Fatalf("the setting after a good start = %q, want all three pushes", got)
	}
}

// Where the hub writes the setting for an older build: in its cleanup at every stop, before the database closes, and
// on the updater's way back to the old binary, before it runs it (an exec skips the cleanup).
func TestPushSentWrittenAtStopAndBeforeGoingBack(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	start := func(t *testing.T) (*Hub, func() int) {
		store := newGradingStore()
		h := newTestHub(t)
		h.push = startNewBuild(t, store, now)
		recordAndSave(t, h.push, "manager-a", "p1", "new-1", now)
		return h, func() int {
			var list []pushSent
			if _, err := store.Setting(context.Background(), pushSentSettingKey, &list); err != nil {
				t.Fatal(err)
			}
			return len(list)
		}
	}
	t.Run("stop", func(t *testing.T) {
		h, entries := start(t)
		budget := newShutdownBudget(h.cfg)
		h.shutdown = budget
		t.Cleanup(budget.close)
		atClose := -1
		h.closeDatabaseAfterWorkers(func() {}, func(context.Context) error { atClose = entries(); return nil })
		if atClose != 1 {
			t.Fatalf("the setting held %d entries when the database closed, want the delivered push", atClose)
		}
	})
	t.Run("going back", func(t *testing.T) {
		h, entries := start(t)
		atExec := -1
		h.upd.exec = func(string) error { atExec = entries(); return nil }
		h.upd.rollback(update.Pending{FromVersion: "v1-202611021500", ToBuild: 202611021700}, "test")
		if atExec != 1 {
			t.Fatalf("the setting held %d entries when the old binary ran, want the delivered push", atExec)
		}
	})
}
