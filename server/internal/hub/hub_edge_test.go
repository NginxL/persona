package hub

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/update"
)

// Behind the edge an update does not exec: once idle, the new binary is in place and the edge is asked to switch to
// it, as the build the manifest says; no watchdog is started (the edge is the watchdog).
func TestUpdateHandsOverToTheEdge(t *testing.T) {
	p := newPublisher(t, "v2-202611021700", 202611021700)
	h, _, target, execs := updatingHub(t, p, "release")
	type call struct {
		target string
		build  int64
	}
	calls := make(chan call, 1)
	h.upd.handover = func(_ context.Context, target string, build int64) error {
		calls <- call{target, build}
		return nil
	}
	h.upd.startWatch = func(string, string) error { t.Error("a watchdog was started behind the edge"); return nil }
	ctx := context.Background()
	if _, err := h.upd.check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.upd.begin(ctx, false); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-calls:
		if c.target != target || c.build != 202611021700 {
			t.Fatalf("handed over to %s build %d", c.target, c.build)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("never handed over")
	}
	if len(execs()) != 0 {
		t.Fatalf("exec'd %v behind the edge", execs())
	}
	if b, _ := os.ReadFile(target); !bytes.Equal(b, p.script) {
		t.Fatal("the new binary is not in place for the edge to start")
	}
	if ok, _ := update.Load(update.PendingPath(h.upd.dir), &update.Pending{}); !ok {
		t.Fatal("no pending marker for the new slot to confirm")
	}
}

// When the new binary does not come up, the edge says so and this hub goes on: what it swapped is put back, the
// marker goes, and the update is recorded as failed (not as a bad build: it never ran).
func TestUpdateTheEdgeCouldNotStart(t *testing.T) {
	p := newPublisher(t, "v2-202611021700", 202611021700)
	h, _, target, _ := updatingHub(t, p, "release")
	h.upd.handover = func(context.Context, string, int64) error { return errors.New("it exited (code 1)") }
	ctx := context.Background()
	if _, err := h.upd.check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.upd.begin(ctx, false); err != nil {
		t.Fatal(err)
	}
	waitForUpdate(t, h.upd, "the failure", func() bool { return h.upd.view().State == "failed" })
	if b, _ := os.ReadFile(target); string(b) != "#!/bin/sh\necho old\n" {
		t.Fatal("the old binary was not put back")
	}
	if _, err := os.Stat(update.PendingPath(h.upd.dir)); !os.IsNotExist(err) {
		t.Fatal("the pending marker stayed")
	}
	hs := h.upd.settings.History
	if len(hs) != 1 || hs[0].Result != "failed" || !strings.Contains(hs[0].Reason, "it exited (code 1)") || len(h.upd.settings.Bad) != 0 {
		t.Fatalf("history %+v, bad %v", hs, h.upd.settings.Bad)
	}
}

// A confirmed update tells the edge, whose own 3-minute check of it ends.
func TestUpdateConfirmTellsTheEdge(t *testing.T) {
	p := newPublisher(t, "v2-202611021700", 202611021700)
	h, _, target, _ := updatingHub(t, p, "release")
	h.build = update.Make("v2-202611021700", "release", "202611021700", p.srv.URL+"/dl")
	told := 0
	h.upd.edgeConfirmed = func() { told++ }
	pending := update.Pending{FromBuild: 202611021500, ToBuild: 202611021700, Target: target, Prev: target + ".prev", Online: []string{"box"}}
	update.Save(update.PendingPath(h.upd.dir), pending)
	h.upd.confirm(context.Background(), pending)
	if told != 1 {
		t.Fatalf("the edge was told %d times", told)
	}
}

// The edge's way back (a new slot that could not take over, or did not confirm in time) puts the old binaries back,
// leaves the rollback for the old version to record, and only for the update under way.
func TestRestoreUpdate(t *testing.T) {
	dir := t.TempDir()
	target := dir + "/personad"
	os.WriteFile(target, []byte("new"), 0o755)
	os.WriteFile(target+".prev", []byte("old"), 0o755)
	pending := update.Pending{FromVersion: "v1", FromBuild: 1, ToVersion: "v2", ToBuild: 2, Target: target, Prev: target + ".prev", Auto: true}
	if err := update.Save(update.PendingPath(dir), pending); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := restoreUpdateIn(dir, 3, nil); ok || err != nil {
		t.Fatal("restored for another build")
	}
	bin, ok, err := restoreUpdateIn(dir, 2, errors.New("cannot migrate"))
	if err != nil || !ok || bin != target {
		t.Fatalf("restore = %q %v", bin, ok)
	}
	if b, _ := os.ReadFile(target); string(b) != "old" {
		t.Fatal("the old binary is not back")
	}
	var rb update.RolledBack
	if ok, _ := update.Load(update.RolledBackPath(dir), &rb); !ok || rb.ToBuild != 2 || !strings.Contains(rb.Reason, "cannot migrate") || !rb.Auto {
		t.Fatalf("rollback marker %+v", rb)
	}
	if _, err := os.Stat(update.PendingPath(dir)); !os.IsNotExist(err) {
		t.Fatal("the pending marker stayed")
	}
	if _, ok, err := restoreUpdateIn(dir, 2, nil); ok || err != nil {
		t.Fatal("restored twice")
	}
}

// A hub that ran as persona-hub comes back as persona-hub: the edge restores every program the update swapped and
// runs Pending.Run, not personad. A pending without Run (an older build's) runs Target, as TestRestoreUpdate shows.
func TestRestoreUpdateRunsTheHubsProgram(t *testing.T) {
	dir := t.TempDir()
	target, hubBin := filepath.Join(dir, "personad"), filepath.Join(dir, "persona-hub")
	for path, old := range map[string]string{target: "old personad", hubBin: "old persona-hub"} {
		os.WriteFile(path, []byte("new"), 0o755)
		os.WriteFile(path+".prev", []byte(old), 0o755)
	}
	pending := update.Pending{FromBuild: 1, ToBuild: 2, Target: target, Prev: target + ".prev", Run: hubBin,
		Programs: []update.Swapped{{Target: hubBin, Prev: hubBin + ".prev"}}}
	if err := update.Save(update.PendingPath(dir), pending); err != nil {
		t.Fatal(err)
	}
	bin, ok, err := restoreUpdateIn(dir, 2, nil)
	if err != nil || !ok || bin != hubBin {
		t.Fatalf("restore = %q %v, want %q", bin, ok, hubBin)
	}
	for path, want := range map[string]string{target: "old personad", hubBin: "old persona-hub"} {
		if b, _ := os.ReadFile(path); string(b) != want {
			t.Fatalf("%s is %q after going back", filepath.Base(path), b)
		}
	}
}
