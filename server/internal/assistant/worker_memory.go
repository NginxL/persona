package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// workerMemoryID names one outcome item of one worker's turn. The machine belongs in the ID because session IDs are
// only unique on their own computer; Agent in the record remains the session ID readers already use.
func workerMemoryID(todoID, machine, session string, turn, itemIndex int) string {
	return fmt.Sprintf("%s/%s/%s/%d/%d", todoID, machine, session, turn, itemIndex)
}

// goWorkerOutcome runs a memory recording off the tool or triage path. The same wait group as task outcomes lets
// tests and shutdown observers wait for the write without holding up the worker's report.
func (s *Service) goWorkerOutcome(run func()) {
	s.tasks.outcomeRuns.Add(1)
	go func() {
		defer s.tasks.outcomeRuns.Done()
		run()
	}()
}

// goRecordWorkerReport records a successful report_to_supervisor(kind=done). Its transcript read happens here, in the
// background: the report's answer does not wait for the node, a model or the knowledge base.
func (s *Service) goRecordWorkerReport(ctx context.Context, t Todo, machine, session, text string, evidence []string, at time.Time) {
	ctx = context.WithoutCancel(ctx)
	s.goWorkerOutcome(func() {
		items, read := s.lastTurnItems(ctx, machine, session)
		// With triage on, a final answer means WorkDone owns the write with its pinned items. With triage off there is
		// no WorkDone fallback, so the successful done report itself must still be recorded.
		index := workerReportIndex(items, text, at, s.workOn(ctx))
		if !read || len(items) == 0 || index < 0 {
			return
		}
		ictx, cancel := context.WithTimeout(ctx, outcomeReadTimeout)
		defer cancel()
		turn, itemIndex, ok := workerIdentity(items, index, func(before int) ([]proto.Item, bool, bool) {
			return s.workerTurnPage(ictx, machine, session, before)
		})
		if !ok {
			s.logf("WARN worker memory todo %s: done report has no stable transcript identity", t.ID)
			return
		}
		var links []string
		for _, e := range evidence {
			links = append(links, urlRe.FindAllString(e, -1)...)
		}
		s.recordWorkerOutcome(ctx, t, machine, session, s.workerCwd(ctx, t, machine, session), turn, itemIndex, text, links, text)
	})
}

// goRecordWorkerDone records a committed WorkDone using the exact items triage read, never a later turn. If this turn
// already reported done, it reuses that report's item identity; otherwise the final assistant text is the outcome item.
func (s *Service) goRecordWorkerDone(ctx context.Context, work db.SessionWork, items []proto.Item) {
	ctx = context.WithoutCancel(ctx)
	s.goWorkerOutcome(func() {
		t, ok := s.workerTodoOf(ctx, work.MachineID, work.SessionID)
		if !ok {
			return
		}
		if len(items) == 0 {
			s.logf("WARN worker memory todo %s: done turn has no captured transcript", t.ID)
			return
		}
		index := workerReportIndex(items, "", time.Time{}, false)
		if index < 0 {
			index = workerFinalTextIndex(items)
		}
		if index < 0 {
			s.logf("WARN worker memory todo %s: done turn has no outcome item", t.ID)
			return
		}
		ictx, cancel := context.WithTimeout(ctx, outcomeReadTimeout)
		defer cancel()
		turn, itemIndex, ok := workerIdentity(items, index, func(before int) ([]proto.Item, bool, bool) {
			return s.workerTurnPage(ictx, work.MachineID, work.SessionID, before)
		})
		if !ok {
			s.logf("WARN worker memory todo %s: done turn has no stable transcript identity", t.ID)
			return
		}
		_, reply := turnExchange(items)
		links := urlRe.FindAllString(reply, -1)
		for _, a := range work.Work.Artifacts {
			links = append(links, a.Ref)
		}
		s.recordWorkerOutcome(ctx, t, work.MachineID, work.SessionID, s.workerCwd(ctx, t, work.MachineID, work.SessionID),
			turn, itemIndex, work.Work.Line, links, reply)
	})
}

