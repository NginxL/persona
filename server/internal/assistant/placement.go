package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// A placement is one logical start. A transport compatibility retry uses the
// same decision. A later start (including a dequeue or resume) gets a new one.
type placement struct {
	ID             string               `json:"id"`
	At             time.Time            `json:"at"`
	PolicyVersion  int                  `json:"policy_version"`
	Mode           string               `json:"mode"`
	Requested      string               `json:"requested"`
	Candidates     []placementCandidate `json:"candidates"`
	Choice         string               `json:"choice,omitempty"`
	Recommendation string               `json:"recommendation,omitempty"`
	ReasonKey      string               `json:"reason_key"`
	Reason         string               `json:"reason,omitempty"`
	WaitAt         time.Time            `json:"wait_at,omitzero"`
	Reroute        *PlanReroute         `json:"reroute,omitempty"`
	TaskID         string               `json:"task_id,omitempty"`
	SessionID      string               `json:"session_id,omitempty"`
}

type placementCandidate struct {
	Machine     string         `json:"machine"`
	Hits        []placementHit `json:"filters,omitempty"`
	LoadPerCore *float64       `json:"load_per_core,omitempty"`
	FreeBytes   *int64         `json:"free_bytes,omitempty"`
	HealthRank  *int           `json:"health_rank,omitempty"`
}

type placementHit struct {
	Key  string    `json:"key"`
	Args i18n.Args `json:"args,omitempty"`
}

type placementKey struct{}
type placementBindKey struct{}
type placementTaskKey struct{}

func newPlacement(machine, mode string) *placement {
	p := &placement{ID: randomHex(16), At: time.Now().UTC(), PolicyVersion: 1, Mode: mode, Requested: machine, ReasonKey: "requested_machine"}
	p.candidate(machine)
	return p
}

func placementOf(ctx context.Context) *placement {
	p, _ := ctx.Value(placementKey{}).(*placement)
	return p
}

// A stored plan can share its candidate slices with a card reader or a retry.
func (p *placement) cloneCandidates() []placementCandidate {
	candidates := slices.Clone(p.Candidates)
	for i := range candidates {
		candidates[i].Hits = slices.Clone(candidates[i].Hits)
	}
	return candidates
}

func (p *placement) candidate(id string) *placementCandidate {
	for i := range p.Candidates {
		if p.Candidates[i].Machine == id {
			return &p.Candidates[i]
		}
	}
	p.Candidates = append(p.Candidates, placementCandidate{Machine: id})
	return &p.Candidates[len(p.Candidates)-1]
}

func (p *placement) hit(id, key string, args i18n.Args) {
	if p == nil {
		return
	}
	c := p.candidate(id)
	if !slices.ContainsFunc(c.Hits, func(h placementHit) bool { return h.Key == key }) {
		c.Hits = append(c.Hits, placementHit{key, args})
	}
}

func (p *placement) violation(machine string, why *i18n.Message) {
	if why != nil {
		p.hit(machine, why.Key, why.Args)
	}
}

func (s *Service) savePlacement(ctx context.Context, p *placement) {
	if s.store == nil {
		return
	}
	store := s.store()
	if store == nil {
		return
	}
	raw, err := json.Marshal(p)
	if err == nil {
		write, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		err = store.PutPlacement(write, db.PlacementRow{ID: p.ID, TaskID: p.TaskID, Machine: p.Choice, SessionID: p.SessionID, At: p.At, Decision: raw})
	}
	if err != nil {
		s.logf("placement %s: save: %v", p.ID, err)
	}
}

// beginPlacementBind is shared by manager-owned calls and the hub's devices.call.
// Only the manager can supply an enforced decision (through the local context).
// A pinned start takes no claim and never consults alternative computers.
func (s *Service) beginPlacementBind(ctx context.Context, machine string, raw json.RawMessage) func(json.RawMessage) {
	p, _ := ctx.Value(placementBindKey{}).(*placement)
	if p == nil {
		p = newPlacement(machine, "report")
	}
	if p.Mode == "report" {
		var target struct {
			Launcher string `json:"launcher"`
			TodoID   string `json:"todo_id"`
			Resume   string `json:"resume_session_id"`
		}
		_ = json.Unmarshal(raw, &target)
		taskID, _ := ctx.Value(placementTaskKey{}).(string)
		p.TaskID = firstNonEmpty(taskID, p.TaskID, target.TodoID)
		p.SessionID = target.Resume
		if p.TaskID == "" && target.Resume != "" {
			if todo, ok := s.todoOfSession(ctx, machine, target.Resume); ok {
				p.TaskID = todo.ID
			}
		}
		s.reportPlacement(ctx, p, machine, target.Launcher)
	}
	p.candidate(machine)
	p.Choice = machine
	s.savePlacement(ctx, p) // durable even if the node never answers
	return func(out json.RawMessage) {
		var result struct {
			Session struct {
				ID string `json:"session_id"`
			} `json:"session"`
		}
		if json.Unmarshal(out, &result) == nil && result.Session.ID != "" {
			p.SessionID = result.Session.ID
			s.savePlacement(ctx, p)
		}
	}
}

