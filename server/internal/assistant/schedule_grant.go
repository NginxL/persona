package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A scheduled task's own start grant (#8045, docs/assistant/unified-entry.md "A scheduled task's own start grant").
//
// A round the hub starts for a scheduled task has no message from the owner, so a writable task it hands out with
// task_plan used to wait on its card however much the owner wanted the schedule to do that work. The owner can now say,
// on the card of one of that schedule's own rounds, "let this scheduled task start these by itself": the kind of each
// writable task (trustKey: machine + repository or folder + edit, as the trust list's) is added to the schedule's
// StartGrant. Everything else keeps the card:
//
//   - the grant is the schedule's only, and only the owner adds to it (Start on the card with schedule_grant, which
//     no tool call and no app body can stand in for); the schedule's page shows it and removes it;
//   - it holds while the schedule is what it was given under (grantBasis: its prompt, where its runs go, its space
//     and whether it is given the briefing's facts or the north star's report); any change drops it;
//   - a schedule that may read untrusted outside content never offers or uses it (grantEligible): no facts, report or
//     saved state note, runs in a conversation of its own, not the health space;
//   - it is used only in a clean round (cleanRound): one the hub started in a new conversation of its own, under an id
//     it chose, whose context leaves out the live snapshot (Lark messages, Inbox items, agent lines: context.go), and
//     whose whole conversation is that one prompt and the agent's own work with Persona's own tools that read nothing
//     from outside (grantSafeTools). What the hub cannot read or tell keeps the card.

// StartGrant is what the owner let one scheduled task's rounds start without the card.
type StartGrant struct {
	// Kinds are trust keys (trustKey): machine|repository or folder|edit. One covers its folder tree as a remembered
	// kind does (trustCovers).
	Kinds []string `json:"kinds"`
	// Basis is the schedule's grantBasis when the owner gave it: another one drops it.
	Basis     string    `json:"basis"`
	GrantedAt time.Time `json:"granted_at"`
}

