package assistant

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A handoff park has its own source. In particular, the PR and external-reply
// observers must never treat its title or check as external wait evidence.
const (
	parkTodoHandoff  = "todo_handoff"
	sigHandoffResult = "handoff_result"
)

type taskHandoffRequest struct {
	TodoID          string `json:"todo_id"`
	ReceivingTodoID string `json:"receiving_todo_id"`
	Head            string `json:"head"`
	Check           string `json:"check"`
	Until           string `json:"until"`
}

type taskHandoffReceipt struct {
	TodoID string `json:"todo_id"`
	ID     string `json:"id"`
	Head   string `json:"head"`
	Check  string `json:"check"`
}

type taskHandoffTarget struct {
	TodoID string `json:"todo_id"`
	Title  string `json:"title"`
	Check  string `json:"check"`
}

// taskHandoff lives on the originating task's review ledger. Its ID is the
// client message ID of a node-confirmed send. The result, when present, moved
// that same review to pending_review in the same ledger write.
type taskHandoff struct {
	ID      string            `json:"id"`
	Target  taskHandoffTarget `json:"target"`
	Head    string            `json:"head"`
	Round   int               `json:"round"`
	Machine string            `json:"machine"`
	Session string            `json:"session"`
	Result  string            `json:"result,omitempty"`
	Detail  string            `json:"detail,omitempty"`
	ToldAt  time.Time         `json:"told_at,omitzero"`
}

type preparedHandoff struct {
	taskHandoff
	origin   string
	reviewAt time.Time
	working  time.Time
	until    time.Time
}

type agentSendReply struct {
	OK               bool `json:"ok"`
	Queued           bool `json:"queued"`
	CompactAfterTurn bool `json:"compact_after_turn"`
	Compacted        bool `json:"compacted"`
}

func (s *Service) handoffOwnerWait(t Todo) bool {
	if t.Supervision.Waits != nil || t.Supervision.State == reviewNeedsUser {
		return true
	}
	a, ok := s.askCopy(t.ID)
	return ok && !a.AskedAt.IsZero() && a.AcceptedAt.IsZero() && !acceptAskStale(&a, t) && !acceptAskEnded(&a, t)
}

func (s *Service) handoffWork(id string) time.Time {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	return s.tasks.working[id]
}

func (s *Service) prepareTaskHandoff(ctx context.Context, self, machine, session string, req taskHandoffRequest) (*preparedHandoff, error) {
	req.TodoID, req.ReceivingTodoID = strings.TrimSpace(req.TodoID), strings.TrimSpace(req.ReceivingTodoID)
	req.Head, req.Check = strings.ToLower(strings.TrimSpace(req.Head)), strings.TrimSpace(req.Check)
	if _, err := hex.DecodeString(req.Head); err != nil || len(req.Head) != 40 && len(req.Head) != 64 {
		return nil, fmt.Errorf("handoff head must be a full commit ID")
	}
	if req.TodoID == "" || req.ReceivingTodoID == "" || req.TodoID == req.ReceivingTodoID ||
		req.Check == "" || len([]rune(req.Check)) > 200 || strings.TrimSpace(req.Until) == "" {
		return nil, fmt.Errorf("handoff requires two distinct to-dos, a check of up to 200 characters and a deadline")
	}
	until, err := parseParkUntil(req.Until, time.Now())
	if err != nil {
		return nil, err
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return nil, err
	}
	origin, ok := s.todoNow(req.TodoID)
	if !ok || origin.DoneAt != nil || origin.Supervision == nil || self == "" || origin.Supervision.Supervisor != self {
		return nil, fmt.Errorf("handoff requires an open to-do that this conversation supervises")
	}
	if s.handoffOwnerWait(origin) || origin.Supervision.AutoReview != nil && origin.Supervision.AutoReview.State == autoRunning {
		return nil, fmt.Errorf("the originating to-do already has a decision or review in progress")
	}
	working := s.handoffWork(origin.ID)
	runs, reached := s.taskAgentsReached(ctx, origin)
	runs = currentTaskAgents(runs, supersededAgents(origin, runs))
	if !reached || len(runs) == 0 {
		return nil, fmt.Errorf("could not confirm that the originating to-do has a stopped Agent")
	}
	for _, run := range runs {
		if run.State == proto.StateWorking || run.Status == proto.StatusWorking || run.State == proto.StateNeedsYou || run.PendingInteractions > 0 {
			return nil, fmt.Errorf("the originating to-do has an Agent working or waiting for an answer")
		}
	}
	receiving, ok := s.taskOf(ctx, machine, session)
	if !ok || receiving.ID != req.ReceivingTodoID || receiving.Cancelled() || receiving.Supervision == nil ||
		receiving.AgentAborted(machine, session) {
		return nil, fmt.Errorf("the receiving Agent must belong to the named open supervised to-do")
	}
	return &preparedHandoff{
		taskHandoff: taskHandoff{ID: "handoff_" + randomHex(16), Machine: machine, Session: session,
			Target: taskHandoffTarget{TodoID: receiving.ID, Title: clip(oneLine(receiving.Title), 120), Check: req.Check},
			Head:   req.Head, Round: origin.Supervision.Round},
		origin: origin.ID, reviewAt: origin.Supervision.UpdatedAt, working: working, until: until,
	}, nil
}

