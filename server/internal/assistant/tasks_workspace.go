package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	workspaceNew      = "new"
	workspaceExisting = "existing"
	workspaceNone     = "none"
	preferExisting    = "prefer-existing"
	preferNew         = "prefer-new"
)

// TaskWorkspace is a task-level policy, separate from its resolved checkout. The
// object variants leave room for future repository entries without changing cwd.
type TaskWorkspace struct {
	Mode     string
	Existing string
	Branch   string
}

func (w *TaskWorkspace) UnmarshalJSON(raw []byte) error {
	*w = TaskWorkspace{}
	var mode string
	if json.Unmarshal(raw, &mode) == nil {
		if mode != workspaceNew && mode != workspaceNone {
			return fmt.Errorf("workspace must be new, {existing: cwd or workspace_id}, {new: {branch: name}}, or none")
		}
		w.Mode = mode
		return nil
	}
	var in struct {
		Existing string `json:"existing"`
		New      *struct {
			Branch string `json:"branch"`
		} `json:"new"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return fmt.Errorf("invalid workspace: %w", err)
	}
	in.Existing = strings.TrimSpace(in.Existing)
	if in.Existing != "" && in.New == nil {
		w.Mode, w.Existing = workspaceExisting, in.Existing
		return nil
	}
	if in.New != nil && in.Existing == "" {
		w.Mode, w.Branch = workspaceNew, strings.TrimSpace(in.New.Branch)
		return nil
	}
	return fmt.Errorf("workspace must select exactly one of new, existing, or none")
}

func (w TaskWorkspace) MarshalJSON() ([]byte, error) {
	switch w.Mode {
	case workspaceExisting:
		return json.Marshal(map[string]string{"existing": w.Existing})
	case workspaceNew:
		if w.Branch != "" {
			return json.Marshal(map[string]any{"new": map[string]string{"branch": w.Branch}})
		}
	}
	return json.Marshal(w.Mode)
}

// TaskWorkspaceResolved is hub-owned. Before start Path is an engine preview;
// after creation it is the engine's answer. No client-supplied resolution is trusted.
type TaskWorkspaceResolved struct {
	Machine     string `json:"machine"`
	Path        string `json:"path"`
	Branch      string `json:"branch,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Created     bool   `json:"created,omitempty"`
	Reused      bool   `json:"reused,omitempty"`
	// Shared: the engine could not prepare a checkout for an inferred new workspace, so the task runs in a cwd
	// another active task also writes in, as every task did before workspaces existed.
	Shared bool `json:"shared,omitempty"`
}

func validWorkspacePreference(v string) bool {
	return v == preferExisting || v == preferNew
}

func (s *Service) taskWorkspacePreference(ctx context.Context) string {
	if s.settings(ctx).TaskWorkspacePreference == preferNew {
		return preferNew
	}
	return preferExisting
}

// resolveTaskWorkspace is metadata-only: it must never create a checkout on a
// waiting card. Re-resolving after an edit or a machine change asks that machine.
func (s *Service) resolveTaskWorkspace(ctx context.Context, t *PlanTask) error {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if t.ReservedTodoID == "" {
		t.ReservedTodoID = firstNonEmpty(t.ReuseTodo, t.TodoID, "todo_"+randomHex(8))
	}
	var sourceCwd string // the inferred cwd's workspace, and the other task that writes there
	var owner *Todo
	t.cwdStatus, t.cwdStatusOf = nil, "" // never an answer about a folder an earlier resolve asked of
	if t.Workspace == nil || t.WorkspaceInferred {
		mode := workspaceNone
		if !t.Readonly {
			var st proto.WorktreeStatus
			if err := s.call(ctx, t.Machine, "worktree.status", map[string]any{"cwd": t.Cwd}, &st); err != nil {
				return fmt.Errorf("%s: %w", i18n.T(s.lang(ctx), "assistant-tasks_workspace.infer", i18n.Args{"machine": t.Machine}), err)
			}
			// What the machine says of t.Cwd, for resolveTaskRepo: used only while t runs in that folder (or its checkout).
			t.cwdStatus, t.cwdStatusOf = &st, t.Machine+"\x00"+proto.NodeClean(t.Cwd)
			if st.Path != "" {
				mode = workspaceExisting
				var source proto.Workspace
				if err := s.call(ctx, t.Machine, "workspace.resolve", map[string]any{"path": t.Cwd, "terminal": true}, &source); err != nil {
					return fmt.Errorf("%s: %w", i18n.T(s.lang(ctx), "assistant-tasks_workspace.resolve", i18n.Args{"path": t.Cwd}), err)
				}
				if !source.Available || !proto.NodeAbs(source.Cwd) {
					return i18n.ErrEnglish("assistant-tasks_workspace.unavailable", i18n.Args{"path": t.Cwd})
				}
				sourceCwd, owner = source.Cwd, s.taskWorkspaceOwner(ctx, t.Machine, source.Cwd, firstNonEmpty(t.TodoID, t.ReuseTodo), false)
				if s.taskWorkspacePreference(ctx) == preferNew || owner != nil {
					mode = workspaceNew
				}
			}
		}
		t.Workspace = &TaskWorkspace{Mode: mode}
		if mode == workspaceExisting {
			t.Workspace.Existing = t.Cwd
		}
		t.WorkspaceInferred = true
	}
	res := &TaskWorkspaceResolved{Machine: t.Machine, Path: proto.NodeClean(t.Cwd)}
	switch t.Workspace.Mode {
	case workspaceNone:
	case workspaceExisting:
		selected := t.Workspace.Existing
		if selected == "" {
			return fmt.Errorf("workspace.existing requires a cwd or workspace_id")
		}
		if m, ok := s.machineByName(ctx, t.Machine); ok {
			selected = proto.ExpandHome(selected, m.Home)
		}
		if !proto.NodeAbs(selected) {
			var list proto.WorkspacesResponse
			if err := s.call(ctx, t.Machine, "workspaces.list", map[string]any{}, &list); err != nil {
				return fmt.Errorf("%s: %w", i18n.T(s.lang(ctx), "assistant-tasks_workspace.list", i18n.Args{"machine": t.Machine}), err)
			}
			var found bool
			for _, w := range list.Workspaces {
				qualified := qualifyWorkspace(t.Machine, w)
				if w.ID == selected || qualified.ID == selected {
					if !w.Available || w.Cwd == "" {
						return i18n.ErrEnglish("assistant-tasks_workspace.catalog_unavailable", i18n.Args{"workspace": selected, "machine": t.Machine})
					}
					selected, res.WorkspaceID, res.Branch = w.Cwd, w.ID, w.Branch
					found = true
					break
				}
			}
			if !found {
				return i18n.ErrEnglish("assistant-tasks_workspace.not_found", i18n.Args{"workspace": selected, "machine": t.Machine})
			}
		}
		var resolved proto.Workspace
		// terminal: the directory as the Agent's terminal opens it (a home reached through a link keeps its own
		// spelling, #10365); older nodes answer the physical path as before.
		if err := s.call(ctx, t.Machine, "workspace.resolve", map[string]any{"path": selected, "terminal": true}, &resolved); err != nil {
			return fmt.Errorf("%s: %w", i18n.T(s.lang(ctx), "assistant-tasks_workspace.resolve", i18n.Args{"path": selected}), err)
		}
		if !resolved.Available || !proto.NodeAbs(resolved.Cwd) {
			return i18n.ErrEnglish("assistant-tasks_workspace.unavailable", i18n.Args{"path": selected})
		}
		res.Path = proto.NodeClean(resolved.Cwd)
	case workspaceNew:
		res.Branch = firstNonEmpty(t.Workspace.Branch, "task/"+firstNonEmpty(t.TodoID, t.ReservedTodoID))
		var prep proto.WorktreePrepare
		if err := s.call(ctx, t.Machine, DeviceWorktreePrepare, map[string]any{"cwd": t.Cwd}, &prep); err != nil {
			if repositoryWithoutCommits(err) {
				// Nothing is committed, so no checkout can be made; the folder itself is the only workspace (#5548).
				if !t.WorkspaceInferred || sourceCwd == "" {
					return i18n.ErrEnglish("assistant-tasks_workspace.no_commits", i18n.Args{"machine": t.Machine, "path": t.Cwd})
				}
				if owner != nil {
					return i18n.ErrEnglish("assistant-tasks_workspace.busy_no_commits",
						i18n.Args{"id": owner.ID, "title": owner.Title, "machine": t.Machine, "path": sourceCwd})
				}
				res.Branch = ""
				t.Workspace, res.Path = &TaskWorkspace{Mode: workspaceExisting, Existing: t.Cwd}, proto.NodeClean(sourceCwd)
				break
			}
			if t.WorkspaceInferred && sourceCwd != "" && engineCannotPrepare(err) {
				// An engine without checkouts (Paseo, Cindy, or a host without checkout support) runs the task in its cwd,
				// as tasks did before workspaces: a busy cwd is shared rather than claimed, a free one is reused.
				res.Branch = ""
				if owner != nil {
					t.Workspace, res.Shared = &TaskWorkspace{Mode: workspaceNone}, true
				} else {
					t.Workspace, res.Path = &TaskWorkspace{Mode: workspaceExisting, Existing: t.Cwd}, proto.NodeClean(sourceCwd)
				}
				break
			}
			return fmt.Errorf("%s: %w", i18n.T(s.lang(ctx), "assistant-tasks_workspace.prepare", i18n.Args{"machine": t.Machine}), err)
		}
		if !proto.NodeAbs(prep.Path) {
			return i18n.ErrEnglish("assistant-tasks_workspace.engine_path")
		}
		res.Path = proto.NodeJoin(prep.Path, herdr.WorktreeDirName(res.Branch))
	default:
		return fmt.Errorf("unknown task workspace policy %q", t.Workspace.Mode)
	}
	t.WorkspaceResolved = res
	return nil
}

// engineCannotPrepare: the machine's engine has no checkouts to prepare (a runtime such as Paseo or Cindy, or a host
// without checkout support), as the node's error code says; any other failure, a missing host included, stays an error.
func engineCannotPrepare(err error) bool {
	var re *rpc.Error
	return errors.As(err, &re) && re.Code == proto.CodeWorktreeUnsupported
}

// repositoryWithoutCommits: the node refused a checkout because the cwd's repository has no commit yet.
func repositoryWithoutCommits(err error) bool {
	var re *rpc.Error
	return errors.As(err, &re) && re.Code == proto.CodeWorktreeNoCommits
}

// taskWorkspaceOwner checks durable active task links, and the gap between
// admission and linking. Callers that admit a writer hold workspaceMu as well.
// Directories compare by taskDirKey: on a Windows machine, case aside, and a
// linked home's two spellings as one. Branch-checkout reuse also checks owners
// below the checkout root.
func (s *Service) taskWorkspaceOwner(ctx context.Context, machine, cwd, except string, descendants bool) *Todo {
	key := s.taskDirKey(ctx, machine)
	machine, cwd = s.effectiveBoardMachine(machine), key(cwd)
	matches := func(dir string) bool {
		dir = key(dir)
		return dir == cwd || descendants && strings.HasPrefix(strings.ReplaceAll(dir, `\`, "/"),
			strings.TrimRight(strings.ReplaceAll(cwd, `\`, "/"), "/")+"/")
	}
	s.tasks.mu.Lock()
	owner := s.tasks.workspaceClaims[machine+"\x00"+cwd]
	if (owner == "" || owner == except) && descendants {
		for key, id := range s.tasks.workspaceClaims {
			m, dir, ok := strings.Cut(key, "\x00")
			if ok && m == machine && id != except && matches(dir) {
				owner = id
				break
			}
		}
	}
	s.tasks.mu.Unlock()
	if owner != "" && owner != except {
		if t, ok := s.todoNow(owner); ok {
			return &t
		}
	}
	for _, t := range s.todosOf(ctx, "*") {
		if t.ID == except || t.DoneAt != nil {
			continue
		}
		for _, a := range t.Agents {
			if a.ReleasedAt == nil && a.DismissedAt == nil && s.effectiveBoardMachine(a.MachineID) == machine && matches(a.Cwd) {
				return &t
			}
		}
	}
	return nil
}

// taskDirKey is how task folders on machine compare: proto.NodeKey's form, with a directory under the home as its
// links resolve (the node's HomeReal) written under the home as its node names it. A node names the folder an Agent
// starts in by the home's own spelling (workspace.resolve terminal, #10365), while a claim or an Agent's folder from
// before, from an older node or from a terminal host that reports it physically has the resolved one: either must
// find the same writer.
func (s *Service) taskDirKey(ctx context.Context, machine string) func(string) string {
	m, ok := s.machineByName(ctx, machine)
	if effective := s.effectiveBoardMachine(machine); !ok && effective != machine {
		m, _ = s.machineByName(ctx, effective)
	}
	return func(dir string) string { return homeDirKey(dir, m.Home, m.HomeReal) }
}

// homeDirKey is proto.NodeKey(dir), with dir under real (a home as its links resolve) written under home; the key
// alone when either home is unknown, they are one path, or are of different systems.
func homeDirKey(dir, home, real string) string {
	dir = proto.NodeKey(dir)
	if !proto.NodeAbs(home) || !proto.NodeAbs(real) || proto.WindowsPath(home) != proto.WindowsPath(real) {
		return dir
	}
	home, real = proto.NodeKey(home), proto.NodeKey(real)
	sep := "/"
	if proto.WindowsPath(real) {
		sep = `\`
	}
	under := strings.TrimSuffix(real, sep) + sep
	if home == real || strings.HasPrefix(home, under) { // a home inside its own target is no spelling to fold
		return dir
	}
	if dir == real {
		return home
	}
	if rest, ok := strings.CutPrefix(dir, under); ok {
		return strings.TrimSuffix(home, sep) + sep + rest
	}
	return dir
}

func (s *Service) taskWorkspaceBusy(ctx context.Context, machine, cwd, except string) error {
	if owner := s.taskWorkspaceOwner(ctx, machine, cwd, except, false); owner != nil {
		return i18n.ErrEnglish("assistant-tasks_workspace.busy", i18n.Args{"id": owner.ID, "title": owner.Title, "machine": machine, "path": cwd})
	}
	return nil
}

// admitTaskWorkspace resolves at the final placement, creates through the same
// engine as agent_start and claims the actual cwd before any session can start.
func (s *Service) admitTaskWorkspace(ctx context.Context, t *PlanTask) error {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return err
	}
	s.tasks.workspaceMu.Lock()
	defer s.tasks.workspaceMu.Unlock()
	prepared := t.WorkspaceResolved != nil && (t.WorkspaceResolved.Created || t.WorkspaceResolved.Reused)
	if prepared && t.WorkspaceResolved.Machine != t.Machine {
		return i18n.ErrEnglish("assistant-tasks_workspace.machine_local", i18n.Args{"from": t.WorkspaceResolved.Machine, "to": t.Machine})
	}
	if !prepared {
		if err := s.resolveTaskWorkspace(ctx, t); err != nil {
			return err
		}
		if t.Workspace.Mode == workspaceNew {
			wt, reused, err := s.newTaskWorktree(ctx, t)
			if err != nil {
				return fmt.Errorf("%s: %w", i18n.T(s.lang(ctx), "assistant-tasks_workspace.create"), err)
			}
			if !proto.NodeAbs(wt.Path) {
				return i18n.ErrEnglish("assistant-tasks_workspace.engine_path")
			}
			t.WorkspaceResolved = &TaskWorkspaceResolved{Machine: t.Machine, Path: proto.NodeClean(wt.Path),
				Branch: firstNonEmpty(wt.Branch, t.WorkspaceResolved.Branch), WorkspaceID: wt.WorkspaceID, Created: !reused, Reused: reused}
			// Once allocated it is machine-local, including automatic and explicit retries.
			t.MachinePolicy = machineFixed
		}
	}
	t.Cwd = t.WorkspaceResolved.Path
	// A shared fallback has mode none, but its resolved marker must still survive the start.
	keep := t.Workspace.Mode != workspaceNone || t.WorkspaceResolved.Shared
	// The repository is the one this folder is in, not the one the plan was made from: an existing workspace, or the
	// checkout the engine made, can be another repository (#8259). Saved below with the admitted cwd, and what
	// "start these directly" remembers. A task without a workspace runs where it was planned: nothing to ask again
	// unless it is to be remembered.
	repo := t.Repo
	if t.repoOf != t.Machine+"\x00"+proto.NodeClean(t.Cwd) && (keep || t.rememberKind || t.grantSchedule != "") {
		s.resolveTaskRepo(ctx, t)
	}
	if t.Workspace.Mode != workspaceNone {
		if err := s.taskWorkspaceBusy(ctx, t.Machine, t.Cwd, t.TodoID); err != nil {
			return err
		}
		if t.TodoID != "" {
			claim := s.effectiveBoardMachine(t.Machine) + "\x00" + s.taskDirKey(ctx, t.Machine)(t.Cwd)
			s.tasks.mu.Lock()
			if s.tasks.workspaceClaims == nil {
				s.tasks.workspaceClaims = map[string]string{}
			}
			s.tasks.workspaceClaims[claim] = t.TodoID
			s.tasks.mu.Unlock()
		}
	}
	if t.planID != "" && keep {
		// Persist the engine's answer before starting, so a retry never creates a second tree.
		return s.patchPlanTask(ctx, t.planID, t.planIndex, func(pt *PlanTask) {
			pt.Cwd, pt.Repo, pt.Workspace, pt.WorkspaceResolved = t.Cwd, t.Repo, t.Workspace, t.WorkspaceResolved
			pt.WorkspaceInferred, pt.ReservedTodoID, pt.MachinePolicy = t.WorkspaceInferred, t.ReservedTodoID, t.MachinePolicy
		})
	}
	if t.planID != "" && t.Repo != repo { // no workspace, same folder: only what its repository is came out otherwise
		return s.patchPlanTask(ctx, t.planID, t.planIndex, func(pt *PlanTask) { pt.Repo = t.Repo })
	}
	return nil
}

// newTaskWorktree makes the task's new checkout, or reuses the checkout of its branch the node finds clean and idle:
// an explicit {new: {branch}}'s (#8357), and this to-do's own task/<todo id>, which a failed start or an earlier Agent
// of the same to-do left in the checkouts folder (#10365). The own branch reuses only a checkout in that folder (never
// the main checkout or a folder of the user's own); a leftover it cannot reuse is refused with its path and the
// workspace {existing} that continues there, and any other failure to look falls back to making the checkout, as an
// older node that does not look gets.
func (s *Service) newTaskWorktree(ctx context.Context, t *PlanTask) (proto.WorktreeCreateResult, bool, error) {
	branch, own := t.Workspace.Branch, false
	if branch == "" {
		branch, own = t.WorkspaceResolved.Branch, true
	}
	if branch != "" {
		var prep proto.WorktreePrepare
		err := s.call(ctx, t.Machine, DeviceWorktreePrepare, map[string]any{"cwd": t.Cwd, "branch": branch}, &prep)
		switch at := prep.Existing; {
		case err != nil && !own:
			return proto.WorktreeCreateResult{}, false, err
		case err != nil:
			if refused := ownCheckoutRefusal(err); refused != nil {
				return proto.WorktreeCreateResult{}, false, refused
			}
		case at != nil:
			if !proto.NodeAbs(at.Path) || at.Branch != branch {
				return proto.WorktreeCreateResult{}, false, i18n.ErrEnglish("assistant-tasks_workspace.engine_checkout")
			}
			// in_checkouts from the node; an older one leaves it out, and its path then says it as written.
			if own && !at.InCheckouts && !nodePathWithin(at.Path, prep.Path) {
				return proto.WorktreeCreateResult{}, false, i18n.ErrEnglish("assistant-tasks_workspace.own_checkout_elsewhere",
					i18n.Args{"branch": branch, "path": at.Path})
			}
			if owner := s.taskWorkspaceOwner(ctx, t.Machine, at.Path, t.TodoID, true); owner != nil {
				return proto.WorktreeCreateResult{}, false, i18n.ErrEnglish("assistant-tasks_workspace.busy",
					i18n.Args{"id": owner.ID, "title": owner.Title, "machine": t.Machine, "path": at.Path})
			}
			return proto.WorktreeCreateResult{Path: at.Path, Branch: at.Branch}, true, nil
		}
	}
	wt, err := s.newWorktreeForTask(ctx, t.Machine, t.Cwd, t.WorkspaceResolved.Branch, s.taskCheckoutOnly(ctx, *t))
	if own && err != nil {
		if path, taken := pathTaken(err); taken {
			return wt, false, taskCheckoutRefusal{i18n.ErrEnglish("assistant-tasks_workspace.own_path_taken",
				i18n.Args{"path": path}).(*i18n.Message), err}
		}
	}
	return wt, false, err
}

// ownCheckoutRefusal is the hub's sentence for the node's coded refusal of the to-do's own leftover checkout (dirty,
// in use, changed or unverifiable), naming its path and what to do next; nil for any other failure.
func ownCheckoutRefusal(err error) error {
	var re *rpc.Error
	if !errors.As(err, &re) {
		return nil
	}
	var data struct{ Branch, Path, Owner string }
	_ = json.Unmarshal(re.Data, &data)
	args := i18n.Args{"branch": data.Branch, "path": data.Path, "owner": data.Owner}
	var key string
	switch re.Code {
	case proto.CodeCheckoutDirty:
		key = "own_checkout_dirty"
	case proto.CodeCheckoutInUse:
		key = "own_checkout_in_use"
	case proto.CodeCheckoutChanged, proto.CodeCheckoutUnverified:
		key = "own_checkout_unverified"
	default:
		return nil
	}
	return taskCheckoutRefusal{i18n.ErrEnglish("assistant-tasks_workspace."+key, args).(*i18n.Message), err}
}

// pathTaken: the node refused to make the checkout because its folder already holds something (code
// worktree_path_taken), and where.
func pathTaken(err error) (string, bool) {
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != proto.CodeWorktreePathTaken {
		return "", false
	}
	var data struct{ Path string }
	_ = json.Unmarshal(re.Data, &data)
	return data.Path, data.Path != ""
}

// taskCheckoutRefusal reads as the hub's sentence and keeps the node's coded refusal under it, so the task's error
// still carries that code (errors.As) for the clients that map codes first.
type taskCheckoutRefusal struct {
	*i18n.Message
	node error
}

func (e taskCheckoutRefusal) Unwrap() []error { return []error{e.Message, e.node} }

// nodePathWithin: the node's path p is dir or lies under it, compared as the node's paths are (proto.NodeKey).
func nodePathWithin(p, dir string) bool {
	if dir == "" {
		return false
	}
	p, dir = strings.ReplaceAll(proto.NodeKey(p), `\`, "/"), strings.TrimRight(strings.ReplaceAll(proto.NodeKey(dir), `\`, "/"), "/")
	return p == dir || strings.HasPrefix(p, dir+"/")
}

func (s *Service) releaseTaskWorkspace(todoID string) {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	for key, owner := range s.tasks.workspaceClaims {
		if owner == todoID {
			delete(s.tasks.workspaceClaims, key)
		}
	}
}

// Preview uses the saved task as its trusted base, applying only card-editable
// fields. It neither changes the saved plan nor allocates a host workspace.
func (s *Service) previewPlanWorkspaces(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		apierr.Write(w, http.StatusForbidden, fmt.Errorf("read-only role cannot edit task workspaces"))
		return
	}
	p, err := s.loadPlan(r.Context(), r.PathValue("id"))
	if err != nil {
		planErr(w, err)
		return
	}
	if p.State != planOpen {
		planErr(w, errPlanNotOpen)
		return
	}
	var in struct {
		Tasks []PlanTask `json:"tasks"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	for i := range p.Tasks {
		if i < len(in.Tasks) {
			applyPlanEdit(&p.Tasks[i], in.Tasks[i])
		}
		if !s.placeTask(r.Context(), &p.Tasks[i]) {
			apierr.Write(w, http.StatusBadRequest, fmt.Errorf("unknown machine %s", p.Tasks[i].Machine))
			return
		}
		if err := s.resolveTaskWorkspace(r.Context(), &p.Tasks[i]); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
	}
	apierr.WriteJSON(w, map[string]any{"plan": p})
}