func (s *Service) reportPlacement(ctx context.Context, p *placement, machine, launcher string) {
	if s.devs == nil {
		return
	}
	if p.SessionID != "" && launcher == "" {
		if row, ok := s.devs.Session(machine, p.SessionID); ok {
			launcher = row.Launcher
		}
	}
	launcher = s.startLauncher(ctx, launcher)
	if h, bad := s.launcherUnusable(ctx, machine, launcher); bad {
		p.hit(machine, "launcher_unhealthy", i18n.Args{"launcher": launcher, "kind": h.Kind})
	}
	if m, ok := s.devs.Summary(machine); ok && !m.Reachable {
		p.hit(machine, "offline", nil)
	}
	task := PlanTask{TodoID: p.TaskID, Machine: machine, Launcher: launcher}
	if todo, ok := s.todoNow(p.TaskID); ok {
		task.Project = todo.Project
	}
	s.placeStart(context.WithValue(ctx, placementKey{}, p), &task, placementReport)
}

func (s *Service) finishPlanPlacement(ctx context.Context, p *placement, t *PlanTask, sid string, err error) {
	p.Reroute, p.Reason = t.Reroute, t.Why
	if sid != "" {
		p.SessionID = sid
	}
	var wait *gatewayQuotaWaitError
	var busy *workspaceBusyQueueError
	var denied *admissionDeniedError
	var line *i18n.Message
	switch {
	case errors.As(err, &wait):
		p.ReasonKey = wait.queue.Key
		p.hit(t.Machine, wait.queue.Key, nil)
		p.Reason = wait.queue.Reason
	case errors.As(err, &busy):
		p.Reason = s.workspaceWaitReason(ctx, busy.machine, busy.path, busy.ownerID, busy.ownerTitle)
		p.ReasonKey = workspaceQueueKey
		p.hit(t.Machine, workspaceQueueKey, i18n.Args{"owner": busy.ownerID})
	case errors.As(err, &denied):
		p.Reason = denied.msg.Text(s.lang(ctx))
	case errors.As(err, &line) && (line.Key == "assistant-tasks.fixed_launcher_unhealthy" || line.Key == "assistant-tasks.launcher_not_on_machine" || line.Key == "assistant-tasks_workspace.busy" || line.Key == "rpc-errors.agent_assistant_workspace"):
		p.ReasonKey = line.Key
		p.violation(t.Machine, line)
		p.Reason = line.Text(s.lang(ctx))
	}
	t.Placement = p
	s.savePlacement(ctx, p)
}

type placementStage int

const (
	placementReport placementStage = iota
	placementPreflight
	placementReserve
	placementPlan
)

// placeStart applies the built-in group, machine and direction lines. Only a flexible
// plan may score alternatives. Reports and approval previews never acquire a claim.
func (s *Service) placeStart(ctx context.Context, t *PlanTask, stage placementStage) (func(), error) {
	if s.devs == nil || !s.admissionEnabled(ctx) {
		return func() {}, nil
	}
	cfg := s.admissionSettings(ctx)
	direction := s.taskDirection(ctx, t)
	launcher := s.startLauncher(ctx, t.Launcher)
	lim := s.limitsFor(ctx, t.Machine, direction, t.Project, cfg).withLauncher(cfg, launcher)
	if stage == placementReport {
		s.admission.mu.Lock()
		s.placementViolationLocked(placementOf(ctx), t.Machine, direction, lim)
		s.admission.mu.Unlock()
		return nil, nil
	}
	canMove := t.MachinePolicy == machineFlexible
	if stage == placementPreflight || stage == placementPlan {
		// A full launcher group is checked first: no machine frees a slot in it, so the task waits on the group (with no
		// queue time limit) rather than on a machine line it might also hit, and a flexible task is not moved for nothing.
		if violation := s.groupViolation(lim); violation != nil {
			return nil, s.admissionRefused(ctx, t.Machine, direction, violation)
		}
		if violation := s.machineViolation(ctx, t.Machine, lim); violation != nil {
			if !canMove {
				return nil, s.admissionRefused(ctx, t.Machine, direction, violation)
			}
			alt, ok := s.rerouteTarget(ctx, t)
			if !ok {
				return nil, s.admissionRefused(ctx, t.Machine, direction, violation)
			}
			from, _ := s.machineByName(ctx, t.Machine)
			s.reroutePlanTask(ctx, t, from, alt, violation.Text(s.lang(ctx)))
			lim = s.limitsFor(ctx, t.Machine, direction, t.Project, cfg).withLauncher(cfg, launcher)
		}
		if stage == placementPreflight {
			return nil, nil
		}
	}
	// Atomic decision: evaluate against the current claims and reserve the slot in one critical section, so two
	// concurrent starts cannot both observe room and both take the last slot.
	release, violation := s.claimIfAllowed(ctx, t.Machine, direction, lim)
	if violation != nil {
		return nil, s.admissionRefused(ctx, t.Machine, direction, violation)
	}
	return release, nil
}

