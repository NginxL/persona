package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/proto"
)

// Reporting conversations (#3192, docs/extensions/lark-agent.md "Dispatched tasks"): a conversation started with report_tasks (a
// plugin's /agent asks for it) is told about the tasks it opens as the main conversation is about its quiet ones
// (quiet.go): they are quiet, and each notice goes into it, so it can hand the result on (the /agent conversation
// replies in Feishu). Kept in the settings table: a task outlives the hub's process.

const reportersKey = "assistant.task_reporters"

// reporterKeep is how long a reporting conversation stays one after it was last started or marked.
const reporterKeep = 14 * 24 * time.Hour

type reporter struct {
	MachineID string    `json:"machine_id"`
	At        time.Time `json:"at"`
	// StoppedAt: the user stopped it (the plugin's /stop, or the stop button in an app; StoppedBy says which, "lark"
	// or "app", #3308). Its tasks' notices are dropped until the user talks in it again.
	StoppedAt *time.Time `json:"stopped_at,omitempty"`
	StoppedBy string     `json:"stopped_by,omitempty"`
}

type reporterBook struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]reporter // by session id
}

// reportersLocked reads the book once; b.mu is held. False while it cannot be read.
func (s *Service) reportersLocked(ctx context.Context) bool {
	b := &s.reporters
	if b.loaded {
		return true
	}
	m := map[string]reporter{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, reportersKey, &m); err != nil {
			s.logf("assistant: reporting conversations: %v", err)
			return false
		}
	}
	b.m, b.loaded = m, true
	return true
}

// markReporter makes conversation sid on machine one that is told about the tasks it opens.
func (s *Service) markReporter(ctx context.Context, machine, sid string) {
	if machine == "" || sid == "" {
		return
	}
	b := &s.reporters
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.reportersLocked(ctx) {
		return
	}
	now := time.Now().UTC()
	for id, r := range b.m {
		if now.Sub(r.At) > reporterKeep {
			delete(b.m, id)
		}
	}
	b.m[sid] = reporter{MachineID: machine, At: now}
	s.saveReportersLocked(ctx, short(sid))
}

// saveReportersLocked writes the book; b.mu is held.
func (s *Service) saveReportersLocked(ctx context.Context, what string) {
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, reportersKey, s.reporters.m); err != nil {
			s.logf("assistant: save reporting conversations (%s): %v", what, err)
		}
	}
}

// reporterOf is the machine of reporting conversation sid; false when it is none.
func (s *Service) reporterOf(ctx context.Context, sid string) (string, bool) {
	if sid == "" {
		return "", false
	}
	b := &s.reporters
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.reportersLocked(ctx) {
		return "", false
	}
	r, ok := b.m[sid]
	if !ok || time.Since(r.At) > reporterKeep {
		return "", false
	}
	return r.MachineID, true
}

// reporterStopped: reporting conversation sid was stopped and the user has not talked in it since.
func (s *Service) reporterStopped(ctx context.Context, sid string) bool {
	b := &s.reporters
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.reportersLocked(ctx) {
		return false
	}
	r, ok := b.m[sid]
	return ok && r.StoppedAt != nil
}

// setReporterStopped marks reporting conversation sid stopped (by "lark" or "app") or, by "", going again; false when
// it is none.
func (s *Service) setReporterStopped(ctx context.Context, sid, by string) bool {
	b := &s.reporters
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.reportersLocked(ctx) {
		return false
	}
	r, ok := b.m[sid]
	if !ok || time.Since(r.At) > reporterKeep {
		return false
	}
	if by == "" {
		if r.StoppedAt == nil {
			return true
		}
		r.StoppedAt, r.StoppedBy = nil, ""
	} else {
		now := time.Now().UTC()
		r.StoppedAt, r.StoppedBy = &now, by
	}
	b.m[sid] = r
	s.saveReportersLocked(ctx, short(sid))
	return true
}

// errNotReporter: the conversation was not started with report_tasks (or that was more than reporterKeep ago).
var errNotReporter = errors.New("not a reporting conversation")

// stoppedTask is a task stopReporting stopped: its agents were working and are interrupted.
type stoppedTask struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Agents int    `json:"agents"`
}

// stopResult is what stopReporting stopped. Already: nothing was going on (the conversation idle, no task working).
type stopResult struct {
	Conversation bool          `json:"conversation"`
	Tasks        []stoppedTask `json:"tasks"`
	Already      bool          `json:"already"`
}

