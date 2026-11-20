package assistant

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// Owner-filed sessions of a direction (#11391). The book is a lazy settings record, like conversation tags: one entry
// per canonical computer/session, with an empty direction meaning explicitly on its own. Filing changes no task,
// conversation, Agent, model call or prompt; the Hub only relays the route and advertises the manager's capability.
const directionSessionsKey = "assistant.direction_sessions"

const directionSessionsPerDirection = 50

type directionFiling struct {
	Direction string    `json:"direction"`
	By        string    `json:"by"`
	At        time.Time `json:"at"`
	Title     string    `json:"title"`
	// Parent is the session a Codex sub-agent session ran under when it was filed (proto.Session.ParentSessionID), so
	// placement rule 1 can follow it while its computer does not list it.
	Parent string `json:"parent,omitempty"`
	// Was is the entry the write of this one replaced (nil: there was none), so that a write putting it back, the
	// Undo, restores it exactly (restoreOf). Kept one deep: a Was has no Was.
	Was *directionFiling `json:"was,omitempty"`
	// RemovedAt: this entry was taken out then. Kept only so the Undo of that can put it back; it places nothing,
	// holds no place, and the session's next write replaces it (directionRemovedKeep drops it after a week).
	RemovedAt *time.Time `json:"removed_at,omitempty"`
}

// directionRemovedKeep is how long an entry taken out is kept for its Undo when its session is written no more.
const directionRemovedKeep = 7 * 24 * time.Hour

// live: the entry places the session (it was not taken out).
func (f directionFiling) live() bool { return f.RemovedAt == nil }

// bare is the entry without what it keeps for an Undo.
func (f directionFiling) bare() directionFiling {
	f.Was, f.RemovedAt = nil, nil
	return f
}

// restoreOf is the entry a write may put back exactly at now, as the Undo of the session's last write: the one that
// write replaced, or the one it took out, while that is not past directionRemovedKeep; nil when there is none (a first
// filing's Undo writes null instead).
func restoreOf(e directionFiling, ok bool, now time.Time) *directionFiling {
	switch {
	case !ok:
		return nil
	case !e.live() && now.Sub(*e.RemovedAt) > directionRemovedKeep:
		return nil
	case !e.live():
		r := e.bare()
		return &r
	case e.Was != nil:
		r := e.Was.bare()
		return &r
	}
	return nil
}

type directionSessionsBook struct {
	mu     sync.Mutex
	loaded bool
	// m is by runtimeListID(machine, session). An entry made before its computer's id was absorbed by another keeps the
	// old id until the session's next write (directionEntries).
	m map[string]*directionFiling
}

// directionFilingResult is the write's answer: what the book holds for the session now and held before (null: no
// entry), and whether that changed.
type directionFilingResult struct {
	Direction *string `json:"direction"`
	Previous  *string `json:"previous"`
	Changed   bool    `json:"changed"`
}

var (
	errDirectionRole            = i18n.Err("assistant-direction_sessions.full_access_required")
	errDirectionMachineNotFound = i18n.Err("assistant-direction_sessions.machine_not_found")
	errDirectionSessionNotFound = i18n.Err("assistant-direction_sessions.session_not_found")
	errDirectionNotFound        = i18n.Err("assistant-direction_sessions.direction_not_found")
	errDirectionEnded           = i18n.Err("assistant-direction_sessions.direction_ended")
	errDirectionFull            = i18n.Err("assistant-direction_sessions.direction_full", i18n.Args{"max": directionSessionsPerDirection})
	errDirectionSupervised      = i18n.Err("assistant-direction_sessions.supervised_task")
	errDirectionMain            = i18n.Err("assistant-direction_sessions.main_conversation")
	errDirectionBackground      = i18n.Err("assistant-direction_sessions.background_supervisor")
	errDirectionLeader          = i18n.Err("assistant-direction_sessions.leader_conversation")
	errDirectionSpace           = i18n.Err("assistant-direction_sessions.space_mismatch")
	errDirectionUnavailable     = i18n.Err("assistant-direction_sessions.unavailable")
	errDirectionEndedTaskCursor = i18n.Err("assistant-direction_sessions.ended_task_not_found")
)

func directionFilingRefusal(w http.ResponseWriter, r *http.Request, status int, code string, err error) {
	lang := i18n.OfWriter(w)
	if lang == "" {
		lang = i18n.FromRequest(r)
	}
	apierr.WriteBody(w, status, apierr.LocalizedBody(status, code, err, lang))
}

