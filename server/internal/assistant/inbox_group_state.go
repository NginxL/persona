package assistant

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/prompts"
)

// applyGroupStates takes the source's state reports out of its batches. A report names an existing grouped event and
// carries only what the owner did in the source; all item content and rules in it are ignored. Normal batches are
// returned untouched for relateBatches.
//
// Reports repeat until the event takes new messages. A reply is therefore recorded once per Last position, and a read
// only while Read is false; group.append clears Read when new messages arrive. A read caused by Persona's own reply,
// or following a reply already recorded at that position, only sets Read. With the AI twin off, these changes are
// still recorded. With the twin off, an existing conversation gets no note or wake and is not moved to Looking.
func (s *Service) applyGroupStates(ctx context.Context, src assistantwire.InboxSource, things []connector.InboxThing) ([]connector.InboxThing, []*InboxItem) {
	rest := things[:0:0]
	var states []connector.InboxThing
	for _, t := range things {
		if t.Group != nil && t.Group.Batch == "" && len(t.Group.New) == 0 {
			states = append(states, t)
		} else {
			rest = append(rest, t)
		}
	}
	if len(states) == 0 {
		return rest, nil
	}

	twinEnabled := s.inboxConfig(ctx).Agent.Enabled
	loc := s.location(ctx)
	now := time.Now()
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	defer b.settleRound()
	var changed []*InboxItem
	for _, t := range states {
		it := b.items[t.ID]
		if it == nil || !it.open() || it.Group == nil || it.Group.Connector != src.ID ||
			!sameConversation(it.Group.Thread, t.Thread) || t.Last != it.Last {
			continue
		}
		replied := t.Done == connector.DoneReplied
		if !replied && !t.Read {
			continue
		}
		hasConversation := it.Agent != nil && it.Agent.SessionID != ""
		own := ownSend(it, t.Last)
		if replied && own {
			continue // A confirmed reply Persona sent keeps the event waiting for the other side.
		}

		if replied {
			if groupStateHasSelfReply(it, t.Last) {
				continue
			}
			b.roundWrite(it)
			b.noteSelfReplied(it, now)
			entry := &it.Chain[len(it.Chain)-1]
			entry.Via = it.Source
			entry.Link.Type = LinkSource
			entry.Link.Position = t.Last
			if hasConversation {
				if twinEnabled {
					at := t.At
					if at.IsZero() {
						at = now
					}
					b.tell(it, inboxNote{Name: prompts.NoteSelfReplied, Text: at.In(loc).Format("2006-01-02 15:04")}, true, now)
					b.setHandling(it, HandlingLooking, "", now)
				}
			} else {
				it.done(now, connector.DoneReplied)
				b.closeHandling(it, "self_replied", now)
			}
		} else {
			if it.Read {
				continue
			}
			b.roundWrite(it)
			it.Read = true
			switch {
			case own || groupStateHasSelfReply(it, t.Last):
				// Persona's own send, or a reply already recorded at this position, is not a new read by the owner.
			case hasConversation:
				if twinEnabled {
					b.tell(it, inboxNote{Name: prompts.NoteSelfRead}, true, now)
					b.setHandling(it, HandlingLooking, "", now)
				}
			case it.Tier == TierFYI || it.Tier == TierNoise:
				b.file(it, now, connector.DoneRead)
				b.closeHandling(it, "read", now)
			}
		}
		if !containsItem(changed, it) {
			changed = append(changed, it)
		}
	}
	return rest, changed
}

// groupStateHasSelfReply says whether this position already has the reply's idempotency marker.
func groupStateHasSelfReply(it *InboxItem, last string) bool {
	for _, e := range it.Chain {
		if e.Kind == ChainSelfReplied && e.Link != nil && e.Link.Position == last {
			return true
		}
	}
	return false
}