// stopReporting stops reporting conversation sid and what it handed out (#3308, docs/extensions/lark-agent.md "Stopping"): its
// turn (unless by is "app": the app's stop button stopped it already), the working agents of its open tasks, and the
// notices about them (those queued for it go, later ones are dropped) until the user talks in it again. Tasks waiting
// for the user's answer and plan cards not started are left as they are: nothing runs there without the user.
func (s *Service) stopReporting(ctx context.Context, sid, by string) (stopResult, error) {
	res := stopResult{Tasks: []stoppedTask{}}
	machine, ok := s.reporterOf(ctx, sid)
	if !ok {
		return res, errNotReporter
	}
	s.setReporterStopped(ctx, sid, by) // first: a notice on its way now is dropped
	if n := s.dropQueuedNotices(sid); n > 0 {
		s.publish(ctx)
	}
	if by == "app" {
		res.Conversation = true
	} else if c, ok := s.session(ctx, machine, sid); ok && (c.Status == proto.StatusWorking || c.Starting || c.Assistant && backgroundRunning(c)) {
		// An assistant conversation whose turn ended may still run background work, which wakes it again when it
		// finishes: its interrupt stops its process, and the background commands (its children) with it (#8960). An
		// idle TUI agent's interrupt is Escape in its terminal, which stops none of them (#3793).
		if err := s.call(ctx, machine, "session.interrupt", map[string]any{"session_id": sid}, nil); err != nil {
			return res, err
		}
		res.Conversation = true
	}
	tasks := s.todosOf(ctx, sid)
	if n := s.dropSupervisionFor(tasks); n > 0 {
		s.publish(ctx)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.Before(tasks[j].CreatedAt) })
	for _, t := range tasks {
		if t.DoneAt != nil {
			continue
		}
		n := 0
		for _, a := range t.Agents {
			if a.ReleasedAt != nil || a.DismissedAt != nil {
				continue
			}
			if c, ok := s.session(ctx, a.MachineID, a.SessionID); !ok || c.Status != proto.StatusWorking && !c.Starting {
				continue
			}
			// The turn it works on, ending with no output now, was stopped by the owner, not failed by the model service
			// (silent_turns.go, #10187). Before the interrupt: the turn may end before the call comes back.
			attempt := s.noteStopped(a.MachineID, a.SessionID, s.taskStretch(t))
			if err := s.call(ctx, a.MachineID, "session.interrupt", map[string]any{"session_id": a.SessionID}, nil); err != nil {
				s.forgetStopAttempt(a.MachineID, a.SessionID, attempt)
				s.logf("stop %s: task %s: agent %s: %v", short(sid), t.ID, short(a.SessionID), err)
				continue
			}
			n++
		}
		if n > 0 {
			res.Tasks = append(res.Tasks, stoppedTask{ID: t.ID, Title: t.Title, Agents: n})
		}
	}
	res.Already = !res.Conversation && len(res.Tasks) == 0
	s.logf("stop %s (%s): conversation %v, %d task(s)", short(sid), by, res.Conversation, len(res.Tasks))
	return res, nil
}

// dropSupervisionFor removes gathered and queued supervision of tasks whose reporting conversation was stopped.
// Other tasks in a mixed queued digest are gathered again, so silencing one reporter cannot drop their notices (#5732).
// The persisted relay queue is inspected too: supervise.queued is memory only and does not survive a restart.
func (s *Service) dropSupervisionFor(tasks []Todo) int {
	ids := map[string]bool{}
	for _, t := range tasks {
		ids[t.ID] = true
	}
	if len(ids) == 0 {
		return 0
	}
	known := s.todosOf(context.Background(), "*")
	b := &s.supervise
	dropped := 0
	var regather []superviseSignal
	b.mu.Lock()
	b.init()
	for key, g := range b.pending {
		if ids[g.Todo] {
			delete(b.pending, key)
			dropped++
		}
	}
	for to, queue := range b.queued {
		kept := make([]superviseSignal, 0, len(queue))
		for _, g := range queue {
			if ids[g.Todo] {
				dropped++
				continue
			}
			kept = append(kept, g)
		}
		if len(kept) == len(queue) {
			continue
		}
		b.setQueuedLocked(to, nil)
		for _, g := range kept {
			b.pending[g.id()] = g
			regather = append(regather, g)
		}
	}
	s.updateSupervisePending(func(pending map[string]superviseSignal) {
		for key, g := range pending {
			if ids[g.Todo] {
				delete(pending, key)
			}
		}
		for _, g := range regather {
			pending[g.id()] = g
		}
	})
	b.mu.Unlock()

	// Every persisted queue, not just those still indexed in memory. A pre-restart message has no structured signals;
	// its embedded todo ids are the compatible fallback.
	conversations := map[string]bool{}
	r := &s.relays
	r.mu.Lock()
	if s.relayLocked(context.Background()) {
		for to := range r.st.Queue {
			conversations[to] = true
		}
	}
	r.mu.Unlock()
	for to := range conversations {
		n, _ := s.dropQueuedSupervisionTasks(to, ids, known)
		dropped += n
	}
	b.mu.Lock()
	if len(b.pending) > 0 {
		s.scheduleDigestLocked(time.Now())
	}
	b.mu.Unlock()
	return dropped
}