// grantBasis names what a grant is given under: t's prompt, where its runs go, its space and what the hub adds to its
// prompt. A plan keeps its round's (PlanTask.ScheduleBasis), so a Start after the schedule changed grants nothing.
func (t Task) grantBasis(binding ...ModelBinding) string {
	parts := []string{strings.TrimSpace(t.Prompt), t.MachineID, t.SessionID, t.Space,
		strconv.FormatBool(t.Facts), strconv.FormatBool(t.Report)}
	// An owner can turn a literal example into a selected reference without changing its bytes.
	if t.PromptSelections {
		parts = append(parts, "prompt_selections")
	}
	// Keep existing grants valid when there are no execution overrides, but bind a grant to an explicit target.
	if t.ExecutionMachineID != "" || t.Cwd != "" {
		parts = append(parts, t.ExecutionMachineID, t.Cwd)
	}
	if len(binding) > 0 {
		raw, _ := json.Marshal(binding[0])
		parts = append(parts, "model", string(raw))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// grantEligible: t's rounds can use a grant at all (and its cards offer one): nothing the hub adds to their prompt
// comes from outside (the briefing's facts list Inbox matters, the report's lines, a state note an earlier run
// wrote), they run in a conversation of their own, outside the health space.
func (t Task) grantEligible() bool {
	return !t.PromptSelections && !t.Facts && !t.Report && t.SessionID == "" && t.Space == "" && (t.StateNote == nil || strings.TrimSpace(t.StateNote.Text) == "")
}

// liveGrant is t's grant while it still applies: given under t as it is now, with a kind in it.
func (t Task) liveGrant(basis ...string) *StartGrant {
	want := t.grantBasis()
	if len(basis) > 0 {
		want = basis[0]
	}
	g := t.StartGrant
	if g == nil || len(g.Kinds) == 0 || g.Basis != want {
		return nil
	}
	return g
}

// Revoke grants on persisted task edits using only the task rows. The task editor checks model changes before
// its atomic save. Authorization still checks the authoritative model and round at use.
func dropEditedTaskGrants(previous, tasks []Task) {
	bases := make(map[string]string)
	for _, task := range previous {
		if task.StartGrant != nil {
			bases[task.ID] = task.grantBasis()
		}
	}
	for i := range tasks {
		if old, ok := bases[tasks[i].ID]; ok && tasks[i].StartGrant != nil && old != tasks[i].grantBasis() {
			tasks[i].StartGrant = nil
		}
	}
}

// Read the authoritative model record for granting or consuming authorization. Untouched tasks retain their
// legacy hash and inheritance; configured tasks bind to the effective execution configuration.
// Unlike resolveModel(task.ID), this accepts the already-locked task without reacquiring the schedule lock.
func (s *Service) taskGrantBasis(ctx context.Context, t Task) (string, error) {
	record, err := s.modelRecord(ctx, "task."+t.ID)
	if err != nil {
		return "", err
	}
	if record.Binding == (ModelBinding{}) {
		return t.grantBasis(), nil
	}
	base, err := s.taskModelBaseline(ctx, s.taskMachine(ctx, t))
	if err != nil {
		return "", err
	}
	return t.grantBasis(mergeModel(base, record.Binding)), nil
}

type scheduleGrantKey struct{}

// scheduleGranting marks the owner's Start (ownerStart) as also letting the plan's scheduled task start these kinds by
// itself: the card's option (POST …/start schedule_grant). scheduleGrants reads the mark in startPlan.
func scheduleGranting(ctx context.Context) context.Context {
	return context.WithValue(ctx, scheduleGrantKey{}, true)
}

func scheduleGrants(ctx context.Context) bool {
	on, _ := ctx.Value(scheduleGrantKey{}).(bool)
	return on
}

// grantScheduleKind adds t's kind (trustKey, now that admission fixed the folder that runs) to schedule id's grant.
// Only startPlan's owner Start reaches here. A schedule that is gone or in the health space gets nothing.
func (s *Service) grantScheduleKind(ctx context.Context, id string, t PlanTask) {
	if t.ScheduleBasis == "" {
		return
	}
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		s.logf("schedule grant: task %s: %v", id, err)
		return
	}
	i := sc.indexLocked(id)
	if i < 0 || !sc.tasks[i].grantEligible() {
		sc.mu.Unlock() // gone, or no longer the schedule whose round the owner saw: nothing granted
		return
	}
	basis, err := s.taskGrantBasis(ctx, sc.tasks[i])
	if err != nil || basis != t.ScheduleBasis {
		sc.mu.Unlock()
		return
	}
	tasks := slices.Clone(sc.tasks)
	g := tasks[i].liveGrant(basis)
	if g == nil {
		g = &StartGrant{Basis: basis, GrantedAt: time.Now().UTC()}
	} else {
		cp := *g
		cp.Kinds = slices.Clone(g.Kinds)
		g = &cp
	}
	key := trustKey(t)
	if !slices.Contains(g.Kinds, key) {
		g.Kinds = append(g.Kinds, key)
		sort.Strings(g.Kinds)
	}
	tasks[i].StartGrant = g
	err = s.saveLocked(sc, tasks)
	sc.mu.Unlock()
	if err != nil {
		s.logf("schedule grant: task %s: %v", id, err)
		return
	}
	s.broadcastSchedule()
}

// revokeScheduleGrant removes kind from schedule id's grant, or the whole grant when kind is "". False when the task
// is gone.
func (s *Service) revokeScheduleGrant(ctx context.Context, id, kind string) (bool, error) {
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return false, err
	}
	i := sc.indexLocked(id)
	if i < 0 {
		sc.mu.Unlock()
		return false, nil
	}
	tasks := slices.Clone(sc.tasks)
	if g := tasks[i].StartGrant; g != nil && kind != "" {
		cp := *g
		cp.Kinds = slices.DeleteFunc(slices.Clone(g.Kinds), func(k string) bool { return k == kind })
		tasks[i].StartGrant = &cp
		if len(cp.Kinds) == 0 {
			tasks[i].StartGrant = nil
		}
	} else {
		tasks[i].StartGrant = nil
	}
	err = s.saveLocked(sc, tasks)
	sc.mu.Unlock()
	if err != nil {
		return true, err
	}
	s.broadcastSchedule()
	return true, nil
}

