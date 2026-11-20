package assistant

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// ---- result cards and recovery ----

func sessionReviewSummary(e sessionReview) string {
	lang := i18n.Lang(e.Language).Or(i18n.Default)
	if e.FindingsCount == nil {
		return sessionReviewT(lang, "review_complete")
	}
	if *e.FindingsCount == 1 {
		return sessionReviewT(lang, "finding_one")
	}
	return sessionReviewT(lang, "findings", i18n.Args{"count": *e.FindingsCount})
}

func sessionReviewInteraction(e sessionReview) proto.Interaction {
	lang := i18n.Lang(e.Language).Or(i18n.Default)
	created := e.StartedAt
	if e.EndedAt != nil {
		created = *e.EndedAt
	}
	return proto.Interaction{
		ID: e.ID, MachineID: e.MachineID, SessionID: e.SessionID, SessionTitle: e.SessionTitle, Cwd: e.Cwd,
		Kind: proto.IxQuestion, ToolName: sessionReviewTool, Summary: sessionReviewSummary(e),
		CreatedAt: created, ExpiresAt: created.Add(sessionReviewCardTTL), Resumable: true,
		Questions: []proto.Question{{
			Question: sessionReviewT(lang, "card_title"), Header: sessionReviewT(lang, "review_complete"),
			Options: []proto.QuestionOption{{Label: sessionReviewT(lang, "send_to_agent")},
				{Label: sessionReviewT(lang, "dismiss"), Action: proto.QuestionOptionDefer}},
		}},
		ReviewReport: e.Findings, ReviewCount: e.FindingsCount, ReviewerSessionID: e.ReviewerID,
	}
}

func (s *Service) sessionReviewInteractions(ctx context.Context) ([]proto.Interaction, bool) {
	reviews, err := s.sessionReviewRecords(ctx)
	if err != nil {
		// Keep the cards already published rather than withdrawing a finished result during a store outage.
		s.fleet.mu.Lock()
		defer s.fleet.mu.Unlock()
		var kept []proto.Interaction
		seen := map[string]bool{}
		for _, p := range s.fleet.interactions {
			if p.ix.ToolName == sessionReviewTool {
				kept = append(kept, p.ix)
				seen[p.ix.ID] = true
			}
		}
		now := time.Now()
		for _, e := range reviews {
			if seen[e.ID] || e.State != sessionReviewPending || e.Findings == "" {
				continue
			}
			ix := sessionReviewInteraction(e)
			if now.Before(ix.ExpiresAt) {
				kept = append(kept, ix)
			}
		}
		sort.Slice(kept, func(i, j int) bool { return kept[i].CreatedAt.Before(kept[j].CreatedAt) })
		if errors.Is(err, errSessionReviewStore) {
			return kept, true // a manager without a store has no session-review feature, not a partial snapshot
		}
		return kept, false
	}
	var out []proto.Interaction
	now := time.Now()
	for _, e := range reviews {
		if e.State != sessionReviewPending || e.Findings == "" {
			continue
		}
		ix := sessionReviewInteraction(e)
		if now.Before(ix.ExpiresAt) {
			out = append(out, ix)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, true
}

func (s *Service) sessionReviewTurnEnded(ctx context.Context, machine, sid string) {
	reviews, err := s.sessionReviewRecords(ctx)
	if err != nil {
		return
	}
	var run sessionReview
	for _, e := range reviews {
		if e.State == sessionReviewRunning && e.MachineID == machine && e.ReviewerID == sid {
			run = e
			break
		}
	}
	if run.ID == "" {
		return
	}
	b := &s.sessionReviews
	b.mu.Lock()
	if b.taking == nil {
		b.taking = map[string]bool{}
	}
	if b.taking[run.ID] {
		b.mu.Unlock()
		return
	}
	b.taking[run.ID] = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.taking, run.ID)
		b.mu.Unlock()
	}()
	reply := s.reviewerReply(ctx, machine, sid)
	if strings.TrimSpace(reply) == "" {
		s.failSessionReview(ctx, run, "empty_answer")
		return
	}
	s.finishSessionReview(ctx, run, reply)
}

func (s *Service) failSessionReview(ctx context.Context, run sessionReview, why string) {
	now := time.Now().UTC()
	changed := false
	_ = s.mutateSessionReview(ctx, run.ID, func(e *sessionReview) error {
		if e.State != sessionReviewRunning {
			return nil
		}
		e.State, e.Error, e.EndedAt, changed = sessionReviewFailed, why, &now, true
		run = *e
		return nil
	})
	if changed {
		s.publishSoon()
		s.cleanSessionReview(ctx, run)
	}
}

