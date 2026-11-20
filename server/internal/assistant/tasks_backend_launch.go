package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/taskworkspace"
)

func (t *PlanTask) backendWorkspace() bool {
	return t.Workspace != nil && t.Workspace.Mode == workspaceBackend
}

func (s *Service) resolveBackendTask(ctx context.Context, t *PlanTask) error {
	id := firstNonEmpty(t.TodoID, t.ReuseTodo)
	selection := t.Workspace.Backend
	if selection == nil || id == "" {
		return taskworkspace.Failure("invalid_input", "plan", "", "", "")
	}
	st, ok := s.store().(workspaceNamingStore)
	if !ok {
		return taskworkspace.Failure("store_unavailable", "plan", selection.OperationID, "", "")
	}
	plan, err := st.WorkspaceNamingPlan(ctx, id, selection.OperationID)
	if err != nil {
		return err
	}
	if plan == nil {
		return taskworkspace.Failure("not_found", "plan", selection.OperationID, "", "")
	}
	if plan.Source.MachineID != t.Machine || plan.Source.Manager != s.managerIdentity() || plan.Source.TodoID != id {
		return taskworkspace.Failure("identity_mismatch", "plan", selection.OperationID, "", "")
	}
	t.MachinePolicy, t.Cwd = machineFixed, plan.RootPath
	resolved := &TaskWorkspaceResolved{Machine: t.Machine, Path: t.Cwd, OperationID: selection.OperationID}
	for _, repo := range plan.Repositories {
		resolved.Repositories = append(resolved.Repositories, BackendLaunchRepository{RepositoryPlan: repo})
	}
	t.WorkspaceResolved = resolved
	return nil
}

func (s *Service) admitBackendTask(ctx context.Context, t *PlanTask) error {
	if t.planID == "" || t.TodoID == "" {
		return taskworkspace.Failure("invalid_input", "admission", "", "", "")
	}
	if err := s.resolveBackendTask(ctx, t); err != nil {
		return err
	}
	op := t.Workspace.Backend.OperationID
	var capabilities struct {
		Start bool `json:"start"`
	}
	if err := s.call(ctx, t.Machine, "task.workspace.capabilities", nil, &capabilities); err != nil {
		if strings.Contains(err.Error(), "unknown method") {
			return taskworkspace.Failure("unsupported", "capability", op, "", t.Cwd)
		}
		return taskworkspace.Failure("node_unavailable", "capability", op, "", t.Cwd)
	}
	if !capabilities.Start {
		return taskworkspace.Failure("unsupported", "capability", op, "", t.Cwd)
	}
	st, err := s.backendWorkspaceStore()
	if err != nil {
		return err
	}
	r, err := st.TaskWorkspace(ctx, t.TodoID, op)
	if err != nil {
		return err
	}
	if r == nil || r.State != "ready" {
		r, err = s.PrepareBackendWorkspace(ctx, t.TodoID, op)
		if err != nil {
			return err
		}
	}
	if r.Error != nil {
		return r.Error
	}
	if r.State != "ready" || r.Plan.RootPath != t.Cwd || r.Plan.Source.MachineID != t.Machine {
		return taskworkspace.Failure("not_ready", "admission", op, "", t.Cwd)
	}
	if err := taskworkspace.ValidateReady(*r); err != nil {
		return err
	}
	for i := range t.WorkspaceResolved.Repositories {
		row := &t.WorkspaceResolved.Repositories[i]
		for _, repo := range r.Repositories {
			if repo.RepoID == row.RepoID {
				row.AbsolutePath, row.ResolvedRef, row.ResolvedCommit = repo.Path, repo.ResolvedRef, repo.ResolvedCommit
				break
			}
		}
	}
	// Every retry of this plan slot presents the same use. A different plan must first retire the old session.
	use := "launch_" + taskworkspace.Hash([]string{t.planID, strconv.Itoa(t.planIndex), r.OperationID})[:32]
	if err = s.releaseStoppedBackendUses(ctx, r, use); err != nil {
		return err
	}
	req := r.Request()
	req.UseID = use
	t.WorkspaceResolved.BackendWorkspaceID, t.WorkspaceResolved.OperationID = r.WorkspaceID, r.OperationID
	if err = s.patchPlanTask(ctx, t.planID, t.planIndex, func(pt *PlanTask) {
		pt.Cwd, pt.MachinePolicy, pt.WorkspaceResolved = t.Cwd, machineFixed, t.WorkspaceResolved
	}); err != nil {
		return err
	}
	t.backendRequest = &req
	acquired, err := s.AcquireBackendWorkspace(ctx, t.TodoID, r.OperationID, use)
	if err != nil {
		var p *taskworkspace.Problem
		if acquired != nil && slices.Contains(acquired.Uses, use) && errors.As(err, &p) && p.Code != "operation_unknown" && p.Code != "operation_busy" {
			s.releaseUnusedBackendStart(ctx, t)
		}
	}
	return err
}