// directionSessionRoutes: owner filing and the single-direction read. The literal tree and offers routes remain more
// specific than GET …/directions/{id} in net/http's ServeMux.
func (s *Service) directionSessionRoutes() {
	s.handle("PUT /api/v1/assistant/sessions/{mid}/{sid}/direction", s.serveDirectionFiling)
	s.handle("GET /api/v1/assistant/directions/{id}", s.serveDirectionRead)
}

// directionTarget is the write's direction: a direction's id, "" for on its own (also when the body has none, as
// before null was taken), or nil for null, which takes the session's entry out of the book.
func directionTarget(raw json.RawMessage) (*string, error) {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0:
		return new(string), nil
	case string(raw) == "null":
		return nil, nil
	}
	var id string
	if err := json.Unmarshal(raw, &id); err != nil {
		return nil, err
	}
	return &id, nil
}

// directionAsk is a write as its handler gathered it, before the book: the target (nil takes the session's entry out),
// the session as its computer lists it, the rules, and what the target direction is. decideDirectionFiling decides it
// against the book as it is when it is saved, under the book's lock.
type directionAsk struct {
	key     [2]string
	target  *string
	row     proto.Session
	listed  bool
	listErr error
	parents map[string]string // the sub-agent sessions of the session's computer: their parents, by session id
	rules   directionRules
	dir     directionState
	spaceOK bool // the listed session is in the target's space, or that cannot be told
}

// directionState is what a write's target is: a current direction, an ended one, or none.
type directionState int

const (
	directionCurrent directionState = iota
	directionEndedState
	directionUnknown
)

// directionRefusal is a write refused, with its status, code and reason; call is the session's computer's own error
// when its list could not be read.
type directionRefusal struct {
	status int
	code   string
	err    error
	call   error
}

// serveDirectionFiling is the owner's write. It asks the session's computer and the directions first, then decides
// and saves under the book's lock, from the book as it is then (fileDirectionSession). Taking an entry out (null)
// asks nothing of the session, so an entry whose session its computer no longer lists, or whose computer left, can
// always go. Such a session can still be set on its own, with every check that needs no row; moving it to a direction
// needs its row (its space), unless the move is the Undo of the session's last write.
func (s *Service) serveDirectionFiling(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		directionFilingRefusal(w, r, http.StatusForbidden, "full_access_required", errDirectionRole)
		return
	}
	var in struct {
		Direction json.RawMessage `json:"direction"`
	}
	if err := decodeBody(w, r, &in, 1<<16); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	target, err := directionTarget(in.Direction)
	if err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	ctx := r.Context()
	machine, sid := s.canonical(r.PathValue("mid")), r.PathValue("sid")
	if machine == "" || !validSessionID(sid) {
		directionFilingRefusal(w, r, http.StatusNotFound, "session_not_found", errDirectionSessionNotFound)
		return
	}
	ask := directionAsk{key: [2]string{machine, sid}, target: target, parents: map[string]string{}, spaceOK: true}
	if target != nil {
		list, listErr := s.sessions(ctx, machine)
		ask.listErr = listErr
		for _, c := range list {
			if c.ParentSessionID != "" {
				ask.parents[c.SessionID] = c.ParentSessionID
			}
			if c.SessionID == sid && !c.IsDeleted() {
				ask.row, ask.listed = c, true
			}
		}
		if err := s.ensureTodosLoaded(ctx); err != nil {
			directionFilingRefusal(w, r, http.StatusServiceUnavailable, "direction_unavailable", errDirectionUnavailable)
			return
		}
		cfg := s.settings(ctx)
		if ask.rules, err = s.directionRulesNow(ctx, cfg); err != nil {
			directionFilingRefusal(w, r, http.StatusServiceUnavailable, "direction_unavailable", errDirectionUnavailable)
			return
		}
		if *target != "" && (*target == cfg.Main || !slices.Contains(cfg.Leaders, *target)) {
			_, known, ok := s.directionSnapshot(ctx, *target)
			if !ok {
				directionFilingRefusal(w, r, http.StatusServiceUnavailable, "direction_unavailable", errDirectionUnavailable)
				return
			}
			ask.dir = cond(known, directionEndedState, directionUnknown)
		}
		if *target != "" && ask.listed {
			if space, known := s.conversationSpace(ctx, *target); known && spaceOf(ask.row) != space {
				ask.spaceOK = false
			}
		}
	}
	out, refusal, err := s.fileDirectionSession(ctx, ask)
	switch {
	case refusal != nil && refusal.call != nil:
		apierr.Call(w, refusal.call)
	case refusal != nil:
		directionFilingRefusal(w, r, refusal.status, refusal.code, refusal.err)
	default:
		s.answerDirectionFiling(w, r, out, err)
	}
}