func (s *Service) finishSessionReview(ctx context.Context, run sessionReview, reply string) {
	findings, count, _, marked := parseReviewFindings(reply)
	if !marked || strings.TrimSpace(findings) == "" {
		s.failSessionReview(ctx, run, "missing_findings_marker")
		return
	}
	findings = clipMiddle(strings.TrimSpace(findings), reviewFindingMax)
	now := time.Now().UTC()
	changed := false
	err := s.changeSessionReviews(ctx, func(doc *sessionReviewDocument) error {
		for i := range doc.Reviews {
			e := &doc.Reviews[i]
			if e.ID != run.ID {
				continue
			}
			if e.State != sessionReviewRunning {
				return nil // a deadline already ended it; a late reply cannot replace that outcome or any older card
			}
			e.State, e.Findings, e.FindingsCount, e.EndedAt, e.Error = sessionReviewPending, findings, &count, &now, ""
			run = *e
			changed = true
		}
		if changed {
			for i := range doc.Reviews {
				e := &doc.Reviews[i]
				if e.ID != run.ID && e.MachineID == run.MachineID && e.SessionID == run.SessionID && e.State == sessionReviewPending {
					e.State, e.ResolvedAt = sessionReviewReplaced, &now
				}
			}
		}
		return nil
	})
	if err != nil {
		s.logf("session review %s: finish: %v", run.ID, err)
		return
	}
	if changed {
		s.publishSoon()
		s.cleanSessionReview(ctx, run)
	}
}

func (s *Service) sessionReviewTick(ctx context.Context, now time.Time) {
	reviews, err := s.sessionReviewRecords(ctx)
	if err != nil {
		return
	}
	for _, e := range reviews {
		switch e.State {
		case sessionReviewRunning:
			switch {
			case now.Sub(e.StartedAt) > sessionReviewTimeout:
				s.failSessionReview(ctx, e, "timeout")
			case e.ReviewerID != "":
				c, ok := s.session(ctx, e.MachineID, e.ReviewerID)
				if !ok || c.Starting || c.Status == proto.StatusWorking {
					continue
				}
				if reply := s.reviewerReply(ctx, e.MachineID, e.ReviewerID); reply != "" {
					s.finishSessionReview(ctx, e, reply)
				}
			}
		case sessionReviewPending:
			ix := sessionReviewInteraction(e)
			if !now.Before(ix.ExpiresAt) {
				_ = s.mutateSessionReview(ctx, e.ID, func(v *sessionReview) error {
					if v.State == sessionReviewPending {
						v.State, v.ResolvedAt = sessionReviewExpired, &now
					}
					return nil
				})
				s.publishSoon()
			}
		}
		if e.State != sessionReviewRunning && e.CleanedAt == nil {
			s.cleanSessionReview(ctx, e)
		}
	}
}

// ---- Send / Dismiss ----

func sessionReviewAnswerKind(ix proto.Interaction, r proto.Resolution) string {
	if r.Dismiss || r.Cancel || r.Action == "dismiss" {
		return "dismiss"
	}
	if r.Action == sessionReviewAnswerSend {
		return sessionReviewAnswerSend
	}
	if len(ix.Questions) == 1 {
		q := ix.Questions[0]
		answer := r.Answers[q.Question]
		for _, option := range q.Options {
			if option.Label != answer {
				continue
			}
			if option.Action == proto.QuestionOptionDefer {
				return "dismiss"
			}
			return sessionReviewAnswerSend
		}
	}
	return ""
}

func (s *Service) sessionReviewPossible(id string) bool {
	if ix, ok := s.pendingIx(id); ok {
		return ix.ToolName == sessionReviewTool
	}
	return strings.HasPrefix(id, sessionReviewPrefix)
}

func (s *Service) sessionReviewForAnswer(ctx context.Context, id string) (sessionReview, proto.Interaction, error) {
	reviews, err := s.sessionReviewRecords(ctx)
	if err != nil {
		return sessionReview{}, proto.Interaction{}, err
	}
	e, ok := sessionReviewByID(reviews, id)
	if !ok {
		return sessionReview{}, proto.Interaction{}, errGone(id)
	}
	return e, sessionReviewInteraction(e), nil
}

