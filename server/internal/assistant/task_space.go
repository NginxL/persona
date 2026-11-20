package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Task workspaces (#4786). Only a top-level leader and its workers on classic herdr share one. The manager persists
// the intended label before dispatch and the node's actual workspace ID afterward. Other hosts and ordinary tasks keep
// their existing directory/workspace placement and never acquire this lifecycle.

const (
	taskSpacesKey        = "assistant.task_workspaces"
	deviceWorkspaceClose = "workspace.close"
	deviceTaskStart      = DeviceSessionStart
	notTaskSpace         = "not_task_workspace"
	taskSpaceLabelMax    = 60
)

type taskSpace struct {
	Machine string `json:"machine"`
	ID      string `json:"workspace_id,omitempty"`
	Label   string `json:"label"`
}

type taskSpaceWire struct {
	TaskID      string `json:"task_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

type taskSpaceReply struct {
	TaskWorkspace *taskSpaceWire `json:"task_workspace"`
	Session       proto.Session  `json:"session"`
}

type taskSpaceBook struct {
	mu     sync.Mutex
	loaded bool
	spaces map[string]taskSpace
	locks  map[string]*sync.Mutex
	tried  map[string]time.Time
}

type taskSpaceCall struct {
	root string
	held bool
}

type taskSpaceCallKey struct{}

func (s *Service) loadTaskSpacesLocked(ctx context.Context) bool {
	b := &s.taskSpaces
	if b.loaded {
		return true
	}
	spaces := map[string]taskSpace{}
	if store := s.store(); store != nil {
		if _, err := store.Setting(ctx, taskSpacesKey, &spaces); err != nil {
			s.logf("task workspaces: load: %v", err)
			return false
		}
	}
	if spaces == nil {
		spaces = map[string]taskSpace{}
	}
	b.spaces, b.loaded = spaces, true
	return true
}

func (s *Service) taskSpaceOf(ctx context.Context, root string) (taskSpace, bool) {
	s.taskSpaces.mu.Lock()
	defer s.taskSpaces.mu.Unlock()
	if root == "" || !s.loadTaskSpacesLocked(ctx) {
		return taskSpace{}, false
	}
	space, ok := s.taskSpaces.spaces[root]
	return space, ok
}

func (s *Service) putTaskSpace(ctx context.Context, root string, space taskSpace) error {
	s.taskSpaces.mu.Lock()
	defer s.taskSpaces.mu.Unlock()
	if root == "" || !s.loadTaskSpacesLocked(ctx) {
		return nil
	}
	if current, ok := s.taskSpaces.spaces[root]; ok && current == space {
		return nil
	}
	next := maps.Clone(s.taskSpaces.spaces)
	next[root] = space
	if store := s.store(); store != nil {
		if err := store.PutSetting(ctx, taskSpacesKey, next); err != nil {
			return err
		}
	}
	s.taskSpaces.spaces = next
	return nil
}

func (s *Service) lockTaskSpace(root string) func() {
	if root == "" {
		return func() {}
	}
	s.taskSpaces.mu.Lock()
	if s.taskSpaces.locks == nil {
		s.taskSpaces.locks = map[string]*sync.Mutex{}
	}
	lock := s.taskSpaces.locks[root]
	if lock == nil {
		lock = &sync.Mutex{}
		s.taskSpaces.locks[root] = lock
	}
	s.taskSpaces.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func taskSpaceRoot(t PlanTask) string {
	switch {
	case t.Role == roleLeader && t.Parent == "":
		return t.TodoID
	case t.Parent != "":
		return t.Parent
	default:
		return ""
	}
}

func taskSpaceLabel(title, id string) string {
	return clip(oneLine(firstNonEmpty(strings.TrimSpace(title), id)), taskSpaceLabelMax)
}

func herdrTaskHost(m Device, params map[string]any) bool {
	host, _ := params["host"].(string)
	if host == "" {
		host = proto.EngineHostsOf(m.Engine).Default
	}
	return host == "" || host == proto.EngineHerdr
}

// withTaskSpaceStart attaches a leader/worker start and holds its task's placement lock through the start, adoption,
// and response recording. A worker without a recorded leader workspace keeps the old placement (for example, a leader
// that predates this change); non-herdr leaders never lose their explicit workspace_id.
func (s *Service) withTaskSpaceStart(ctx context.Context, t PlanTask, m Device, params map[string]any) (context.Context, func(), error) {
	root := taskSpaceRoot(t)
	if t.backendWorkspace() || root == "" || !herdrTaskHost(m, params) {
		return ctx, func() {}, nil
	}
	unlock := s.lockTaskSpace(root)
	space, ok := s.taskSpaceOf(ctx, root)
	if t.Role == roleLeader && t.Parent == "" {
		if !ok || s.canonical(space.Machine) != s.canonical(m.ID) {
			space = taskSpace{Machine: s.canonical(m.ID), Label: taskSpaceLabel(t.Title, root)}
			if err := s.putTaskSpace(ctx, root, space); err != nil {
				unlock()
				return ctx, func() {}, err
			}
		}
	} else if !ok || s.canonical(space.Machine) != s.canonical(m.ID) {
		unlock()
		return ctx, func() {}, nil
	}
	params["task_id"] = root
	if space.ID != "" {
		params["workspace_id"] = space.ID
		delete(params, "workspace_label")
	} else {
		delete(params, "workspace_id")
		params["workspace_label"] = space.Label
	}
	return context.WithValue(ctx, taskSpaceCallKey{}, taskSpaceCall{root: root, held: true}), unlock, nil
}

// taskCheckoutOnly reports whether a new task checkout must not open a workspace of its own. A herdr leader and a
// worker with a recorded leader workspace use the shared task workspace; all other tasks retain worktree.create's UI.
func (s *Service) taskCheckoutOnly(ctx context.Context, t PlanTask) bool {
	root := taskSpaceRoot(t)
	if root == "" {
		return false
	}
	m, ok := s.machineByName(ctx, t.Machine)
	if !ok || !herdrTaskHost(m, nil) {
		return false
	}
	if t.Role == roleLeader && t.Parent == "" {
		return true
	}
	space, ok := s.taskSpaceOf(ctx, root)
	return ok && s.canonical(space.Machine) == s.canonical(t.Machine)
}

func taskSpaceRootFromContext(ctx context.Context) (string, bool) {
	call, _ := ctx.Value(taskSpaceCallKey{}).(taskSpaceCall)
	return call.root, call.held
}

// sessionTaskSpaceRoot finds the leader task whose agent this is. Done tasks remain eligible so an explicit resume
// reopens their recorded workspace; released or dismissed links no longer confer placement.
func (s *Service) sessionTaskSpaceRoot(ctx context.Context, machine, session string) string {
	if session == "" || !s.todosLoaded() {
		return ""
	}
	machine = s.canonical(machine)
	root := ""
	s.todos.mu.RLock()
	for _, todo := range s.todos.todos {
		for _, agent := range todo.Agents {
			if agent.MachineID != machine || agent.SessionID != session || agent.ReleasedAt != nil || agent.DismissedAt != nil {
				continue
			}
			candidate := ""
			switch agent.Source {
			case sourceLeader:
				candidate = todo.ID
			case sourceWorker:
				candidate = agent.Via
			}
			if candidate != "" && (root == "" || todo.DoneAt == nil) {
				root = candidate
			}
		}
	}
	s.todos.mu.RUnlock()
	return root
}

// attachTaskSpaceResume carries the recorded placement through the shared call path used by rework and explicit
// continue. With a live ID no label is needed; after closure the retained label recreates the task workspace.
func (s *Service) attachTaskSpaceResume(ctx context.Context, machine, method string, params map[string]any) string {
	session, _ := params["session_id"].(string)
	if method == deviceTaskStart {
		session, _ = params["resume_session_id"].(string)
	}
	root := s.sessionTaskSpaceRoot(ctx, machine, session)
	space, ok := s.taskSpaceOf(ctx, root)
	if !ok || s.canonical(space.Machine) != s.canonical(machine) {
		return ""
	}
	params["task_id"] = root
	if space.ID != "" {
		params["workspace_id"] = space.ID
		delete(params, "workspace_label")
	} else {
		delete(params, "workspace_id")
		params["workspace_label"] = space.Label
	}
	if method == deviceTaskStart {
		params["tab_name"] = space.Label
	}
	return root
}

func (s *Service) noteTaskSpaceResult(ctx context.Context, root, machine string, raw json.RawMessage) {
	if root == "" || len(raw) == 0 {
		return
	}
	var reply taskSpaceReply
	if json.Unmarshal(raw, &reply) != nil {
		return
	}
	space, _ := s.taskSpaceOf(ctx, root)
	space.Machine = s.canonical(machine)
	if reply.TaskWorkspace != nil {
		space.ID = reply.TaskWorkspace.WorkspaceID
		if reply.TaskWorkspace.Label != "" {
			space.Label = reply.TaskWorkspace.Label
		}
	} else {
		space.ID = reply.Session.WorkspaceID
		var ownership struct {
			TaskID          string `json:"task_id"`
			TaskWorkspaceID string `json:"task_workspace_id"`
		}
		if space.ID == "" {
			return
		}
		if err := s.call(ctx, machine, "session.info", map[string]any{"session_id": reply.Session.SessionID}, &ownership); err != nil ||
			ownership.TaskID != root || ownership.TaskWorkspaceID != space.ID {
			return // a kept shell's current user workspace is not the task's recorded workspace
		}
	}
	if space.ID == "" || space.Label == "" {
		return
	}
	if err := s.putTaskSpace(ctx, root, space); err != nil {
		s.logf("task %s: record workspace: %v", root, err)
	}
}

// taskSpacesSessionsChanged reconciles asynchronous starts and answers lost after a tab was created. A stopped row
// may fill an empty placeholder, but cannot overwrite a newer ID, so it cannot move placement back to an old workspace.
func (s *Service) taskSpacesSessionsChanged(ctx context.Context, d DeviceSessions) {
	for _, session := range d.Sessions {
		if session.WorkspaceID == "" {
			continue
		}
		root := s.sessionTaskSpaceRoot(ctx, d.MachineID, session.SessionID)
		space, ok := s.taskSpaceOf(ctx, root)
		if !ok || space.ID == session.WorkspaceID {
			continue
		}
		if space.ID != "" && (session.PaneID == "" || session.Status == proto.StatusOffline) {
			continue // an old stopped row cannot move a newer placement back to an earlier workspace
		}
		var ownership struct {
			TaskID          string `json:"task_id"`
			TaskWorkspaceID string `json:"task_workspace_id"`
		}
		if err := s.call(ctx, d.MachineID, "session.info", map[string]any{"session_id": session.SessionID}, &ownership); err != nil ||
			ownership.TaskID != root || ownership.TaskWorkspaceID != session.WorkspaceID {
			continue // a moved live row is not proof that the task owns its new user workspace
		}
		space.ID = session.WorkspaceID
		if err := s.putTaskSpace(ctx, root, space); err != nil {
			s.logf("task %s: record workspace from sessions: %v", root, err)
		}
	}
}

func (s *Service) taskSpaceTried(root string) time.Time {
	s.taskSpaces.mu.Lock()
	defer s.taskSpaces.mu.Unlock()
	return s.taskSpaces.tried[root]
}

// taskSpaceMachines are all computers with an Agent under root, including workers on other machines.
func (s *Service) taskSpaceMachines(root string) map[string]bool {
	out := map[string]bool{}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for _, todo := range s.todos.todos {
		for _, agent := range todo.Agents {
			belongs := agent.Source == sourceLeader && todo.ID == root || agent.Source == sourceWorker && agent.Via == root
			if belongs && agent.ReleasedAt == nil && agent.DismissedAt == nil {
				out[s.canonical(agent.MachineID)] = true
			}
		}
	}
	return out
}

// taskSpaceBusy requires every Agent under the top-level task, and anything else live in that workspace, to be gone
// or confirmed stopped. Every agent's own machine must have answered a session list; a missing machine is never
// proof of retirement. lastStop distinguishes a stale idle row from a genuinely resumed Agent with newer activity.
func (s *Service) taskSpaceBusy(root, workspace string, lists map[string][]proto.Session, seen doneSeen) bool {
	rows := map[[2]string]proto.Session{}
	for machine, sessions := range lists {
		for _, row := range sessions {
			rows[[2]string{machine, row.SessionID}] = row
		}
	}
	retired := map[[2]string]bool{}
	busy := false
	s.todos.mu.RLock()
	for _, todo := range s.todos.todos {
		for _, agent := range todo.Agents {
			belongs := agent.Source == sourceLeader && todo.ID == root || agent.Source == sourceWorker && agent.Via == root
			if !belongs || agent.ReleasedAt != nil || agent.DismissedAt != nil {
				continue
			}
			key := [2]string{s.canonical(agent.MachineID), agent.SessionID}
			if _, listed := lists[key[0]]; !listed {
				busy = true
				continue
			}
			row, ok := rows[key]
			if !ok || row.PaneID == "" || row.Status == proto.StatusOffline {
				retired[key] = true
				continue
			}
			watched := seen[key]
			if watched.stopped && !watched.lastStop.IsZero() && !row.UpdatedAt.After(watched.lastStop) {
				retired[key] = true
				continue
			}
			busy = true
		}
	}
	s.todos.mu.RUnlock()
	if busy {
		return true
	}
	for key, row := range rows {
		if row.WorkspaceID == workspace && row.PaneID != "" && row.Status != proto.StatusOffline && !retired[key] {
			return true
		}
	}
	return false
}

// closeDoneTaskSpaces runs in the same pass that retires done tasks' Agents, so accepted and cancelled tasks share
// the same gate. A closed record keeps its label for later rework but has no ID and is idempotent on later passes.
func (s *Service) closeDoneTaskSpaces(ctx context.Context, now time.Time, seen doneSeen) {
	if !s.todosLoaded() {
		return
	}
	s.taskSpaces.mu.Lock()
	if !s.loadTaskSpacesLocked(ctx) {
		s.taskSpaces.mu.Unlock()
		return
	}
	spaces := maps.Clone(s.taskSpaces.spaces)
	s.taskSpaces.mu.Unlock()
	reachable := map[string]bool{}
	for _, device := range s.devices(ctx) {
		reachable[s.canonical(device.ID)] = device.Reachable
	}
	lists := map[string][]proto.Session{}
nextTask:
	for root, recorded := range spaces {
		if recorded.ID == "" || !reachable[s.canonical(recorded.Machine)] || now.Sub(s.taskSpaceTried(root)) < taskCloseRetry {
			continue
		}
		unlock := s.lockTaskSpace(root)
		todo, known := s.todoNow(root)
		space, ok := s.taskSpaceOf(ctx, root)
		if !known || todo.DoneAt == nil || !ok || space.ID == "" || space.ID != recorded.ID {
			unlock()
			continue
		}
		machine := s.canonical(space.Machine)
		machines := s.taskSpaceMachines(root)
		machines[machine] = true
		for machineID := range machines {
			if !reachable[machineID] {
				unlock()
				continue nextTask
			}
			if _, loaded := lists[machineID]; loaded {
				continue
			}
			got, err := s.sessions(ctx, machineID)
			if err != nil {
				unlock()
				continue nextTask
			}
			lists[machineID] = got
		}
		if s.taskSpaceBusy(root, space.ID, lists, seen) {
			unlock()
			continue
		}
		s.closeTaskSpace(ctx, root, space, now)
		unlock()
	}
}

func (s *Service) closeTaskSpace(ctx context.Context, root string, space taskSpace, now time.Time) {
	var result struct {
		Closed bool `json:"closed"`
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	err := s.call(callCtx, space.Machine, deviceWorkspaceClose, map[string]any{"task_id": root, "workspace_id": space.ID}, &result)
	cancel()
	var rpcErr *rpc.Error
	switch {
	case err == nil:
		if result.Closed {
			s.logf("task %s: closed its workspace %s/%s", root, space.Machine, space.ID)
		}
	case errors.As(err, &rpcErr) && rpcErr.Code == notTaskSpace:
		s.logf("task %s: workspace %s/%s left open: %v", root, space.Machine, space.ID, err)
	default:
		s.logf("task %s: close workspace %s/%s: %v", root, space.Machine, space.ID, err)
		s.taskSpaces.mu.Lock()
		if s.taskSpaces.tried == nil {
			s.taskSpaces.tried = map[string]time.Time{}
		}
		s.taskSpaces.tried[root] = now
		s.taskSpaces.mu.Unlock()
		return
	}
	s.taskSpaces.mu.Lock()
	delete(s.taskSpaces.tried, root)
	s.taskSpaces.mu.Unlock()
	space.ID = ""
	if err := s.putTaskSpace(ctx, root, space); err != nil {
		s.logf("task %s: record closed workspace: %v", root, err)
	}
}
