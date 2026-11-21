package assistant

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Noting the conversations to-dos name that were never noted (#10941, conversation_space.go): a manager notes a
// conversation's space when it starts one or a computer lists one, and a computer lists a quiet assistant conversation
// only for a week and only its 100 newest. A conversation quiet since before the manager noted spaces (an upgrade) is
// listed by no computer any more, so the to-dos it opened, supervised or wrote to could never be told (todoUntold): an
// extension was never shown them, and what would tell it of them waited for good. The backfill asks the computers the
// assistant ran on, and the one the origins book places each on (originSet: the Lark agent's computer, the Inbox AI
// twin's, any start that named one), to list such conversations however quiet (the profile's keep, spaceKeep), a batch
// at a time, so each is noted as any listed one is. One no computer listed after spaceBackfillWait while every computer
// it is kept on was on (or is no longer the hub's) is given up (its transcript is gone) and stays untold, the rule
// failing closed for it; it is not kept again for spaceBackfillRetry, though the manager starts again. One a computer
// listed is never given up: it is kept until its note is written. One kept the wait while another of those computers
// was off waits for that one (parked, not kept), and is kept again once every one is on. The conversations are those
// of every to-do, open or done (its origin, its supervisor and those its history was written in, read once a run of
// the manager, off the profile's way: spaceBackfillScanSoon), and any a verdict could not tell since
// (spaceBackfillWant).

// spaceBackfillBatch is how many never noted conversations the computers are asked to list at once.
const spaceBackfillBatch = 50

// spaceBackfillMost bounds the conversations one run of the manager takes up (past it, the rest wait for the next run).
const spaceBackfillMost = 20000

// spaceBackfillGoneKey is the setting that keeps the conversations the backfill gave up, by id with when, so a manager
// that starts again (a redeploy) does not keep them again until spaceBackfillRetry has passed.
const (
	spaceBackfillGoneKey = "assistant.space_backfill_gone"
	spaceBackfillRetry   = 7 * 24 * time.Hour
)

const (
	// spaceBackfillWait is how long a conversation is kept, every computer it is kept on being on, before it is given
	// up; with only the assistant's own on, before it is parked.
	spaceBackfillWait = 3 * time.Minute
	// spaceBackfillTick is how often a batch is checked for ones to give up.
	spaceBackfillTick = 30 * time.Second
	// spaceBackfillRescan is how soon a read of the to-dos that failed is tried again.
	spaceBackfillRescan = time.Minute
)

// fillState is where a conversation is in the backfill.
type fillState uint8

const (
	fillWaiting fillState = iota + 1 // queued for a batch
	fillKept                         // in the batch: its computers are asked to list it
	fillListed                       // in the batch, and a computer listed it: kept until its note is written
	fillParked                       // kept the wait while a computer it may be on was off: waits for every one to be on
	fillOver                         // noted, or given up: not asked again
)

// spaceBackfill is what the backfill has left (above).
type spaceBackfill struct {
	mu       sync.Mutex
	scanned  bool                 // the to-dos were read for their conversations
	scanning bool                 // that read is on (spaceBackfillScanSoon)
	scanAt   time.Time            // when that was last tried
	queue    []string             // waiting for the batch, in order; one no longer waiting is skipped
	state    map[string]fillState // every conversation taken up
	batch    map[string]time.Time // the kept ones, since when (since the assistant's computer was last seen off)
	far      map[string]string    // the computer each one's origin record names (originMachines): kept on it too
	parked   []string             // waiting for every computer to be on; one no longer parked is skipped
	offAt    map[string]time.Time // when each computer one is kept on was last seen off
	armed    bool                 // a check of the batch is scheduled
	// active: the batch holds any (fillLocked keeps it), read without the lock on a list's way.
	active atomic.Bool
	// gone: the conversations given up, as the setting keeps them (spaceBackfillGoneKey), once read (goneRead).
	gone     map[string]time.Time
	goneRead bool
	after    func(d time.Duration, f func()) // tests replace the timer
	clock    func() time.Time                // and the time
}

// now is the time; b.mu is held.
func (b *spaceBackfill) now() time.Time {
	if b.clock != nil {
		return b.clock()
	}
	return time.Now()
}

// later runs f after d; b.mu is not held.
func (b *spaceBackfill) later(d time.Duration, f func()) {
	b.mu.Lock()
	after := b.after
	b.mu.Unlock()
	if after != nil {
		after(d, f)
		return
	}
	time.AfterFunc(d, f)
}

// fillLocked moves what waits into the batch while it has room; it says whether one was. b.mu is held, and every
// change of the batch ends with it.
func (b *spaceBackfill) fillLocked(now time.Time) bool {
	added := false
	for len(b.batch) < spaceBackfillBatch && len(b.queue) > 0 {
		id := b.queue[0]
		b.queue = b.queue[1:]
		if b.state[id] != fillWaiting {
			continue
		}
		if b.batch == nil {
			b.batch = map[string]time.Time{}
		}
		b.state[id], b.batch[id], added = fillKept, now, true
	}
	b.active.Store(len(b.batch) > 0)
	return added
}

// takeLocked queues those of ids not taken up yet, in order, each with the computer its origin record names (far); it
// says whether one was. b.mu is held.
func (b *spaceBackfill) takeLocked(ids []string, far map[string]string) bool {
	took := false
	for _, id := range ids {
		if id == "" || b.state[id] != 0 || len(b.state) >= spaceBackfillMost {
			continue
		}
		if _, _, leader := parseLeaderRef(id); leader {
			continue // a task leader's agent: told by its run's directory (leaderSpace), never noted
		}
		if b.state == nil {
			b.state = map[string]fillState{}
		}
		b.state[id] = fillWaiting
		b.queue = append(b.queue, id)
		if m := far[id]; m != "" {
			if b.far == nil {
				b.far = map[string]string{}
			}
			b.far[id] = m
		}
		took = true
	}
	return took
}

// machinesLocked are the computers conversation id is kept on: base (spaceBackfillMachines) and the one its origin
// record names. b.mu is held.
func (b *spaceBackfill) machinesLocked(base []string, id string) []string {
	if m := b.far[id]; m != "" && !slices.Contains(base, m) {
		return append(slices.Clip(base), m)
	}
	return base
}

// originMachines are the computers the origins book places conversations ids on (originSet: the one it was started on;
// backfillOrigins: the one that listed it), by id: none for one it has no record of, or while it cannot be read.
func (s *Service) originMachines(ctx context.Context, ids []string) map[string]string {
	out := map[string]string{}
	b := &s.origins
	b.mu.Lock()
	if s.originsLocked(ctx) {
		for _, id := range ids {
			if o := b.m[id]; o != nil && o.MachineID != "" {
				out[id] = o.MachineID
			}
		}
	}
	b.mu.Unlock()
	for id, m := range out {
		out[id] = s.canonical(m)
	}
	return out
}

// spaceKeep are the sessions their computers are to list however quiet for the spaces to be told, sorted: the task
// leaders' agents that opened or supervise an open task, on their own computer (leaderSpace reads one whose links keep
// no directory from its list), and the backfill's batch on every computer the assistant ran on
// (spaceBackfillMachines) and the one each one's origin record names. It reads nothing of the store: the to-dos are
// read for the backfill on their own the first time (spaceBackfillScanSoon), and the batch that fills goes in the next
// profile.
func (s *Service) spaceKeep(ctx context.Context, v Settings) []SessionRef {
	s.spaceBackfillScanSoon(v)
	seen := map[SessionRef]bool{}
	s.todos.mu.RLock()
	for _, t := range s.todos.todos {
		if t.DoneAt != nil {
			continue
		}
		refs := []string{t.Origin}
		if t.Supervision != nil {
			refs = append(refs, t.Supervision.Supervisor)
		}
		for _, ref := range refs {
			if machine, sid, ok := parseLeaderRef(ref); ok {
				seen[SessionRef{MachineID: s.canonical(machine), SessionID: sid}] = true
			}
		}
	}
	s.todos.mu.RUnlock()
	machines := spaceBackfillMachines(v)
	b := &s.spaceFill
	b.mu.Lock()
	for id := range b.batch {
		for _, m := range b.machinesLocked(machines, id) {
			seen[SessionRef{MachineID: m, SessionID: id}] = true
		}
	}
	b.mu.Unlock()
	out := make([]SessionRef, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b SessionRef) int {
		return cmp.Or(cmp.Compare(a.MachineID, b.MachineID), cmp.Compare(a.SessionID, b.SessionID))
	})
	return out
}

