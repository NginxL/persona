package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
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

// workerMemoryID is the legacy durable prefix for one outcome item of one worker's turn. The machine belongs in the ID
// because session IDs are only unique on their own computer; Agent in the record remains the session ID readers use.
func workerMemoryID(todoID, machine, session string, turn, itemIndex int) string {
	return fmt.Sprintf("%s/%s/%s/%d/%d", todoID, machine, session, turn, itemIndex)
}

// workerMemoryItemID disambiguates content blocks that share a transcript Seq. ItemIndex remains the stable turn offset;
// the encoded item key distinguishes parallel tool calls without making IDs from one-off timestamps or goroutine order.
func workerMemoryItemID(todoID, machine, session string, turn, itemIndex int, itemKey string) string {
	id := workerMemoryID(todoID, machine, session, turn, itemIndex)
	if itemKey == "" {
		return id
	}
	return id + "/" + base64.RawURLEncoding.EncodeToString([]byte(itemKey))
}

func workerOutcomeItemKey(it proto.Item) string {
	if it.Kind == proto.KindToolUse {
		var report struct {
			Text     string   `json:"text"`
			Evidence []string `json:"evidence"`
		}
		if json.Unmarshal(it.ToolInput, &report) == nil {
			h := fnv.New64a()
			_, _ = h.Write([]byte(clip(strings.TrimSpace(report.Text), 2000)))
			for _, evidence := range workerNormalizedEvidence(report.Evidence) {
				_, _ = h.Write([]byte{0})
				_, _ = h.Write([]byte(evidence))
			}
			return fmt.Sprintf("report:%x", h.Sum64())
		}
	}
	if it.ToolUseID != "" {
		return it.ToolUseID
	}
	return it.ID
}

// goWorkerOutcome runs a memory recording off the tool or triage path. The same wait group as task outcomes lets
// tests and shutdown observers wait for the write without holding up the worker's report.
func (s *Service) goWorkerOutcome(run func()) {
	if s.runWorkerOutcome != nil {
		s.runWorkerOutcome(run)
		return
	}
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
	cwd := workerPinnedCwd(t, machine, session)
	s.goWorkerOutcome(func() {
		items, read := s.lastTurnItems(ctx, machine, session)
		// A successful done report is always eligible. If WorkDone later commits, it reuses this report's stable identity.
		index := workerReportIndex(items, text, at, evidence)
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
		links := workerEvidenceLinks(evidence)
		s.recordWorkerOutcome(ctx, t, machine, session, cwd, turn, itemIndex,
			workerOutcomeItemKey(items[index]), workerLegacyIDAllowed(items, index), text, links, text)
	})
}

func workerNormalizedEvidence(evidence []string) []string {
	normalized := make([]string, 0, len(evidence))
	for _, item := range evidence {
		item = clip(strings.TrimSpace(item), 300)
		if item != "" && len(normalized) < 8 {
			normalized = append(normalized, item)
		}
	}
	return normalized
}

func workerEvidenceLinks(evidence []string) []string {
	var links []string
	for _, item := range evidence {
		links = append(links, urlRe.FindAllString(item, -1)...)
	}
	return links
}

func workerReportEvidenceLinks(it proto.Item) []string {
	if it.Kind != proto.KindToolUse {
		return nil
	}
	var report struct {
		Evidence []string `json:"evidence"`
	}
	if json.Unmarshal(it.ToolInput, &report) != nil {
		return nil
	}
	return workerEvidenceLinks(workerNormalizedEvidence(report.Evidence))
}

// workerOutcomeCandidate reports whether an item could have owned a legacy outcome record: a successful done report or
// final assistant text. Unrelated or failed tools do not make the old ID ambiguous.
func workerOutcomeCandidate(items []proto.Item, index int) bool {
	it := items[index]
	if it.Role != "assistant" {
		return false
	}
	if it.Kind == proto.KindText {
		return index == workerFinalTextIndex(items)
	}
	if it.Kind != proto.KindToolUse ||
		it.ToolName != "report_to_supervisor" && !slices.Contains(compat.MCPToolNames([]string{"report_to_supervisor"}), it.ToolName) {
		return false
	}
	var report struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(it.ToolInput, &report) != nil || report.Kind != upReportDone {
		return false
	}
	for _, result := range items[index+1:] {
		if result.Kind == proto.KindToolResult && result.ToolUseID == it.ToolUseID {
			return !result.IsError
		}
	}
	return true
}

