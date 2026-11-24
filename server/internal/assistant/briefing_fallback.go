package assistant

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// briefReportStart keeps a Report's ordinary model input unchanged (north-star lines only), while retaining the full
// fixed snapshot for a possible model-failure fallback. briefSent attaches it without a normal feed item, briefClaim
// and briefPush ignore it on success, so a successful Report has exactly its old card/feed/push behavior.
func (s *Service) briefReportStart(ctx context.Context, t Task, now time.Time) string {
	run := &briefRun{facts: s.briefFactsAt(ctx, now, t.ID), label: t.label(), fallbackOnly: true}
	b := &s.brief
	b.mu.Lock()
	if b.byTask == nil {
		b.byTask = map[string]*briefRun{}
	}
	b.byTask[t.ID] = run
	b.mu.Unlock()
	if _, ok := s.store().(northStarStore); !ok {
		return ""
	}
	return prompts.RenderNorthStarFacts(run.facts.NorthStarLines)
}

// briefFailure says this scheduled turn has full fixed facts whose model answer failed. Its ordinary error mirror and
// finished push are suppressed; watchScheduledRun sends the one durable fallback instead.
func (s *Service) briefFailure(session, asked string, cut bool) bool {
	if !cut || s.briefOf(session) == nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(stripContext(asked)), "⏰")
}

func briefFallbackKey(run ScheduleRun) string {
	return run.RefID + "@" + run.Due.UTC().Format(time.RFC3339Nano)
}

// claimBriefFallback persists the receipt before any channel is asked to send. A receipt write that fails fails closed:
// no unremembered card goes out, and the next attempt can try the durable claim again. The map is separate from the
// 200-entry display history, so ordinary run trimming cannot forget it.
func (s *Service) claimBriefFallback(ctx context.Context, run ScheduleRun) bool {
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return false
	}
	defer sc.mu.Unlock()
	key := briefFallbackKey(run)
	if _, ok := sc.briefFallbacks[key]; ok {
		return false
	}
	next := maps.Clone(sc.briefFallbacks)
	if next == nil {
		next = map[string]time.Time{}
	}
	next[key] = time.Now().UTC()
	if st := s.store(); st != nil {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := st.PutSetting(wctx, briefFallbacksKey, next); err != nil {
			s.logf("briefing fallback receipt %s: %v", key, err)
			return false
		}
	}
	sc.briefFallbacks = next
	return true
}

// briefFallback is the one notification path for a failed Facts/Report turn. Retry failures and a new hub on the same
// store see the same routine+due receipt. A later successful retry still uses briefMirror/briefPush normally and does
// not retract this card.
func (s *Service) briefFallback(ctx context.Context, scheduled ScheduleRun, t Task, m Device) {
	s.briefFallbackRun(ctx, scheduled, t, m, s.briefOf(scheduled.SessionID))
}

// briefFallbackRun delivers one retained snapshot. A started turn finds it by session; a run that failed before its
// conversation started passes the by-task snapshot directly. Both consume the same routine+due receipt.
func (s *Service) briefFallbackRun(ctx context.Context, scheduled ScheduleRun, t Task, m Device, run *briefRun) {
	if !t.Facts && !t.Report {
		return
	}
	if run == nil {
		s.logf("task %s: model failed but its briefing facts are no longer available", t.ID)
		return
	}
	if !s.claimBriefFallback(ctx, scheduled) {
		return
	}
	l := run.facts.l
	header := i18n.T(l, "assistant-briefing.fallback_header")
	id := briefFallbackID(scheduled)
	card, plain, shown := s.briefCardWithHeader(run.facts, run.label, "", header)
	card.Buttons(s.openButton(ctx, m.ID, scheduled.SessionID))
	cards := splitBriefAnswerCards(card, plain, header)

	// The app item has its own deterministic ID, so even a caller retried within this process cannot append it twice.
	body := run.facts.body(s, run.label)
	body.Notice = header
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	if _, err := s.appendFeedOnce(fctx, "feed_brief_fallback_"+id, scheduled.SessionID, feedBrief, scheduled.RefID, body); err != nil {
		s.logf("briefing fallback feed %s: %v", t.ID, err)
	}
	cancel()

	if feat, ready := s.chat().Features(ctx); ready && feat.Pushes {
		mirror := &mirrorSend{l: l, cards: cards,
			target: botTarget{Kind: targetPersona, MachineID: m.ID, SessionID: scheduled.SessionID}, label: run.label,
			key: "f" + id, sent: func() { s.briefAsked(run.facts, shown) }}
		// The fallback has no assistant answer of its own. Track the failed turn that is actually last, so a transient
		// Feishu failure can retry, but a newer answer still makes this old occurrence too late to post.
		_, mirror.answer, mirror.turn, _ = s.channelLastTurnOf(ctx, m.ID, scheduled.SessionID)
		s.feishuMirrorPost(ctx, mirror, 0)
	}

	// deliver preserves this stable notification ID (push/pushResult would replace it with a random one).
	s.noteAsked(scheduled.SessionID)
	s.deliverCard(cardBriefFallback, Notification{ID: "ntf_brief_fallback_" + id, Kind: assistantwire.NotifyDone, DeviceID: m.ID, Subtitle: m.Name,
		SessionID: scheduled.SessionID, Title: run.label, Body: plain})
	s.briefListed(run.facts)
}

// briefNotStarted covers model resolution and conversation-start failures, where no watcher exists. The fixed facts go
// to the task's conversation, or main when this run intended to create one. A Facts/Report retry keeps the original due;
// ordinary tasks never enter this path and retain oneOffNotStarted's old failure notification and retry behavior.
func (s *Service) briefNotStarted(ctx context.Context, t Task, failed ScheduleRun, retry bool) {
	scheduled := failed
	if scheduled.SessionID == "" {
		scheduled.SessionID = t.SessionID
	}
	if scheduled.SessionID == "" {
		scheduled.SessionID = t.RunSessionID
	}
	cfg := s.settings(ctx)
	if scheduled.SessionID == "" {
		scheduled.SessionID = cfg.Main
	}
	machine := scheduled.MachineID
	if machine == "" {
		machine = t.MachineID
	}
	if machine == "" {
		machine = cfg.MachineID
	}
	if m, ok := s.device(ctx, machine); ok {
		scheduled.MachineID = m.ID
		s.briefFallbackRun(ctx, scheduled, t, m, s.briefOfTask(t.ID))
	} else {
		s.logf("task %s: model failed before a conversation started and no device can deliver its briefing facts", t.ID)
	}
	if !retry || failed.Retried || failed.Manual {
		return
	}
	delay := runRetryDelay
	if s.runRetry > 0 {
		delay = s.runRetry
	}
	go func() {
		time.Sleep(delay)
		rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cur, ok := s.retryStillDue(rctx, t.ID, failed)
		cancel()
		if !ok {
			return
		}
		if _, err := s.runTask(cur, failed.Due, time.Now(), false, true); err != nil {
			s.logf("task %s briefing retry: %v", t.ID, err)
		}
	}()
}