// queuedPlacement snapshots the refusal shown when this attempt entered the queue.
// Subsequent queue refreshes have their own status and do not start another Agent.
func queuedPlacement(task PlanTask, q *TaskQueue) *placement {
	p := task.Placement
	if p == nil || p.TaskID != task.TodoID {
		p = newPlacement(task.Machine, "enforce")
		p.TaskID = task.TodoID
	} else {
		cp := *p
		cp.Candidates = p.cloneCandidates()
		p = &cp
	}
	p.ReasonKey = q.Key
	p.hit(task.Machine, q.Key, nil)
	p.WaitAt, p.Reason = q.NextAt, q.Reason
	return p
}

func (t PlanTask) queueReason() string {
	if p := t.Placement; p != nil && p.ReasonKey == t.Queue.Key && p.WaitAt.Equal(t.Queue.NextAt) {
		return p.Reason
	}
	return t.Queue.Reason
}

// A plan preview and a retry after a launcher failure can both pick the next
// target before startTaskAgent. Carry that choice into its actual placement.
func (s *Service) previewHealthPlacement(ctx context.Context, t *PlanTask) bool {
	if t.MachinePolicy != machineFlexible {
		return false
	}
	p := newPlacement(t.Machine, "enforce")
	if !s.healthyPlace(context.WithValue(ctx, placementKey{}, p), t) {
		return false
	}
	p.Choice, p.Reason, p.Reroute = t.Machine, t.Why, t.Reroute
	t.Placement = p
	return true
}

// placePlanStart runs after the owner's gate. A quota wait leaves before any
// claim. The existing admission and workspace queues retain ownership of retries.
func (s *Service) placePlanStart(ctx context.Context, t *PlanTask, own startPick, params map[string]any, retry bool) (Device, error) {
	s.healthyPlace(ctx, t)
	m, ok := s.machineByName(ctx, t.Machine)
	if !ok {
		return Device{}, fmt.Errorf("unknown machine %q", t.Machine)
	}
	// The last look, on the machine it will really start on (a retry, a remedy, a recovered start and a flexible task moved
	// by the health check all pass here): a task leader never starts on a work-trust computer.
	if assistantWorkspace(m, t.Cwd) {
		return Device{}, i18n.Err("rpc-errors.agent_assistant_workspace")
	}
	if err := s.leaderOnWorkMachine(t, m); err != nil {
		placementOf(ctx).hit(m.ID, "leader_work_machine", nil)
		return Device{}, err
	}
	if !m.Reachable {
		placementOf(ctx).hit(m.ID, "offline", nil)
		return Device{}, machineOffline(m.Name)
	}
	if err := s.deferGatewayStart(ctx, t, m, own, params, retry); err != nil {
		return Device{}, err
	}
	// The resource/direction admission gate (admission.go): fixed tasks are refused here, flexible tasks may have
	// been moved to a less loaded machine. The claim is stored on the task and held until its agent is linked or the
	// start fails for good (runPlanTaskStart), so it still counts while metrics and the ledger catch up.
	release, err := s.admitTaskStart(ctx, t)
	if err != nil {
		return Device{}, err
	}
	t.admissionRelease = release
	if m.ID != t.Machine { // a flexible reroute inside the gate: every later step must use the machine it landed on
		m, ok = s.machineByName(ctx, t.Machine)
		if !ok {
			return Device{}, fmt.Errorf("unknown machine %q", t.Machine)
		}
		if err := s.leaderOnWorkMachine(t, m); err != nil {
			return Device{}, err
		}
		if !m.Reachable {
			return Device{}, machineOffline(m.Name)
		}
	}
	return m, nil
}
