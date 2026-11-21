package assistant

import (
	"cmp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// The no_progress signal (#10089): a supervised task's agent keeps its session moving, so `stalled` never comes, but
// what it does is only waiting, polling or the same command again, with no real progress. `stalled` reads whether the
// transcript moved; this reads what moved it.
//
// Progress starts a new stretch: a report_to_supervisor with evidence or a later step (reportNow), a done_when
// criterion whose state or evidence changed (recordCriterionReports), the agent's own plan with more steps done or
// another current step, a tool call that changes something (an edit, a write, a commit, a push, a PR opened or merged,
// a release, a deploy), and a message from the user or the supervisor. Transcript writes, reports without evidence and
// reading, searching, building or testing are not progress by themselves.
//
// Each tool call the hub sees (a new ActivityAt, from the session lists and the supervision pass) counts once; it is a
// wait when its activity is waiting (sleep, until, while, wait, ScheduleWakeup, Monitor, background output) or checking
// CI, or when it says the same as an earlier call of the stretch. The signal comes for a working agent once the
// stretch is spinAfter old, the hub saw at least superviseSpinCalls calls in it, at least four in five were waits, and
// the latest one is recent (an agent that went silent is `stalled`'s).
//
// The watch is in memory: after a manager restart a stretch starts over, so the signal can come later, never sooner.

var (
	// superviseSpinMin is the least time with no progress, only waits, that counts as spinning (or 1.5× what the task
	// should take).
	superviseSpinMin = 30 * time.Minute
	// superviseSpinCalls is the least number of tool calls the hub must have seen in the stretch.
	superviseSpinCalls = 4
	// superviseSpinIdle is how long an agent's watch is kept after it was last seen in a list.
	superviseSpinIdle = 6 * time.Hour
)

// spinLines bounds the activity lines a stretch remembers; a line past it counts as a new one.
const spinLines = 64

// spinAfter is how long a task that should take expect minutes may go with only waits before its supervisor hears of it.
func spinAfter(expect int) time.Duration {
	return max(superviseSpinMin, time.Duration(expect)*time.Minute*3/2)
}

// spinWatch is what the hub saw of one agent since its last progress.
type spinWatch struct {
	since      time.Time // the stretch began: when the agent was first seen, or its last progress
	progressAt time.Time // the last real progress or message, unlike since, never merely the first observation
	lastCall   time.Time // the latest tool call's ActivityAt
	seen       time.Time // when the agent was last observed
	calls      int       // tool calls seen in the stretch
	waits      int       // of them, waits or repeats
	lines      map[string]int
	plan       bool // a plan was seen: planDone and planCur hold
	planDone   int
	planCur    string
	step       int // the latest step its reports named
}

func (w *spinWatch) restart(now time.Time) {
	w.since, w.progressAt, w.calls, w.waits, w.lines = now, now, 0, 0, nil
}

type progressBook struct {
	mu sync.Mutex
	m  map[[2]string]*spinWatch
}

// watchLocked is k's watch, made on first sight (b.mu held).
func (b *progressBook) watchLocked(k [2]string, now time.Time) *spinWatch {
	if b.m == nil {
		b.m = map[[2]string]*spinWatch{}
	}
	w := b.m[k]
	if w == nil {
		w = &spinWatch{since: now}
		b.m[k] = w
	}
	w.seen = now
	return w
}

// activity kinds, for the watch
const (
	callNeutral = iota
	callWait
	callProgress
)

var (
	waitActivities     = []string{"act_waiting", "act_waiting_task", "act_ci"}
	progressActivities = []string{"act_edit", "act_write", "act_commit", "act_push", "act_pr_create", "act_pr_merge", "act_release", "act_deploy"}
)

// activityCall is what kind of call an activity line (transcript.Activity) says: its fixed line, or its verb.
func activityCall(line string) int {
	key, _ := transcript.ActivityKey(line)
	if key == "" {
		if verb, _, ok := strings.Cut(line, " "); ok {
			key, _ = transcript.ActivityKey(verb)
		}
	}
	switch {
	case slices.Contains(waitActivities, key):
		return callWait
	case slices.Contains(progressActivities, key):
		return callProgress
	}
	return callNeutral
}

// observeProgress takes in what c shows now: a plan that moved, and a tool call not seen yet.
func (s *Service) observeProgress(c proto.Session, now time.Time) {
	if c.SessionID == "" || c.Assistant {
		return
	}
	b := &s.progress
	b.mu.Lock()
	defer b.mu.Unlock()
	w := b.watchLocked([2]string{c.MachineID, c.SessionID}, now)
	if p := c.Plan; p != nil {
		if w.plan && (p.Done > w.planDone || p.Current != w.planCur) {
			w.restart(now)
		}
		w.plan, w.planDone, w.planCur = true, p.Done, p.Current
	}
	if c.ActivityAt == nil || c.Activity == "" || !c.ActivityAt.After(w.lastCall) {
		return
	}
	w.lastCall = *c.ActivityAt
	line := strings.TrimSpace(c.Activity)
	switch kind := activityCall(line); {
	case kind == callProgress:
		w.restart(now)
	default:
		w.calls++
		if kind == callWait || w.lines[line] > 0 {
			w.waits++
		}
		if w.lines == nil {
			w.lines = map[string]int{}
		}
		if _, ok := w.lines[line]; ok || len(w.lines) < spinLines {
			w.lines[line]++
		}
	}
}

// progressMade: machine/session made progress, or was told something (a new stretch begins).
func (s *Service) progressMade(machine, session string, now time.Time) {
	if session == "" {
		return
	}
	b := &s.progress
	b.mu.Lock()
	b.watchLocked([2]string{machine, session}, now).restart(now)
	b.mu.Unlock()
}

// progressReported: a report_to_supervisor is progress when it carries evidence or names a later step.
func (s *Service) progressReported(machine, session string, evidence, step int, now time.Time) {
	if session == "" {
		return
	}
	b := &s.progress
	b.mu.Lock()
	defer b.mu.Unlock()
	w := b.watchLocked([2]string{machine, session}, now)
	if evidence > 0 || step > w.step {
		w.restart(now)
	}
	w.step = max(w.step, step)
}

// progressSessions forgets the watches of machine's sessions the list no longer has, and of any not seen for long.
func (s *Service) progressSessions(d DeviceSessions, now time.Time) {
	listed := make(map[string]bool, len(d.Sessions))
	for _, c := range d.Sessions {
		listed[c.SessionID] = true
	}
	b := &s.progress
	b.mu.Lock()
	for k, w := range b.m {
		if k[0] == d.MachineID && !listed[k[1]] || now.Sub(w.seen) > superviseSpinIdle {
			delete(b.m, k)
		}
	}
	b.mu.Unlock()
	for _, c := range d.Sessions {
		if c.State == proto.StateWorking {
			c.MachineID = cmp.Or(c.MachineID, d.MachineID)
			s.observeProgress(c, now)
		}
	}
}

// noProgressKey names a stretch: the session and when it began.
func noProgressKey(session string, since time.Time) string {
	return session + "@" + since.UTC().Format(time.RFC3339Nano)
}

// noProgressSignal: c, an agent of supervised task t, works but has only waited since its last progress.
func (s *Service) noProgressSignal(t Todo, c proto.Session, now time.Time) (superviseSignal, bool) {
	if t.Supervision == nil || c.Assistant || c.State != proto.StateWorking {
		return superviseSignal{}, false
	}
	s.observeProgress(c, now)
	b := &s.progress
	b.mu.Lock()
	defer b.mu.Unlock()
	w := b.m[[2]string{c.MachineID, c.SessionID}]
	expect := t.Supervision.ExpectMinutes
	if w == nil || w.calls < superviseSpinCalls || w.waits*5 < w.calls*4 || now.Sub(w.since) < spinAfter(expect) ||
		now.Sub(w.lastCall) > stallAfter(expect) {
		return superviseSignal{}, false
	}
	g := superviseSignal{Todo: t.ID, Kind: sigNoProgress, Key: noProgressKey(c.SessionID, w.since), Machine: c.MachineID,
		Session: c.SessionID, Minutes: int(now.Sub(w.since).Minutes()), Calls: w.calls, Waits: w.waits, Detail: w.topLines(3)}
	return g, true
}

// noProgressHolds: g's stretch is still the one its agent is in (no progress came while it waited to go out).
func (s *Service) noProgressHolds(g superviseSignal) bool {
	b := &s.progress
	b.mu.Lock()
	defer b.mu.Unlock()
	w := b.m[[2]string{g.Machine, g.Session}]
	return w != nil && noProgressKey(g.Session, w.since) == g.Key
}

// topLines are the n activity lines seen most in the stretch, the most first: what it keeps doing.
func (w *spinWatch) topLines(n int) string {
	type seen struct {
		line  string
		count int
	}
	list := make([]seen, 0, len(w.lines))
	for l, c := range w.lines {
		list = append(list, seen{l, c})
	}
	slices.SortFunc(list, func(a, b seen) int { return cmp.Or(b.count-a.count, strings.Compare(a.line, b.line)) })
	out := make([]string, 0, n)
	for i := 0; i < len(list) && i < n; i++ {
		out = append(out, list[i].line)
	}
	return strings.Join(out, "; ")
}
