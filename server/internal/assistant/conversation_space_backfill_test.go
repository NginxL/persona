package assistant

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
)

// The backfill of the spaces of conversations never noted (conversation_space_backfill.go, #10941) as the owner meets
// it: its read of every to-do's conversations holds up no write of the owner's, and a conversation is given up as
// gone only once every computer that may have its transcript was on and listed it not. The names are made up.

// heldFeedStore is the store with its read of the to-dos' history held (a busy or locked database) while held is set:
// the read waits for release, whatever its caller's deadline, and says when it is waiting (entered) and when it has
// answered (answered).
type heldFeedStore struct {
	*repositorystore.Store
	held     atomic.Bool
	entered  chan struct{}
	release  chan struct{}
	answered chan struct{}
	once     sync.Once
	done     sync.Once
}

func newHeldFeedStore(database *db.DB) *heldFeedStore {
	return &heldFeedStore{Store: repositorystore.New(database), entered: make(chan struct{}), release: make(chan struct{}),
		answered: make(chan struct{})}
}

func (s *heldFeedStore) FeedConversations(ctx context.Context, refs, kinds []string) (map[string][]string, error) {
	if s.held.Load() {
		s.once.Do(func() { close(s.entered) })
		<-s.release
		s.done.Do(func() { close(s.answered) })
	}
	return s.Store.FeedConversations(ctx, refs, kinds)
}

// The regression check of the backfill, its latency: after a restart the first profile used to read every to-do's
// conversations from the store inline, while publish held its lock, so every owner's write that publishes (a to-do's
// rename, close or delete, an archive, a new chat) and the first snapshot waited for that read, up to its 5 s bound,
// and again a minute later while the read kept failing. Now the read is on its own: with it held, every one of them
// answers, and once it answers the conversation it found is kept in the next snapshot.
func TestSpaceBackfillReadHoldsUpNoOwnerWrite(t *testing.T) {
	database := todoTestDB(t)
	ctx := context.Background()
	e := newTodoEnv(t, database)
	setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
	main, side := scopeSession("conv1", "Persona", ""), scopeSession("conv_side", "Weekend", "")
	main.Assistant, side.Assistant = true, true
	e.sessions("server", main, side)
	if _, err := e.s.createTaskTodo(ctx, "Book the movers", "", "conv_quiet", "", ""); err != nil {
		t.Fatal(err) // conv_quiet is never listed: quiet since before the manager noted spaces
	}
	renamed, closed, deleted := e.create("Water the plants"), e.create("Pay the rent"), e.create("Call the bank")

	// The manager starts again on a store whose read of the to-dos' history does not answer.
	st := newHeldFeedStore(database)
	st.held.Store(true)
	t.Cleanup(func() { closeOnce(st.release) })
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)
	clock := fakeBackfill(s)
	e = &todoEnv{t: t, s: s, f: f}
	answers := func(what string, fn func()) {
		t.Helper()
		done := make(chan struct{})
		go func() { defer close(done); fn() }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatalf("%s waits for the backfill's read of the to-dos", what)
		}
		select {
		case <-st.answered:
			t.Fatalf("%s answered only once the backfill's read did", what)
		default:
		}
	}
	answers("the first snapshot", func() { s.publish(ctx) })
	select {
	case <-st.entered:
	case <-time.After(20 * time.Second):
		t.Fatal("the first snapshot did not set the backfill's read off")
	}
	answers("a computer's list", func() { e.sessions("server", main, side) })
	status := map[string]int{}
	answers("a rename", func() {
		status["rename"] = e.do("PATCH", "/api/v1/todos/"+renamed.ID, `{"title":"Water the ferns"}`).Code
	})
	answers("a close", func() { status["close"] = e.do("PATCH", "/api/v1/todos/"+closed.ID, `{"done":true}`).Code })
	answers("a delete", func() { status["delete"] = e.do("DELETE", "/api/v1/todos/"+deleted.ID, "").Code })
	answers("an archive", func() {
		status["archive"] = e.do("POST", "/api/v1/assistant/conversations/conv_side/archive", "").Code
	})
	answers("a new chat", func() {
		status["new chat"] = appRequest(t, s, "POST", "/api/v1/assistant/conversations", `{"prompt":"plan the trip"}`, "en").Code
	})
	answers("the snapshot a minute later", func() {
		clock.advance(spaceBackfillRescan)
		s.publish(ctx)
	})
	for what, code := range status {
		if code >= http.StatusBadRequest {
			t.Errorf("%s: %d", what, code)
		}
	}
	if got := s.spaceBackfillKept(); len(got) != 0 {
		t.Fatalf("kept before the read answered: %v", got)
	}

	// The read answers: what it found is kept, and the hub is told.
	closeOnce(st.release)
	waitFor(t, "the backfill's read of the to-dos", s.spaceBackfillScanIdle)
	waitFor(t, "conv_quiet kept in a snapshot", func() bool {
		p := f.lastSnapshot().Profile
		return p != nil && slices.Contains(p.Keep, SessionRef{MachineID: "server", SessionID: "conv_quiet"})
	})
}

