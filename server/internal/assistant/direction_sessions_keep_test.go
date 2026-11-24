package assistant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The sessions placed under a current direction stay listed however quiet they are (#11515): the profile's keep takes
// them, a filing write publishes it, and the automatic archive leaves the owner's filings. Every env here sets only the
// computer, the main conversation and the directions: no setting turns any of it on.

// directionKeepNow publishes and answers the profile's keep (nil: left out).
func directionKeepNow(t *testing.T, s *Service, f *fakeHub) []SessionRef {
	t.Helper()
	s.publish(context.Background())
	p := f.lastSnapshot().Profile
	if p == nil {
		t.Fatal("the snapshot has no profile")
	}
	return p.Keep
}

func directionSnaps(f *fakeHub) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.snaps)
}

// directionClosedAgent is an Agent closed and idle for five days, past archive_closed_days.
func directionClosedAgent(id string) proto.Session {
	return proto.Session{SessionID: id, Agent: "claude", Cwd: "/home/u/code", Status: proto.StatusOffline, State: proto.StateOffline,
		Title: "Agent " + id, StartedByPersona: true, UpdatedAt: time.Now().Add(-5 * 24 * time.Hour).UTC()}
}

// One session on its own, one taken out, two filed under a current direction on two computers, one filed under a
// direction that then ends, and a conversation the current direction's leader opened: keep holds the two filings and
// the opened conversation besides what it held before, sorted by computer and session; it lets go of the conversation
// set on its own or deleted, and of an ended direction's sessions until the direction is made again.
func TestDirectionKeepHoldsTheCurrentDirectionsSessions(t *testing.T) {
	ft := directionFilingFleetEnv(t, newFleetMem())
	ctx := context.Background()
	if err := ft.s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	directionFleetComputer(t, ft, "box", directionStoppedAgent("agent-box"), directionStoppedAgent("agent-own"),
		directionStoppedAgent("agent-out"), directionStoppedAgent("agent-b"))
	directionFleetComputer(t, ft, "laptop", directionStoppedAgent("agent-laptop"))
	opened := proto.Session{MachineID: "mac", SessionID: "chat-opened", Cwd: "/home/u/.persona/assistant", Assistant: true,
		Status: proto.StatusIdle, Title: "Opened by a leader"}
	ft.d.mu.Lock()
	mac := append(slices.Clone(ft.d.machines["mac"].sessions), opened)
	ft.d.mu.Unlock()
	ft.sessions("mac", mac...)
	ft.f.setSessions("mac", mac...)
	ft.s.originSet(ctx, "mac", "chat-opened", OriginSpawned, "conv_lead_a", "")

	before := directionKeepNow(t, ft.s, ft.f)
	directionFilingMustPut(t, ft.s, "box", "agent-box", "conv_lead_a")
	directionFilingMustPut(t, ft.s, "laptop", "agent-laptop", "conv_lead_a")
	directionFilingMustPut(t, ft.s, "box", "agent-own", "")
	directionFilingMustPut(t, ft.s, "box", "agent-out", "conv_lead_a")
	directionFilingMustRemove(t, ft.s, "box", "agent-out")
	directionFilingMustPut(t, ft.s, "box", "agent-b", "conv_lead_b")

	base := []SessionRef{{MachineID: "mac", SessionID: treeMain}, {MachineID: "mac", SessionID: "conv_lead_a"},
		{MachineID: "mac", SessionID: "conv_lead_b"}}
	if !slices.Equal(before, append(slices.Clone(base), SessionRef{MachineID: "mac", SessionID: "chat-opened"})) {
		t.Fatalf("keep before any filing: %+v", before)
	}
	want := append(slices.Clone(base), SessionRef{MachineID: "box", SessionID: "agent-b"}, SessionRef{MachineID: "box", SessionID: "agent-box"},
		SessionRef{MachineID: "laptop", SessionID: "agent-laptop"}, SessionRef{MachineID: "mac", SessionID: "chat-opened"})
	if got := directionKeepNow(t, ft.s, ft.f); !slices.Equal(got, want) {
		t.Fatalf("keep:\n got %+v\nwant %+v", got, want)
	}

	// The opened conversation set on its own leaves keep; taken out of the book again it is the leader's once more.
	openedRef := SessionRef{MachineID: "mac", SessionID: "chat-opened"}
	directionFilingMustPut(t, ft.s, "mac", "chat-opened", "")
	if got := directionKeepNow(t, ft.s, ft.f); slices.Contains(got, openedRef) {
		t.Fatalf("a conversation set on its own is still kept: %+v", got)
	}
	directionFilingMustRemove(t, ft.s, "mac", "chat-opened")
	if got := directionKeepNow(t, ft.s, ft.f); !slices.Contains(got, openedRef) {
		t.Fatalf("the leader's conversation is not kept again: %+v", got)
	}
	// Deleted, its origin stays on record, and the deletion record leaves it out.
	if code, _ := doRoute(t, ft.s, http.MethodDelete, "/api/v1/assistant/conversations/chat-opened", `{"machine_id":"mac"}`, roleFull); code != http.StatusOK {
		t.Fatalf("delete the opened conversation: %d", code)
	}
	if got := directionKeepNow(t, ft.s, ft.f); slices.Contains(got, openedRef) {
		t.Fatalf("a deleted conversation is still kept: %+v", got)
	}

	// B ends: its filing leaves keep and stays in the book; made a direction again, it is kept again.
	filedB := SessionRef{MachineID: "box", SessionID: "agent-b"}
	if code, _ := doRoute(t, ft.s, http.MethodPost, "/api/v1/assistant/directions/conv_lead_b/end", "", roleFull); code != http.StatusOK {
		t.Fatalf("end B: %d", code)
	}
	ft.s.directionsSync(ctx)
	if got := directionKeepNow(t, ft.s, ft.f); slices.Contains(got, filedB) || !slices.Contains(got, SessionRef{MachineID: "box", SessionID: "agent-box"}) {
		t.Fatalf("keep after B ended: %+v", got)
	}
	if !directionBookHas(t, ft.s, "box", "agent-b") {
		t.Fatalf("ending B took its entry out of the book: %s", directionBookSnapshot(t, ft.s))
	}
	if code, _ := doRoute(t, ft.s, http.MethodPost, "/api/v1/assistant/directions/conv_lead_b/make", "", roleFull); code != http.StatusOK {
		t.Fatalf("make B again: %d", code)
	}
	ft.s.directionsSync(ctx)
	if got := directionKeepNow(t, ft.s, ft.f); !slices.Contains(got, filedB) {
		t.Fatalf("B made again does not keep its filing: %+v", got)
	}
}