// sessionReviewResolve makes an app's answer synchronous. Send happens before the card is claimed: a failure keeps the
// same card and no outcome. A durable delivery marker and client_msg_id make a later retry safe after any uncertain step.
func (s *Service) sessionReviewResolve(ctx context.Context, id string, r proto.Resolution, approver *assistantwire.HandoffApprover) (bool, error) {
	if !s.sessionReviewPossible(id) {
		return false, nil
	}
	e, ix, err := s.sessionReviewForAnswer(ctx, id)
	if err != nil {
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == CodeGone {
			return false, nil // not one of this manager's review cards
		}
		return true, err // a store failure must not turn into an unforwarded generic answer
	}
	if e.State != sessionReviewPending {
		return true, s.resolveAnswer(ctx, id, r, true, nil, approver)
	}
	action := sessionReviewAnswerKind(ix, r)
	if action == "" {
		return true, &rpc.Error{Code: "bad_request", Message: "a review card takes Send to agent or Dismiss"}
	}
	if action == sessionReviewAnswerSend && e.Delivery != sessionReviewDeliverySent {
		if err := s.deliverSessionReview(ctx, &e); err != nil {
			return true, &rpc.Error{Code: "session_review_send_failed", Message: sessionReviewT(s.lang(ctx), "send_failed", i18n.Args{"error": err.Error()})}
		}
	}
	if err := s.resolveAnswer(ctx, id, r, true, nil, approver); err != nil {
		return true, err
	}
	return true, s.finishSessionReviewAnswer(ctx, e, action)
}

// sessionReviewAction handles the manager.action replay after the ordinary card queue resolves. Delivery is idempotent.
func (s *Service) sessionReviewAction(ctx context.Context, id string, r proto.Resolution) (bool, error) {
	if !s.sessionReviewPossible(id) {
		return false, nil
	}
	e, ix, err := s.sessionReviewForAnswer(ctx, id)
	if err != nil {
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == CodeGone {
			return false, nil
		}
		return true, err
	}
	if e.State != sessionReviewPending {
		return true, nil
	}
	action := sessionReviewAnswerKind(ix, r)
	if action == "" {
		return true, &rpc.Error{Code: "bad_request", Message: "a review card takes Send to agent or Dismiss"}
	}
	if action == sessionReviewAnswerSend && e.Delivery != sessionReviewDeliverySent {
		if err := s.deliverSessionReview(ctx, &e); err != nil {
			return true, &rpc.Error{Code: "session_review_send_failed", Message: sessionReviewT(s.lang(ctx), "send_failed", i18n.Args{"error": err.Error()})}
		}
	}
	return true, s.finishSessionReviewAnswer(ctx, e, action)
}

func (s *Service) deliverSessionReview(ctx context.Context, e *sessionReview) error {
	mode := e.AnswerMode
	if mode == "" {
		c, ok := s.session(ctx, e.MachineID, e.SessionID)
		if !ok {
			return errors.New("the source Agent session is unavailable")
		}
		mode = sessionReviewAnswerSend
		if stoppedAgent(c) {
			mode = sessionReviewAnswerResume
		}
		if err := s.mutateSessionReview(ctx, e.ID, func(v *sessionReview) error {
			if v.State != sessionReviewPending {
				return errGone(v.ID)
			}
			v.AnswerMode = mode
			e.AnswerMode = mode
			return nil
		}); err != nil {
			return err
		}
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionReviewSendWait)
	defer cancel()
	var err error
	switch mode {
	case sessionReviewAnswerResume:
		// The owner's Send is the explicit resume. Resume with no prompt: if another actor already resumed the source,
		// the node returns the live session without consuming a prompt. The guarded send below carries the report once.
		if err = s.call(sctx, e.MachineID, DeviceSessionStart, map[string]any{"resume_session_id": e.SessionID}, nil); err == nil {
			err = s.call(sctx, e.MachineID, DeviceSessionSendOnce, map[string]any{
				"session_id": e.SessionID, "text": e.Findings, "client_msg_id": e.ID,
			}, nil)
		}
	case sessionReviewAnswerSend:
		err = s.call(sctx, e.MachineID, DeviceSessionSendOnce, map[string]any{
			"session_id": e.SessionID, "text": e.Findings, "client_msg_id": e.ID,
		}, nil)
	default:
		err = fmt.Errorf("unknown review answer mode %q", mode)
	}
	if err != nil {
		return err
	}
	return s.mutateSessionReview(context.Background(), e.ID, func(v *sessionReview) error {
		if v.State != sessionReviewPending {
			return errGone(v.ID)
		}
		v.Delivery = sessionReviewDeliverySent
		e.Delivery = sessionReviewDeliverySent
		return nil
	})
}

func (s *Service) finishSessionReviewAnswer(ctx context.Context, e sessionReview, action string) error {
	now := time.Now().UTC()
	state := sessionReviewDismissed
	if action == sessionReviewAnswerSend {
		state = sessionReviewSent
	}
	if err := s.mutateSessionReview(ctx, e.ID, func(v *sessionReview) error {
		if v.State != sessionReviewPending {
			return nil
		}
		v.State, v.ResolvedAt = state, &now
		return nil
	}); err != nil {
		return err
	}
	s.publishSoon()
	return nil
}