// spaceBackfillMachines are the computers the batch is kept on, each once: those the assistant ran on (its own, the
// main conversation's, the former main ones').
func spaceBackfillMachines(v Settings) []string {
	var out []string
	add := func(m string) {
		if m != "" && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	add(v.MachineID)
	add(v.mainRouteMachine())
	for _, f := range v.Former {
		add(f.MachineID)
	}
	return out
}

// spaceBackfillScanSoon has the to-dos read for the backfill (spaceBackfillScan) on their own, once a run of the
// manager, and again spaceBackfillRescan after a read that failed: the read takes every to-do's conversations from the
// store, and the profile that asks is on a publish's way, which every owner's write that publishes waits behind. Once
// the batch holds any, the next profile keeps them.
func (s *Service) spaceBackfillScanSoon(v Settings) {
	b := &s.spaceFill
	b.mu.Lock()
	if b.scanned || b.scanning || !b.scanAt.IsZero() && b.now().Sub(b.scanAt) < spaceBackfillRescan {
		b.mu.Unlock()
		return
	}
	b.scanning, b.scanAt = true, b.now()
	b.mu.Unlock()
	go func() {
		if s.spaceBackfillScan(s.runCtx(), v) {
			s.publishSoon() // the profile's keep
		}
		b.mu.Lock()
		b.scanning = false
		b.mu.Unlock()
	}()
}

// spaceBackfillScanIdle says whether no read of the to-dos for the backfill is on, nor the publish it asked for when the
// publish does not wait (tests wait for it).
func (s *Service) spaceBackfillScanIdle() bool {
	b := &s.spaceFill
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.scanning
}

// spaceBackfillScan reads the conversations every to-do names (the open ones' first) and takes up those never noted
// and with no deletion record; it says whether the batch took any. One that fails leaves the to-dos to be read again.
func (s *Service) spaceBackfillScan(ctx context.Context, v Settings) bool {
	b := &s.spaceFill
	ctx, cancel := context.WithTimeout(ctx, conversationSpaceTimeout)
	defer cancel()
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return false // tried again with a later profile
	}
	type named struct {
		open bool
		refs []string
	}
	todos := map[string]named{}
	s.todos.mu.RLock()
	for id, t := range s.todos.todos {
		n := named{open: t.DoneAt == nil, refs: []string{t.Origin}}
		if t.Supervision != nil {
			n.refs = append(n.refs, t.Supervision.Supervisor)
		}
		todos[id] = n
	}
	s.todos.mu.RUnlock()
	ids := slices.Sorted(func(yield func(string) bool) {
		for id := range todos {
			if !yield(id) {
				return
			}
		}
	})
	feed, err := s.todoFeedConversations(ctx, ids)
	if err != nil && !errors.Is(err, errNoFeedConversations) {
		s.logf("conversation spaces: the conversations of the to-dos' history: %v", err)
		return false
	}
	var open, done []string
	for _, id := range ids {
		n := todos[id]
		refs := slices.Concat(n.refs, feed[id])
		if n.open {
			open = append(open, refs...)
		} else {
			done = append(done, refs...)
		}
	}
	var cands []string
	took := map[string]bool{"": true, v.Main: true} // the main conversation is kept apart (keptSessions)
	for _, c := range append(open, done...) {
		if _, _, leader := parseLeaderRef(c); !took[c] && !leader {
			took[c] = true
			cands = append(cands, c)
		}
	}
	noted, err := s.readNotedSpaces(ctx, cands)
	if err != nil {
		s.logf("conversation spaces: the to-dos' conversations: %v", err)
		return false
	}
	cands = slices.DeleteFunc(cands, func(c string) bool { _, ok := noted[c]; return ok })
	deleted := s.deletedConversationsOf(ctx, v.MachineID, cands)
	cands = slices.DeleteFunc(cands, func(c string) bool { _, ok := deleted[c]; return ok })
	s.spaceBackfillReadGone(ctx)
	far := s.originMachines(ctx, cands)
	b.mu.Lock()
	b.scanned = true
	b.takeLocked(cands, far)
	filled := b.fillLocked(b.now())
	arm := len(b.batch) > 0 && !b.armed
	b.armed = b.armed || arm
	b.mu.Unlock()
	if arm {
		b.later(spaceBackfillTick, s.spaceBackfillCheck)
	}
	return filled
}