func (s *Service) scheduleGrantRoutes() {
	// The schedule's page removes one kind (body {"kind": "machine|folder|edit"}) or the whole grant (no body). There is
	// no route that adds one: only the owner's Start on a round's card does (startPlan).
	s.handle("DELETE /api/v1/assistant/tasks/{id}/start-grant", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change scheduled tasks"))
			return
		}
		var in struct {
			Kind string `json:"kind"`
		}
		if r.ContentLength != 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
				apierr.Write(w, http.StatusBadRequest, err)
				return
			}
		}
		ok, err := s.revokeScheduleGrant(r.Context(), r.PathValue("id"), strings.TrimSpace(in.Kind))
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		if !ok {
			apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-schedule.gone"))
			return
		}
		v, _ := s.taskView(i18n.With(r.Context(), s.requestLang(r)), r.PathValue("id"))
		apierr.WriteJSON(w, map[string]any{"task": v})
	})
}

// ---- the rounds the hub started ----

// schedRound is a round the hub sent for scheduled task task into a conversation: what task_plan from that
// conversation checks its turn against (scheduledRoundOf). In memory only: after a hub restart a round's plan asks.
type schedRound struct {
	task  string
	at    time.Time
	basis string
}

// roundTTL bounds how long after it was sent a round's turn may still hand out work under its schedule's grant.
const roundTTL = 2 * time.Hour

func roundKey(machine, session string) string { return machine + "\x00" + session }

// noteRound records that the hub is sending a round of task into session on machine.
func (s *Service) noteRound(machine, session, task, basis string, at time.Time) {
	if machine == "" || session == "" {
		return
	}
	s.sched.mu.Lock()
	defer s.sched.mu.Unlock()
	if s.sched.rounds == nil {
		s.sched.rounds = map[string][]schedRound{}
	}
	k := roundKey(machine, session)
	// Its own earlier round there is over by now (one round of a task at a time, sched.begin): this one replaces it.
	list := slices.DeleteFunc(s.sched.rounds[k], func(r schedRound) bool { return r.task == task || at.Sub(r.at) > roundTTL })
	s.sched.rounds[k] = append(list, schedRound{task: task, at: at, basis: basis})
}

// endRound forgets task's round in session once its turn ended (watchScheduledRun), and its clean mark.
func (s *Service) endRound(machine, session, task string) {
	s.sched.mu.Lock()
	defer s.sched.mu.Unlock()
	if s.sched.clean[session] == task {
		delete(s.sched.clean, session)
	}
	k := roundKey(machine, session)
	list := slices.DeleteFunc(s.sched.rounds[k], func(r schedRound) bool { return r.task == task })
	if len(list) == 0 {
		delete(s.sched.rounds, k)
		return
	}
	s.sched.rounds[k] = list
}

// noteCleanRound marks session, a conversation the hub is about to start under that id for a round of task, as a
// clean round's: its context leaves out the live snapshot (Context) and its plans may use task's grant.
func (s *Service) noteCleanRound(session, task string) {
	s.sched.mu.Lock()
	defer s.sched.mu.Unlock()
	if s.sched.clean == nil {
		s.sched.clean = map[string]string{}
	}
	s.sched.clean[session] = task
}

// cleanRoundSession: session is a clean round's conversation while its round is under way (noteCleanRound).
func (s *Service) cleanRoundSession(session string) bool {
	if session == "" {
		return false
	}
	s.sched.mu.Lock()
	defer s.sched.mu.Unlock()
	_, ok := s.sched.clean[session]
	return ok
}

// roundUnderWay: the hub has a scheduled round under way in session on machine.
func (s *Service) roundUnderWay(machine, session string) bool {
	s.sched.mu.Lock()
	defer s.sched.mu.Unlock()
	return len(s.sched.rounds[roundKey(machine, session)]) > 0
}

// liveRound is the one round the hub has under way in session, false when there is none or more than one (two tasks'
// rounds into one conversation: which turn is whose cannot be told, so neither's grant applies).
func (s *Service) liveRound(machine, session string, now time.Time) (schedRound, bool) {
	s.sched.mu.Lock()
	defer s.sched.mu.Unlock()
	var live []schedRound
	for _, r := range s.sched.rounds[roundKey(machine, session)] {
		if now.Sub(r.at) <= roundTTL {
			live = append(live, r)
		}
	}
	if len(live) != 1 {
		return schedRound{}, false
	}
	return live[0], true
}

