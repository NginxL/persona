package devices

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The manager's own list of computers (managerconf.MachinesKey in its manager.json; docs/assistant/managers.md "Each
// manager's computers"): a manager whose hub has not answered within ownListWait of its start connects them, the way
// the hub's last hand-over had them (its tailcat DERP map). Not when the hub was in a mesh group: which computers are
// this hub's to connect, and when, are the group's, so it waits for the hub. Every hand-over is written there
// (keepOwnListLocked); the hub's, when it comes, is the list (Configure), and what the connections made meanwhile
// could not do without the hub is done then (afterOwnList).

// ownListWait is how long a starting manager waits for the hub's hand-over before it connects its own list: a hub that
// is up answers well within it (the built-in manager is started by its hub), so then nothing connects before it.
var ownListWait = 20 * time.Second

// startOwnList connects the machines of the manager's own list, the hub's hand-over not having come (Run).
func (d *Devices) startOwnList(ctx context.Context) {
	if d.opts.ConfigFile == "" {
		return
	}
	list, from, ok, err := managerconf.OwnMachines(d.opts.ConfigFile)
	if err != nil {
		d.logf("own list of machines: %v: waiting for the hub's", err)
		return
	}
	if !ok {
		return
	}
	if from.Mesh {
		d.logf("own list of machines: the hub was in a mesh group: waiting for it")
		return
	}
	d.mu.Lock()
	if d.got { // the hub's came meanwhile
		d.mu.Unlock()
		return
	}
	d.cfg = devicewire.Config{Machines: list, TailcatDERPMapURL: from.TailcatDERPMapURL, PrivateNetwork: from.PrivateNetwork}
	d.early = true
	d.mu.Unlock()
	d.logf("own list of machines: the hub has not answered: %d from %s", len(list), d.opts.ConfigFile)
	d.startMachines(ctx)
}

// afterOwnList is the hub's first hand-over after a start from the own list: a node that connected meanwhile is
// offered the hub's build, which was not known then (updateOnConnect). Its launchers and the hub's part of its
// connection follow BusUp, as on every handshake.
func (d *Devices) afterOwnList(ctx context.Context) {
	for _, m := range d.cfgMachines() {
		go m.updateOnConnect(ctx)
	}
}

// keepOwnListLocked writes the hub's hand-over, as it is now, into the manager's own list (Configure, with d.ownMu
// held from reading the list to writing it). With MachinesOwn the list is the manager's own: only what else of the
// hand-over it keeps changes, but the first time (no list yet, another hub's, or one taken over before the hub handed
// MachinesOwn), when the hand-over is taken over and marked its own. Without it, a list marked its own is not written
// over (the hub handing the list again is an older build switched back to: the list is kept for when it hands
// MachinesOwn again), nor is one that does not read in own mode.
func (d *Devices) keepOwnListLocked() {
	if d.opts.ConfigFile == "" {
		return
	}
	defer d.checkOwnHeld()
	d.mu.Lock()
	cfg, got := d.cfg, d.got
	d.mu.Unlock()
	if !got {
		return
	}
	if cfg.MachinesOwn && cfg.Machines == nil {
		cfg.Machines = []json.RawMessage{}
	}
	if cfg.Machines == nil {
		return
	}
	_, was, ok, err := managerconf.OwnMachines(d.opts.ConfigFile)
	switch {
	case cfg.MachinesOwn && err != nil:
		// Its own list is the list: one that does not read is fixed by hand, never written over.
		d.sayOwn("own list of machines: " + err.Error() + ": not written over; the computers stay as they are until it is fixed")
		return
	case !cfg.MachinesOwn && was.Own && (ok || err != nil):
		d.sayOwn("own list of machines: the hub hands the list again (an older build?): " + d.opts.ConfigFile +
			"'s is kept, not written over, for when it hands it over to this manager again")
		return
	}
	from := managerconf.MachinesFromHub{TailcatDERPMapURL: cfg.TailcatDERPMapURL, PrivateNetwork: cfg.PrivateNetwork, HubID: cfg.HubID, Mesh: cfg.Mesh != nil,
		Own: cfg.MachinesOwn}
	if cfg.MachinesOwn { // taking the hand-over over as its own: another hub's own list is kept aside first
		if path, err := managerconf.SetAsideOtherHubs(d.opts.ConfigFile, cfg.HubID); err != nil {
			d.sayOwn("own list of machines: another hub's list could not be kept aside: " + err.Error() + ": not written over")
			return
		} else if path != "" {
			d.logf("own list of machines: %s's was another hub's: kept as %s before the hub's is taken over", d.opts.ConfigFile, path)
		}
	}
	changed, skipped, replaced, err := managerconf.KeepMachines(d.opts.ConfigFile, cfg.Machines, from)
	var say string
	switch {
	case err != nil:
		say = "own list of machines: not kept in " + d.opts.ConfigFile + ": " + err.Error()
	case replaced != nil:
		say = "own list of machines: " + d.opts.ConfigFile + "'s did not read (" + replaced.Error() + "): the hub's written over it"
	case changed && skipped != nil:
		say = "own list of machines: kept in " + d.opts.ConfigFile + " without what does not read: " + skipped.Error()
	}
	d.sayOwn(say)
}

