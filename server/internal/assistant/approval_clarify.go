package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// preparedApproval is the process-local continuation behind one permission
// card. Its fields are guarded by Service.fleet.mu except op and caller, which
// are immutable. The public interaction carries only CanClarify, never this
// operation.
type preparedApproval struct {
	op           approvalops.Operation
	caller       context.Context
	outcome      approvalops.Outcome
	result       proto.Resolution
	err          error
	finished     bool
	reportStatus string

	clarified  bool
	waiting    bool // its decision window ended unanswered and it was kept for the owner (keepWaiting)
	release    chan struct{}
	attempt    *clarifyAttempt
	terminal   bool
	deliveries map[string]clarifyDelivery

	decisionTimer    *time.Timer
	pauseTimer       *time.Timer
	decisionDeadline time.Time // current effective decision deadline while not paused
	remaining        time.Duration
	paused           bool
	pauseStarted     time.Time
	pausedTotal      time.Duration

	// A turn covers only questions accepted before it starts. A question
	// accepted during that turn waits for the next one, as the node queues it.
	questionSeq uint64
	acceptedSeq uint64
	turnSeq     uint64
	answeredSeq uint64
	turnActive  bool
	channelRuns map[string]bool
}

type clarifyAttempt struct {
	done        chan struct{}
	err         error
	first       bool
	clientMsgID string
	text        string
	originKey   string
}

type clarifyDelivery struct {
	text      string
	originKey string
}

// clarificationOrigin is the verified card callback's return destination. It
// contains no form values; only the question text travels with the owner turn.
type clarificationOrigin struct {
	Channel   string
	ChatID    string
	MessageID string
}

var (
	errApprovalClarifying = errors.New("approval is clarifying")
	errClarifyUnavailable = errors.New("this approval cannot take a question")
	// clarificationPauseBudget is cumulative per card, not per question.
	clarificationPauseBudget = 24 * time.Hour
	clarifyQuestionRunes     = 1000
	// approvalWaitingWindow is how long a prepared card stays pending after its decision window ended unanswered.
	approvalWaitingWindow = 24 * time.Hour
)

// waitPreparedCard registers an authoritative prepared approval. The original
// caller returns either its final tool result or, on the first accepted
// question, a non-final Clarifying outcome while the card stays pending.
func (s *Service) waitPreparedCard(ctx context.Context, ix proto.Interaction, op approvalops.Operation) (approvalops.Outcome, error) {
	if reservedCard(ix) || ix.Kind != proto.IxPermission || op.Apply == nil {
		return approvalops.Outcome{}, errClarifyUnavailable
	}
	live := ix.MachineID != "" && ix.SessionID != ""
	if live {
		// Clarification needs an exact live conversation for the owner's question
		// and its answer. Unavailable origins retain the older one-shot card instead.
		// A deleted or unreadable origin stays prepared so clarify returns that exact
		// error rather than disguising it as a different fallback.
		if err := s.conversationAvailable(ctx, ix.MachineID, ix.SessionID); err == nil {
			// The latest complete session list already answers the common case;
			// ask the node only when that list does not contain the conversation.
			s.fleet.mu.Lock()
			_, listed := s.fleet.prev[ix.MachineID][ix.SessionID]
			s.fleet.mu.Unlock()
			if !listed {
				if _, ok := s.session(ctx, ix.MachineID, ix.SessionID); !ok {
					live = false
				}
			}
		}
	}
	if !live {
		return s.waitOneShotPreparedCard(ctx, ix, op)
	}
	state := &preparedApproval{op: op, caller: ctx, release: make(chan struct{}), deliveries: map[string]clarifyDelivery{}, decisionDeadline: ix.ExpiresAt}
	ix.CanClarify = true
	_, err := s.waitInteractionPrepared(ctx, s.ownCard(ctx, ix), state)
	if errors.Is(err, errApprovalClarifying) {
		s.fleet.mu.Lock()
		waiting := state.waiting
		s.fleet.mu.Unlock()
		if waiting {
			return approvalops.Outcome{Clarifying: true, Text: approvalops.WaitingText}, nil
		}
		return approvalops.Outcome{Clarifying: true, Text: approvalops.ClarifyingText}, nil
	}
	if err != nil {
		return approvalops.Outcome{}, err
	}
	if state.err != nil {
		return approvalops.Outcome{}, state.err
	}
	return state.outcome, nil
}