func (s *Service) sendTaskHandoff(ctx context.Context, h *preparedHandoff, params map[string]any, sent *agentSendReply) error {
	// A receiving Agent can report as soon as its input starts. Let its receipt
	// wait for the send acknowledgement and ledger write for this origin.
	s.tasks.reviews.acquire(h.origin)
	defer s.tasks.reviews.release(h.origin)
	if !s.handoffStillCurrent(h) {
		return fmt.Errorf("the originating to-do changed; nothing was sent")
	}
	receiving, ok := s.taskOf(ctx, h.Machine, h.Session)
	if !ok || receiving.ID != h.Target.TodoID || receiving.Cancelled() || receiving.AgentAborted(h.Machine, h.Session) {
		return fmt.Errorf("the receiving Agent no longer belongs to the named to-do; nothing was sent")
	}
	if err := s.call(ctx, h.Machine, DeviceSessionSend, params, sent); err != nil {
		if sendUnknown(err) {
			return fmt.Errorf("%w (handoff delivery is unconfirmed; no wait was recorded)", err)
		}
		return err
	}
	if !sent.OK {
		return fmt.Errorf("the node did not confirm the send; no handoff was recorded")
	}
	// Once acknowledged, persist even if the supervisor stopped waiting for the answer.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.recordTaskHandoff(ctx, h); err != nil {
		return fmt.Errorf("the node accepted the message, but the wait could not be confirmed: %w", err)
	}
	return nil
}

func (s *Service) handoffStillCurrent(h *preparedHandoff) bool {
	t, ok := s.todoNow(h.origin)
	return ok && t.DoneAt == nil && t.Supervision != nil && t.Supervision.UpdatedAt.Equal(h.reviewAt) &&
		h.working.Equal(s.handoffWork(h.origin)) && !s.handoffOwnerWait(t) && time.Now().Before(h.until)
}

func (s *Service) recordTaskHandoff(ctx context.Context, h *preparedHandoff) error {
	s.tasks.completionMu.Lock()
	defer s.tasks.completionMu.Unlock()
	s.todos.writing.Lock()
	defer s.todos.writing.Unlock()
	receiving, ok := s.taskOf(ctx, h.Machine, h.Session)
	if !ok || receiving.ID != h.Target.TodoID || receiving.Cancelled() || receiving.AgentAborted(h.Machine, h.Session) {
		return fmt.Errorf("the receiving Agent no longer belongs to the named to-do")
	}
	recordedAt := time.Now().UTC()
	reason := i18n.T(s.lang(ctx), "assistant-tasks.handoff_wait", i18n.Args{"task": h.Target.Title, "check": h.Target.Check})
	_, changed, err := s.updateSupervision(ctx, h.origin, func(v *Supervision) bool {
		if !s.handoffStillCurrent(h) {
			return false
		}
		r := TaskReview{At: recordedAt, Verdict: reviewParked, WaitingOn: parkTodoHandoff, Reason: reason,
			Until: h.until, Parks: 1, Handoff: &h.taskHandoff}
		v.State = reviewParked
		v.Reviews = append(v.Reviews, r)
		if len(v.Reviews) > 20 {
			v.Reviews = v.Reviews[len(v.Reviews)-20:]
		}
		return true
	})
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("the originating to-do or review changed")
	}
	s.logf("task %s: handoff %s confirmed for task %s", h.origin, h.ID, h.Target.TodoID)
	return nil
}

func (v *Supervision) handoffResult() *taskHandoff {
	if v == nil || v.State != reviewPending || len(v.Reviews) == 0 {
		return nil
	}
	h := v.Reviews[len(v.Reviews)-1].Handoff
	if h != nil && h.Round == v.Round && h.Result != "" {
		return h
	}
	return nil
}