// scheduledRoundOf is the scheduled task whose round the turn task_plan is called in is, from what the hub knows
// (liveRound) and what the conversation says (its latest turn began with that task's own scheduled prompt, sent after
// the round began, and no other message came into it). "" for every other turn.
func (s *Service) scheduledRoundOf(ctx context.Context, machine, session string) (task Task, ok bool) {
	r, ok := s.liveRound(machine, session, time.Now())
	if !ok {
		return Task{}, false
	}
	t, ok := s.scheduledTask(ctx, r.task)
	if !ok {
		return Task{}, false
	}
	basis, err := s.taskGrantBasis(ctx, t)
	if err != nil || basis != r.basis {
		return Task{}, false
	}
	t.runGrantBasis = r.basis
	items, read := s.lastTurnItems(ctx, machine, session)
	if !read {
		return Task{}, false
	}
	_, turn := splitLastTurn(items)
	if !roundTurn(turn, r.at, t.label()) {
		return Task{}, false
	}
	return t, true
}

// roundEarly is how much earlier than the hub's note of a round its prompt's own time may be (clocks of the hub and
// a machine apart).
const roundEarly = 2 * time.Minute

// roundTurn: turn (its items, prompt first) began with the scheduled prompt of the task called label (the
// scheduler's message source, which an app's or another conversation's message never carries:
// prompts.MessageSourceText, ProtectUserMessage) written no earlier than the round, and nothing else came into it from
// outside the agent's own work.
func roundTurn(turn []proto.Item, at time.Time, label string) bool {
	if len(turn) == 0 {
		return false
	}
	prompt := turn[0]
	if prompt.Role != "user" || prompt.Kind != proto.KindText || prompt.Queued || !scheduledPrompt(prompt, label) {
		return false
	}
	if prompt.TS.IsZero() || prompt.TS.Before(at.Add(-roundEarly)) {
		return false
	}
	for _, it := range turn[1:] {
		if it.Role == "user" && it.Kind != proto.KindToolResult {
			return false // a message that came in while it worked, a picture, a notice injected into the turn
		}
	}
	return true
}

// scheduledPrompt: it is the run prompt of the scheduled task called label (patrol or follow-up), by its message
// source.
func scheduledPrompt(it proto.Item, label string) bool {
	src := prompts.ItemSource(it)
	return src != nil && (src.Kind == proto.SourcePatrol || src.Kind == proto.SourceFollowup) && src.Title == label
}

// ---- outside content ----

// grantSafeTools are Persona's own tools a clean round may call and still use its schedule's grant: each reads nothing
// written outside Persona (no mail, Inbox, web page, chat, agent transcript or file) and only says where work can run
// or plans it. Any other tool, built-in or not, keeps the card.
var grantSafeTools = map[string]bool{
	"machines_list": true, "repositories_list": true, "repository_get": true, "todo_repositories_get": true, "notify_user": true,
}

// grantSafeBuiltins are the agent's own tools that read nothing: Claude Code's to-do list and its loading of a
// deferred tool's schema (it loads task_plan's).
var grantSafeBuiltins = map[string]bool{"TodoWrite": true, "ToolSearch": true}

func grantSafeTool(name string) bool {
	if grantSafeBuiltins[name] {
		return true
	}
	name = compat.CanonicalMCPToolName(name)
	rest, ok := strings.CutPrefix(name, "mcp__")
	i := strings.LastIndex(rest, "__")
	if !ok || i <= 0 || !compat.IsMCPServer(strings.TrimPrefix(rest[:i], "plugin_")) {
		return false
	}
	return grantSafeTools[compat.Tool(rest[i+2:])]
}

// grantSafeAfterPrompt: an item of a clean round after its prompt carries nothing from outside: the agent's own text,
// its safe tools and their results, the transcript's own notices.
func grantSafeAfterPrompt(it proto.Item) bool {
	switch it.Role {
	case "assistant":
		switch it.Kind {
		case proto.KindText, proto.KindThinking:
			return true
		case proto.KindToolUse:
			return grantSafeTool(it.ToolName)
		}
		return false
	case "user":
		return it.Kind == proto.KindToolResult // the result of a tool use checked above
	}
	return it.Kind == proto.KindNotice
}