// awaitPrepared releases only the original tool call on clarification. Final
// resolution, expiry and replacement still finish the interaction normally.
// Every waiter listens to the broadcast, never the one-shot p.done: a snapshot
// waiter cannot steal the original caller's terminal result.
func (s *Service) awaitPrepared(ctx context.Context, p *pending) (proto.Resolution, error) {
	s.fleet.mu.Lock()
	if p.resolved == nil {
		p.resolved = make(chan struct{})
	}
	resolved := p.resolved
	s.fleet.mu.Unlock()
	terminal := func() (proto.Resolution, error) {
		s.fleet.mu.Lock()
		r := p.prepared.result
		s.fleet.mu.Unlock()
		s.finishInteraction(p.ix.ID)
		return r, nil
	}
	// Prefer an already published terminal result over a clarification release
	// left closed by an earlier question (a snapshot can attach after both).
	select {
	case <-resolved:
		return terminal()
	default:
	}
	select {
	case <-resolved:
		return terminal()
	case <-p.prepared.release:
		select {
		case <-resolved:
			return terminal()
		default:
		}
		return proto.Resolution{}, errApprovalClarifying
	case <-ctx.Done():
		s.endPrepared(p, "cancelled", "The approval was cancelled before the owner answered; nothing changed.")
		return proto.Resolution{}, ctx.Err()
	}
}

// startPreparedTimer is called after the card is registered and announced.
func (s *Service) startPreparedTimer(p *pending) {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if p.prepared == nil || p.prepared.terminal {
		return
	}
	p.prepared.decisionTimer = time.AfterFunc(max(0, time.Until(p.ix.ExpiresAt)), func() {
		s.expirePrepared(p)
	})
}

func (s *Service) expirePrepared(p *pending) {
	if s.keepWaiting(p) {
		return
	}
	s.endPreparedIf(p, "expired", "", func(a *preparedApproval) bool { return !a.paused })
}

// keepWaiting is the end of an unanswered card's first decision window. The card is not taken off the apps: the
// owner may have had no notification at all (no working push, the app in the background) and only sees it when they
// open the Persona app or the web. Its original tool call returns a non-final result (approvalops.WaitingText), the
// card stays pending for approvalWaitingWindow more with its new deadline told to the apps (no second push), and its
// final outcome reaches the conversation through the approval journal, as after a question. It keeps nothing waiting
// when the caller already went away, once the card has clarified (its own timing applies) or ended. False: expire.
func (s *Service) keepWaiting(p *pending) bool {
	for {
		s.fleet.mu.Lock()
		a := p.prepared
		if a == nil || a.terminal || p.claimed || s.fleet.interactions[p.ix.ID] != p || a.clarified || a.paused ||
			a.caller.Err() != nil {
			s.fleet.mu.Unlock()
			return false
		}
		if attempt := a.attempt; attempt != nil {
			s.fleet.mu.Unlock()
			<-attempt.done
			continue
		}
		// Taken like a first question: no other attempt starts until this one ends.
		attempt := &clarifyAttempt{done: make(chan struct{}), first: true}
		a.attempt = attempt
		ix := p.ix
		s.fleet.mu.Unlock()

		err := s.approvalJournalStarted(context.Background(), ix)
		s.fleet.mu.Lock()
		a.attempt = nil
		close(attempt.done)
		ended := a.terminal || p.claimed || s.fleet.interactions[p.ix.ID] != p
		if err != nil || ended || a.caller.Err() != nil {
			s.fleet.mu.Unlock()
			if err != nil {
				s.logf("approval %s: could not keep the unanswered card waiting, it expires: %v", ix.ID, err)
				return false
			}
			s.approvalJournalAborted(context.Background(), ix.ID)
			return ended // ended meanwhile: nothing more to do; its caller gone: it expires as before
		}
		a.clarified, a.waiting = true, true
		p.ix.ExpiresAt = time.Now().Add(approvalWaitingWindow).UTC()
		a.decisionDeadline = p.ix.ExpiresAt
		ix = p.ix
		if a.decisionTimer != nil {
			a.decisionTimer.Stop()
		}
		a.decisionTimer = time.AfterFunc(approvalWaitingWindow, func() { s.expirePrepared(p) })
		s.fleet.mu.Unlock()
		s.show(ix) // the apps keep the card, with its new deadline
		close(a.release)
		return true
	}
}

