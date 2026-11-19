package assistant

import (
	"context"
	"crypto/rand"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

func (s *Service) inboxRetryRoutes() {
	s.handle("POST /api/v1/inbox/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) && r.Header.Get(RoleHeader) != roleExtension {
			apierr.Write(w, http.StatusForbidden, errAnswerOnly)
			return
		}
		if !s.inboxUp(w, r) {
			return
		}
		it, status, code, err := s.retryInboxHandling(r.Context(), r.PathValue("id"))
		if status == http.StatusConflict {
			body := apierr.LocalizedBody(status, code, err, i18n.OfWriter(w))
			body["item"] = it
			apierr.WriteBody(w, status, body)
		} else if err != nil {
			apierr.Write(w, status, err)
		} else {
			apierr.WriteJSON(w, it)
		}
	})
}

// retryInboxHandling takes an open failed item once and saves its recovery before scheduling any work.
func (s *Service) retryInboxHandling(ctx context.Context, id string) (InboxItem, int, string, error) {
	cfg := s.inboxConfig(ctx).Agent
	b := &s.inbox
	b.mu.Lock()
	it := b.items[id]
	if it == nil {
		b.mu.Unlock()
		return InboxItem{}, http.StatusNotFound, "", errNoInboxItem
	}
	failed := it.Handling != nil && it.Handling.State == HandlingFailed ||
		it.Handling == nil && it.Agent != nil && it.Agent.State == AgentFailed
	if !it.open() || !failed || currentSuggestion(it) != nil || (it.Agent != nil && it.Agent.savingRetry) {
		out := it.listed()
		b.mu.Unlock()
		return out, http.StatusConflict, "inbox_retry_stale", i18n.ErrEnglish("inbox-errors.retry_stale")
	}
	if !cfg.Enabled || b.policyTwin(it) == TwinNever {
		key := "assistant-inbox_agent.disabled"
		if cfg.Enabled {
			key = "assistant-inbox_agent.never"
		}
		out := it.listed()
		b.mu.Unlock()
		return out, http.StatusConflict, "inbox_retry_disabled", i18n.ErrEnglish(key)
	}

	w := beginWrite(it)
	now := time.Now().UTC()
	a := &InboxAgent{Task: it.Summary, Reason: it.Reason}
	if it.Agent != nil {
		*a = *it.Agent
		a.Pending = slices.Clone(a.Pending)
		// The explicit retry schedules delivery of every retained note, including held source notices.
		for i := range a.Pending {
			a.Pending[i].Deferred = false
		}
	}
	reason := a.Error
	if h := it.Handling; h != nil {
		reason = h.Error
		if a.SessionID == "" {
			a.MachineID, a.SessionID = h.MachineID, h.SessionID
		}
	}
	// The superseded turn cannot write suggestions or finish an in-flight delivery against the new attempt.
	a.ProcessingTurn = rand.Text()
	a.Back, a.Wake, a.DeferredAt, a.WakeSlot = "", false, nil, false
	a.Attempts, a.RetryAfter, a.StartedAt, a.DraftedAt = 0, nil, &now, nil
	a.savingRetry = true
	it.Agent = a
	// Of advances when escalation queues source messages. The pending notes above already carry those messages.
	if a.SessionID != "" && a.Of != it.Last && it.Context != "" {
		b.tell(it, inboxNote{Name: prompts.NoteNewMessages, Context: it.Context}, false, now)
	}
	a.Of = it.Last
	resetInboxFollowChecks(it)
	b.chainAdd(it, InboxChainEntry{Kind: ChainInstructed, At: now, Via: ViaUser,
		Text: i18n.T(b.userLang(), "assistant-inbox_chain.retry_requested"), Detail: reason})
	b.tell(it, inboxNote{Name: prompts.NoteInstructed, Retry: true,
		Text: i18n.T(b.userLang(), "assistant-inbox_agent.retry_instruction", i18n.Args{"reason": reason})}, false, now)
	if inboxLatestAgentLink(it) != nil {
		// Sweep checks the linked Agent first. Its stop report will release the held retry note.
		a.State = AgentRunning
		b.setHandling(it, HandlingAgent, "", now)
	} else {
		b.wakeInboxRetry(it, now)
	}
	b.settle(w)
	b.mu.Unlock()

	failedWrites := s.saveWrites(ctx, []*inboxWrite{w}, "its handling retry")
	b.mu.Lock()
	a.savingRetry = false
	out := it.listed()
	b.mu.Unlock()
	if failedWrites[id] {
		return InboxItem{}, http.StatusServiceUnavailable, "", errInboxNotPersisted
	}
	s.handInbox(ctx)
	return out, http.StatusOK, "", nil
}

// wakeInboxRetry resumes processing after the linked Agent stopped or was found missing. The caller holds inbox.mu.
func (b *inboxBook) wakeInboxRetry(it *InboxItem, now time.Time) {
	b.wakeInboxConversation(it, now)
	it.Agent.Back = ""
	b.setHandling(it, HandlingLooking, "", now)
}
