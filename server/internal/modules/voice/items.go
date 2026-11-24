package voice

import (
	"context"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// httpItems is a conversation's transcript read over the hub's HTTP API (…/items?after_seq=, …/items/{id}/full): the
// source channel.ReadTurn reads a voice run's answer from, the one copy of the rules the assistant's channels use too.
type httpItems struct {
	hub              *hubClient
	machine, session string
}

var _ channel.ItemSource = httpItems{}

func (h httpItems) ItemsAfter(ctx context.Context, after int) ([]proto.Item, bool, int, error) {
	page, err := h.hub.itemsAfter(ctx, h.machine, h.session, after)
	if err != nil {
		return nil, false, after, err
	}
	return page.Items, page.MoreAfter, page.last(), nil
}

func (h httpItems) Full(ctx context.Context, id string) (proto.Item, error) {
	return h.hub.itemFull(ctx, h.machine, h.session, id)
}
