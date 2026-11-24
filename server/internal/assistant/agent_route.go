package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// New agent goes under a leader (docs/design/proposals/owner-at-a-glance.md "New chat opens a direction; New agent goes
// under one", #3608). POST /api/v1/assistant/route/agent suggests a leader for a new agent. Once the agent starts,
// POST /api/v1/assistant/agents/{mid}/{sid}/supervisor makes it a supervised task of that leader. The leader gets
// its supervision messages (digestTo) and reviews it. Which leader:
//
//  1. the knowledge-base project that owns the agent's directory, when a current leader is filed under it;
//  2. else the prompt matched against the leaders' names and projects by assistant.route within routeTimeout;
//  3. else Persona, the main conversation (no leaders, no match, the model slow or failing).

// Why a leader was suggested.
const (
	agentRouteProject = "project"
	agentRouteModel   = "model"
	agentRouteDefault = "default"
)

// agentRouteAnswer is the model's answer (prompts assistant/route_agent).
type agentRouteAnswer struct {
	Leader string `json:"leader"`
}

// agentRoute is where a new agent goes: Supervisor a conversation id (the main conversation's when Main), By why.
type agentRoute struct {
	Supervisor string `json:"supervisor"`
	Main       bool   `json:"main,omitempty"`
	Title      string `json:"title,omitempty"` // the leader's name as the apps show it ("" for the main conversation)
	By         string `json:"by"`
}

// decideAgentRoute is the rule table for a new agent: the leaders filed under a project that owns its directory (in
// dirProjects, by slug) win; else the leader the model named (an id it made up counts for nothing); else main.
func decideAgentRoute(main string, leaders []routeLeader, dirProjects []string, a agentRouteAnswer, err error) agentRoute {
	for _, l := range leaders {
		if l.Project != "" && slices.Contains(dirProjects, l.Project) {
			return agentRoute{Supervisor: l.ID, Title: l.Title, By: agentRouteProject}
		}
	}
	if err == nil {
		if i := slices.IndexFunc(leaders, func(l routeLeader) bool { return l.ID == strings.TrimSpace(a.Leader) }); i >= 0 {
			return agentRoute{Supervisor: leaders[i].ID, Title: leaders[i].Title, By: agentRouteModel}
		}
	}
	return agentRoute{Supervisor: main, Main: true, By: agentRouteDefault}
}

// routeAgent suggests the conversation a new agent on machine in cwd, started with prompt, goes under.
func (s *Service) routeAgent(ctx context.Context, machine, cwd, prompt string) agentRoute {
	main := s.settings(ctx).Main
	leaders := s.routeLeaders(ctx)
	if len(leaders) == 0 {
		return agentRoute{Supervisor: main, Main: true, By: agentRouteDefault}
	}
	var slugs []string
	if e, err := s.kbGet(ctx); err == nil && e != nil && cwd != "" {
		for _, p := range e.ProjectsForDir(machine, cwd) {
			slugs = append(slugs, p.Slug)
		}
	}
	if r := decideAgentRoute(main, leaders, slugs, agentRouteAnswer{}, errors.New("not asked")); r.By == agentRouteProject {
		return r
	}
	prompt = strings.TrimSpace(contextBlock.ReplaceAllString(prompt, ""))
	if prompt == "" {
		return decideAgentRoute(main, leaders, slugs, agentRouteAnswer{}, errors.New("no prompt"))
	}
	in := prompts.RouteInput{Sentence: cutRunes(prompt, 1500)}
	for _, l := range leaders {
		in.Leaders = append(in.Leaders, prompts.RouteLeader{ID: l.ID, Title: l.Title, Project: l.Project})
	}
	var a agentRouteAnswer
	mctx, cancel := context.WithTimeout(ctx, routeTimeout)
	err := s.askRouteModel(mctx, prompts.RenderRouteAgent(), prompts.RenderRouteInput(in), &a)
	cancel()
	if err != nil {
		s.logf("route agent on %s: %v (Persona)", machine, err)
	}
	return decideAgentRoute(main, leaders, slugs, a, err)
}

// putAgentUnder puts agent sid on machine mid under conversation lead (the main conversation or a current leader; ""
// is the main conversation): an agent of no task becomes a supervised task of lead, with its card in lead's
// conversation; one already on a task the hub tracks moves to lead (its signals follow from the next one); one on the
// owner's own to-do stays theirs. made says a task was made.
func (s *Service) putAgentUnder(ctx context.Context, mid, sid, lead, title string) (Todo, bool, error) {
	return s.assignAgentUnder(ctx, mid, sid, lead, title, true, "")
}