// decideDirectionFiling decides ask against records at now: refused, or how the book changes. The Undo of the
// session's last write puts back exactly the entry that write replaced or took out (restoreOf): it needs neither the
// session's row nor a current direction with room, as the write it undoes did not. The limit counts with look, whose
// computers were asked before the book's lock was taken (frozen: one it lacks is noted in missing).
func (s *Service) decideDirectionFiling(ctx context.Context, ask directionAsk, records map[string]directionFiling,
	look *directionLook, now time.Time) (directionFilingWrite, *directionRefusal, error) {
	key, sid := ask.key, ask.key[1]
	if ask.target == nil {
		return s.planDirectionFiling(records, key, nil, now), nil, nil
	}
	target := *ask.target
	refuse := func(status int, code string, err error) (directionFilingWrite, *directionRefusal, error) {
		return directionFilingWrite{}, &directionRefusal{status: status, code: code, err: err}, nil
	}
	entry, has := s.directionEntries(records)[key]
	filed := has && entry.live()
	restore := restoreOf(entry.directionFiling, has, now)
	undo := restore != nil && restore.Direction == target
	row := ask.row
	needsRow := !undo && (!filed || target != "")
	switch {
	case ask.listed && identity.IsAssistantDir(row.Cwd) && !identity.IsAssistantDirOf(row.Cwd, s.id):
		return refuse(http.StatusNotFound, "session_not_found", errDirectionSessionNotFound)
	case ask.listed:
	case needsRow && errors.Is(ask.listErr, ErrNoDevice):
		return refuse(http.StatusNotFound, "machine_not_found", errDirectionMachineNotFound)
	case needsRow && ask.listErr != nil:
		return directionFilingWrite{}, &directionRefusal{call: ask.listErr}, nil
	case needsRow:
		return refuse(http.StatusNotFound, "session_not_found", errDirectionSessionNotFound)
	case undo: // not listed now: the entry it puts back is as it was filed
		row.Title, row.ParentSessionID = restore.Title, restore.Parent
	default: // in the book, not listed now, set on its own: kept as it was filed
		row.Title, row.ParentSessionID = entry.Title, entry.Parent
	}
	cfg := ask.rules.cfg
	switch {
	case sid == cfg.Main && key[0] == s.canonical(cfg.mainRouteMachine()):
		return refuse(http.StatusConflict, "direction_main_conversation", errDirectionMain)
	case sid == cfg.BackgroundSupervisor && sid != cfg.Main && key[0] == s.canonical(cmp.Or(cfg.BackgroundSupervisorMachineID, cfg.MachineID)):
		return refuse(http.StatusConflict, "direction_background_supervisor", errDirectionBackground)
	case ask.rules.special[key]:
		return refuse(http.StatusConflict, "direction_leader_conversation", errDirectionLeader)
	case ask.rules.followers.follows(key, func(id string) string { return cond(id == sid, row.ParentSessionID, ask.parents[id]) }):
		return refuse(http.StatusConflict, "direction_supervised_task", errDirectionSupervised)
	}
	if target != "" {
		switch {
		case ask.dir == directionUnknown:
			return refuse(http.StatusNotFound, "direction_not_found", errDirectionNotFound)
		case ask.dir == directionEndedState && !undo:
			return refuse(http.StatusConflict, "direction_ended", errDirectionEnded)
		case !undo && !ask.spaceOK:
			return refuse(http.StatusConflict, "direction_space_mismatch", errDirectionSpace)
		}
	}
	next := restore
	if !undo {
		next = &directionFiling{Direction: target, By: titleByUser, At: now, Title: row.Title, Parent: row.ParentSessionID}
	}
	p := s.planDirectionFiling(records, key, next, now)
	if p.out.Changed && target != "" && !undo {
		n, err := s.directionHeld(ctx, ask.rules, records, target, key, look)
		if err != nil {
			return directionFilingWrite{}, nil, err
		}
		if n >= directionSessionsPerDirection {
			return refuse(http.StatusConflict, "direction_full", errDirectionFull)
		}
	}
	return p, nil, nil
}