// approvalNudgeDeadline returns the live effective deadline for a card the Lark
// sweep is about to nudge. A question pauses that deadline; completed pauses have
// already moved the published deadline by their cumulative duration. Reading the
// prepared interaction here also follows an implementation that extends its window.
func (s *Service) approvalNudgeDeadline(ix proto.Interaction) (time.Time, bool) {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	p := s.fleet.interactions[ix.ID]
	if p == nil {
		// The hub lookup immediately before this check is authoritative for an
		// ordinary interaction that has no process-local prepared approval.
		return ix.ExpiresAt, true
	}
	if p.claimed || p.resolution != nil {
		return time.Time{}, false
	}
	a := p.prepared
	if a == nil {
		return p.ix.ExpiresAt, true
	}
	if a.terminal || a.paused {
		return time.Time{}, false
	}
	return p.ix.ExpiresAt, true
}

// pausePreparedLocked preserves the remaining decision time and publishes the
// actual cumulative-pause deadline. Repeated questions while paused change neither.
func (s *Service) pausePreparedLocked(p *pending) bool {
	a := p.prepared
	if a.paused || a.terminal {
		return false
	}
	now := time.Now()
	a.remaining = max(0, a.decisionDeadline.Sub(now))
	if a.decisionTimer != nil {
		a.decisionTimer.Stop()
	}
	a.paused = true
	a.pauseStarted = now
	p.ix.ExpiresAt = now.Add(clarificationPauseBudget - a.pausedTotal).UTC()
	s.armPauseBudgetLocked(p)
	return true
}

func (s *Service) armPauseBudgetLocked(p *pending) {
	a := p.prepared
	left := clarificationPauseBudget - a.pausedTotal
	if a.paused {
		left -= time.Since(a.pauseStarted)
	}
	if a.pauseTimer != nil {
		a.pauseTimer.Stop()
	}
	a.pauseTimer = time.AfterFunc(max(0, left), func() {
		s.endPreparedIf(p, "expired", "", func(a *preparedApproval) bool {
			return a.paused && a.pausedTotal+time.Since(a.pauseStarted) >= clarificationPauseBudget
		})
	})
}

// resumePreparedLocked is called when the turn covering every accepted
// question has committed its answer (and every required channel run has
// finished). A stale timer callback cannot resume a new pause. The returned card
// tells clients the resumed decision deadline without another arrival or push.
func (s *Service) resumePreparedLocked(p *pending) (proto.Interaction, bool) {
	a := p.prepared
	if !a.paused || a.terminal || a.attempt != nil || a.questionSeq != a.acceptedSeq || a.acceptedSeq != a.answeredSeq || len(a.channelRuns) != 0 {
		return proto.Interaction{}, false
	}
	now := time.Now()
	a.pausedTotal += now.Sub(a.pauseStarted)
	a.paused = false
	if a.pauseTimer != nil {
		a.pauseTimer.Stop()
		a.pauseTimer = nil
	}
	a.decisionDeadline = now.Add(a.remaining).UTC()
	p.ix.ExpiresAt = a.decisionDeadline
	a.decisionTimer = time.AfterFunc(a.remaining, func() { s.expirePrepared(p) })
	return p.ix, true
}

// clarificationTurnStarted associates the new turn with questions accepted
// before it started. The original approval turn does not count.
func (s *Service) clarificationTurnStarted(machine, session string) {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	for _, p := range s.fleet.interactions {
		a := p.prepared
		if a == nil || !a.clarified || a.terminal || p.ix.MachineID != machine || p.ix.SessionID != session {
			continue
		}
		a.turnActive = true
		a.turnSeq = a.acceptedSeq
	}
}