// recordWorkerOutcome writes one worker outcome to the todo's explicit project, or to the projects its directory
// belongs to. A successful model extraction may add a decision and reason; a failure keeps exactly the original short
// conclusion and links. No in-memory claim is made: memory.jsonl alone decides whether a replay is already recorded.
// Live callers pin the outcome text; direct callers may omit it and use the session's listed last text instead.
func (s *Service) recordWorkerOutcome(ctx context.Context, t Todo, machine, session, cwd string, turn, itemIndex int,
	conclusion string, links []string, pinnedText ...string) {
	if strings.TrimSpace(conclusion) == "" || !s.kbWritable(ctx) {
		return
	}
	e, err := s.kbGet(ctx)
	if err != nil || e == nil {
		s.logf("WARN worker memory todo %s: knowledge base unavailable: %v", t.ID, err)
		return
	}
	projects := s.projectsForTask(ctx, e, t, machine, cwd)
	if len(projects) == 0 {
		return
	}
	id := workerMemoryID(t.ID, machine, session, turn, itemIndex)
	pending := make([]*kb.Project, 0, len(projects))
	for _, p := range projects {
		if !e.ProjectMemoryHas(p.Slug, id) {
			pending = append(pending, p)
		}
	}
	if len(pending) == 0 {
		return
	}

	modelText := conclusion
	if len(pinnedText) > 0 {
		if strings.TrimSpace(pinnedText[0]) != "" {
			modelText = pinnedText[0]
		}
	} else if c, ok := s.session(ctx, machine, session); ok && strings.TrimSpace(c.LastText) != "" {
		modelText = c.LastText
	}
	agents := []outcomeAgent{{MachineID: machine, SessionID: session, Cwd: cwd, Text: modelText}}
	mctx, cancel := context.WithTimeout(ctx, outcomeModelTimeout)
	defer cancel()
	out, modelErr := s.pullOutcome(mctx, t, agents)
	if modelErr != nil {
		if !ai.IsPaused(modelErr) {
			s.logf("task %s: worker memory outcome: %v (the original conclusion instead)", t.ID, modelErr)
		}
		out = taskOutcome{Outcome: conclusion, Links: slices.Clone(links)}
	} else {
		// Links supplied by the report or work state remain evidence even when the model omits them.
		out.Links = append(out.Links, links...)
	}
	out = tidyOutcome(out)
	if out.Outcome == "" {
		out.Outcome = tidyOutcome(taskOutcome{Outcome: conclusion}).Outcome
	}
	recordedAt := time.Now().UTC().Format(time.RFC3339Nano)
	base := kb.ProjectMemoryRecord{TodoID: t.ID, Agent: session, Turn: turn, ItemIndex: itemIndex,
		Verification: kb.ProjectMemoryAgentReported, RecordedAt: recordedAt}
	var records []kb.ProjectMemoryRecord
	if modelErr == nil {
		for i, d := range out.Decisions {
			record := base
			record.ID = fmt.Sprintf("%s/decision/%d", id, i+1)
			record.Type = kb.ProjectMemoryDecision
			record.Question, record.Decision, record.Reason = d.Question, d.Choice, d.Reason
			records = append(records, record)
		}
		for i, pitfall := range out.Pitfalls {
			record := base
			record.ID = fmt.Sprintf("%s/pitfall/%d", id, i+1)
			record.Type = kb.ProjectMemoryPitfall
			record.Conclusion = pitfall
			records = append(records, record)
		}
	}
	// The base outcome goes last: its presence means the whole batch was attempted, while a partial retry dedupes the
	// typed records already appended. The durable identity itself remains the M1 worker/turn/item identity.
	outcome := base
	outcome.ID, outcome.Type, outcome.Conclusion, outcome.Links = id, kb.ProjectMemoryOutcome, out.Outcome, out.Links
	records = append(records, outcome)
	for _, p := range pending {
		if err := e.AppendProjectMemoryBatch(p.Slug, records); err != nil {
			s.logf("WARN worker memory todo %s: append project %s: %v", t.ID, p.Slug, err)
		}
	}
}

