package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
)

// What a switch survives: an end racing its begin, a manager that stopped waiting, a hub that stopped mid-begin, a
// stale or foreign end, a move or a removal at the same time, and a manager that never ends it.

// switchSteps is operation op's steps in computer mid's audit, oldest first.
func switchSteps(t *testing.T, database *db.DB, mid, op string) []string {
	t.Helper()
	rows, err := database.LinkSwitchHistory(context.Background(), mid, op)
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, r := range rows {
		steps = append(steps, r.Step)
	}
	return steps
}

func linkInUse(t *testing.T, h *Hub, mid string) bool {
	t.Helper()
	used, err := h.store.MachineLinkInUse(context.Background(), mid)
	if err != nil {
		t.Fatal(err)
	}
	return used
}

// gatedStore holds a begin in its checks (MachineLinkInUse) until released.
type gatedStore struct {
	*db.DB
	once             sync.Once
	entered, release chan struct{}
}

func (s *gatedStore) MachineLinkInUse(ctx context.Context, machine string) (bool, error) {
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
	return s.DB.MachineLinkInUse(ctx, machine)
}

// An end that comes while its begin is under way waits for it, then reads what the begin did: the rollback undoes it.
func TestLinkSwitchEndWaitsForItsBegin(t *testing.T) {
	h, database, _ := switchHub(t)
	g := &gatedStore{DB: database, entered: make(chan struct{}), release: make(chan struct{})}
	h.store = g
	op := newSwitchOp()
	begun := make(chan error, 1)
	go func() {
		_, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: op, SSHHost: "me@box.example"})
		begun <- err
	}()
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the begin never got to its checks")
	}
	ended := make(chan error, 1)
	go func() {
		ended <- busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-box", Operation: op, Outcome: devicewire.LinkSwitchRolledBack})
	}()
	select {
	case err := <-ended:
		t.Fatalf("the end answered (%v) while its begin was under way", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(g.release)
	if err := <-begun; err != nil {
		t.Fatal(err)
	}
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
	if diskKind(t, h, "lsw-box") != "ssh" || h.cfgEntry("lsw-box").Kind != "ssh" || linkInUse(t, h, "lsw-box") {
		t.Fatal("the begin the rollback waited for is still in effect")
	}
	if steps := switchSteps(t, database, "lsw-box", op); !slices.Equal(steps, []string{"begin", "begun", "rolled_back"}) {
		t.Fatalf("audited %v", steps)
	}
}

// countingStore counts a begin's checks (MachineLinkInUse).
type countingStore struct {
	*db.DB
	checks int
}

func (s *countingStore) MachineLinkInUse(ctx context.Context, machine string) (bool, error) {
	s.checks++
	return s.DB.MachineLinkInUse(ctx, machine)
}

// A begin whose manager stopped waiting while it waited for its turn changes nothing, and asks nothing more.
func TestLinkSwitchBeginAbandonedByItsManager(t *testing.T) {
	h, database, _ := switchHub(t)
	counted := &countingStore{DB: database}
	h.store = counted
	op := newSwitchOp()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw, _ := json.Marshal(devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: op, SSHHost: "me@box.example"})
	if _, err := busMethods[devicewire.MethodLinkSwitchBegin](h, ctx, nil, raw); !errors.Is(err, context.Canceled) {
		t.Fatalf("begin: %v", err)
	}
	checks := counted.checks
	if checks != 0 || diskKind(t, h, "lsw-box") != "ssh" || linkInUse(t, h, "lsw-box") || len(switchSteps(t, database, "lsw-box", op)) != 0 ||
		h.linkSwitch.live("lsw-box", time.Now()) {
		t.Fatalf("an abandoned begin went on (%d checks)", checks)
	}
}

