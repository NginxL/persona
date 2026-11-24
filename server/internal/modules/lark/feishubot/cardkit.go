package feishubot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// CardKit (open.feishu.cn "Card entity"): a card made first and sent by its id (CardEntityContent as an interactive
// message's content), which can then be written into without sending the message again: the progress card's live text
// is streamed into it and comes in letter by letter (the module's stream.go). Every write carries a sequence
// that must grow for the card and a uuid; a message sent with an entity is still updated whole by UpdateCard (PATCH on
// the message), which replaces the entity's content too. Needs the app's cardkit:card:write.

// codeUUIDUsed: a write's uuid was used a moment ago, so this is a retry of one that landed.
const codeUUIDUsed = 200770

// CardCreate makes a card entity of card (its JSON 2.0) and returns its id. An entity never sent costs nothing, so a
// create that is retried is harmless.
func (c *Client) CardCreate(ctx context.Context, card string) (string, error) {
	var out struct {
		CardID string `json:"card_id"`
	}
	err := c.callRetry(ctx, "create card", http.MethodPost, "/open-apis/cardkit/v1/cards",
		map[string]any{"type": "card_json", "data": card}, &out, true)
	return out.CardID, err
}

// CardUpdate writes the whole card into entity id.
func (c *Client) CardUpdate(ctx context.Context, id, card string, seq int64) error {
	return c.cardWrite(ctx, "update card entity", http.MethodPut, "/open-apis/cardkit/v1/cards/"+url.PathEscape(id), map[string]any{
		"card": map[string]any{"type": "card_json", "data": card}, "sequence": seq, "uuid": cardUUID(id, seq)})
}

// CardStream writes text, the element's whole text, into element of entity id while it streams: what text adds to
// what the element had shows letter by letter.
func (c *Client) CardStream(ctx context.Context, id, element, text string, seq int64) error {
	return c.cardWrite(ctx, "stream card", http.MethodPut,
		"/open-apis/cardkit/v1/cards/"+url.PathEscape(id)+"/elements/"+url.PathEscape(element)+"/content",
		map[string]any{"content": text, "sequence": seq, "uuid": cardUUID(id, seq)})
}

// CardSettings turns entity id's streaming on or off.
func (c *Client) CardSettings(ctx context.Context, id string, streaming bool, seq int64) error {
	settings, _ := json.Marshal(map[string]any{"config": map[string]any{"streaming_mode": streaming}})
	return c.cardWrite(ctx, "card settings", http.MethodPatch, "/open-apis/cardkit/v1/cards/"+url.PathEscape(id)+"/settings",
		map[string]any{"settings": string(settings), "sequence": seq, "uuid": cardUUID(id, seq)})
}

// cardWrite is one write into an entity; a retry finding its uuid used landed the first time.
func (c *Client) cardWrite(ctx context.Context, op, method, path string, body map[string]any) error {
	err := c.call(ctx, op, method, path, body, nil)
	if errorCode(err) == codeUUIDUsed {
		return nil
	}
	return err
}

// cardUUID is the uuid of entity id's write seq: the same write sent again is the same uuid.
func cardUUID(id string, seq int64) string { return "ck-" + id + "-" + strconv.FormatInt(seq, 10) }