// dropQueuedSupervisionTasks removes only the selected tasks' supervise messages from one persisted queue and returns
// their signals for a caller that needs to replay them elsewhere. Structured signals preserve the exact rest of a mixed
// digest. For an older message, other todo ids found in its text are turned into fresh unjudged signals rather than
// silently losing their notification.
func (s *Service) dropQueuedSupervisionTasks(conversation string, ids map[string]bool, known []Todo) (int, []superviseSignal) {
	return s.dropQueuedSupervisionTasksAtomic(conversation, ids, known, nil)
}

// superviseQueueReplay commits a rehome's outbox update with the queue change. taken and regather are the queue's
// selected and remaining signals; the callback may also merge in-memory and already durable stretches. It returns
// whether the durable outbox changed. The caller holds supervise.mu when it touches the in-memory book.
type superviseQueueReplay func(pending map[string]superviseSignal, taken, regather []superviseSignal) bool

func (s *Service) dropQueuedSupervisionTasksAtomic(conversation string, ids map[string]bool, known []Todo, replay superviseQueueReplay) (int, []superviseSignal) {
	return s.dropQueuedSupervisionMatchingAtomic(conversation, ids, known, nil, replay)
}

// dropQueuedSupervisionSignalAtomic is the exact-signal form used when a provisional start result is confirmed or an
// old idle retires. Unstructured legacy text is removable only as an unambiguous one-entry notice of the exact kind and
// task; mixed text stays because its other members cannot be reconstructed exactly.
func (s *Service) dropQueuedSupervisionSignalAtomic(conversation string, target superviseSignal, replay superviseQueueReplay) (int, []superviseSignal) {
	return s.dropQueuedSupervisionMatchingAtomic(conversation, nil, nil, &target, replay)
}

func (s *Service) dropQueuedSupervisionMatchingAtomic(conversation string, ids map[string]bool, known []Todo, exact *superviseSignal, replay superviseQueueReplay) (int, []superviseSignal) {
	r := &s.relays
	r.mu.Lock()
	if !s.relayLocked(context.Background()) {
		r.mu.Unlock()
		return 0, nil
	}
	q := r.st.Queue[conversation]
	kept := make([]relayMsg, 0, len(q))
	var regather, taken []superviseSignal
	changed := false
	for i, m := range q {
		if m.Tool != superviseTool {
			kept = append(kept, m)
			continue
		}
		target := false
		remaining := make([]superviseSignal, 0) // non-nil: an empty list must persist as [], not legacy null
		// An empty detached list has no signals to replay; its text still identifies tasks for a selective /stop.
		signals, structuredPayload := decodeDigestSignals(m.Signals)
		structured := structuredPayload && len(signals) > 0
		if structured {
			for _, g := range signals {
				selected := ids[g.Todo]
				if exact != nil {
					selected = g.Todo == exact.Todo && g.Kind == exact.Kind && g.Key == exact.Key
				}
				if selected {
					target = true
					taken = append(taken, g)
				} else {
					remaining = append(remaining, g)
				}
			}
		} else if exact != nil {
			legacy := exact.Kind == sigUnjudged && legacyStartUnknownMessage(m.Text, exact.Todo) ||
				exact.Kind == sigIdle && legacyIdleMessage(m.Text, exact.Todo)
			if !structuredPayload && legacy {
				target = true
				taken = append(taken, *exact)
			}
		} else {
			for id := range ids {
				target = target || strings.Contains(m.Text, id)
			}
			if target {
				at := m.At
				if at.IsZero() {
					at = time.Now().UTC()
				}
				for id := range ids {
					if strings.Contains(m.Text, id) {
						taken = append(taken, superviseSignal{Todo: id, Kind: sigUnjudged,
							Key: "pre-restart-" + at.UTC().Format(time.RFC3339Nano), At: at, Detail: "重启前排队的督办"}) // i18n-ignore: the supervision message is the assistant's prompt
					}
				}
				for _, other := range known {
					if other.ID == "" || ids[other.ID] || !strings.Contains(m.Text, other.ID) {
						continue
					}
					remaining = append(remaining, superviseSignal{Todo: other.ID, Kind: sigUnjudged,
						Key: "pre-restart-" + at.UTC().Format(time.RFC3339Nano), At: at, Detail: "重启前排队的督办"}) // i18n-ignore: the supervision message is the assistant's prompt
				}
			}
		}
		if i == 0 && r.draining[conversation] || m.Withdrawn || !m.DoubtedAt.IsZero() {
			// A retained head keeps its original text only for the send already in flight. Once any selected signal is
			// removed, that text is stale for every later try. Keep the other structured signals with the withdrawn head;
			// drainRelays checks any durable doubt, then replays them only if the original did not land (#5732).
			if target {
				m.Withdrawn = true
				m.Signals = encodeDigestSignals(remaining)
				changed = true
			}
			kept = append(kept, m)
			continue
		}
		if !target {
			kept = append(kept, m)
			continue
		}
		regather = append(regather, remaining...)
	}
	n := len(q) - len(kept)
	dirty := n > 0 || changed
	if replay != nil {
		if r.st.SupervisePending == nil {
			r.st.SupervisePending = map[string]superviseSignal{}
		}
		dirty = replay(r.st.SupervisePending, taken, regather) || dirty
		if len(r.st.SupervisePending) == 0 {
			r.st.SupervisePending = nil
		}
	}
	if !dirty {
		r.mu.Unlock()
		return 0, taken
	}
	if len(kept) == 0 {
		delete(r.st.Queue, conversation)
	} else {
		r.st.Queue[conversation] = kept
	}
	s.saveRelayLocked()
	r.mu.Unlock()

	if replay != nil {
		return n, taken
	}
	b := &s.supervise
	b.mu.Lock()
	b.init()
	s.updateSupervisePending(func(pending map[string]superviseSignal) {
		for _, g := range regather {
			cur, ok := b.pending[g.id()]
			if !ok {
				// The durable outbox can hold a newer stretch before its normal in-memory restore.
				cur, ok = pending[g.id()]
			}
			if !ok {
				cur = g
			}
			b.pending[g.id()] = cur
			pending[g.id()] = cur
		}
	})
	b.mu.Unlock()
	return n, taken
}