// assignAgentUnder is the App's move path. agent_transfer disables adoption so a task that
// ended before the lookup cannot accidentally become a new task, and sets requireSupervisor
// for a direction lead so it can hand off only a task it currently supervises.
func (s *Service) assignAgentUnder(ctx context.Context, mid, sid, lead, title string, allowAdoption bool, requireSupervisor string) (Todo, bool, error) {
	cfg := s.settings(ctx)
	if lead == "" {
		lead = cfg.Main
	}
	if lead == "" {
		return Todo{}, false, errors.New("there is no main conversation to supervise it")
	}
	if err := s.transferTarget(ctx, cfg, lead); err != nil {
		return Todo{}, false, err
	}
	am, ok := s.device(ctx, mid)
	if !ok {
		return Todo{}, false, fmt.Errorf("%w: no machine %q", errAgentNotFound, mid)
	}
	c, ok := s.session(ctx, am.ID, sid)
	if !ok {
		return Todo{}, false, fmt.Errorf("%w: no session %s on %s", errAgentNotFound, sid, am.Name)
	}
	if c.Assistant {
		return Todo{}, false, errors.New("that is an assistant conversation, not a coding agent")
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return Todo{}, false, err
	}
	// Serialize a move with digest delivery and queued supervision reconciliation.
	s.supervise.routingMu.Lock()
	defer s.supervise.routingMu.Unlock()
	if t, ok := s.openTodoOfAgent(am.ID, c.SessionID); ok {
		if t.Origin == "" {
			return Todo{}, false, fmt.Errorf("%s is on the owner's own to-do %q: it stays theirs", agentLabel(c), t.Title)
		}
		moved, blockedBy, err := s.transferTaskLocked(ctx, t, lead, requireSupervisor)
		if err != nil {
			return Todo{}, false, err
		}
		if blockedBy != "" {
			return Todo{}, false, s.transferRefused(ctx, c, t, blockedBy)
		}
		return moved, false, nil
	}
	if !allowAdoption {
		return Todo{}, false, errors.New("that Agent has no open task to transfer: use agent_supervise to adopt work first")
	}
	notes := i18n.T(s.lang(ctx), "assistant-tasks.adopt_notes", i18n.Args{"agent": agentLabel(c), "machine": am.Name, "cwd": c.Cwd})
	todo, made, err := s.adoptAgent(ctx, lead, am, c, title, "", notes, nil, 0, false)
	if err != nil {
		return Todo{}, false, err
	}
	if made {
		s.taskCard(ctx, lead, todo.ID, wakeActive)
	}
	return todo, made, nil
}

// transferTarget refuses a supervisor a task may not be moved to: one that is neither the main conversation nor a
// current lead, a lead validateSupervisorLead turns down (a reporting-only direction), or a lead whose runtime cannot
// receive supervision policy. The main conversation always takes one.
func (s *Service) transferTarget(ctx context.Context, cfg Settings, lead string) error {
	if lead == cfg.Main {
		return nil
	}
	if err := s.validateSupervisorLead(ctx, lead); err != nil {
		return err
	}
	if s.runtimeWithoutSupervisionPolicy(ctx, cfg.MachineID, lead) {
		return errors.New("the target lead's runtime cannot receive supervision policy")
	}
	return nil
}

// transferTaskLocked is the move itself, the same for Move to, agent_transfer and an ended direction's hand-back
// (handBackDirectionTasks): open task t becomes an explicit assignment of lead (supervisor_source "explicit", kept
// across restarts), its card follows it (the old conversation keeps its history), and its supervisor-held cards,
// untried queued signals and pending outbox go with it (transferPendingAsks). With requireSupervisor set, t moves only
// while that conversation supervises it; blockedBy then names the one that does. routingMu is held by the caller.
func (s *Service) transferTaskLocked(ctx context.Context, t Todo, lead, requireSupervisor string) (
	moved Todo, blockedBy string, err error) {
	v, changed, err := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
		if requireSupervisor != "" {
			cur := cmp.Or(v.Supervisor, t.Origin)
			if cur != requireSupervisor {
				blockedBy = cur
				return false
			}
		}
		if v.Supervisor == lead && v.SupervisorSource == "explicit" {
			return false
		}
		v.Supervisor, v.SupervisorSource = lead, "explicit"
		return true
	})
	if err != nil {
		return Todo{}, "", err
	}
	if blockedBy != "" {
		return t, blockedBy, nil
	}
	if changed {
		s.taskCard(ctx, lead, t.ID, wakeActive)
	}
	t.Supervision = &v
	if err := s.transferPendingAsks(ctx, t); err != nil {
		return Todo{}, "", err
	}
	return t, "", nil
}

