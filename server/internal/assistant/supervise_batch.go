package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

const maxSupervisionBatchMinutes = 1440

// supervisionBatchMinutesOf is the additional per-recipient routine batching interval. Zero keeps the legacy shared
// gather and spacing exactly as they were. Invalid saved values fail back to that behavior.
func supervisionBatchMinutesOf(v Settings) int {
	if v.SupervisionBatchMinutes == nil || *v.SupervisionBatchMinutes < 0 || *v.SupervisionBatchMinutes > maxSupervisionBatchMinutes {
		return 0
	}
	return *v.SupervisionBatchMinutes
}

func (s *Service) supervisionBatchInterval(ctx context.Context) time.Duration {
	return time.Duration(supervisionBatchMinutesOf(s.settings(ctx))) * time.Minute
}

// supervisionBatchImmediate bypasses only the optional per-recipient batch. The legacy 15-second gather still applies.
// A recipient's whole digest goes together when any signal in it is immediate.
func supervisionBatchImmediate(g superviseSignal) bool {
	switch g.Kind {
	case sigNeedsYou, sigAsk, sigEscalated, sigDecide, sigDecisionReply, sigParkDecided, sigOwnerCannot, sigRecheck, sigUndecidable, sigFailed:
		return true
	case sigCheck: // the owner's answer to a check's decision; the check's own news can wait for the batch
		return g.Report == checkDecidedReport
	}
	return false
}

// digestWaitWithBatch is digestWait while optional batching is off. With it on, recipient batch deadlines replace the
// legacy shared last-message gap, so one recipient cannot hold up another. New routine signals make one scheduling pass
// after the ordinary gather, where gateDigestBatches assigns their recipient's deadline.
func digestWaitWithBatch(now, last time.Time, pending []superviseSignal, p superviseTimings, batch time.Duration) (time.Duration, bool) {
	if batch <= 0 {
		return digestWait(now, last, pending, p)
	}
	var due time.Time
	for _, g := range pending {
		at := g.At.Add(p.gather)
		if g.NotBefore.After(at) {
			at = g.NotBefore
		}
		if batch > 0 && !supervisionBatchImmediate(g) && g.batchUntil.After(at) {
			at = g.batchUntil
		}
		if due.IsZero() || at.Before(due) {
			due = at
		}
	}
	if due.IsZero() {
		return 0, false
	}
	return max(0, due.Sub(now)), true
}

// supervisionBatchDeferred reports whether g stays pending while another recipient or an immediate signal is delivered.
func supervisionBatchDeferred(g superviseSignal, now time.Time) bool {
	return !supervisionBatchImmediate(g) && g.batchUntil.After(now.Add(time.Second))
}

// gateDigestBatches holds routine groups until their own recipient's deadline. An immediate group also releases that
// recipient's previously held routine signals, so a mixed digest is delivered whole. Returned groups still use the
// existing deliverDigest path, including its busy-recipient relay queue.
func (s *Service) gateDigestBatches(order []string, groups map[string][]superviseSignal, now time.Time, interval time.Duration) []string {
	if interval <= 0 {
		return order
	}
	out := make([]string, 0, len(order))
	for _, to := range order {
		list := groups[to]
		immediate := slices.ContainsFunc(list, supervisionBatchImmediate)
		b := &s.supervise
		b.mu.Lock()
		deadline := b.batch[to]
		b.mu.Unlock()
		if !immediate {
			if deadline.IsZero() {
				anchor := now
				for _, g := range list {
					if !g.At.IsZero() && g.At.Before(anchor) {
						anchor = g.At
					}
				}
				deadline = anchor.Add(interval)
			}
			if deadline.After(now.Add(time.Second)) {
				s.holdDigestBatch(to, deadline, list)
				delete(groups, to)
				continue
			}
		}
		groups[to] = s.releaseDigestBatch(to, list)
		out = append(out, to)
	}
	return out
}

// holdDigestBatch returns an unaccepted group to pending with one recipient deadline. A newer copy queued meanwhile
// stays newer and inherits the same deadline; neither copy is marked sent.
func (s *Service) holdDigestBatch(to string, deadline time.Time, list []superviseSignal) {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	if b.batch == nil {
		b.batch = map[string]time.Time{}
	}
	b.batch[to] = deadline
	for _, g := range list {
		if b.sent[g.id()] == g.Key {
			delete(b.sent, g.id())
		}
		if cur, ok := b.pending[g.id()]; ok {
			g = cur
		}
		g.batchTo, g.batchUntil = to, deadline
		b.pending[g.id()] = g
	}
}

// releaseDigestBatch takes every held signal for to, merges newer copies queued while this digest was being built,
// clears its batch clock and reserves the released signals in the existing early sent cache.
func (s *Service) releaseDigestBatch(to string, current []superviseSignal) []superviseSignal {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	var held []superviseSignal
	for id, g := range b.pending {
		if g.batchTo != to {
			continue
		}
		held = append(held, g)
		delete(b.pending, id)
	}
	delete(b.batch, to)
	out := mergeSignals(current, held)
	for i := range out {
		out[i].batchTo, out[i].batchUntil = "", time.Time{}
		b.sent[out[i].id()] = out[i].Key
	}
	return out
}

// rescheduleSupervision makes a settings write take effect without waiting for an old timer. Existing nonzero batch
// deadlines stay anchored; setting zero releases them through the legacy scheduler on the newly scheduled pass.
func (s *Service) rescheduleSupervision() {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) > 0 {
		s.scheduleDigestLocked(time.Now())
	}
}

func (s *Service) setSupervisionBatchMinutes(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var input *int
	if err := json.Unmarshal(raw, &input); err != nil || input == nil || *input < 0 || *input > maxSupervisionBatchMinutes {
		return nil, i18n.Err("assistant-settings_tool.bad_supervision_batch_minutes")
	}
	value := *input
	before := supervisionBatchMinutesOf(s.settings(ctx))
	if value == before {
		return &ownChange{summary: i18n.T(s.lang(ctx), "assistant-settings_tool.supervision_batch_minutes_unchanged")}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_supervision_batch_minutes", i18n.Args{"before": before, "after": value})
	}, apply: func(ctx context.Context) (string, error) {
		if _, err := s.updateSettings(ctx, func(v *Settings) error { v.SupervisionBatchMinutes = &value; return nil }); err != nil {
			return "", err
		}
		s.settingsChanged(ctx)
		s.rescheduleSupervision()
		return i18n.T(s.lang(ctx), "assistant-settings_tool.supervision_batch_minutes_saved", i18n.Args{"value": value}), nil
	}}, nil
}