// checkOwnHeld says whether the list running is the own list, held as its own in manager.json: the hub handed
// MachinesOwn, and the file's list reads, is marked its own and is the one running (the hub takes those machines out
// of hub.json only then: States.OwnList). d.ownMu is held.
func (d *Devices) checkOwnHeld() {
	held := false
	list, from, ok, err := managerconf.OwnMachines(d.opts.ConfigFile)
	d.mu.Lock()
	if d.got && d.cfg.MachinesOwn && err == nil && ok && from.Own && sameList(list, d.cfg.Machines) {
		held = true
	}
	changed := d.ownHeld != held
	d.ownHeld = held
	d.mu.Unlock()
	if changed {
		d.changed()
	}
}

// sameList: a and b are the same entries in the same order (as JSON).
func sameList(a, b []json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		var x, y any
		if json.Unmarshal(a[i], &x) != nil || json.Unmarshal(b[i], &y) != nil {
			return false
		}
		xb, _ := json.Marshal(x)
		yb, _ := json.Marshal(y)
		if string(xb) != string(yb) {
			return false
		}
	}
	return true
}

// sayOwn logs the trouble with the own list once, not at every hand-over ("": none now); d.ownMu is held.
func (d *Devices) sayOwn(say string) {
	if say != "" && say != d.ownSaid {
		d.logf("%s", say)
	}
	d.ownSaid = say
}

// withOwnList is cfg with the manager's own list in place of the hub's, when the hub hands it MachinesOwn (the hub
// relays the edits of that list: putMachine, removeMachine). The first time (no list, another hub's, or one taken over
// before the hub handed MachinesOwn) the hub's is the list, and keepOwnListLocked takes it over as its own. One that
// does not read keeps the computers as they are (at the manager's start: the hub's hand-over, until it is fixed).
// d.ownMu is held.
func (d *Devices) withOwnList(cfg devicewire.Config) devicewire.Config {
	if !cfg.MachinesOwn || d.opts.ConfigFile == "" {
		return cfg
	}
	list, from, ok, err := managerconf.OwnMachines(d.opts.ConfigFile)
	switch {
	case err != nil:
		d.mu.Lock()
		if d.got {
			cfg.Machines = d.cfg.Machines
		}
		d.mu.Unlock()
	case ok && from.Own && (from.HubID == "" || cfg.HubID == "" || from.HubID == cfg.HubID): // not another hub's
		cfg.Machines = list
	}
	return cfg
}

// errNotOwnList: the hub did not hand this manager MachinesOwn, so it edits hub.json instead.
var errNotOwnList = &rpc.Error{Code: devicewire.CodeNotOwnList, Message: "this manager does not keep its own list of computers"}

// ownsList: the hub handed MachinesOwn, and the manager has its list in a manager.json.
func (d *Devices) ownsList() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.got && d.cfg.MachinesOwn && d.opts.ConfigFile != ""
}

// putMachine is devices.machines.put: entry added to the manager's own list, or put in place of the one with its id;
// connected (again) at once and reported.
func (d *Devices) putMachine(ctx context.Context, entry json.RawMessage) error {
	id, err := managerconf.EntryOf(entry)
	if err == nil {
		err = json.Unmarshal(entry, &MachineConfig{}) // one that does not read as a machine would never connect
	}
	if err != nil {
		return &rpc.Error{Code: "invalid_params", Message: "devices.machines.put: " + err.Error()}
	}
	d.ownMu.Lock()
	defer d.ownMu.Unlock()
	if !d.ownsList() {
		return errNotOwnList
	}
	list, err := managerconf.PutMachine(d.opts.ConfigFile, entry)
	if err != nil {
		return ownListErr(err)
	}
	d.logf("[%s] put in the own list of machines", id)
	d.applyOwnList(list)
	return nil
}

// removeMachine is devices.machines.remove: the machine taken out of the own list and let go.
func (d *Devices) removeMachine(ctx context.Context, id string) (bool, error) {
	d.ownMu.Lock()
	defer d.ownMu.Unlock()
	if !d.ownsList() {
		return false, errNotOwnList
	}
	list, removed, err := managerconf.RemoveMachine(d.opts.ConfigFile, id)
	if err != nil {
		return false, ownListErr(err)
	}
	if removed {
		d.logf("[%s] taken out of the own list of machines", id)
	}
	d.applyOwnList(list)
	return removed, nil
}

// ownListErr is err as the hub is answered: a list not taken over as the manager's own is not_own_list.
func ownListErr(err error) error {
	if errors.Is(err, managerconf.ErrNotOwnList) {
		return &rpc.Error{Code: devicewire.CodeNotOwnList, Message: err.Error()}
	}
	return err
}

// machineEntry is devices.machines.get: the entry as the own list has it.
func (d *Devices) machineEntry(id string) (json.RawMessage, error) {
	d.ownMu.Lock()
	defer d.ownMu.Unlock()
	if !d.ownsList() {
		return nil, errNotOwnList
	}
	list, from, ok, err := managerconf.OwnMachines(d.opts.ConfigFile)
	if err != nil {
		return nil, err
	}
	if !ok || !from.Own {
		return nil, errNotOwnList
	}
	for _, e := range list {
		var head struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(e, &head) == nil && head.ID == id {
			return e, nil
		}
	}
	return nil, &rpc.Error{Code: devicewire.CodeNoDevice, Message: "no machine " + strconv.Quote(id) + " in this manager's list"}
}

// applyOwnList makes the machines list (the own list as an edit left it); d.ownMu is held.
func (d *Devices) applyOwnList(list []json.RawMessage) {
	d.mu.Lock()
	d.cfg.Machines = list
	run := d.ctx
	d.mu.Unlock()
	d.checkOwnHeld()
	if run != nil {
		d.startMachines(run)
	}
}