// transferRefused explains why a direction lead may not move a task supervised by cur, naming cur and who can move it.
func (s *Service) transferRefused(ctx context.Context, c proto.Session, t Todo, cur string) error {
	switch {
	case s.isMain(ctx, cur):
		return fmt.Errorf("%s works for task %q (%s), supervised by the main conversation (%s): "+
			"only the main conversation or the owner can move it", agentLabel(c), t.Title, t.ID, cur)
	case s.isLeader(ctx, cur):
		return fmt.Errorf("%s works for task %q (%s), supervised by the direction lead %s: "+
			"only that supervisor, the main conversation, or the owner can move it", agentLabel(c), t.Title, t.ID, cur)
	default:
		if _, _, ok := parseLeaderRef(cur); ok {
			return fmt.Errorf("%s works for task %q (%s), supervised by task leader %s: "+
				"only the main conversation or the owner can move it", agentLabel(c), t.Title, t.ID, cur)
		}
		return fmt.Errorf("%s works for task %q (%s), supervised by conversation %s: "+
			"only the main conversation or the owner can move it", agentLabel(c), t.Title, t.ID, cur)
	}
}

// toolAgentTransfer exposes Move to without sending or interrupting the running Agent.
// Main may move any open supervised task; a direction lead may hand off only one it supervises.
func (s *Service) toolAgentTransfer(ctx context.Context, self string, args json.RawMessage) (string, error) {
	main := s.isMain(ctx, self)
	if !main && !s.isLeader(ctx, self) {
		return "", errors.New("only the main conversation or a direction's lead transfers tasks between supervisors")
	}
	var a struct {
		Machine    string `json:"machine"`
		SessionID  string `json:"session_id"`
		Supervisor string `json:"supervisor"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	lead := strings.TrimSpace(a.Supervisor)
	if lead == "" {
		return "", errors.New("supervisor is required: use a current direction or the main conversation id from conversations_list")
	}
	am, c, err := s.findAgent(ctx, a.Machine, a.SessionID)
	if err != nil {
		return "", err
	}
	requireSupervisor := ""
	if !main {
		requireSupervisor = self
	}
	todo, _, err := s.assignAgentUnder(ctx, am.ID, c.SessionID, lead, "", false, requireSupervisor)
	if err != nil {
		return "", err
	}
	undo := "Use agent_transfer with the previous supervisor to move it back."
	if !main && lead != self {
		undo = "Only the new supervisor, the main conversation, or the owner can move it back."
	}
	return fmt.Sprintf("Task %s (%q) is now supervised by conversation %s; its existing card is there. "+
		"The same Agent continues: nothing was sent, started or interrupted. %s",
		todo.ID, todo.Title, lead, undo), nil
}

var errAgentNotFound = errors.New("not found")

// agentRouteRoutes: the New agent sheet's two calls (docs/protocol/protocol.md "REST (App → hub)").
func (s *Service) agentRouteRoutes() {
	s.handle("POST /api/v1/assistant/route/agent", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) { // it costs a model call: only a device that may start the agent asks
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot start agents"))
			return
		}
		var in struct {
			MachineID string `json:"machine_id"`
			Cwd       string `json:"cwd"`
			Prompt    string `json:"prompt"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), routeTimeout+3*time.Second)
		defer cancel()
		apierr.WriteJSON(w, s.routeAgent(ctx, in.MachineID, in.Cwd, in.Prompt))
	})
	s.handle("POST /api/v1/assistant/agents/{mid}/{sid}/supervisor", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot hand out tasks"))
			return
		}
		var in struct {
			Supervisor string `json:"supervisor"` // a leader conversation; "" the main conversation
			Title      string `json:"title"`
		}
		if r.ContentLength != 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
				apierr.Write(w, http.StatusBadRequest, err)
				return
			}
		}
		todo, made, err := s.putAgentUnder(r.Context(), r.PathValue("mid"), r.PathValue("sid"), strings.TrimSpace(in.Supervisor), in.Title)
		if err != nil {
			status := http.StatusConflict
			switch {
			case errors.Is(err, errAgentNotFound):
				status = http.StatusNotFound
			case errors.Is(err, errDirectionUnavailable):
				status = http.StatusServiceUnavailable
			}
			apierr.Write(w, status, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"todo": todo, "made": made})
	})
}