func (s *Service) answerDirectionFiling(w http.ResponseWriter, r *http.Request, out directionFilingResult, err error) {
	if err != nil {
		directionFilingRefusal(w, r, http.StatusServiceUnavailable, "direction_unavailable", errDirectionUnavailable)
		return
	}
	if out.Changed {
		// The snapshot's keep follows the book at once (#11515), and publishing builds the tree again. Never under the
		// book's lock: fileDirectionSession has let it go, and the snapshot reads the book.
		s.publish(r.Context())
	}
	apierr.WriteJSON(w, out)
}

// directionSpecialSessions includes current and ended leader conversations, plus main and the background supervisor, by
// canonical computer, and the ids that are or were directions. Special conversations are never listed as filed
// sessions, and only a known direction's recorded opener counts as an implicit placement.
func (s *Service) directionSpecialSessions(ctx context.Context, cfg Settings) (map[[2]string]bool, map[string]bool, error) {
	var keys [][2]string
	knownDirections := map[string]bool{}
	if cfg.Main != "" {
		keys = append(keys, [2]string{cfg.mainRouteMachine(), cfg.Main})
	}
	if cfg.BackgroundSupervisor != "" {
		keys = append(keys, [2]string{cmp.Or(cfg.BackgroundSupervisorMachineID, cfg.MachineID), cfg.BackgroundSupervisor})
	}
	for _, id := range cfg.Leaders {
		if id != "" && id != cfg.Main {
			keys = append(keys, [2]string{cfg.MachineID, id})
			knownDirections[id] = true
		}
	}
	b := &s.directions
	b.mu.Lock()
	if !s.directionsLocked(ctx) {
		b.mu.Unlock()
		return nil, nil, errors.New("directions could not be read")
	}
	for id, d := range b.m {
		if d == nil {
			continue
		}
		keys = append(keys, [2]string{cmp.Or(d.MachineID, cfg.MachineID), id})
		knownDirections[id] = true
	}
	b.mu.Unlock()
	out := make(map[[2]string]bool, len(keys))
	for _, k := range keys { // a direction recorded on a computer id another one absorbed since is that one's
		out[[2]string{s.canonical(k[0]), k[1]}] = true
	}
	return out, knownDirections, nil
}

// directionSnapshot returns a copy of one direction record. A current leader without its first sync has no record yet.
func (s *Service) directionSnapshot(ctx context.Context, id string) (direction, bool, bool) {
	b := &s.directions
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.directionsLocked(ctx) {
		return direction{}, false, false
	}
	d, ok := b.m[id]
	if !ok || d == nil {
		return direction{}, false, true
	}
	return *d, true, true
}

// openSupervisedTask: a task placement rule 1 holds an Agent to (#11391): not done or cancelled (a failed start still
// waiting for its owner is not done) and supervised. Whatever the To-dos page files it under: a shelved task and one
// the owner has seen hold their Agents too.
func openSupervisedTask(t *Todo) bool {
	return t != nil && t.CancelledAt == nil && !t.closedForGood() && t.Supervision != nil && t.Supervision.Supervisor != ""
}

// taskFollowers are the sessions placement rule 1 gives to their task: the active Agents of the open supervised tasks,
// by canonical computer and session. The write, the single-direction read and the tree ask follows, so the three
// never disagree on which Agents follow a task.
type taskFollowers map[[2]string]bool

func (s *Service) taskFollowers() taskFollowers {
	var agents [][2]string
	s.todos.mu.RLock()
	for _, t := range s.todos.todos {
		if !openSupervisedTask(t) {
			continue
		}
		for _, a := range t.Agents {
			if a.active() {
				agents = append(agents, [2]string{a.MachineID, a.SessionID})
			}
		}
	}
	s.todos.mu.RUnlock()
	out := make(taskFollowers, len(agents))
	for _, k := range agents {
		out[[2]string{s.canonical(k[0]), k[1]}] = true
	}
	return out
}

// directionParentDepth is how far follows looks up a session's parents: a sub-agent's sub-agents nest that deep at most,
// and a loop of parents ends there.
const directionParentDepth = 8

// follows: session k is an Agent of an open supervised task, or a sub-agent session under one (parentOf names a
// session's parent on the same computer, "" for none), as the tree nests it under that Agent.
func (f taskFollowers) follows(k [2]string, parentOf func(sid string) string) bool {
	for range directionParentDepth {
		if f[k] {
			return true
		}
		parent := parentOf(k[1])
		if parent == "" || parent == k[1] {
			return false
		}
		k[1] = parent
	}
	return false
}