func (s *Service) clarificationTurnEnded(machine, session string) {
	s.fleet.mu.Lock()
	var tells []proto.Interaction
	for _, p := range s.fleet.interactions {
		a := p.prepared
		if a == nil || !a.clarified || a.terminal || p.ix.MachineID != machine || p.ix.SessionID != session || !a.turnActive {
			continue
		}
		a.turnActive = false
		a.answeredSeq = max(a.answeredSeq, a.turnSeq)
		if ix, ok := s.resumePreparedLocked(p); ok {
			tells = append(tells, ix)
		}
	}
	s.fleet.mu.Unlock()
	for _, ix := range tells {
		s.show(ix)
	}
}

func clarificationOriginKey(origin *clarificationOrigin) string {
	if origin == nil {
		return ""
	}
	return origin.Channel + "\x00" + origin.ChatID + "\x00" + origin.MessageID
}

func validateClarifyQuestion(text string) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return errors.New("question must not be blank")
	}
	if utf8.RuneCountInString(trimmed) > clarifyQuestionRunes {
		return fmt.Errorf("question is too long (at most %d characters)", clarifyQuestionRunes)
	}
	return nil
}

// clarify delivers verbatim owner text to the exact originating conversation.
// Validation has no side effects. A final answer racing delivery waits for the
// attempt, so exactly one of clarification and that answer claims the next
// state. Different questions submitted concurrently are processed in order;
// only a replay of the same delivery is deduplicated.
func (s *Service) clarify(ctx context.Context, id, text, clientMsgID string, origin *clarificationOrigin) error {
	if err := validateClarifyQuestion(text); err != nil {
		return err
	}
	if clientMsgID == "" {
		clientMsgID = "approval-question-" + id + "-" + randomHex(6)
	}
	originKey := clarificationOriginKey(origin)

	s.fleet.mu.Lock()
	p := s.fleet.interactions[id]
	if p == nil || p.prepared == nil || !p.ix.CanClarify {
		s.fleet.mu.Unlock()
		return errClarifyUnavailable
	}
	a := p.prepared
	if old, ok := a.deliveries[clientMsgID]; ok {
		s.fleet.mu.Unlock()
		if old.text != text || old.originKey != originKey {
			return errors.New("client_msg_id was already used for a different question")
		}
		return nil
	}
	if a.terminal || p.claimed {
		s.fleet.mu.Unlock()
		return errClarifyUnavailable
	}
	if running := a.attempt; running != nil {
		s.fleet.mu.Unlock()
		<-running.done
		if running.clientMsgID == clientMsgID {
			if running.text != text || running.originKey != originKey {
				return errors.New("client_msg_id was already used for a different question")
			}
			return running.err
		}
		return s.clarify(ctx, id, text, clientMsgID, origin)
	}
	attempt := &clarifyAttempt{done: make(chan struct{}), first: !a.clarified,
		clientMsgID: clientMsgID, text: text, originKey: originKey}
	a.attempt = attempt
	s.fleet.mu.Unlock()

	if attempt.first {
		if err := s.approvalJournalStarted(ctx, p.ix); err != nil {
			s.finishClarifyAttempt(p, attempt, nil, err)
			return err
		}
	}

	// Validate the exact destination before changing expiry or releasing the
	// tool. SendParams rejects a missing/ended conversation; no main-chat fallback.
	questionParams := map[string]any{"session_id": p.ix.SessionID, "text": text, "client_msg_id": clientMsgID}
	if token := assistantwire.ClarificationProofOf(ctx); token != "" {
		questionParams[assistantwire.ClarificationProofParam] = token
		questionParams[assistantwire.ClarificationInteractionParam] = id
	}
	params, err := s.SendParams(ctx, SendParams{MachineID: p.ix.MachineID, SessionID: p.ix.SessionID, Params: questionParams, VerbatimText: true})
	if err != nil {
		s.finishClarifyAttempt(p, attempt, nil, err)
		return err
	}
	var run *channel.Run
	if origin != nil && origin.Channel == channel.Feishu {
		// Base is captured before the owner's message, as for every channel run:
		// the watcher must read this question's answer, not an older turn.
		run = &channel.Run{ID: "approval-" + id + "-" + randomHex(4), Channel: channel.Feishu,
			MachineID: p.ix.MachineID, SessionID: p.ix.SessionID, TargetKind: channel.TargetConversation,
			ReplyTo: origin.MessageID, Started: time.Now().UTC(), Base: s.lastSeq(ctx, p.ix.MachineID, p.ix.SessionID),
			MsgIDs: []string{clientMsgID}, Ext: map[string]string{"clarify": id, "chat": origin.ChatID}}
	}
	s.UserSpoke(ctx, SessionRef{MachineID: p.ix.MachineID, SessionID: p.ix.SessionID})
	if p.ix.Private {
		// The owner's question starts a new turn after the original Just me turn
		// may have ended. Mark that answer turn before dispatch, not at its end.
		s.justMeSent(p.ix.MachineID, p.ix.SessionID)
	}
	err = s.sendOnce(ctx, p.ix.MachineID, params)
	if err != nil && !relayInDoubt(err) {
		s.finishClarifyAttempt(p, attempt, nil, err)
		return err
	}
	if err != nil {
		// The request may already have been accepted. Rolling back into a blocked
		// tool call could strand that owner message behind it, so the card moves
		// fail-closed into Clarifying and the stable delivery ID deduplicates a
		// retry instead.
		s.logf("approval %s: question delivery outcome uncertain; retaining the pending card: %v", id, err)
		err = nil
	}
	s.finishClarifyAttempt(p, attempt, run, nil)
	return nil
}