// directionKeepBook stores entries as the book and has the manager read it again (a book written by writes the
// limit would refuse, as an Undo or a filed Agent that stopped following its task can leave it).
func directionKeepBook(t *testing.T, s *Service, entries map[string]directionFiling) {
	t.Helper()
	if err := s.store().PutSetting(context.Background(), directionSessionsSettingKey, entries); err != nil {
		t.Fatal(err)
	}
	s.directionFilings.mu.Lock()
	s.directionFilings.loaded = false
	s.directionFilings.mu.Unlock()
}

// A direction keeps 50: the owner's filings first, newest first, then the conversations its leader opened. Another
// direction's sessions do not count against it.
func TestDirectionKeepHoldsFiftyPerDirectionFilingsFirst(t *testing.T) {
	e := directionFilingEnv(t, nil)
	ctx := context.Background()
	base := time.Now().Add(-24 * time.Hour).UTC()
	entries := map[string]directionFiling{
		runtimeListID("box", "agent-of-b"): {Direction: "conv_lead_b", By: titleByUser, At: base, Title: "B's"},
	}
	file := func(n int) {
		for i := range n {
			id := fmt.Sprintf("agent-%02d", i)
			entries[runtimeListID("box", id)] = directionFiling{Direction: "conv_lead_a", By: titleByUser, At: base.Add(time.Duration(i) * time.Minute), Title: id}
		}
		directionKeepBook(t, e.s, entries)
	}
	e.s.originSet(ctx, "server", "chat-opened", OriginSpawned, "conv_lead_a", "") // newer than every filing
	placed := func() (filings []string, opened, ofB bool) {
		for _, r := range directionKeepNow(t, e.s, e.f) {
			switch {
			case r.MachineID == "box" && r.SessionID == "agent-of-b":
				ofB = true
			case r.MachineID == "box":
				filings = append(filings, r.SessionID)
			case r.SessionID == "chat-opened":
				opened = true
			}
		}
		return filings, opened, ofB
	}
	ids := func(from, to int) []string {
		var out []string
		for i := from; i < to; i++ {
			out = append(out, fmt.Sprintf("agent-%02d", i))
		}
		return out
	}

	file(49)
	if filings, opened, ofB := placed(); !slices.Equal(filings, ids(0, 49)) || !opened || !ofB {
		t.Fatalf("49 filings: %v, opened conversation %v, B's %v", filings, opened, ofB)
	}
	file(50)
	if filings, opened, ofB := placed(); !slices.Equal(filings, ids(0, 50)) || opened || !ofB {
		t.Fatalf("50 filings: %v, opened conversation %v (want the filings only), B's %v", filings, opened, ofB)
	}
	file(51)
	if filings, opened, ofB := placed(); !slices.Equal(filings, ids(1, 51)) || opened || !ofB {
		t.Fatalf("51 filings: %v (want the 50 newest), opened conversation %v, B's %v", filings, opened, ofB)
	}
}