func (s *Service) reportTaskHandoff(ctx context.Context, machine, session string, receipt taskHandoffReceipt, kind, text string, evidence []string, now time.Time) (string, error) {
	if kind != upReportDone && kind != upReportBlocked {
		return "", fmt.Errorf("a handoff receipt requires kind done or blocked")
	}
	receipt.TodoID, receipt.ID = strings.TrimSpace(receipt.TodoID), strings.TrimSpace(receipt.ID)
	receipt.Head, receipt.Check = strings.ToLower(strings.TrimSpace(receipt.Head)), strings.TrimSpace(receipt.Check)
	if receipt.TodoID == "" || receipt.ID == "" {
		return "", fmt.Errorf("a handoff receipt requires the originating to-do and handoff ID")
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", err
	}
	s.tasks.reviews.acquire(receipt.TodoID)
	defer s.tasks.reviews.release(receipt.TodoID)
	origin, ok := s.todoNow(receipt.TodoID)
	if !ok || origin.DoneAt != nil {
		return "", fmt.Errorf("no current handoff for that to-do")
	}
	receiving, owns := s.taskOf(ctx, machine, session)
	matches := func(h *taskHandoff) bool {
		return h != nil && owns && !receiving.Cancelled() && !receiving.AgentAborted(machine, session) &&
			h.Target.TodoID == receiving.ID && h.Machine == machine && h.Session == session &&
			h.ID == receipt.ID && h.Head == receipt.Head && h.Target.Check == receipt.Check
	}
	if h := origin.Supervision.handoffResult(); matches(h) {
		if h.Result != "received" && h.Result != "failed" {
			return "", fmt.Errorf("the handoff already ended (%s)", h.Result)
		}
		return "That handoff result is already recorded.", nil
	}
	p := origin.Supervision.parking()
	if p == nil || !matches(p.Handoff) || !now.Before(p.Until) {
		return "", fmt.Errorf("no matching confirmed handoff; check the originating to-do, handoff ID, head and check")
	}
	outcome := "received"
	if kind == upReportBlocked {
		outcome = "failed"
	}
	for _, item := range evidence[:min(8, len(evidence))] {
		if item = clip(strings.TrimSpace(item), 300); item != "" {
			text += "\n" + item
		}
	}
	changed, err := s.finishTaskHandoff(ctx, origin.ID, p.Handoff.ID, outcome, clip(text, 3000), now)
	if err != nil {
		return "", err
	}
	if !changed {
		return "", fmt.Errorf("the originating to-do no longer waits for this handoff")
	}
	return "Handoff result recorded for the originating to-do's supervisor.", nil
}

// finishTaskHandoff rechecks the exact park while holding the lock shared
// with to-do completion and Agent reassignment. Only a current park can move.
func (s *Service) finishTaskHandoff(ctx context.Context, id, handoffID, result, detail string, now time.Time) (bool, error) {
	s.todos.writing.Lock()
	defer s.todos.writing.Unlock()
	v, changed, err := s.updateSupervision(ctx, id, func(v *Supervision) bool {
		p := v.parking()
		t, ok := s.todoNow(id)
		if !ok || t.DoneAt != nil || p == nil || p.Handoff == nil || p.Handoff.ID != handoffID ||
			p.Handoff.Round != v.Round || s.handoffOwnerWait(t) {
			return false
		}
		h := p.Handoff
		target, exists := s.todoNow(h.Target.TodoID)
		switch {
		case !exists || target.Cancelled():
			result, detail = "cancelled", ""
		case target.StartError != "":
			result, detail = "failed", target.StartError
		case !now.Before(p.Until):
			result, detail = "expired", ""
		case result == "":
			return false // the receiving task still has time to reply
		default:
			bound, ok := s.taskOf(ctx, h.Machine, h.Session)
			if !ok || bound.ID != target.ID || bound.AgentAborted(h.Machine, h.Session) {
				return false // the caller no longer works on the receiving task
			}
		}
		h.Result, h.Detail = result, detail
		v.State = reviewPending
		return true
	})
	if changed {
		s.signal(handoffResultSignal(id, v.handoffResult()))
		s.logf("task %s: handoff %s result %s", id, handoffID, result)
	}
	return changed, err
}

func (s *Service) checkTaskHandoff(ctx context.Context, t Todo, now time.Time) Todo {
	p := t.Supervision.parking()
	if p == nil || p.Handoff == nil {
		return t
	}
	target, exists := s.todoNow(p.Handoff.Target.TodoID)
	if exists && !target.Cancelled() && target.StartError == "" && now.Before(p.Until) {
		return t
	}
	changed, err := s.finishTaskHandoff(ctx, t.ID, p.Handoff.ID, "", "", now)
	if err != nil {
		s.logf("task %s: handoff %s: %v", t.ID, p.Handoff.ID, err)
	}
	if changed {
		if cur, ok := s.todoNow(t.ID); ok {
			return cur
		}
	}
	return t
}

func handoffResultSignal(id string, h *taskHandoff) superviseSignal {
	return superviseSignal{Todo: id, Kind: sigHandoffResult, Key: h.ID}
}

func (s *Service) handoffResultsTold(ctx context.Context, signals []superviseSignal) {
	for _, g := range signals {
		if g.Kind != sigHandoffResult {
			continue
		}
		if _, _, err := s.updateSupervision(ctx, g.Todo, func(v *Supervision) bool {
			h := v.handoffResult()
			if h == nil || h.ID != g.Key || !h.ToldAt.IsZero() {
				return false
			}
			h.ToldAt = time.Now().UTC()
			return true
		}); err != nil {
			s.logf("task %s: recording handoff result delivery: %v", g.Todo, err)
		}
	}
}

func handoffResultDetail(h *taskHandoff) string {
	return prompts.RenderTodoHandoffResult(prompts.TodoHandoffResult{
		Todo: h.Target.TodoID, Title: h.Target.Title, Head: h.Head, Check: h.Target.Check, Result: h.Result, Detail: h.Detail,
	})
}
