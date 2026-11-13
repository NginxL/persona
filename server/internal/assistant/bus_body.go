package assistant

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devicewire"
)

// bodyStreamDial bounds dialing the relay a streamed request's body comes on (http.request_streamed, #6574).
const bodyStreamDial = 10 * time.Second

// streamedBody is the body of an http.request_streamed: the relay s names, dialed with the manager's bus token (only
// the manager the request is for may take it), read whole into one buffer before the route runs.
func (b *BusHub) streamedBody(ctx context.Context, s *assistantwire.BodyStream) ([]byte, error) {
	if s == nil || s.ID == "" {
		return nil, errors.New("no body stream")
	}
	dctx, cancel := context.WithTimeout(ctx, bodyStreamDial)
	c, err := bus.DialAt(dctx, b.opts.Hub, b.opts.Token, b.opts.HTTPClient, devicewire.RelayPath, url.Values{"id": {s.ID}}.Encode())
	cancel()
	if err != nil {
		return nil, err
	}
	return bus.ReadBody(ctx, c, s.Size)
}