// A filing write that changes the book publishes: the next snapshot's keep has the session with nothing else
// published, and taking the entry out takes it out. A write that changes nothing publishes nothing.
func TestDirectionFilingWritePublishesKeep(t *testing.T) {
	e := directionFilingEnv(t, nil)
	chat := SessionRef{MachineID: "server", SessionID: "chat-a"}
	kept := func() bool {
		p := e.f.lastSnapshot().Profile
		return p != nil && slices.Contains(p.Keep, chat)
	}
	// The setup publishes twice: setSettings at once, and directionsSync from a goroutine it does not wait for
	// (directions.go). Count from after both, or a late second one looks like the filing's (#13945).
	waitFor(t, "the setup's two snapshots", func() bool { return directionSnaps(e.f) >= 2 })
	n := directionSnaps(e.f)
	directionFilingMustPut(t, e.s, "server", "chat-a", "conv_lead_a")
	if got := directionSnaps(e.f); got != n+1 || !kept() {
		t.Fatalf("after filing: %d snapshots (want %d), kept %v", got, n+1, kept())
	}
	directionFilingMustPut(t, e.s, "server", "chat-a", "conv_lead_a")
	if got := directionSnaps(e.f); got != n+1 {
		t.Fatalf("an unchanged write published: %d snapshots, want %d", got, n+1)
	}
	directionFilingMustRemove(t, e.s, "server", "chat-a")
	if got := directionSnaps(e.f); got != n+2 || kept() {
		t.Fatalf("after taking it out: %d snapshots (want %d), kept %v", got, n+2, kept())
	}
}

// When the book, the recorded openers, the deletion record or the directions cannot be read, the profile leaves keep
// out, so the nodes keep what they list: a failed read never drops a row. Read again, keep is whole.
func TestProfileLeavesKeepOutWhenPlacementsCannotBeRead(t *testing.T) {
	errDown := errors.New("synthetic read failure")
	for _, tc := range []struct {
		key    string
		reread func(s *Service)
	}{
		{directionSessionsKey, func(s *Service) {
			s.directionFilings.mu.Lock()
			s.directionFilings.loaded = false
			s.directionFilings.mu.Unlock()
		}},
		{originsKey, func(s *Service) { s.origins.mu.Lock(); s.origins.loaded = false; s.origins.mu.Unlock() }},
		{deletedConversationsKey, func(s *Service) {
			s.deletedConversations.mu.Lock()
			s.deletedConversations.loaded = false
			s.deletedConversations.mu.Unlock()
		}},
		{directionsKey, func(s *Service) { s.directions.mu.Lock(); s.directions.loaded = false; s.directions.mu.Unlock() }},
	} {
		t.Run(tc.key, func(t *testing.T) {
			st := &memStore{}
			e := directionFilingEnv(t, st)
			e.s.originSet(context.Background(), "server", "chat-opened", OriginSpawned, "conv_lead_a", "")
			directionFilingMustPut(t, e.s, "server", "chat-a", "conv_lead_a")
			want := []SessionRef{{MachineID: "server", SessionID: "chat-a"}, {MachineID: "server", SessionID: "chat-opened"}}
			if got := directionKeepNow(t, e.s, e.f); !directionKeepHas(got, want) {
				t.Fatalf("precondition: keep %+v", got)
			}
			st.failKey(tc.key, errDown)
			tc.reread(e.s)
			if got := directionKeepNow(t, e.s, e.f); got != nil {
				t.Fatalf("keep with %s unreadable: %+v, want it left out", tc.key, got)
			}
			st.failKey(tc.key, nil)
			if got := directionKeepNow(t, e.s, e.f); !directionKeepHas(got, want) {
				t.Fatalf("keep once %s reads again: %+v", tc.key, got)
			}
		})
	}
}