// The hub stopped after a begin's kind changed and before its code was recorded (its memory gone; the audit has its
// begin). The computer stays held: no move, no removal, no renaming after its system. Its rollback without a hash
// revokes every Persona Link credential of the computer while it is kind link (the code may have become one) and puts
// it back to ssh; one naming a hash, or a success, is refused.
func TestLinkSwitchHubStoppedMidBegin(t *testing.T) {
	h, database, _ := switchHub(t)
	ctx := context.Background()
	op := newSwitchOp()
	h.machineAdd.Lock()
	if err := h.linkSwitchRecord(ctx, nil, "lsw-bare", op, "device", switchBegin, nil); err != nil {
		t.Fatal(err)
	}
	err := h.swapMachineKind(ctx, "lsw-bare", kindLink, func(MachineConfig) error { return nil })
	h.machineAdd.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.linkSwitch = linkSwitches{} // the hub restarted
	cred, _ := linkToken(t, h, "lsw-bare")

	if _, err := h.moveMachine(ctx, "lsw-bare", "m-0e0e0e"); !errors.Is(err, errLinkSwitchInProgress) {
		t.Fatalf("moved: %v", err)
	}
	if w := adminMachinesRequest(h, "DELETE", "/api/v1/admin/machines/lsw-bare", ""); w.Code != http.StatusConflict {
		t.Fatalf("removed: %d", w.Code)
	}
	h.nameAfterSystem(ctx, "lsw-bare", "bare-host")
	if listedName(h, "lsw-bare") != "lsw-bare" {
		t.Fatal("renamed after its system")
	}
	refusedWith(t, "a hash it never recorded", busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-bare", Operation: op,
		PairHash: hashToken("jp_x"), Outcome: devicewire.LinkSwitchRolledBack}), bus.CodeInvalidParams)
	refusedWith(t, "a success with no code", busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-bare", Operation: op,
		Outcome: devicewire.LinkSwitchSucceeded}), bus.CodeInvalidParams)
	if _, ok := h.authenticate(ctx, cred); !ok || diskKind(t, h, "lsw-bare") != "link" {
		t.Fatal("a refused end changed something")
	}
	if err := busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-bare", Operation: op, Outcome: devicewire.LinkSwitchRolledBack}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.authenticate(ctx, cred); ok {
		t.Fatal("its Persona Link credential still works")
	}
	if diskKind(t, h, "lsw-bare") != "ssh" || h.cfgEntry("lsw-bare").Kind != "ssh" {
		t.Fatal("not back to ssh")
	}
	if steps := switchSteps(t, database, "lsw-bare", op); !slices.Equal(steps, []string{"begin", "rolled_back"}) {
		t.Fatalf("audited %v", steps)
	}
	if _, err := h.moveMachine(ctx, "lsw-bare", "m-0e0e0e"); errors.Is(err, errLinkSwitchInProgress) {
		t.Fatal("still held after its rollback")
	}
}

// noBegunRow is the database refusing the row that records a begin's code.
type noBegunRow struct{ *db.DB }

func (s noBegunRow) AddMachineAudit(ctx context.Context, machine, action, actor string, detail json.RawMessage) error {
	if strings.Contains(string(detail), `"step":"begun"`) {
		return errors.New("the audit refused")
	}
	return s.DB.AddMachineAudit(ctx, machine, action, actor, detail)
}

// A code whose row cannot be written is taken back: revoked, the kind put back, the begin ended as failed.
func TestLinkSwitchBeginUndoneWithoutItsRow(t *testing.T) {
	h, database, _ := switchHub(t)
	h.store = noBegunRow{database}
	op := newSwitchOp()
	if _, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: op, SSHHost: "me@box.example"}); err == nil {
		t.Fatal("begun without its row")
	}
	if linkInUse(t, h, "lsw-box") || diskKind(t, h, "lsw-box") != "ssh" || h.cfgEntry("lsw-box").Kind != "ssh" || h.linkSwitch.live("lsw-box", time.Now()) {
		t.Fatal("not undone")
	}
	if steps := switchSteps(t, database, "lsw-box", op); !slices.Equal(steps, []string{"begin", "begin_failed"}) {
		t.Fatalf("audited %v", steps)
	}
}

// An end is final and binds to its switch: a rolled back one answers the same and changes nothing, also once another
// switch of the computer began; its id begins nothing again; a success is not claimed for it; a hash of another
// switch's code is refused.
func TestLinkSwitchEndsAreFinal(t *testing.T) {
	h, _, _ := switchHub(t)
	ctx := context.Background()
	first, second := newSwitchOp(), newSwitchOp()
	end := func(op, hash, outcome string) error {
		return busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-box", Operation: op, PairHash: hash, Outcome: outcome})
	}
	got1, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: first, SSHHost: "me@box.example"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := end(first, got1.PairHash, devicewire.LinkSwitchRolledBack); err != nil {
			t.Fatal(err)
		}
	}
	refusedWith(t, "a success after its rollback", end(first, got1.PairHash, devicewire.LinkSwitchSucceeded), bus.CodeInvalidParams)
	_, err = busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: first, SSHHost: "me@box.example"})
	refusedWith(t, "its id again", err, bus.CodeInvalidParams)

	got2, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: second, SSHHost: "me@box.example"})
	if err != nil {
		t.Fatal(err)
	}
	code, out := redeem(t, h, pairTokenOf(t, got2.Link), `{}`)
	if code != 200 {
		t.Fatalf("redeem %d", code)
	}
	cred := out["token"].(string)
	for _, hash := range []string{"", got1.PairHash} {
		if err := end(first, hash, devicewire.LinkSwitchRolledBack); err != nil {
			t.Fatal(err)
		}
	}
	refusedWith(t, "the first's hash, rolling back the second", end(second, got1.PairHash, devicewire.LinkSwitchRolledBack), bus.CodeInvalidParams)
	refusedWith(t, "the first's hash, the second succeeding", end(second, got1.PairHash, devicewire.LinkSwitchSucceeded), bus.CodeInvalidParams)
	if _, ok := h.authenticate(ctx, cred); !ok || diskKind(t, h, "lsw-box") != "link" {
		t.Fatal("an old or foreign end undid the switch under way")
	}
	if err := end(second, got2.PairHash, devicewire.LinkSwitchSucceeded); err != nil {
		t.Fatal(err)
	}
}

