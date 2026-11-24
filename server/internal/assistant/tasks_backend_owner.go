package assistant

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

type ownerBackendTodoKey struct{}

type backendStartInput struct {
	StartID     string `json:"start_id"`
	OperationID string `json:"operation_id"`
	Host        string `json:"host,omitempty"`
	Launcher    string `json:"launcher,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
}

func (s *Service) startOwnerBackendPlan(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		apierr.Write(w, http.StatusForbidden, errors.New("only the owner can start a workspace task"))
		return
	}
	var in backendStartInput
	if !namingBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	if !workspacenaming.ValidOperationID(in.StartID) || !workspacenaming.ValidOperationID(in.OperationID) {
		backendWorkspaceError(w, taskworkspace.Failure("invalid_input", "plan", in.OperationID, "", ""))
		return
	}
	ctx := context.WithValue(ownerStart(r.Context()), ownerBackendTodoKey{}, id)
	p, err := s.ownerBackendPlan(ctx, id, in)
	if err != nil {
		backendWorkspaceError(w, err)
		return
	}
	apierr.WriteJSON(w, map[string]any{"plan": p})
}

func (s *Service) ownerBackendPlan(ctx context.Context, id string, in backendStartInput) (*TaskPlan, error) {
	planID := "plan_" + taskworkspace.Hash([]string{id, in.StartID})[:32]
	digest := taskworkspace.Hash(in)
	p, err := s.loadPlan(ctx, planID)
	if errors.Is(err, errPlanNotFound) {
		todo, ok, err := s.todo(ctx, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, taskworkspace.Failure("not_found", "plan", in.OperationID, "", "")
		}
		st, ok := s.store().(workspaceNamingStore)
		if !ok {
			return nil, taskworkspace.Failure("store_unavailable", "plan", in.OperationID, "", "")
		}
		named, err := st.WorkspaceNamingPlan(ctx, id, in.OperationID)
		if err != nil {
			return nil, err
		}
		if named == nil {
			return nil, taskworkspace.Failure("not_found", "plan", in.OperationID, "", "")
		}
		conversation := firstNonEmpty(supervisorOf(todo), todo.Origin, s.settings(ctx).Main)
		if _, err = s.reusableTodo(ctx, conversation, id, false); err != nil {
			return nil, err
		}
		callerPermission := "" // this entry point has no permission field: the caller named none
		task := PlanTask{Title: todo.Title, Prompt: firstNonEmpty(strings.TrimSpace(in.Prompt), todo.Notes, todo.Title), Machine: named.Source.MachineID, Launcher: in.Launcher,
			CallerPermission: &callerPermission,
			ReuseTodo:        id, BackendStartDigest: digest, Workspace: &TaskWorkspace{Mode: workspaceBackend, Backend: &BackendWorkspaceSelection{OperationID: in.OperationID, Host: in.Host}}}
		if err = s.resolveBackendTask(ctx, &task); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		p = &TaskPlan{ID: planID, Conversation: conversation, Tasks: []PlanTask{task}, State: planOpen, CreatedAt: now, UpdatedAt: now}
		if err = s.savePlan(ctx, p); err != nil {
			prior, readErr := s.loadPlan(ctx, planID)
			if readErr != nil {
				return nil, err
			}
			p = prior
		}
	} else if err != nil {
		return nil, err
	}
	if len(p.Tasks) != 1 || p.Tasks[0].BackendStartDigest != digest {
		return nil, taskworkspace.Failure("operation_conflict", "start", in.OperationID, "", "")
	}
	if p.State != planOpen {
		return p, nil
	}
	started, err := s.startPlan(ctx, planID, nil, false)
	if errors.Is(err, errPlanNotOpen) {
		return s.loadPlan(ctx, planID)
	}
	return started, err
}