func (s *Service) finishClarifyAttempt(p *pending, attempt *clarifyAttempt, run *channel.Run, err error) {
	s.fleet.mu.Lock()
	a := p.prepared
	attempt.err = err
	release := false
	var tell proto.Interaction
	tellDeadline := false
	if err == nil && !a.terminal && !p.claimed && s.fleet.interactions[p.ix.ID] == p {
		release = attempt.first && !a.clarified
		a.clarified = true
		a.questionSeq++
		a.acceptedSeq = a.questionSeq
		a.deliveries[attempt.clientMsgID] = clarifyDelivery{text: attempt.text, originKey: attempt.originKey}
		if run != nil {
			if a.channelRuns == nil {
				a.channelRuns = map[string]bool{}
			}
			a.channelRuns[run.ID] = true
		}
		tellDeadline = s.pausePreparedLocked(p)
		tell = p.ix
	} else if err == nil {
		err = errClarifyUnavailable
		attempt.err = err
	}
	a.attempt = nil
	if !a.terminal {
		if ix, ok := s.resumePreparedLocked(p); ok {
			tell, tellDeadline = ix, true
		}
	}
	close(attempt.done)
	s.fleet.mu.Unlock()

	if tellDeadline {
		s.show(tell)
	}
	if err != nil && attempt.first {
		s.approvalJournalAborted(context.Background(), p.ix.ID)
	}
	if run != nil && attempt.err == nil {
		s.startChannelRun(run)
	}
	if release {
		close(a.release)
	}
}

// clarificationChannelEnded resumes expiry after this Lark answer has either
// been delivered or its failure has been reported. Other overlapping answers
// keep their own entries and continue to hold the pause.
func (s *Service) clarificationChannelEnded(id, runID string) {
	s.fleet.mu.Lock()
	p := s.fleet.interactions[id]
	if p == nil || p.prepared == nil {
		s.fleet.mu.Unlock()
		return
	}
	delete(p.prepared.channelRuns, runID)
	ix, tell := s.resumePreparedLocked(p)
	s.fleet.mu.Unlock()
	if tell {
		s.show(ix)
	}
}

// approvalAnswerTurnStarted/Ended are small indirections used by Service's turn
// events and tests.
func (s *Service) approvalAnswerTurnStarted(r SessionRef) {
	s.clarificationTurnStarted(r.MachineID, r.SessionID)
}
func (s *Service) approvalAnswerTurnEnded(r SessionRef) {
	s.clarificationTurnEnded(r.MachineID, r.SessionID)
}

// approvalBook is defined here; its durable journal implementation is in
// approval_journal.go.
type approvalBook struct {
	mu         sync.Mutex
	loaded     bool
	list       []approvalJournalEntry
	delivering bool
	again      bool
	retry      bool
}
