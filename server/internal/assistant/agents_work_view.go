package assistant

import (
	"context"
	"sort"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the hub's machines and sessions, as one pass sees them ----

// workView asks the hub for each thing once in a pass: the machines, a machine's sessions as the apps get them (with
// the hub's work on them) and which of them are muted.
type workView struct {
	s       *Service
	ctx     context.Context
	devices map[string]Device // nil: not asked yet
	lists   map[string][]proto.Session
	muted   map[string]map[string]bool
	rows    map[[2]string]viewRow      // the sessions read one at a time (session), while the whole list is not read
	listed  map[string][]proto.Session // the rows the nodes list, without the archive's (listedSessions)
	failed  map[string]bool            // the machines whose sessions the hub did not answer with: as if they were away
}

func (s *Service) newWorkView(ctx context.Context) *workView {
	return &workView{s: s, ctx: ctx, lists: map[string][]proto.Session{}, muted: map[string]map[string]bool{},
		failed: map[string]bool{}, rows: map[[2]string]viewRow{}, listed: map[string][]proto.Session{}}
}

// viewRow is one session read on its own, or that it was not found.
type viewRow struct {
	sess proto.Session
	ok   bool
}

// one says whether machine's sessions are read one at a time: the fleet is this process's and its whole list was not
// read in this pass. The fleet machine is the one sessions.list would read; an unknown machine is not (sessions says so).
func (v *workView) one(machine string) (fleetMachine, bool) {
	if _, read := v.lists[machine]; read || !v.s.fleetOn() || machine == DeviceAll {
		return fleetMachine{}, false
	}
	return v.s.fleetMachine(machine)
}

func (v *workView) device(id string) (Device, bool) {
	if v.devices == nil {
		v.devices = map[string]Device{}
		for _, d := range v.s.devices(v.ctx) {
			v.devices[d.ID] = d
		}
	}
	d, ok := v.devices[id]
	return d, ok
}

func (v *workView) machines() []Device {
	v.device("")
	out := make([]Device, 0, len(v.devices))
	for _, d := range v.devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// sessions are machine's sessions as the apps get them (sessions.list, with muted: which have pushes off).
func (v *workView) sessions(machine string) []proto.Session {
	if list, ok := v.lists[machine]; ok {
		return list
	}
	list, muted, err := v.s.sessionsMuted(v.ctx, machine)
	if err != nil {
		v.s.logf("work: sessions of %s: %v", machine, err)
		v.failed[machine] = true
	}
	v.lists[machine] = list
	v.muted[machine] = muted
	return list
}

// session is one of machine's sessions as the apps get it: the row of its list when this pass read it, else the row
// alone (decoratedSession), not the whole list for one turn end (#3197).
func (v *workView) session(machine, sid string) (proto.Session, bool) {
	if m, ok := v.one(machine); ok {
		k := [2]string{m.ID, sid}
		r, read := v.rows[k]
		if !read {
			r.sess, r.ok = v.s.decoratedSession(m.ID, sid)
			v.rows[k] = r
		}
		return r.sess, r.ok
	}
	for _, c := range v.sessions(machine) {
		if c.SessionID == sid {
			return c, true
		}
	}
	return proto.Session{}, false
}

func (v *workView) isMuted(machine, sid string) bool {
	if m, ok := v.one(machine); ok { // as mutedSessions says of the row
		c, found := v.session(machine, sid)
		return found && (c.HiddenByDirectory() || v.s.mutedAs(m.ID, sid, c.SpaceName))
	}
	for _, c := range v.sessions(machine) {
		if c.SessionID == sid && c.HiddenByDirectory() {
			return true
		}
	}
	return v.muted[machine][sid]
}

// listedSessions are the rows machine's node lists, as the apps get them (Service.listedSessions): for a pass over
// what runs, which never reads an archived row the node no longer lists (each is offline). The whole list when this
// pass read it already.
func (v *workView) listedSessions(machine string) []proto.Session {
	if _, ok := v.lists[machine]; ok || !v.s.fleetOn() || machine == DeviceAll {
		return v.sessions(machine)
	}
	if list, ok := v.listed[machine]; ok {
		return list
	}
	list, err := v.s.listedSessions(v.ctx, machine)
	if err != nil {
		v.s.logf("work: sessions of %s: %v", machine, err)
		v.failed[machine] = true
	}
	v.listed[machine] = list
	return list
}
