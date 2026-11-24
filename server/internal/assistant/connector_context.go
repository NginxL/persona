package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/connector"
)

// connectorMainTrack is the main track's name in a connector call's context, as PERSONA_SPACE says it.
const connectorMainTrack = "main"

// connectorCallContext is where a connector call from conversation sid comes from (#13246,
// docs/protocol/connector-protocol.md "Call context"): its track, the track's kind, and the conversation's
// information categories as a whole (conversationUse), left out when not known (a track the manager cannot tell, a
// store that cannot be read, a turn waiting to be tagged): the connector takes that for sensitive.
func (s *Service) connectorCallContext(ctx context.Context, sid string) connector.CallContext {
	space, known := s.conversationSpace(ctx, sid)
	if !known {
		return connector.CallContext{}
	}
	out := connector.CallContext{Track: connectorMainTrack}
	if space != "" {
		out.Track = space
		if e, ok := s.trackEntryOf(ctx, space); ok {
			out.TrackKind = e.Kind
		}
	}
	if info := s.conversationUse(ctx, sid); info.Known {
		out.Categories = append([]string{}, info.Categories...)
	}
	return out
}