// An end from a manager that does not carry the computer (it moved): only the manager that began the switch, naming
// its code, revokes that code's credential (nothing else, the kind stays the new manager's), told rolled_back_gone;
// any other is refused as no_machine and changes nothing.
func TestLinkSwitchEndFromAnotherManager(t *testing.T) {
	h, database, _ := switchHub(t)
	ctx := context.Background()
	op := newSwitchOp()
	got, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: op, SSHHost: "me@box.example"})
	if err != nil {
		t.Fatal(err)
	}
	code, out := redeem(t, h, pairTokenOf(t, got.Link), `{}`)
	if code != 200 {
		t.Fatalf("redeem %d", code)
	}
	cred := out["token"].(string)
	h.devices.mu.Lock()
	h.devices.assign = map[string]string{"lsw-box": "m-0e0e0e"} // moved meanwhile (a race; a move waits for the switch)
	h.devices.mu.Unlock()
	third := &busConn{ident: db.BusIdentity{ID: "m-0d0d0d"}}
	for _, c := range []struct {
		name string
		conn *busConn
		hash string
	}{{"a manager that did not begin it", third, got.PairHash}, {"without its hash", nil, ""}, {"another hash", nil, hashToken("jp_x")}} {
		refusedWith(t, c.name, busEnd(h, c.conn, devicewire.LinkSwitchEnd{MachineID: "lsw-box", Operation: op, PairHash: c.hash,
			Outcome: devicewire.LinkSwitchRolledBack}), devicewire.LinkSwitchCodeNoMachine)
		if _, ok := h.authenticate(ctx, cred); !ok || !h.linkSwitch.live("lsw-box", time.Now()) {
			t.Fatalf("%s: changed something", c.name)
		}
	}
	refusedWith(t, "its own, with its hash", busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-box", Operation: op, PairHash: got.PairHash,
		Outcome: devicewire.LinkSwitchRolledBack}), devicewire.LinkSwitchCodeRolledBackGone)
	if _, ok := h.authenticate(ctx, cred); ok || h.linkSwitch.live("lsw-box", time.Now()) || diskKind(t, h, "lsw-box") != "link" {
		t.Fatal("its credential, its switch or its kind")
	}
	if steps := switchSteps(t, database, "lsw-box", op); !slices.Equal(steps, []string{"begin", "begun", "rolled_back"}) {
		t.Fatalf("audited %v", steps)
	}
}

// A move or a removal holds its computers from a begin while it runs (config_changed), and a switch holds them from
// a move or a removal, also once the hub restarted (the audit says it is under way).
func TestLinkSwitchAndMovesWaitForEachOther(t *testing.T) {
	h, _, _ := switchHub(t)
	ctx := context.Background()
	release, err := h.holdLinkSwitch(ctx, "lsw-box")
	if err != nil {
		t.Fatal(err)
	}
	op := newSwitchOp()
	_, err = busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: op, SSHHost: "me@box.example"})
	refusedWith(t, "while a move holds it", err, devicewire.LinkSwitchCodeChanged)
	if diskKind(t, h, "lsw-box") != "ssh" || linkInUse(t, h, "lsw-box") {
		t.Fatal("a held computer began a switch")
	}
	release()
	if _, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: op, SSHHost: "me@box.example"}); err != nil {
		t.Fatal(err)
	}
	h.linkSwitch = linkSwitches{} // the hub restarted
	if _, err := h.moveMachine(ctx, "lsw-box", "m-0e0e0e"); !errors.Is(err, errLinkSwitchInProgress) {
		t.Fatalf("moved: %v", err)
	}
	if w := adminMachinesRequest(h, "DELETE", "/api/v1/admin/machines/lsw-box", ""); w.Code != http.StatusConflict {
		t.Fatalf("removed: %d", w.Code)
	}
	if _, err := h.holdManagerLinkSwitch(ctx, devicesOwner); !errors.Is(err, errLinkSwitchInProgress) {
		t.Fatalf("its manager removed: %v", err)
	}
}