// spaceBackfillWant takes up the conversations a verdict could not tell (todoVerdicts: never noted, not listed, no
// deletion record), for their computers to list them; a batch with room asks for them at once.
func (s *Service) spaceBackfillWant(ids []string) {
	if len(ids) == 0 {
		return
	}
	b := &s.spaceFill
	b.mu.Lock()
	read := b.goneRead
	fresh := slices.ContainsFunc(ids, func(id string) bool { return id != "" && b.state[id] == 0 })
	b.mu.Unlock()
	if !fresh {
		return // each taken up already
	}
	ctx, cancel := context.WithTimeout(s.runCtx(), conversationSpaceTimeout)
	defer cancel()
	if !read { // those given up before are not taken up again (once a run of the manager)
		s.spaceBackfillReadGone(ctx)
	}
	far := s.originMachines(ctx, ids)
	b.mu.Lock()
	if !b.takeLocked(ids, far) {
		b.mu.Unlock()
		return
	}
	added := b.fillLocked(b.now())
	arm := len(b.batch) > 0 && !b.armed
	b.armed = b.armed || arm
	b.mu.Unlock()
	if arm {
		b.later(spaceBackfillTick, s.spaceBackfillCheck)
	}
	if added {
		go s.publishSoon() // the profile's keep (a verdict may be read on a write's way)
	}
}