// closeOnce closes c unless it is closed.
func closeOnce(c chan struct{}) {
	select {
	case <-c:
	default:
		close(c)
	}
}

// keptOn says whether the profile has machine list conversation id however quiet.
func keptOn(t *testing.T, s *Service, machine, id string) bool {
	t.Helper()
	p := s.profile(context.Background(), []TaskView{}, &TodoReport{})
	if p == nil {
		t.Fatal("no profile")
	}
	return slices.Contains(p.Keep, SessionRef{MachineID: machine, SessionID: id})
}

// givenUp says whether the setting keeps conversation id as given up (false in memory).
func givenUp(t *testing.T, database *db.DB, id string) bool {
	t.Helper()
	if database == nil {
		return false
	}
	gone := map[string]time.Time{}
	if _, err := database.Setting(context.Background(), spaceBackfillGoneKey, &gone); err != nil {
		t.Fatal(err)
	}
	_, ok := gone[id]
	return ok
}

// The regression check of the backfill's give-up, a former computer: the main conversation moved off a laptop, whose
// old chats' transcripts are there. While the laptop is asleep and the assistant's computer on, a chat of its was given
// up after three minutes as if its transcript were gone, kept so for a week across restarts, and its to-do hidden from
// extensions though the laptop, once back, has it. Now it waits for the laptop (not kept meanwhile, the batch going
// on), is kept on it again once every computer is on, and is noted when the laptop lists it; one the laptop does not
// list is given up only once every computer was on for the whole wait.
func TestSpaceBackfillWaitsForAComputerThatIsOff(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
		ctx := context.Background()
		start := func(e *todoEnv) *backfillTimers {
			clock := fakeBackfill(e.s)
			setSettings(t, e.s, func(v *Settings) {
				v.Main = "conv1"
				v.Former = []FormerMain{{MachineID: "laptop", SessionID: "conv_laptop_main", Until: time.Now().Add(-30 * 24 * time.Hour)}}
			})
			return clock
		}
		clock := start(e)
		main := scopeSession("conv1", "Persona", "")
		main.Assistant = true
		e.sessions("server", main)
		e.sessions("laptop") // the laptop lists none of its old chats: they are quiet
		passport, err := e.s.createTaskTodo(ctx, "Renew the passport", "", "conv_on_laptop", "", "")
		if err != nil {
			t.Fatal(err)
		}
		deeds, err := e.s.createTaskTodo(ctx, "Find the deeds", "", "conv_gone", "", "")
		if err != nil {
			t.Fatal(err)
		}
		all := []Todo{passport, deeds}
		if database != nil { // the manager starts again: its read of the to-dos takes them up
			e = newTodoEnv(t, database)
			clock = start(e)
			e.sessions("server", main)
			e.sessions("laptop")
		}
		wantShown(t, e, "never noted", all)
		if got := keeps(t, e.s, "conv_on_laptop", "conv_gone"); !got["conv_on_laptop"] || !got["conv_gone"] {
			t.Fatalf("kept on the assistant's computer: %v", got)
		}
		if !keptOn(t, e.s, "laptop", "conv_on_laptop") || !keptOn(t, e.s, "laptop", "conv_gone") {
			t.Fatal("not kept on the former computer")
		}

		// The laptop sleeps for far longer than the wait: nothing is given up, and nothing is kept meanwhile.
		e.f.mu.Lock()
		e.f.down = map[string]bool{"laptop": true}
		e.f.mu.Unlock()
		for range 20 {
			clock.advance(spaceBackfillWait)
			clock.fire()
		}
		for _, id := range []string{"conv_on_laptop", "conv_gone"} {
			if givenUp(t, database, id) {
				t.Fatalf("%s given up while the laptop is off", id)
			}
			if keptOn(t, e.s, "laptop", id) || keptOn(t, e.s, "server", id) {
				t.Fatalf("%s still kept once it waits for the laptop", id)
			}
		}
		wantShown(t, e, "waiting for the laptop", all)

		// The laptop wakes: both are kept on it again, and it lists the one it has.
		e.f.mu.Lock()
		e.f.down = nil
		e.f.mu.Unlock()
		clock.advance(spaceBackfillTick)
		clock.fire()
		if !keptOn(t, e.s, "laptop", "conv_on_laptop") || !keptOn(t, e.s, "laptop", "conv_gone") {
			t.Fatal("not kept on the laptop once it is back")
		}
		old := scopeSession("conv_on_laptop", "Passport", "")
		old.Assistant = true
		e.sessions("laptop", old)
		e.sessions("laptop") // and leaves it out again
		wantShown(t, e, "conv_on_laptop noted", all, passport)
		if keptOn(t, e.s, "laptop", "conv_on_laptop") {
			t.Fatal("conv_on_laptop still kept once noted")
		}

		// conv_gone is on no computer: given up once every one was on for the whole wait, not before.
		clock.advance(spaceBackfillWait / 2)
		clock.fire()
		if !keptOn(t, e.s, "laptop", "conv_gone") || givenUp(t, database, "conv_gone") {
			t.Fatal("conv_gone given up before every computer was on for the whole wait")
		}
		clock.advance(spaceBackfillWait / 2)
		clock.fire()
		if keptOn(t, e.s, "laptop", "conv_gone") || database != nil && !givenUp(t, database, "conv_gone") {
			t.Fatal("conv_gone not given up once every computer was on for the whole wait")
		}
		if givenUp(t, database, "conv_on_laptop") {
			t.Fatal("conv_on_laptop given up")
		}
		wantShown(t, e, "conv_gone given up", all, passport)
		if database == nil {
			return
		}
		again := newTodoEnv(t, database)
		start(again)
		again.sessions("server", main)
		wantShown(t, again, "after a restart", all, passport)
	})
}