// A switch its manager never ends holds its computer for linkSwitchFor only; a success after that is still taken.
func TestLinkSwitchTimeRunsOut(t *testing.T) {
	was := linkSwitchFor
	linkSwitchFor = 300 * time.Millisecond
	t.Cleanup(func() { linkSwitchFor = was })
	h, _, _ := switchHub(t)
	ctx := context.Background()
	op := newSwitchOp()
	got, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: op, SSHHost: "me@box.example"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.moveMachine(ctx, "lsw-box", "m-0e0e0e"); !errors.Is(err, errLinkSwitchInProgress) {
		t.Fatalf("moved while it switched: %v", err)
	}
	eventually(t, 5*time.Second, "the switch's time to run out", func() bool {
		busy, err := h.linkSwitchAudited(ctx, "lsw-box")
		return err == nil && !busy && !h.linkSwitch.live("lsw-box", time.Now())
	})
	if h.linkSwitch.live("lsw-box", time.Now()) {
		t.Fatal("still under way after its time ran out")
	}
	if _, err := h.moveMachine(ctx, "lsw-box", "m-0e0e0e"); errors.Is(err, errLinkSwitchInProgress) {
		t.Fatal("still held after its time ran out")
	}
	if err := busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-box", Operation: op, PairHash: got.PairHash,
		Outcome: devicewire.LinkSwitchSucceeded}); err != nil {
		t.Fatalf("a late success: %v", err)
	}
	refusedWith(t, "a rollback after it", busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-box", Operation: op,
		Outcome: devicewire.LinkSwitchRolledBack}), devicewire.LinkSwitchCodeSucceeded)
}

// A rollback the hub takes though the computer is no longer the manager's that began the switch: moved to another
// manager (once the switch no longer held it) or removed. The code and its credential are revoked and the switch is
// over; the manager is told rolled_back_gone (the rollback is taken), and so again when it asks again.
func TestLinkSwitchRollbackOfAComputerThatLeft(t *testing.T) {
	was := linkSwitchFor
	linkSwitchFor = 200 * time.Millisecond
	t.Cleanup(func() { linkSwitchFor = was })
	for _, how := range []string{"moved", "removed"} {
		t.Run(how, func(t *testing.T) {
			h, database, _ := switchHub(t)
			ctx := context.Background()
			h.bus.idents = &keptIdents{ids: &memIdents{}, kept: map[string]json.RawMessage{}} // a roster with the other manager
			busToken(t, h, "m-0e0e0e", "other")
			op := newSwitchOp()
			got, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: op, SSHHost: "me@box.example"})
			if err != nil {
				t.Fatal(err)
			}
			code, out := redeem(t, h, pairTokenOf(t, got.Link), `{}`)
			if code != 200 {
				t.Fatalf("redeem %d", code)
			}
			cred := out["token"].(string)
			eventually(t, 5*time.Second, "the switch no longer holding it", func() bool {
				busy, err := h.linkSwitchAudited(ctx, "lsw-box")
				return err == nil && !busy && !h.linkSwitch.live("lsw-box", time.Now())
			})
			switch how {
			case "moved":
				if _, err := h.moveMachine(ctx, "lsw-box", "m-0e0e0e"); err != nil {
					t.Fatalf("move: %v", err)
				}
			case "removed":
				if w := adminMachinesRequest(h, "DELETE", "/api/v1/admin/machines/lsw-box", ""); w.Code != http.StatusOK {
					t.Fatalf("remove: %d %s", w.Code, w.Body.String())
				}
			}
			for _, hash := range []string{got.PairHash, got.PairHash, ""} {
				refusedWith(t, "the rollback", busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-box", Operation: op, PairHash: hash,
					Outcome: devicewire.LinkSwitchRolledBack}), devicewire.LinkSwitchCodeRolledBackGone)
			}
			if _, ok := h.authenticate(ctx, cred); ok {
				t.Fatal("its credential still works")
			}
			if steps := switchSteps(t, database, "lsw-box", op); !slices.Equal(steps, []string{"begin", "begun", "rolled_back"}) {
				t.Fatalf("audited %v", steps)
			}
			// A rollback of a switch that never began there: nothing was undone, so not rolled_back_gone.
			never := busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-box", Operation: newSwitchOp(), Outcome: devicewire.LinkSwitchRolledBack})
			if how == "moved" {
				refusedWith(t, "a switch it never began", never, devicewire.LinkSwitchCodeNoMachine)
			} else if never != nil {
				t.Fatalf("a switch it never began: %v", never)
			}
		})
	}
}