// spaceBackfillListed marks the conversations of the batch that list (a computer's, SessionsChanged) has as assistant
// conversations: a computer has their transcripts, so they are never given up, and stay kept until their notes are
// written (a write that failed is tried again with the next list).
func (s *Service) spaceBackfillListed(list []proto.Session) {
	b := &s.spaceFill
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range list {
		if b.state[c.SessionID] != fillKept {
			continue
		}
		if _, ok := s.listedSpace(c); ok {
			b.state[c.SessionID] = fillListed
		}
	}
}

// spaceBackfillNoted takes the conversations ids out of the backfill once their spaces are noted
// (noteConversationSpaces), and fills the batch with what waits.
func (s *Service) spaceBackfillNoted(ids []string) {
	b := &s.spaceFill
	b.mu.Lock()
	if len(b.state) == 0 {
		b.mu.Unlock()
		return
	}
	for _, id := range ids {
		switch b.state[id] {
		case fillWaiting, fillKept, fillListed, fillParked:
			b.state[id] = fillOver
			delete(b.batch, id)
		}
	}
	added := b.fillLocked(b.now())
	b.mu.Unlock()
	if added {
		go s.publishSoon()
	}
}

// spaceBackfillComputers says which computers are on (on), as the hub's list of them says: one it lists, when online;
// one it does not, when it is not the assistant's own and is no longer the hub's (the list complete without it, as
// machineRemoved reads it; one the hub has moved out of the manager's list, or a list that is not complete, counts as
// off). false when the list cannot be read.
func (s *Service) spaceBackfillComputers(ctx context.Context, v Settings) (on func(string) bool, read bool) {
	var res struct {
		Devices  []Device `json:"devices"`
		Complete bool     `json:"complete"`
		Others   []string `json:"others"`
	}
	if err := s.call(ctx, DeviceAll, DeviceDevicesList, map[string]any{}, &res); err != nil {
		return nil, false
	}
	return func(m string) bool {
		if i := slices.IndexFunc(res.Devices, func(d Device) bool { return d.ID == m }); i >= 0 {
			return res.Devices[i].Online
		}
		return m != v.MachineID && res.Complete && !slices.Contains(res.Others, m)
	}, true
}

