package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
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

// dropQueuedNotices takes the task notices waiting for conversation to off its queue (one going in right now stays)
// and says how many went.
func (s *Service) dropQueuedNotices(conversation string) int {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return 0
	}
	q := b.st.Queue[conversation]
	kept := make([]relayMsg, 0, len(q))
	for i, m := range q {
		if m.Tool != noticeTool || i == 0 && b.draining[conversation] {
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
