package assistant

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const approvalJournalKey = "assistant.approval_clarifications"

var errApprovalConversationGone = errors.New("the originating conversation no longer exists")

// approvalJournalEntry contains delivery state only. In particular it has no
// executable callback, tool arguments, Resolution.Values or approval fields.
type approvalJournalEntry struct {
	ID        string    `json:"id"`
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	Status    string    `json:"status,omitempty"`
	Result    string    `json:"result,omitempty"`
	Private   bool      `json:"private,omitempty"`
	Accepted  bool      `json:"accepted,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Service) loadApprovalJournalLocked(ctx context.Context) bool {
	b := &s.approvals
	if b.loaded {
		return true
	}
	var list []approvalJournalEntry
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, approvalJournalKey, &list); err != nil {
			s.logf("load approval clarification journal: %v", err)
			return false
		}
	}
	b.list, b.loaded = list, true
	return true
}

func (s *Service) saveApprovalJournalLocked(ctx context.Context) error {
	if st := s.store(); st != nil {
		list := s.approvals.list
		if list == nil {
			list = []approvalJournalEntry{}
		}
		return st.PutSetting(ctx, approvalJournalKey, list)
	}
	return nil
}

func (s *Service) approvalJournalStarted(ctx context.Context, ix proto.Interaction) error {
	b := &s.approvals
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadApprovalJournalLocked(ctx) {
		return errors.New("the clarification journal is unavailable")
	}
	if slices.ContainsFunc(b.list, func(a approvalJournalEntry) bool { return a.ID == ix.ID }) {
		return nil
	}
	b.list = append(b.list, approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID, Private: ix.Private, UpdatedAt: time.Now().UTC()})
	if err := s.saveApprovalJournalLocked(ctx); err != nil {
		b.list = slices.DeleteFunc(b.list, func(a approvalJournalEntry) bool { return a.ID == ix.ID })
		return fmt.Errorf("keep the clarification result journal: %w", err)
	}
	return nil
}

// approvalJournalApplying is persisted immediately before the retained
// operation executes. A restart in this state has an uncertain outcome, never a
// fabricated cancellation or success.
func (s *Service) approvalJournalApplying(ctx context.Context, ix proto.Interaction) error {
	b := &s.approvals
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadApprovalJournalLocked(ctx) {
		return errors.New("the clarification journal is unavailable")
	}
	i := slices.IndexFunc(b.list, func(a approvalJournalEntry) bool { return a.ID == ix.ID })
	if i < 0 {
		b.list = append(b.list, approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID, Private: ix.Private})
		i = len(b.list) - 1
	} else if ix.Private {
		b.list[i].Private = true
	}
	if b.list[i].Result != "" {
		return errors.New("the approval already has a terminal result")
	}
	b.list[i].Status = "applying"
	b.list[i].UpdatedAt = time.Now().UTC()
	if err := s.saveApprovalJournalLocked(ctx); err != nil {
		return fmt.Errorf("save approval execution state: %w", err)
	}
	return nil
}

func (s *Service) approvalJournalAborted(ctx context.Context, id string) {
	b := &s.approvals
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadApprovalJournalLocked(ctx) {
		return
	}
	b.list = slices.DeleteFunc(b.list, func(a approvalJournalEntry) bool { return a.ID == id && a.Result == "" && !a.Accepted })
	if err := s.saveApprovalJournalLocked(ctx); err != nil {
		s.logf("abort approval clarification journal: %v", err)
	}
}

// approvalResultText always names the literal interaction ID and says that the
// original card is no longer waiting, including when execution is uncertain.
func approvalResultText(id, status, text string) string {
	word := map[string]string{
		"resolved": "applied", "denied": "denied", "expired": "expired",
		"replaced": "replaced", "stale": "stale and cancelled", "cancelled": "cancelled", "dismissed": "dismissed",
		"failed": "left with an uncertain execution outcome",
	}[status]
	if word == "" {
		word = "finished"
	}
	return fmt.Sprintf("Approval %s is %s and is no longer pending. %s", id, word, text)
}

func (s *Service) approvalJournalFinished(ctx context.Context, ix proto.Interaction, status, text string) {
	b := &s.approvals
	b.mu.Lock()
	if !s.loadApprovalJournalLocked(ctx) {
		b.mu.Unlock()
		return
	}
	i := slices.IndexFunc(b.list, func(a approvalJournalEntry) bool { return a.ID == ix.ID })
	if i < 0 {
		b.list = append(b.list, approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID, Private: ix.Private})
		i = len(b.list) - 1
	} else if ix.Private {
		// Upgrade a legacy or recovered entry before its terminal delivery.
		b.list[i].Private = true
	}
	if b.list[i].Result == "" {
		b.list[i].Status = status
		b.list[i].Result = approvalResultText(ix.ID, status, text)
		b.list[i].UpdatedAt = time.Now().UTC()
	}
	if err := s.saveApprovalJournalLocked(ctx); err != nil {
		s.logf("save approval result %s: %v", ix.ID, err)
	}
	b.mu.Unlock()
	go s.deliverApprovalResults()
}

// deliverApprovalResults hands each result to the exact conversation or its
// durable relay queue. There is deliberately no fallback to today's main chat.
// A single drain prevents concurrent accepts; a result added during a drain
// triggers one more pass. Transient failures wait for the retry timer; a result
// handed to the conversation or durable queue is removed. A result is
// permanently undeliverable only when the deletion ledger names its original
// conversation; an uncertain execution outcome stays durable even then.
func (s *Service) deliverApprovalResults() {
	b := &s.approvals
	b.mu.Lock()
	if b.delivering {
		b.again = true
		b.mu.Unlock()
		return
	}
	if !s.loadApprovalJournalLocked(context.Background()) {
		b.mu.Unlock()
		return
	}
	b.delivering = true
	for {
		b.again = false
		var due []approvalJournalEntry
		kept := b.list[:0]
		cleaned := false
		for _, a := range b.list {
			if a.Accepted {
				// Versions before result cleanup retained handed-off entries forever.
				cleaned = true
				continue
			}
			kept = append(kept, a)
			if a.Result != "" {
				due = append(due, a)
			}
		}
		b.list = kept
		if cleaned {
			if err := s.saveApprovalJournalLocked(context.Background()); err != nil {
				s.logf("clean handed-off approval results: %v", err)
			}
		}
		b.mu.Unlock()
		failed := false
		for _, a := range due {
			err := s.deliverApprovalResult(a)
			gone := errors.Is(err, errApprovalConversationGone)
			if err != nil && !gone {
				failed = true
				s.logf("deliver approval result %s: %v", a.ID, err)
				continue
			}
			if gone && a.Status == "failed" {
				// A deleted destination cannot receive the warning, but its durable
				// record is the last evidence that execution may have happened.
				s.logf("retain uncertain approval result %s for recovery: %v", a.ID, err)
				continue
			}
			if gone {
				s.logf("discard approval result %s: %v", a.ID, err)
			}
			b.mu.Lock()
			before := len(b.list)
			b.list = slices.DeleteFunc(b.list, func(x approvalJournalEntry) bool { return x.ID == a.ID })
			if len(b.list) != before {
				if err := s.saveApprovalJournalLocked(context.Background()); err != nil {
					s.logf("remove handed-off approval result %s: %v", a.ID, err)
				}
			}
			b.mu.Unlock()
		}
		b.mu.Lock()
		if failed {
			s.scheduleApprovalResultRetryLocked()
		}
		if !b.again {
			b.delivering = false
			b.mu.Unlock()
			return
		}
	}
}

func (s *Service) scheduleApprovalResultRetryLocked() {
	b := &s.approvals
	if b.retry {
		return
	}
	b.retry = true
	time.AfterFunc(time.Minute, func() {
		b.mu.Lock()
		b.retry = false
		b.mu.Unlock()
		s.deliverApprovalResults()
	})
}

func (s *Service) deliverApprovalResult(a approvalJournalEntry) error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, deleted, err := s.deletedConversation(ctx, a.MachineID, a.SessionID); err != nil {
		return fmt.Errorf("check the originating conversation's deletion record: %w", err)
	} else if deleted {
		return errApprovalConversationGone
	}
	var res struct {
		OK bool `json:"ok"`
	}
	if err := s.call(ctx, a.MachineID, DeviceSessionGet, map[string]any{"session_id": a.SessionID}, &res); err != nil {
		return err
	}
	if !res.OK {
		// session.get reads the manager's current cache. A restart, an offline
		// node, or a list that has not arrived yet says nothing about deletion.
		return errors.New("the originating conversation is not currently listed")
	}
	msg := relayMsg{From: "approval-result-" + a.ID, FromTitle: "Approval result", Tool: askAnswerTool,
		Text: a.Result, At: a.UpdatedAt, Private: a.Private}
	if msg.At.IsZero() {
		msg.At = time.Now().UTC()
	}
	working := false
	if c, ok := s.session(ctx, a.MachineID, a.SessionID); ok {
		working = c.Status == proto.StatusWorking || c.Starting
	}
	if !working {
		if err := s.tryDirectRelay(ctx, a.MachineID, a.SessionID, &msg); err == nil {
			return nil
		} else if !relayInDoubt(err) {
			// A transient send refusal may still be retried through the durable
			// queue; an actually missing conversation was rejected above.
			s.logf("approval result %s direct send: %v; queued", a.ID, err)
		}
	}
	return s.queueApprovalResult(a.MachineID, a.SessionID, msg)
}

// queueApprovalResult deduplicates by the stable once_id before persisting.
func (s *Service) queueApprovalResult(machine, to string, msg relayMsg) error {
	b := &s.relays
	b.mu.Lock()
	if !s.relayLocked(context.Background()) {
		b.mu.Unlock()
		return errors.New("the relay queue is unavailable")
	}
	id := relayMsgID(to, msg)
	for _, old := range b.st.Queue[to] {
		if relayMsgID(to, old) == id {
			b.mu.Unlock()
			return nil
		}
	}
	msg.QueuedAt, msg.Depth = time.Now().UTC(), len(b.st.Queue[to])+1
	b.st.Queue[to] = append(b.st.Queue[to], msg)
	s.saveRelayLocked()
	b.mu.Unlock()
	if c, ok := s.session(context.Background(), machine, to); ok {
		s.drainRelays(machine, []proto.Session{c})
	}
	return nil
}

// recoverApprovalJournals is fail-closed: no process-local operation survives a
// manager restart. An interrupted apply is explicitly uncertain; any other
// unfinished clarified card is cancelled without executing.
func (s *Service) recoverApprovalJournals(ctx context.Context) {
	b := &s.approvals
	b.mu.Lock()
	if !s.loadApprovalJournalLocked(ctx) {
		b.mu.Unlock()
		return
	}
	var unfinished []approvalJournalEntry
	for i := range b.list {
		if b.list[i].Result != "" {
			continue
		}
		if b.list[i].Status == "applying" {
			b.list[i].Status = "failed"
			b.list[i].Result = approvalResultText(b.list[i].ID, "failed", "The assistant restarted while the approved operation was executing. Its execution outcome is uncertain: check current state before retrying; do not assume it applied or that nothing changed.")
		} else {
			b.list[i].Status = "cancelled"
			b.list[i].Result = approvalResultText(b.list[i].ID, "cancelled", "The assistant restarted, so the retained operation was cancelled without executing. Ask for a fresh approval if the change is still wanted.")
		}
		b.list[i].UpdatedAt = time.Now().UTC()
		unfinished = append(unfinished, b.list[i])
	}
	if len(unfinished) > 0 {
		if err := s.saveApprovalJournalLocked(ctx); err != nil {
			s.logf("save restarted approval outcomes: %v", err)
		}
	}
	b.mu.Unlock()
	for _, a := range unfinished {
		s.fleet.mu.Lock()
		p := s.fleet.interactions[a.ID]
		s.fleet.mu.Unlock()
		if p != nil && p.prepared != nil {
			s.endPrepared(p, "cancelled", "The assistant restarted; nothing was applied unless the result says its outcome is uncertain.")
		}
	}
	s.deliverApprovalResults()
}