func directionKeepHas(keep, want []SessionRef) bool {
	for _, r := range want {
		if !slices.Contains(keep, r) {
			return false
		}
	}
	return keep != nil
}

// A closed Agent idle past archive_closed_days on no open to-do is not archived by itself while it is filed under a
// current direction; once its direction ends it is again. One whose entry was taken out, and one set on its own, are
// archived as ever. The owner's own Archive still archives the filed one.
func TestAutoArchiveLeavesASessionFiledUnderACurrentDirection(t *testing.T) {
	ft := directionFilingFleetEnv(t, newFleetMem())
	ctx := context.Background()
	if err := ft.s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	ft.f.mu.Lock()
	ft.f.sessions["box"] = nil // the fake hub reaches box's node
	ft.f.mu.Unlock()
	directionFleetComputer(t, ft, "box", directionClosedAgent("agent-filed"), directionClosedAgent("agent-free"),
		directionClosedAgent("agent-out"), directionClosedAgent("agent-own"))
	directionFilingMustPut(t, ft.s, "box", "agent-filed", "conv_lead_a")
	directionFilingMustPut(t, ft.s, "box", "agent-out", "conv_lead_a")
	directionFilingMustRemove(t, ft.s, "box", "agent-out") // its entry stays a week, marked, for its Undo
	directionFilingMustPut(t, ft.s, "box", "agent-own", "")
	candidates := func(now time.Time) []string {
		var out []string
		for _, k := range ft.s.autoArchiveCandidates(ctx, now) {
			out = append(out, k[1])
		}
		slices.Sort(out)
		return out
	}
	if got := candidates(time.Now()); !slices.Equal(got, []string{"agent-free", "agent-out", "agent-own"}) {
		t.Fatalf("candidates while filed: %v", got)
	}
	if n, got := ft.s.archiveClosedSessions(ctx, time.Now()), archivedIDs(t, ft.s); n != 3 || got["agent-filed"] ||
		!got["agent-free"] || !got["agent-out"] || !got["agent-own"] {
		t.Fatalf("the pass archived %d: %v", n, got)
	}

	// The owner's own Archive is unchanged: it archives the filed session, and restores it.
	if code, body := doRoute(t, ft.s, http.MethodPost, "/api/v1/sessions/box/agent-filed/archive", "", roleFull); code != http.StatusOK {
		t.Fatalf("the owner's archive of a filed session: %d %v", code, body)
	}
	if !archivedIDs(t, ft.s)["agent-filed"] {
		t.Fatalf("the owner's archive did not archive the filed session: %v", archivedIDs(t, ft.s))
	}
	if code, body := doRoute(t, ft.s, http.MethodPost, "/api/v1/sessions/box/agent-filed/unarchive", "", roleFull); code != http.StatusOK {
		t.Fatalf("restore: %d %v", code, body)
	}

	later := time.Now().Add(4 * 24 * time.Hour) // past archive_closed_days since the restore
	if got := candidates(later); len(got) != 0 {
		t.Fatalf("candidates while filed, after the restore: %v", got)
	}
	if code, _ := doRoute(t, ft.s, http.MethodPost, "/api/v1/assistant/directions/conv_lead_a/end", "", roleFull); code != http.StatusOK {
		t.Fatalf("end A: %d", code)
	}
	if got := candidates(later); !slices.Equal(got, []string{"agent-filed"}) {
		t.Fatalf("candidates once its direction ended: %v", got)
	}
}

