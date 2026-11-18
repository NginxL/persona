package assistant

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// listCountingDevs counts the copies of a machine's whole list (Sessions): a reader of one row makes none.
type listCountingDevs struct {
	*fleetDevs
	n atomic.Int64
}

func (d *listCountingDevs) Sessions(id string) ([]proto.Session, bool) {
	d.n.Add(1)
	return d.fleetDevs.Sessions(id)
}

func (d *listCountingDevs) lists() int { return int(d.n.Load()) }

// settle waits until no background pass has copied a machine's whole list for a quiet window (longer than liveLoop's
// 100 ms coalescing): a pass the fixture started counts too, so the count after it belongs to the call under test
// alone, not to whenever the background work happened to land.
func (d *listCountingDevs) settle() {
	poll := time.NewTicker(200 * time.Millisecond)
	defer poll.Stop()
	for {
		n := d.lists()
		<-poll.C
		if d.lists() == n {
			return
		}
	}
}

// countLists has s count the whole lists its readers copy. Called before anything starts a goroutine on s.
func countLists(s *Service) *listCountingDevs {
	d := &listCountingDevs{fleetDevs: s.devs.(*fleetDevs)}
	s.devs = d
	return d
}

func sessionJSON(t *testing.T, sess proto.Session) string {
	t.Helper()
	b, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The manager's own readers get the rows of sessions.list without its JSON: each row encodes to the bytes the answer
// has, and the muted ones are the answer's.
func TestInProcessSessionsAreTheListsRows(t *testing.T) {
	s := readsGoldenFleet(t)
	s.setMutes(assistantwire.Mutes{Sessions: []string{"mac/plain"}, Spaces: []string{"mac/app"}})
	ctx := context.Background()
	for round := range 2 {
		res, ok, err := s.localFleetDeviceCall(ctx, DeviceCall{Device: "mac", Method: DeviceSessionsList, Params: json.RawMessage(`{}`)})
		if !ok || err != nil {
			t.Fatalf("sessions.list: %v %v", ok, err)
		}
		var want struct {
			Sessions []proto.Session `json:"sessions"`
			Muted    []string        `json:"muted"`
		}
		if err := json.Unmarshal(res, &want); err != nil {
			t.Fatal(err)
		}
		list, muted, err := s.sessionsMuted(ctx, "mac")
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != len(want.Sessions) || len(list) != 7 {
			t.Fatalf("round %d: %d rows, the answer has %d (want 7)", round, len(list), len(want.Sessions))
		}
		for i := range list {
			if got, w := sessionJSON(t, list[i]), sessionJSON(t, want.Sessions[i]); got != w {
				t.Errorf("round %d row %d:\n got %s\nwant %s", round, i, got, w)
			}
		}
		wantMuted := map[string]bool{}
		for _, id := range want.Muted {
			wantMuted[id] = true
		}
		if !maps.Equal(muted, wantMuted) || len(muted) != 3 {
			t.Errorf("round %d: muted %v, the answer's %v (want plain, lead, worker)", round, muted, wantMuted)
		}
	}
	if _, _, err := s.sessionsMuted(ctx, "nowhere"); err != ErrNoDevice {
		t.Errorf("an unknown machine: %v, want ErrNoDevice", err)
	}
}

// decoratedSession is the row of the machine's whole list (TestDecoratedSessionIsTheListsRow) with folds and the
// archive's rows of every kind, and builds no list: the node's row decorated alone, the archive's read from its record.
func TestDecoratedSessionIsTheListsFoldedRow(t *testing.T) {
	s := readsGoldenFleet(t)
	whole := s.decorated("mac")
	d := countLists(s)
	for _, row := range whole {
		got, ok := s.decoratedSession("mac", row.SessionID)
		if !ok {
			t.Errorf("%s: not found", row.SessionID)
			continue
		}
		if !reflect.DeepEqual(got, row) {
			t.Errorf("%s:\n got %s\nwant %s", row.SessionID, sessionJSON(t, got), sessionJSON(t, row))
		}
	}
	for _, sid := range []string{"elsewhere", "never-listed"} { // another machine's record; no row at all
		if _, ok := s.decoratedSession("mac", sid); ok {
			t.Errorf("%s found on mac", sid)
		}
	}
	if d.lists() != 0 {
		t.Errorf("one row at a time copied the whole list %d times", d.lists())
	}
}

// listedSessions are the whole list's rows the node lists, as the list has them, without the archive's.
func TestListedSessionsAreTheNodesRows(t *testing.T) {
	s := readsGoldenFleet(t)
	whole := s.decorated("mac")
	got, err := s.listedSessions(context.Background(), "mac")
	if err != nil {
		t.Fatal(err)
	}
	var want []proto.Session
	for _, row := range whole {
		if _, listed := s.nodeSession("mac", row.SessionID); listed {
			want = append(want, row)
		}
	}
	if len(got) != 5 || len(want) != 5 {
		t.Fatalf("%d rows, %d of the list's the node lists (want 5)", len(got), len(want))
	}
	for i := range got {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("row %d:\n got %s\nwant %s", i, sessionJSON(t, got[i]), sessionJSON(t, want[i]))
		}
	}
	if !got[3].Archived || got[3].ArchiveChangedAt == nil {
		t.Errorf("the archived row the node lists lost its mark: %+v", got[3])
	}
	if _, err := s.listedSessions(context.Background(), "nowhere"); err != ErrNoDevice {
		t.Errorf("an unknown machine: %v, want ErrNoDevice", err)
	}
}