// dropQueuedNotices takes the task notices waiting for conversation to off its queue (one going in right now stays)
// and says how many went.
func (s *Service) dropQueuedNotices(conversation string) int {
	return s.dropQueuedTool(conversation, noticeTool)
}

func (s *Service) dropQueuedSupervision(conversation string) int {
	return s.dropQueuedTool(conversation, superviseTool)
}

// dropQueuedTool takes queued messages of one tool off conversation's relay queue (one going in right now stays).
func (s *Service) dropQueuedTool(conversation, tool string) int {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return 0
	}
	q := b.st.Queue[conversation]
	kept := make([]relayMsg, 0, len(q))
	for i, m := range q {
		if m.Tool != tool || i == 0 && b.draining[conversation] {
			kept = append(kept, m)
		}
	}
	n := len(q) - len(kept)
	if n == 0 {
		return 0
	}
	if len(kept) == 0 {
		delete(b.st.Queue, conversation)
	} else {
		b.st.Queue[conversation] = kept
	}
	s.saveRelayLocked()
	return n
}

// UserStopped: the user stopped a conversation's turn in an app. A reporting one is stopped with what it handed out,
// as the plugin's /stop does (the plugin sees it in GET /api/v1/assistant/reporters and says so in Feishu).
func (s *Service) UserStopped(ctx context.Context, r SessionRef) {
	s.retireTodoWork([2]string{r.MachineID, r.SessionID})
	if _, ok := s.reporterOf(ctx, r.SessionID); !ok {
		return
	}
	if _, err := s.stopReporting(ctx, r.SessionID, "app"); err != nil {
		s.logf("stop %s from the app: %v", short(r.SessionID), err)
	}
}

// reporterRoutes: POST /api/v1/assistant/conversations/{id}/stop (the plugin's /stop) and GET
// /api/v1/assistant/reporters (which were stopped, and when).
func (s *Service) reporterRoutes() {
	s.handle("POST /api/v1/assistant/conversations/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validSessionID(id) {
			apierr.Write(w, http.StatusBadRequest, errors.New("invalid conversation id"))
			return
		}
		res, err := s.stopReporting(r.Context(), id, "lark")
		switch {
		case errors.Is(err, errNotReporter):
			apierr.Write(w, http.StatusNotFound, err)
		case err != nil:
			apierr.Call(w, err)
		default:
			apierr.WriteJSON(w, res)
		}
	})
	s.handle("GET /api/v1/assistant/reporters", func(w http.ResponseWriter, r *http.Request) {
		type view struct {
			SessionID string `json:"session_id"`
			reporter
		}
		b := &s.reporters
		b.mu.Lock()
		if !s.reportersLocked(r.Context()) {
			b.mu.Unlock()
			apierr.Write(w, http.StatusServiceUnavailable, errors.New("reporting conversations could not be read"))
			return
		}
		list := make([]view, 0, len(b.m))
		for id, rep := range b.m {
			if time.Since(rep.At) <= reporterKeep {
				list = append(list, view{SessionID: id, reporter: rep})
			}
		}
		b.mu.Unlock()
		sort.Slice(list, func(i, j int) bool { return list[i].At.After(list[j].At) })
		raw, _ := json.Marshal(map[string]any{"reporters": list})
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(raw)
	})
}