func (s *Service) directionSessionsLocked(ctx context.Context) bool {
	b := &s.directionFilings
	if b.loaded {
		return true
	}
	m := map[string]*directionFiling{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, directionSessionsKey, &m); err != nil {
			s.logf("load direction sessions: %v", err)
			return false
		}
	}
	if m == nil {
		m = map[string]*directionFiling{}
	}
	b.m, b.loaded = m, true
	return true
}

// directionSessionRecords returns a value copy: readers never hold or mutate the book's live map.
func (s *Service) directionSessionRecords(ctx context.Context) (map[string]directionFiling, error) {
	b := &s.directionFilings
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.directionSessionsLocked(ctx) {
		return nil, errors.New("direction sessions could not be read")
	}
	return directionRecordsOf(b.m), nil
}

func (s *Service) directionOriginRecords(ctx context.Context) (map[string]convOrigin, error) {
	b := &s.origins
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.originsLocked(ctx) {
		return nil, errors.New("conversation origins could not be read")
	}
	out := make(map[string]convOrigin, len(b.m))
	for id, origin := range b.m {
		if origin != nil {
			out[id] = *origin
		}
	}
	return out, nil
}

// directionEntry is one of the book's entries and the key it is kept under.
type directionEntry struct {
	key string
	directionFiling
}

// directionEntries are the book's entries by canonical computer and session, one each. A session filed under a computer
// id another computer has absorbed since has its entry under the old id, and a move from before this rule could leave
// a second one under the new id: the newest is the session's (the canonical key's on a tie), and its next write keeps
// only that one, under the canonical id (fileDirectionSession).
func (s *Service) directionEntries(records map[string]directionFiling) map[[2]string]directionEntry {
	out := make(map[[2]string]directionEntry, len(records))
	for key, record := range records {
		var tuple [2]string
		if json.Unmarshal([]byte(key), &tuple) != nil {
			continue
		}
		k := [2]string{s.canonical(tuple[0]), tuple[1]}
		e := directionEntry{key: key, directionFiling: record}
		if had, ok := out[k]; ok && !directionNewer(e, had, runtimeListID(k[0], k[1])) {
			continue
		}
		out[k] = e
	}
	return out
}

// directionNewer: entry a is the session's rather than b (directionEntries); canonical is the key a write keeps.
func directionNewer(a, b directionEntry, canonical string) bool {
	if c := a.At.Compare(b.At); c != 0 {
		return c > 0
	}
	if (a.key == canonical) != (b.key == canonical) {
		return a.key == canonical
	}
	return a.key < b.key
}

// directionRecordsOf is a value copy of the live book (its lock held).
func directionRecordsOf(m map[string]*directionFiling) map[string]directionFiling {
	out := make(map[string]directionFiling, len(m))
	for key, record := range m {
		if record != nil {
			out[key] = *record
		}
	}
	return out
}

// directionKeysOf are the keys the book keeps session k's entries under (the canonical one, and old ones of an
// absorbed computer id).
func (s *Service) directionKeysOf(records map[string]directionFiling, k [2]string) []string {
	var keys []string
	for key := range records {
		var tuple [2]string
		if json.Unmarshal([]byte(key), &tuple) == nil && tuple[1] == k[1] && s.canonical(tuple[0]) == k[0] {
			keys = append(keys, key)
		}
	}
	return keys
}

// directionFilingTries is how many times a write lets the book's lock go to ask a computer the book named meanwhile.
const directionFilingTries = 4

// directionFilingWrite is what writing an entry for a session does to the book as records has it: the answer, and
// whether and how the book changes (its keys drop go, keep is put under key).
type directionFilingWrite struct {
	out   directionFilingResult
	write bool
	drop  []string
	key   string
	keep  *directionFiling
}