// noteFailingStore is the store with the writes of the conversations' spaces refused while fail is set.
type noteFailingStore struct {
	*repositorystore.Store
	fail atomic.Bool
}

func (s *noteFailingStore) NoteConversationSpaces(ctx context.Context, rows []db.ConversationSpace) error {
	if s.fail.Load() {
		return errors.New("the database refuses writes")
	}
	return s.Store.NoteConversationSpaces(ctx, rows)
}

// The regression check of the backfill's give-up, a failed write: a chat the computer listed while the spaces could not
// be written for longer than the wait was given up as if its transcript were gone, and once the computer no longer
// listed it its to-do was hidden from extensions for a week, though the database was back. Now a chat a computer
// listed is never given up: it stays kept, so the computer lists it again and its note is written once the database
// takes it.
func TestSpaceBackfillNeverGivesUpAListedChat(t *testing.T) {
	database := todoTestDB(t)
	ctx := context.Background()
	e := newTodoEnv(t, database)
	setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
	main := scopeSession("conv1", "Persona", "")
	main.Assistant = true
	e.sessions("server", main)
	todo, err := e.s.createTaskTodo(ctx, "Book the flights", "", "conv_trip", "", "")
	if err != nil {
		t.Fatal(err)
	}
	st := &noteFailingStore{Store: repositorystore.New(database)}
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st) // the manager starts again
	clock := fakeBackfill(s)
	e = &todoEnv{t: t, s: s, f: f}
	e.sessions("server", main)
	if got := keeps(t, s, "conv_trip"); !got["conv_trip"] {
		t.Fatalf("conv_trip not kept: %v", got)
	}
	trip := scopeSession("conv_trip", "Trip", "")
	trip.Assistant = true
	st.fail.Store(true)
	e.sessions("server", main, trip) // listed, its note not written
	wantShown(t, e, "listed", []Todo{todo}, todo)
	for range 4 {
		clock.advance(spaceBackfillWait)
		clock.fire()
	}
	if !keeps(t, s, "conv_trip")["conv_trip"] || givenUp(t, database, "conv_trip") {
		t.Fatal("conv_trip given up though the computer listed it")
	}
	st.fail.Store(false)
	e.sessions("server", main, trip) // the computer lists what it is kept to: the note is written
	if keeps(t, s, "conv_trip")["conv_trip"] {
		t.Fatal("conv_trip still kept once noted")
	}
	e.sessions("server", main)
	wantShown(t, e, "noted, no longer listed", []Todo{todo}, todo)
	again := newTodoEnv(t, database)
	fakeBackfill(again.s)
	again.sessions("server", main)
	wantShown(t, again, "after a restart", []Todo{todo}, todo)
	if givenUp(t, database, "conv_trip") || slices.Contains(again.s.spaceBackfillKept(), "conv_trip") {
		t.Fatal("conv_trip given up, or kept again, after a restart")
	}
}