func (s *Service) startBackendTask(ctx context.Context, t *PlanTask, params map[string]any, out any) error {
	req := t.backendRequest
	if req == nil {
		return errors.New("backend workspace admission required")
	}
	if _, err := s.CheckBackendWorkspaceUse(ctx, t.TodoID, req.OperationID, req.UseID); err != nil {
		return err
	}
	params["backend_workspace"] = *req
	if host := t.Workspace.Backend.Host; host != "" {
		params["host"] = host
	}
	var result json.RawMessage
	err := s.call(ctx, t.Machine, DeviceSessionStart, params, &result)
	if err != nil {
		// A lost reply may hide a committed Node receipt. Reconcile the exact use.
		if startResultUnknown(err) {
			recoverCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentStartListTimeout)
			defer cancel()
			view, inspectErr := s.InspectBackendWorkspace(recoverCtx, t.TodoID, req.OperationID)
			if inspectErr == nil && view.Observation != nil && view.Observation.OperationID == req.OperationID && view.Observation.WorkspaceID == req.WorkspaceID && view.Observation.NodeOperationID == req.OperationID && view.Observation.Launch != nil && view.Observation.Launch.UseID == req.UseID && view.Observation.Launch.SessionID != "" {
				session, ok := s.adoptStartedSession(recoverCtx, Device{ID: t.Machine}, "", t.Cwd, view.Observation.Launch.SessionID, nil)
				if ok && session.Cwd == t.Cwd && !session.Archived && !session.Starting && session.StartError == "" && session.Status != proto.StatusOffline && session.Status != proto.StatusInterrupted {
					result, _ = json.Marshal(map[string]any{"session": session})
					return json.Unmarshal(result, out)
				}
			}
			return taskworkspace.Failure("operation_unknown", "start", req.OperationID, "", t.Cwd)
		}
		return err
	}
	var answer struct {
		Error *taskworkspace.Problem `json:"error"`
	}
	if err = json.Unmarshal(result, &answer); err != nil {
		return err
	}
	if answer.Error != nil {
		return answer.Error
	}
	return json.Unmarshal(result, out)
}

func (s *Service) releaseUnusedBackendStart(ctx context.Context, t *PlanTask) {
	req := t.backendRequest
	if req == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	// Node holds its launch lock through dispatch. A live or ambiguous receipt refuses release.
	_, err := s.ReleaseBackendWorkspace(ctx, t.TodoID, req.OperationID, req.UseID)
	if err != nil {
		s.logf("task %s: workspace use retained: %v", t.TodoID, err)
	}
}

func (s *Service) releaseStoppedBackendUses(ctx context.Context, r *taskworkspace.Record, keep string) error {
	for _, use := range r.Uses {
		if use == keep || !strings.HasPrefix(use, "launch_") {
			continue
		}
		// Only release a positively identified stopped session. A preparing start can still dispatch.
		view, err := s.InspectBackendWorkspace(ctx, r.Plan.Source.TodoID, r.OperationID)
		if err != nil {
			return err
		}
		obs := view.Observation
		if obs == nil || obs.Error != nil || obs.OperationID != r.OperationID || obs.NodeOperationID != r.OperationID || obs.WorkspaceID != r.WorkspaceID {
			return taskworkspace.Failure("operation_unknown", "release", r.OperationID, "", r.Plan.RootPath)
		}
		// A confirmed cancellation also rules out a late start that had not dispatched.
		unstarted := r.State == "cancelled" && obs.State == "cancelled" && obs.Launch == nil
		if !unstarted && (obs.Launch == nil || obs.Launch.UseID != use || obs.Launch.SessionID == "") {
			return taskworkspace.Failure("worktree_in_use", "release", r.OperationID, "", r.Plan.RootPath)
		}
		if _, err = s.ReleaseBackendWorkspace(ctx, r.Plan.Source.TodoID, r.OperationID, use); err != nil {
			return err
		}
	}
	return nil
}