// directionArchiveHookStore is the fleet's store with a hook run once, after the first save of the archive records: what
// lands while a pass archives, after it took its candidates and before it writes the next one.
type directionArchiveHookStore struct {
	*fleetMem
	mu        sync.Mutex
	onArchive func()
}

func (st *directionArchiveHookStore) PutSetting(ctx context.Context, key string, v any) error {
	if err := st.fleetMem.PutSetting(ctx, key, v); err != nil || key != agentArchivesKey {
		return err
	}
	st.mu.Lock()
	hook := st.onArchive
	st.onArchive = nil
	st.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

// A filing that lands while a pass archives keeps its session: two closed Agents are candidates, both are filed under a
// current direction once the pass has written its first archive, and the second is looked at again before its write
// and left.
func TestAutoArchiveLooksAgainBeforeEachWrite(t *testing.T) {
	mem := newFleetMem()
	st := &directionArchiveHookStore{fleetMem: mem}
	ft := directionFilingFleetEnvOn(t, st, mem)
	ctx := context.Background()
	if err := ft.s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	ft.f.mu.Lock()
	ft.f.sessions["box"] = nil
	ft.f.mu.Unlock()
	directionFleetComputer(t, ft, "box", directionClosedAgent("agent-x"), directionClosedAgent("agent-y"))
	if got := ft.s.autoArchiveCandidates(ctx, time.Now()); len(got) != 2 {
		t.Fatalf("precondition: both are candidates: %v", got)
	}
	at := time.Now().UTC()
	st.onArchive = func() { // the owner files both, as the book has it once that write is saved
		directionKeepBook(t, ft.s, map[string]directionFiling{
			runtimeListID("box", "agent-x"): {Direction: "conv_lead_a", By: titleByUser, At: at, Title: "Agent agent-x"},
			runtimeListID("box", "agent-y"): {Direction: "conv_lead_a", By: titleByUser, At: at, Title: "Agent agent-y"},
		})
	}
	n, got := ft.s.archiveClosedSessions(ctx, time.Now()), archivedIDs(t, ft.s)
	if n != 1 || len(got) != 1 {
		t.Fatalf("the pass archived %d (%v): the one filed before its write should stay", n, got)
	}
}

// When the book cannot be read, the automatic archive archives nothing that round, not even a session no direction
// holds; read again, the pass goes on and still leaves the filed one.
func TestAutoArchiveArchivesNothingWhenTheBookCannotBeRead(t *testing.T) {
	st := newFleetMem()
	ft := directionFilingFleetEnv(t, st)
	ctx := context.Background()
	if err := ft.s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	ft.f.mu.Lock()
	ft.f.sessions["box"] = nil
	ft.f.mu.Unlock()
	directionFleetComputer(t, ft, "box", directionClosedAgent("agent-filed"), directionClosedAgent("agent-free"))
	directionFilingMustPut(t, ft.s, "box", "agent-filed", "conv_lead_a")
	st.failKey(directionSessionsKey, errors.New("synthetic read failure"))
	ft.s.directionFilings.mu.Lock()
	ft.s.directionFilings.loaded = false
	ft.s.directionFilings.mu.Unlock()
	if got := ft.s.autoArchiveCandidates(ctx, time.Now()); len(got) != 0 {
		t.Fatalf("candidates with the book unreadable: %v", got)
	}
	if n := ft.s.archiveClosedSessions(ctx, time.Now()); n != 0 || len(archivedIDs(t, ft.s)) != 0 {
		t.Fatalf("archived %d with the book unreadable: %v", n, archivedIDs(t, ft.s))
	}
	st.failKey(directionSessionsKey, nil)
	if n := ft.s.archiveClosedSessions(ctx, time.Now()); n != 1 || !archivedIDs(t, ft.s)["agent-free"] {
		t.Fatalf("archived %d once the book reads: %v", n, archivedIDs(t, ft.s))
	}
}