// workerLegacyIDAllowed says whether the old Seq-only ID unambiguously names this outcome. When parallel items of the
// same kind share a Seq, an old record cannot tell which one it was, so it must not suppress either keyed replacement.
func workerLegacyIDAllowed(items []proto.Item, index int) bool {
	if index < 0 || index >= len(items) {
		return false
	}
	selected := items[index]
	for i, other := range items {
		if i != index && other.Seq == selected.Seq && other.Kind == selected.Kind && workerOutcomeCandidate(items, i) &&
			workerOutcomeItemKey(other) != workerOutcomeItemKey(selected) {
			return false
		}
	}
	return true
}

// goRecordWorkerDone records a committed WorkDone using the exact items triage read, never a later turn. Resolve the todo
// and its directory before starting the asynchronous write so a later assignment cannot change ownership.
func (s *Service) goRecordWorkerDone(ctx context.Context, work db.SessionWork, items []proto.Item) {
	ctx = context.WithoutCancel(ctx)
	t, ok := s.workerTodoOf(ctx, work.MachineID, work.SessionID)
	if !ok || t.DoneAt != nil && work.TurnAt.After(*t.DoneAt) {
		return
	}
	cwd := workerPinnedCwd(t, work.MachineID, work.SessionID)
	s.goRecordWorkerDoneForTodo(ctx, t, cwd, work, items)
}

func (s *Service) goRecordWorkerDoneForTodo(ctx context.Context, t Todo, cwd string, work db.SessionWork, items []proto.Item) {
	s.goWorkerOutcome(func() {
		if len(items) == 0 {
			s.logf("WARN worker memory todo %s: done turn has no captured transcript", t.ID)
			return
		}
		index := workerReportIndex(items, "", time.Time{})
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
		links := workerReportEvidenceLinks(items[index])
		links = append(links, urlRe.FindAllString(reply, -1)...)
		for _, a := range work.Work.Artifacts {
			links = append(links, a.Ref)
		}
		s.recordWorkerOutcome(ctx, t, work.MachineID, work.SessionID, cwd,
			turn, itemIndex, workerOutcomeItemKey(items[index]), workerLegacyIDAllowed(items, index), work.Work.Line, links, reply)
	})
}

// recordWorkerOutcome writes one worker outcome to the todo's explicit project, or to the projects its directory
// belongs to. A successful model extraction may add a decision and reason; a failure keeps exactly the original short
// conclusion and links. No in-memory claim is made: memory.jsonl alone decides whether a replay is already recorded.
// Live callers pin the outcome text; direct callers may omit it and use the session's listed last text instead.
func (s *Service) recordWorkerOutcome(ctx context.Context, t Todo, machine, session, cwd string, turn, itemIndex int,
	itemKey string, allowLegacy bool, conclusion string, links []string, pinnedText ...string) {
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
	legacyID := workerMemoryID(t.ID, machine, session, turn, itemIndex)
	id := workerMemoryItemID(t.ID, machine, session, turn, itemIndex, itemKey)
	pending := make([]*kb.Project, 0, len(projects))
	for _, p := range projects {
		// An unambiguous record written before item keys were added still owns this outcome. A legacy prefix shared by
		// parallel items is not enough evidence to suppress either one; newly written records carry their keyed ID.
		if !e.ProjectMemoryHas(p.Slug, id) &&
			(!allowLegacy || id == legacyID || !e.ProjectMemoryHas(p.Slug, legacyID)) {
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

// workerPinnedCwd uses the directory already recorded on the todo's agent. It avoids a synchronous node read on the
// report response path and keeps directory ownership tied to the same handoff as the todo.
func workerPinnedCwd(t Todo, machine, session string) string {
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

// workerReportIndex finds a done report's tool call. For a live report, at selects the matching call if a turn repeated
// the same report text, and supplied evidence breaks a tie between parallel calls. WorkDone omits that filter and finds
// the latest non-error done report so it reuses the same identity.
func workerReportIndex(items []proto.Item, text string, at time.Time, evidence ...[]string) int {
	best := -1
	bestDelta := time.Duration(1<<63 - 1)
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.Kind != proto.KindToolUse || it.Role != "assistant" ||
			it.ToolName != "report_to_supervisor" && !slices.Contains(compat.MCPToolNames([]string{"report_to_supervisor"}), it.ToolName) {
			continue
		}
		var report struct {
			Kind     string   `json:"kind"`
			Text     string   `json:"text"`
			Evidence []string `json:"evidence"`
		}
		if json.Unmarshal(it.ToolInput, &report) != nil || report.Kind != upReportDone {
			continue
		}
		if text != "" && clip(strings.TrimSpace(report.Text), 2000) != text {
			continue
		}
		if len(evidence) > 0 && !slices.Equal(workerNormalizedEvidence(report.Evidence), workerNormalizedEvidence(evidence[0])) {
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