// The regression check of the backfill, a chat on another computer: the Lark agent's chats run on the computer its
// settings name, the Inbox AI twin's on its own, and any start may name one. Such a chat quiet since before the upgrade
// was kept only on the computers the assistant ran on, none of which has its transcript, and a verdict read only the
// assistant's computer's list: it was given up though its computer was on, its to-do hidden from extensions for good,
// and, with no extension at all, the outcome line of a task it opened held and lost at the next restart. It is kept on
// the computer the origins book places it on too now, told by that computer's list while its note cannot be written,
// and given up only once that computer too was on for the whole wait.
func TestSpaceBackfillKeepsAChatOnTheComputerItStartedOn(t *testing.T) {
	database := todoTestDB(t)
	dir := t.TempDir()
	ctx := context.Background()
	main := scopeSession("conv1", "Persona", "")
	main.Assistant = true
	start := func() (*todoEnv, *noteFailingStore, *backfillTimers) {
		f := newFakeHub().withMachine()
		f.setSessions("box", proto.Session{SessionID: "s1", Title: "Fix login", Cwd: "/code/weather/ios"})
		f.setSessions("studio") // on: its chats are quiet
		f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: outcomeReply})
		f.mu.Lock()
		f.hello.KB = &KBSettings{}
		f.mu.Unlock()
		st := &noteFailingStore{Store: repositorystore.New(database)}
		s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t), KB: &KBOptions{Dir: dir}}))
		t.Cleanup(s.kbEnd)
		clock := fakeBackfill(s)
		setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
		e := &todoEnv{t: t, s: s, f: f}
		e.sessions("server", main)
		return e, st, clock
	}
	e, _, _ := start()
	if _, err := kbEng(t, e.s).Apply(kb.KindProject, "Weather App", kb.Update{Dirs: []string{"box:/code/weather"}}); err != nil {
		t.Fatal(err)
	}
	// The Lark agent's chats on studio, quiet since before the manager noted spaces: one's transcript is there, the
	// other's is gone.
	e.s.originSet(ctx, "studio", "conv_far", OriginLarkAgent, "", "")
	e.s.originSet(ctx, "studio", "conv_far_gone", OriginLarkAgent, "", "")
	crash, err := e.s.createTodo(ctx, "Fix the landscape crash", "", "conv_far", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.linkTodoAgent(ctx, crash.ID, "box", "s1", "Fix login", "/code/weather/ios", "claude"); err != nil {
		t.Fatal(err)
	}
	prints, err := e.s.createTodo(ctx, "Order the prints", "", "conv_far_gone", "")
	if err != nil {
		t.Fatal(err)
	}
	all := []Todo{crash, prints}
	e.s.kbEnd()

	e, st, clock := start() // the upgrade's first start
	timers := fakeOutcomeTimers(e.s)
	wantShown(t, e, "never noted", all)
	for _, id := range []string{"conv_far", "conv_far_gone"} {
		if !keptOn(t, e.s, "studio", id) || !keptOn(t, e.s, "server", id) {
			t.Fatalf("%s not kept on the computer it started on and the assistant's", id)
		}
	}

	// The task closes; studio lists its chat as it is kept to while the note cannot be written: told by studio's list,
	// the outcome line held at the close is written.
	e.f.outcome = `{"outcome":"fixed the landscape crash, PR merged"}`
	timeline := func() []string { return kbEng(t, e.s).Store.Log("weather-app", 20) }
	e.s.recordTaskOutcome(todoNow(e.s, crash.ID), time.Now().UTC())
	if log := timeline(); len(log) != 0 {
		t.Fatalf("the timeline while conv_far cannot be told: %q", log)
	}
	far := scopeSession("conv_far", "Prints", "")
	far.Assistant = true
	st.fail.Store(true)
	e.sessions("studio", far)
	waitFor(t, "the note tried", e.s.listedSpacesWritten)
	timers.fire()
	if log := timeline(); len(log) != 1 || !strings.Contains(log[0], "fixed the landscape crash") {
		t.Fatalf("the timeline once studio lists conv_far: %q", log)
	}
	wantShown(t, e, "conv_far listed by studio", all, crash)
	st.fail.Store(false)
	e.sessions("studio", far) // its note written
	waitFor(t, "the note written", e.s.listedSpacesWritten)
	e.sessions("studio") // quiet again
	if keptOn(t, e.s, "studio", "conv_far") {
		t.Fatal("conv_far still kept once noted")
	}
	wantShown(t, e, "conv_far noted", all, crash)

	// conv_far_gone: not given up while studio is off, however long; given up once studio was on for the whole wait.
	e.f.mu.Lock()
	e.f.down = map[string]bool{"studio": true}
	e.f.mu.Unlock()
	for range 10 {
		clock.advance(spaceBackfillWait)
		clock.fire()
	}
	if givenUp(t, database, "conv_far_gone") || keptOn(t, e.s, "studio", "conv_far_gone") {
		t.Fatal("conv_far_gone given up, or still kept, while studio is off")
	}
	e.f.mu.Lock()
	e.f.down = nil
	e.f.mu.Unlock()
	clock.advance(spaceBackfillTick)
	clock.fire()
	if !keptOn(t, e.s, "studio", "conv_far_gone") {
		t.Fatal("conv_far_gone not kept on studio again once it is on")
	}
	clock.advance(spaceBackfillWait)
	clock.fire()
	if !givenUp(t, database, "conv_far_gone") {
		t.Fatal("conv_far_gone not given up once studio was on for the whole wait")
	}
	if givenUp(t, database, "conv_far") {
		t.Fatal("conv_far given up")
	}
	e.s.kbEnd()

	again, _, _ := start()
	wantShown(t, again, "after a restart", all, crash)
}
