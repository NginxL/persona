package assistant

import (
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// How loud a track may be (#13472, docs/assistant/health.md "Tracks and kinds"): a push from a track's conversation
// that is not a card (notify_user, a turn's finished push) carries at most its track's push_cap (attention.push_cap
// of its effective config). The manager settles the level before the hub gets the push, so the hub needs nothing new.
// A card (a question, an approval, a decision) keeps its own level: what needs the owner is never held back by it.

// pushLevelRank orders the push levels, the most interrupting first.
func pushLevelRank(level string) int {
	switch level {
	case assistantwire.LevelP1:
		return 1
	case assistantwire.LevelP2:
		return 2
	}
	return 0
}

// impliedLevel is the level a push that is no card has: its own, or for "" the one the hub derives for a conversation
// that is not the main one (hub push_grading.go levelFor, fleet_link.go pushGradingNotifyLevel): a finished turn is
// P2, anything else P1. A track's conversation is never the main one.
func impliedLevel(kind, level string) string {
	switch {
	case level != "":
		return level
	case kind == assistantwire.NotifyDone:
		return assistantwire.LevelP2
	}
	return assistantwire.LevelP1
}

// cappedLevel is the level of a push of kind with level, held to at most limit: level itself (even "", for the hub to
// derive) when what it implies is no louder, else limit. A limit of P0 (or none) leaves every level as it is.
func cappedLevel(kind, level, limit string) string {
	if pushLevelRank(limit) == 0 || pushLevelRank(impliedLevel(kind, level)) >= pushLevelRank(limit) {
		return level
	}
	return limit
}

// cardPush: n carries a card (a question or an approval, a to-do's decision), which push_cap never lowers.
func cardPush(n Notification) bool {
	return n.Interaction != nil || n.Todo != nil || n.Kind == assistantwire.NotifyInput || n.Kind == assistantwire.NotifyDecide
}

// anyTrackCapped: some track that is not archived caps its pushes below P0, so a push may need its conversation's
// track looked up at all.
func (s *Service) anyTrackCapped(ctx context.Context) bool {
	return slices.ContainsFunc(s.tracks(ctx), func(e trackEntry) bool {
		return !e.Archived && pushLevelRank(e.Config.Attention.PushCap) > 0
	})
}

// trackPushCap is the push_cap of the track conversation sid runs in: "" for the main space, a conversation whose track
// cannot be told, or a track the registry does not know (it keeps the level it was given).
func (s *Service) trackPushCap(ctx context.Context, sid string) string {
	if sid == "" {
		return ""
	}
	space, known := s.conversationSpace(ctx, sid)
	if !known || space == "" {
		return ""
	}
	if e, ok := s.trackEntryOf(ctx, space); ok {
		return e.Config.Attention.PushCap
	}
	return ""
}

// capTrackPush holds push n, when it is no card, to its conversation's track's push_cap. With no track capped below P0
// (every built-in kind, the health space) it looks nothing up and changes nothing.
func (s *Service) capTrackPush(ctx context.Context, n *Notification) {
	if cardPush(*n) || n.SessionID == "" || !s.anyTrackCapped(ctx) {
		return
	}
	n.Level = cappedLevel(n.Kind, n.Level, s.trackPushCap(ctx, n.SessionID))
}