// spaceBackfillCheck goes through the batch. A conversation a computer listed stays until its note is written. While
// the assistant's computer is off, the others' waits start again. One kept spaceBackfillWait and still not listed is
// given up once every computer it is kept on (spaceBackfillMachines and its origin's) has been on for the whole wait,
// and parked while one of them is off (those on do not have it); what is parked is kept again, first, once every one
// it is kept on is. It fills the batch again, tells the computers, and checks again while the batch or what is parked
// holds any.
func (s *Service) spaceBackfillCheck() {
	b := &s.spaceFill
	b.mu.Lock()
	b.armed = false
	empty := len(b.batch) == 0 && len(b.parked) == 0
	b.mu.Unlock()
	if empty || s.runCtx().Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.runCtx(), conversationSpaceTimeout)
	v := s.settings(ctx)
	on, read := s.spaceBackfillComputers(ctx, v)
	cancel()
	isOn := func(m string) bool { return read && on(m) }
	base := spaceBackfillMachines(v)
	changed := false
	var gaveUp []string
	b.mu.Lock()
	now := b.now()
	if b.offAt == nil {
		b.offAt = map[string]time.Time{}
	}
	machines := slices.Clone(base)
	for _, ids := range [][]string{slices.Collect(maps.Keys(b.batch)), b.parked} {
		for _, id := range ids {
			if m := b.far[id]; m != "" && !slices.Contains(machines, m) {
				machines = append(machines, m)
			}
		}
	}
	for _, m := range machines {
		if !isOn(m) {
			b.offAt[m] = now
		}
	}
	allOn := func(id string) bool { // every computer id is kept on is on now
		return !slices.ContainsFunc(b.machinesLocked(base, id), func(m string) bool { return !isOn(m) })
	}
	onFor := func(id string) bool { // and was for the whole wait
		return !slices.ContainsFunc(b.machinesLocked(base, id), func(m string) bool {
			return now.Sub(b.offAt[m]) < spaceBackfillWait
		})
	}
	own := isOn(v.MachineID)
	for id, since := range b.batch {
		if b.state[id] == fillListed {
			continue
		}
		switch {
		case !own:
			b.batch[id] = now
		case now.Sub(since) < spaceBackfillWait:
		case !allOn(id):
			b.state[id] = fillParked
			delete(b.batch, id)
			b.parked = append(b.parked, id)
			changed = true
		case onFor(id):
			b.state[id] = fillOver
			delete(b.batch, id)
			changed = true
			gaveUp = append(gaveUp, id)
		}
	}
	if len(b.parked) > 0 {
		var back, still []string
		for _, id := range b.parked {
			switch {
			case b.state[id] != fillParked:
			case allOn(id):
				b.state[id] = fillWaiting
				back = append(back, id)
			default:
				still = append(still, id)
			}
		}
		b.parked = still
		b.queue = append(back, b.queue...)
		changed = changed || len(back) > 0
	}
	if changed {
		b.fillLocked(now)
	}
	arm := (len(b.batch) > 0 || len(b.parked) > 0) && !b.armed // a want since may have scheduled one
	b.armed = b.armed || arm
	b.mu.Unlock()
	if len(gaveUp) > 0 {
		s.spaceBackfillGaveUp(gaveUp)
	}
	if changed {
		s.publishSoon() // the batch's keep is another now
	}
	if arm {
		b.later(spaceBackfillTick, s.spaceBackfillCheck)
	}
}

// spaceBackfillReadGone reads, once, the conversations given up before (spaceBackfillGoneKey): those given up less than
// spaceBackfillRetry ago are not taken up again. One that cannot be read is tried again with the next scan or give-up.
func (s *Service) spaceBackfillReadGone(ctx context.Context) {
	b := &s.spaceFill
	b.mu.Lock()
	read := b.goneRead
	b.mu.Unlock()
	st := s.store()
	if read || st == nil {
		return
	}
	gone := map[string]time.Time{}
	if _, err := st.Setting(ctx, spaceBackfillGoneKey, &gone); err != nil {
		s.logf("conversation spaces: the conversations given up: %v", err)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.goneRead {
		return
	}
	b.goneRead = true
	now := b.now()
	for id, at := range gone {
		if now.Sub(at) >= spaceBackfillRetry {
			delete(gone, id)
			continue
		}
		if b.state == nil {
			b.state = map[string]fillState{}
		}
		if b.state[id] == 0 {
			b.state[id] = fillOver
		}
	}
	for id, at := range b.gone { // given up before the read
		gone[id] = at
	}
	b.gone = gone
}

// spaceBackfillGaveUp keeps ids, just given up, in the setting (best effort: a manager that starts again without it
// keeps them once more).
func (s *Service) spaceBackfillGaveUp(ids []string) {
	st := s.store()
	if st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.runCtx(), conversationSpaceTimeout)
	defer cancel()
	s.spaceBackfillReadGone(ctx)
	b := &s.spaceFill
	b.mu.Lock()
	if b.gone == nil {
		b.gone = map[string]time.Time{}
	}
	now := b.now()
	for _, id := range ids {
		b.gone[id] = now
	}
	for id, at := range b.gone {
		if now.Sub(at) >= spaceBackfillRetry {
			delete(b.gone, id)
		}
	}
	if !b.goneRead {
		b.mu.Unlock()
		return // what the setting holds is not known: it is not written over
	}
	gone := maps.Clone(b.gone)
	b.mu.Unlock()
	if err := st.PutSetting(ctx, spaceBackfillGoneKey, gone); err != nil {
		s.logf("conversation spaces: keep %d given up: %v", len(ids), err)
	}
}

// spaceBackfillKept are the conversations of the batch now, sorted (tests).
func (s *Service) spaceBackfillKept() []string {
	b := &s.spaceFill
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.batch))
	for id := range b.batch {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