// planDirectionFiling plans writing next for session k (nil takes its entry out). The entry written keeps the one it
// replaces as its Was; an entry taken out stays, marked removed, for its Undo. An unchanged placement still writes when
// the session's entry is not only under its canonical key: the entry stays, under that key alone.
func (s *Service) planDirectionFiling(records map[string]directionFiling, k [2]string, next *directionFiling, now time.Time) directionFilingWrite {
	p := directionFilingWrite{drop: s.directionKeysOf(records, k), key: runtimeListID(k[0], k[1])}
	var current *directionFiling // the session's entry, nil: none (or one taken out)
	e, ok := s.directionEntries(records)[k]
	if ok && e.live() {
		current = &e.directionFiling
		p.out.Previous = &e.Direction
	}
	if next != nil {
		p.out.Direction = &next.Direction
	}
	p.out.Changed = (current == nil) != (next == nil) || current != nil && next != nil && current.Direction != next.Direction
	switch {
	case p.out.Changed && next != nil:
		keep := *next
		keep.RemovedAt, keep.Was = nil, nil
		if current != nil {
			was := current.bare()
			keep.Was = &was
		}
		p.keep = &keep
	case p.out.Changed: // taken out: kept, marked, for its Undo
		removed := current.bare()
		removed.RemovedAt = &now
		p.keep = &removed
	case len(p.drop) == 0 || len(p.drop) == 1 && p.drop[0] == p.key:
		return p
	default:
		keep := e.directionFiling
		p.keep = &keep
	}
	p.write = true
	return p
}

// fileDirectionSession decides ask and saves it, both under the book's lock and from the book as it is then, so two
// writes never decide from a book the other changed. The computers are never asked under the lock: the limit counts
// with the lists asked before it, and when the book names a computer not asked yet, the lock is let go, that computer
// asked, and the write decided again (after directionFilingTries it counts with what it has, its stored parents for
// the rest). Copy-on-write keeps a failed save invisible even before the next lazy reload; loaded=false also forces
// that reload, as the other settings books do.
func (s *Service) fileDirectionSession(ctx context.Context, ask directionAsk) (directionFilingResult, *directionRefusal, error) {
	st := s.store()
	if st == nil {
		return directionFilingResult{}, nil, errors.New("no database")
	}
	b := &s.directionFilings
	look := newDirectionLook(s, nil)
	for try := 1; ; try++ {
		now := time.Now().UTC()
		b.mu.Lock()
		if !s.directionSessionsLocked(ctx) {
			b.mu.Unlock()
			return directionFilingResult{}, nil, errors.New("direction sessions could not be read")
		}
		look.frozen, look.missing = true, nil
		p, refusal, err := s.decideDirectionFiling(ctx, ask, directionRecordsOf(b.m), look, now)
		if missing := slices.Sorted(maps.Keys(look.missing)); len(missing) > 0 && try < directionFilingTries {
			b.mu.Unlock()
			look.frozen = false
			for _, machine := range missing {
				look.list(ctx, machine)
			}
			continue
		}
		if err != nil || refusal != nil || !p.write {
			b.mu.Unlock()
			return p.out, refusal, err
		}
		m := maps.Clone(b.m)
		for _, key := range p.drop {
			delete(m, key)
		}
		for key, record := range m { // entries taken out long ago: their Undo is past
			if record != nil && record.RemovedAt != nil && now.Sub(*record.RemovedAt) > directionRemovedKeep {
				delete(m, key)
			}
		}
		if p.keep != nil {
			m[p.key] = p.keep
		}
		if err := st.PutSetting(ctx, directionSessionsKey, m); err != nil {
			b.loaded = false
			b.mu.Unlock()
			return directionFilingResult{}, nil, err
		}
		b.m = m
		b.mu.Unlock()
		return p.out, nil, nil
	}
}

// dropDirectionSession removes only an assistant conversation's filing (its entries under every id of its computer).
// Hub-side reversible Agent deletion has no manager notice, so those filings deliberately remain.
func (s *Service) dropDirectionSession(ctx context.Context, machine, sid string) error {
	st := s.store()
	if st == nil {
		return errors.New("no database")
	}
	b := &s.directionFilings
	b.mu.Lock()
	if !s.directionSessionsLocked(ctx) {
		b.mu.Unlock()
		return errors.New("direction sessions could not be read")
	}
	keys := s.directionKeysOf(directionRecordsOf(b.m), [2]string{s.canonical(machine), sid})
	if len(keys) == 0 {
		b.mu.Unlock()
		return nil
	}
	next := maps.Clone(b.m)
	for _, key := range keys {
		delete(next, key)
	}
	if err := st.PutSetting(ctx, directionSessionsKey, next); err != nil {
		b.loaded = false
		b.mu.Unlock()
		return err
	}
	b.m = next
	b.mu.Unlock()
	s.directionTreeSoon()
	return nil
}