// A turn end reads the session that stopped, and whether it is muted, without its machine's whole list; a pass that
// reads the list later in the same view reads its rows there.
func TestWorkViewReadsOneSession(t *testing.T) {
	s := readsGoldenFleet(t)
	s.setMutes(assistantwire.Mutes{Spaces: []string{"mac/app"}})
	whole := s.decorated("mac")
	ctx := context.Background()
	d := countLists(s)
	for _, row := range whole {
		v := s.newWorkView(ctx)
		got, ok := v.session("mac", row.SessionID)
		if !ok || !reflect.DeepEqual(got, row) {
			t.Errorf("%s: %v\n got %s\nwant %s", row.SessionID, ok, sessionJSON(t, got), sessionJSON(t, row))
		}
		if want := s.mutedAs("mac", row.SessionID, row.SpaceName); v.isMuted("mac", row.SessionID) != want {
			t.Errorf("%s: muted %v, the list says %v", row.SessionID, !want, want)
		}
	}
	v := s.newWorkView(ctx)
	if _, ok := v.session("mac", "never-listed"); ok || v.failed["mac"] {
		t.Errorf("a session no list has: found, or its machine failed")
	}
	if d.lists() != 0 {
		t.Fatalf("one session at a time copied the whole list %d times", d.lists())
	}
	if _, ok := v.session("nowhere", "x"); ok || !v.failed["nowhere"] {
		t.Errorf("an unknown machine: not failed, as sessions.list's error made it")
	}
	if list := v.sessions("mac"); len(list) != len(whole) {
		t.Fatalf("the whole list: %d rows, want %d", len(list), len(whole))
	}
	lists := d.lists()
	if c, ok := v.session("mac", "gone-restored"); !ok || c.SessionID != "gone-restored" || d.lists() != lists {
		t.Errorf("once the list is read, a row is read from it")
	}
	if !v.isMuted("mac", "lead") || v.isMuted("mac", "plain") {
		t.Errorf("muted from the list read: lead %v, plain %v", v.isMuted("mac", "lead"), v.isMuted("mac", "plain"))
	}
}

// The per-minute passes do not list a machine for a task done long ago whose agent's session is gone (a lookup), and
// read what runs without the archive's rows; a done task's agent still listed is read and closed as before.
func TestIdlePassesReadWhatRuns(t *testing.T) {
	s := readsFleet(t, 50, 300)
	ctx := context.Background()
	d := countLists(s)
	doneTaskOnGoneAgent(t, s)
	d.settle() // the fixture's own passes list in the background too; wait them out before counting (#6121)
	before := d.lists()
	refused := taskCloseRefused{}
	s.closeDoneTaskAgents(ctx, refused, doneSeen{})
	if n := d.lists() - before; n != 0 {
		t.Errorf("closeDoneTaskAgents listed the machine %d times for an agent no longer there", n)
	}
	list, err := s.listedSessions(ctx, "mac")
	if err != nil || len(list) != 50 {
		t.Fatalf("listedSessions: %d rows, %v (want the node's 50)", len(list), err)
	}
	if slices.ContainsFunc(list, func(c proto.Session) bool { return c.Status == proto.StatusOffline }) {
		t.Errorf("an archive row among the rows the node lists")
	}
	if c, ok := s.doneTaskAgentRow(ctx, nil, doneTaskAgent{machine: "mac", session: "session-00002"}); !ok ||
		c.SessionID != "session-00002" || c.State == "" || c.MachineID != "mac" {
		t.Errorf("a done task's agent still listed: %v %+v", ok, c)
	}
	if _, ok := s.doneTaskAgentRow(ctx, nil, doneTaskAgent{machine: "mac", session: "gone-00001"}); ok {
		t.Errorf("an archive row read as a running agent")
	}
	if _, ok := s.doneTaskAgentRow(ctx, nil, doneTaskAgent{machine: "nowhere", session: "x"}); ok {
		t.Errorf("a row on an unknown machine")
	}
}