// workerTodoOf is the open todo a worker belongs to, or its most recently completed one after triage closed it. Project
// ownership comes from that todo, never from its title. Released and dismissed agents no longer confer ownership.
func (s *Service) workerTodoOf(ctx context.Context, machine, session string) (Todo, bool) {
	if t, ok := s.openTodoOfAgent(machine, session); ok {
		return t, true
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return Todo{}, false
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	var found *Todo
	for _, t := range s.todos.todos {
		if t.DoneAt == nil || found != nil && !t.DoneAt.After(*found.DoneAt) {
			continue
		}
		for _, a := range t.Agents {
			if a.MachineID == machine && a.SessionID == session && a.ReleasedAt == nil && a.DismissedAt == nil {
				found = t
				break
			}
		}
	}
	if found == nil {
		return Todo{}, false
	}
	return copyTodoLocked(found), true
}

func (s *Service) workerCwd(ctx context.Context, t Todo, machine, session string) string {
	if c, ok := s.session(ctx, machine, session); ok && c.Cwd != "" {
		return c.Cwd
	}
	for _, a := range t.Agents {
		if a.MachineID == machine && a.SessionID == session {
			return a.Cwd
		}
	}
	return ""
}

// workerIdentity returns the opening prompt's absolute transcript sequence and the outcome item's absolute sequence
// offset. A capped page may omit the prompt; older reads pages backward rather than letting both fields shift.
// A prompt-less turn uses the outcome sequence itself and offset zero, which is also stable across page boundaries.
func workerIdentity(items []proto.Item, index int, older func(before int) (page []proto.Item, hasMore, ok bool)) (turn, itemIndex int, ok bool) {
	if index < 0 || index >= len(items) {
		return 0, 0, false
	}
	for i := index; i >= 0; i-- {
		if workerPrompt(items[i]) {
			return workerIdentityFromPrompt(items, index, items[i])
		}
	}
	before := items[0].Seq
	for before > 1 {
		page, hasMore, read := older(before)
		if !read {
			return 0, 0, false
		}
		if len(page) == 0 {
			if hasMore {
				return 0, 0, false
			}
			break
		}
		for i := len(page) - 1; i >= 0; i-- {
			if workerPrompt(page[i]) {
				return workerIdentityFromPrompt(items, index, page[i])
			}
		}
		if !hasMore {
			break
		}
		next := page[0].Seq
		if next <= 0 || next >= before {
			return 0, 0, false
		}
		before = next
	}
	if items[index].Seq <= 0 {
		return 0, 0, false
	}
	return items[index].Seq, 0, true
}

func workerIdentityFromPrompt(items []proto.Item, index int, prompt proto.Item) (turn, itemIndex int, ok bool) {
	if prompt.Seq <= 0 || items[index].Seq < prompt.Seq {
		return 0, 0, false
	}
	return prompt.Seq, items[index].Seq - prompt.Seq, true
}

// workerPrompt is the same turn boundary as the node's paged transcript: a real user question, not a queued, wake or
// injected prompt. Explicit user/session provenance protects quoted notification text.
func workerPrompt(it proto.Item) bool {
	if it.Role != "user" || it.Kind != proto.KindText || it.Queued || it.Wake || it.Injected != nil {
		return false
	}
	return it.ClientMsgID != "" || it.From != "" || prompts.ParseInjected(it.Text) == nil
}

func (s *Service) workerTurnPage(ctx context.Context, machine, session string, before int) (items []proto.Item, hasMore, ok bool) {
	var res struct {
		Items   []proto.Item `json:"items"`
		HasMore bool         `json:"has_more"`
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.call(cctx, machine, DeviceSessionItems, map[string]any{"session_id": session, "turns": 1, "before_seq": before}, &res); err != nil {
		return nil, false, false
	}
	return res.Items, res.HasMore, true
}

// workerReportIndex finds a done report's tool call. For a live report (pending), a later final answer means the turn
// already ended and WorkDone owns the write; at selects the matching call if a turn repeated the same report text.
// For WorkDone it finds the latest non-error done report and reuses its identity.
func workerReportIndex(items []proto.Item, text string, at time.Time, pending bool) int {
	best := -1
	bestDelta := time.Duration(1<<63 - 1)
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.Kind != proto.KindToolUse || it.Role != "assistant" ||
			it.ToolName != "report_to_supervisor" && !slices.Contains(compat.MCPToolNames([]string{"report_to_supervisor"}), it.ToolName) {
			continue
		}
		var report struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		if json.Unmarshal(it.ToolInput, &report) != nil || report.Kind != upReportDone {
			continue
		}
		if text != "" && clip(strings.TrimSpace(report.Text), 2000) != text {
			continue
		}
		if pending && workerFinalTextIndex(items[i+1:]) >= 0 {
			continue
		}
		failed := false
		for _, result := range items[i+1:] {
			if result.Kind == proto.KindToolResult && result.ToolUseID == it.ToolUseID {
				failed = result.IsError
				break
			}
		}
		if failed {
			continue
		}
		if !at.IsZero() && !it.TS.IsZero() {
			delta := it.TS.Sub(at)
			if delta < 0 {
				delta = -delta
			}
			if delta < bestDelta {
				best, bestDelta = i, delta
			}
		} else if best == -1 {
			best = i
		}
	}
	return best
}

func workerFinalTextIndex(items []proto.Item) int {
	for i := len(items) - 1; i >= 0; i-- {
		if it := items[i]; it.Kind == proto.KindText && it.Role == "assistant" && strings.TrimSpace(it.Text) != "" {
			return i
		}
	}
	return -1
}