// cleanRound: the round of t in session is one its grant may start work from. The hub started session for it under
// the id it chose, with no live snapshot (cleanRoundSession), and the whole conversation, as its machine reads it now,
// is that round's prompt and the agent's own safe work. Anything the hub cannot read or tell is not clean.
func (s *Service) cleanRound(ctx context.Context, t Task, machine, session string) bool {
	if !t.grantEligible() || !s.cleanRoundSession(session) {
		return false
	}
	s.sched.mu.Lock()
	mine := s.sched.clean[session] == t.ID
	s.sched.mu.Unlock()
	if !mine {
		return false
	}
	var res struct {
		Items   []proto.Item `json:"items"`
		HasMore *bool        `json:"has_more"`
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.call(cctx, machine, DeviceSessionItems, map[string]any{"session_id": session, "turns": 10}, &res); err != nil {
		return false
	}
	if res.HasMore == nil || *res.HasMore || len(res.Items) == 0 {
		return false // more than the page: not one round's alone, or a node that does not say
	}
	first := slices.IndexFunc(res.Items, func(it proto.Item) bool { return it.Role == "user" })
	if first < 0 || !scheduledPrompt(res.Items[first], t.label()) || res.Items[first].Queued {
		return false
	}
	// The read reaches this very call: its last item is the task_plan call being answered, the round's only one (an
	// earlier one's answer names other work, which may come from outside: its duplicates).
	last := len(res.Items) - 1
	if last <= first || !planCall(res.Items[last]) {
		return false
	}
	for i, it := range res.Items {
		if i != first && i != last && (i < first && it.Kind != proto.KindNotice || i > first && !grantSafeAfterPrompt(it)) {
			return false
		}
	}
	return true
}

// planCall: it is the agent's call of Persona's task_plan.
func planCall(it proto.Item) bool {
	if it.Role != "assistant" || it.Kind != proto.KindToolUse {
		return false
	}
	rest, ok := strings.CutPrefix(compat.CanonicalMCPToolName(it.ToolName), "mcp__")
	i := strings.LastIndex(rest, "__")
	return ok && i > 0 && compat.IsMCPServer(strings.TrimPrefix(rest[:i], "plugin_")) && compat.Tool(rest[i+2:]) == "task_plan"
}

// scheduledTurn: the latest turn in items began with a scheduled task's prompt (no owner's message started it).
func scheduledTurn(items []proto.Item) bool {
	_, turn := splitLastTurn(items)
	if len(turn) == 0 {
		return false
	}
	src := prompts.ItemSource(turn[0])
	return src != nil && (src.Kind == proto.SourcePatrol || src.Kind == proto.SourceFollowup)
}

// scheduleGrantCover is what the grant of t, whose round task_plan is called in, lets start without the card: nil when
// t has no live grant or the round is not clean.
func (s *Service) scheduleGrantCover(ctx context.Context, t Task, machine, session string) func(PlanTask) bool {
	current, ok := s.scheduledTask(ctx, t.ID)
	if !ok {
		return nil
	}
	basis, err := s.taskGrantBasis(ctx, current)
	if err != nil || basis != t.runGrantBasis {
		return nil
	}
	g := current.liveGrant(basis)
	if g == nil || !s.cleanRound(ctx, t, machine, session) {
		return nil
	}
	have := map[string]bool{}
	for _, k := range g.Kinds {
		have[k] = true
	}
	homes := s.trustHomes(ctx)
	return func(p PlanTask) bool {
		return !p.Readonly && (trustCovers(have, homes, trustKey(p)) || trustCovers(have, homes, trustFolderKey(p)))
	}
}

// scheduledTask is task id as the schedule holds it now.
func (s *Service) scheduledTask(ctx context.Context, id string) (Task, bool) {
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return Task{}, false
	}
	defer sc.mu.Unlock()
	if i := sc.indexLocked(id); i >= 0 {
		return sc.tasks[i], true
	}
	return Task{}, false
}

// inScheduledTurn: task_plan from session on machine is called in a turn a scheduled task started, as the hub knows it
// (a round under way there) or as the conversation says (its latest turn began with a scheduled prompt: after a hub
// restart too). A conversation its own machine cannot read back to it is taken as the ordinary turn it almost always is.
func (s *Service) inScheduledTurn(ctx context.Context, machine, session string) bool {
	if s.roundUnderWay(machine, session) {
		return true
	}
	items, ok := s.lastTurnItems(ctx, machine, session)
	return ok && scheduledTurn(items)
}

// claudeConversation: session on machine is not known to run another agent than Claude Code, the only one whose
// rounds can be clean (it starts under the id the hub picks): another's card does not offer a grant it could never
// use. Only the offer: a round that is not clean never uses a grant (cleanRound).
func (s *Service) claudeConversation(ctx context.Context, machine, session string) bool {
	c, ok := s.session(ctx, machine, session)
	return !ok || c.Agent == "" || c.Agent == "claude"
}
