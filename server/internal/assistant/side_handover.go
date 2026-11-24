package assistant

import (
	"context"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A review a pre-#5732 side-supervised ledger leaves waiting (#8043, docs/assistant/unified-entry.md "Supervision").
// New conversation-dispatched tasks belong to the main conversation; this compatibility path covers an old ledger
// that still names a side conversation. Its wait for review was told once per review (watchSignals), so a conversation
// that was no longer used, or whose turn ended without task_review, could leave the task awaiting review for good.
//
// So a task that has waited for a side conversation's review for sideReviewAfter, while that conversation is not in a
// turn, is handed to the main conversation: its ledger names the main conversation as its supervisor from then on,
// and the review is signalled once more, saying why. digestTo keeps that signal with main even when a legacy
// background conversation is configured. The move is the record: a task the main conversation supervises is not looked
// at again, so it happens once, also across a restart. The origin stays: the side conversation keeps the task's card
// and gets the one-line notice of how it ended (taskEndedNotice); acceptance stays with main.
//
// Left as they were: a leader conversation's task (its supervisor is what places it in the direction the owner set up,
// direction_tree.go, so moving it would take it out of that direction; its signals already go to the main conversation
// once the leader is gone or its direction ended), a task leader's worker (task_leader.go), and the other signals of a
// side conversation's task.

// sideReviewAfter is how long a task may wait for a side conversation's review before the review is handed to the
// main conversation's follow-up.
var sideReviewAfter = 30 * time.Minute

// handOverUnreviewed, from the supervision loop's watch (superviseWatch): the tasks that waited for a side conversation's review past
// sideReviewAfter go to the main conversation's follow-up (handOverReview).
func (s *Service) handOverUnreviewed(ctx context.Context, now time.Time) {
	cfg := s.settings(ctx)
	if cfg.Main == "" || cfg.MachineID == "" {
		return
	}
	away := map[string]bool{} // each side conversation is asked after once
	for _, t := range s.todosOf(ctx, "*") {
		v := t.Supervision
		if v == nil || t.DoneAt != nil || v.state(t) != reviewPending || now.Sub(v.UpdatedAt) < sideReviewAfter {
			continue
		}
		side := v.Supervisor
		if !s.sideSupervisor(ctx, cfg, side) {
			continue
		}
		if _, asked := away[side]; !asked {
			away[side] = s.conversationAway(ctx, cfg, side)
		}
		if away[side] {
			s.handOverReview(ctx, t, side, cfg.Main, now)
		}
	}
}

// sideSupervisor: supervisor is a legacy side conversation that supervised what it handed out: not the main
// conversation, a leader conversation, the background supervisor or a task leader's agent.
func (s *Service) sideSupervisor(ctx context.Context, cfg Settings, supervisor string) bool {
	return supervisor != "" && supervisor != cfg.Main && !s.isLeader(ctx, supervisor) && !s.isBackground(ctx, supervisor) &&
		!s.isLeaderRef(supervisor)
}

// conversationAway: conversation id of the assistant's machine is not in a turn, as far as the machine can say: it is
// listed and neither working nor starting (a turn may be the review itself, and what is queued behind a turn goes in
// when it ends), it is no assistant conversation any more, or it is gone from the list. A machine that cannot say now
// says nothing: neither does a list with no assistant conversation at all, which is what the manager has from its
// start until the node has connected and listed (backgroundThere).
func (s *Service) conversationAway(ctx context.Context, cfg Settings, id string) bool {
	inTurn := func(c proto.Session) bool { return c.Assistant && (c.Status == proto.StatusWorking || c.Starting) }
	if c, ok := s.session(ctx, cfg.MachineID, id); ok {
		return !inTurn(c)
	}
	if d, ok := s.device(ctx, cfg.MachineID); !ok || !d.Online || !d.Reachable {
		return false
	}
	list, err := s.sessions(ctx, cfg.MachineID)
	if err != nil {
		return false
	}
	listed := false
	for _, c := range list {
		if c.SessionID == id {
			return !inTurn(c)
		}
		listed = listed || c.Assistant
	}
	return listed
}

// handOverReview makes conversation to (the main one) the supervisor of task t, which waited for side conversation
// side's review, and signals the review again: under the key of the ledger as it is now, and with what was said of it
// before forgotten, so that it is told where it goes now and, the ledger no longer naming a side conversation, only
// this once. A task reviewed, sent back to work or moved meanwhile is left alone.
func (s *Service) handOverReview(ctx context.Context, t Todo, side, to string, now time.Time) {
	var waited time.Duration
	v, changed, err := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
		if v.Supervisor != side || v.state(t) != reviewPending {
			return false
		}
		waited = now.Sub(v.UpdatedAt)
		v.Supervisor = to
		return true
	})
	if err != nil {
		s.logf("task %s: hand its review over to the main conversation: %v", t.ID, err) // tried again at the next pass
		return
	}
	if !changed {
		return
	}
	s.logf("task %s: conversation %s left its review waiting for %s; the main conversation's follow-up takes it over",
		t.ID, short(side), waited.Round(time.Minute))
	g := superviseSignal{Todo: t.ID, Kind: sigReview, Key: reviewKey(&v),
		Detail: fmt.Sprintf("派出它的对话过了 %d 分钟没验收，转给你验收", int(waited.Minutes()))} // i18n-ignore: the supervision message is the assistant's prompt
	b := &s.supervise
	b.dedupMu.Lock()
	s.forgetSupervision(ctx, []superviseSignal{g}, s.webBase(ctx)) // said to the side conversation within the window: not said here yet
	b.dedupMu.Unlock()
	s.signal(g)
}
