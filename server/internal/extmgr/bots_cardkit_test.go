package extmgr

import (
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/feishubot"
)

// The assistant's card entities (CardKit, feishubot/cardkit.go): it makes one, sends it with feishu.post by its id,
// then writes into it — the whole card, an element's text streamed, its streaming turned off — each write with the
// sequence it gives.
func TestCardEntityCalls(t *testing.T) {
	w := newBotWorld(t)
	w.pair(t)
	card := feishubot.NewCard("", "", "Working").MarkdownID("live", "").Streaming().JSON()
	id, _ := w.feishu(t, assistantwire.FeishuCardCreate, map[string]any{"card": card})["card_id"].(string)
	if id == "" {
		t.Fatal("no card id")
	}
	w.api.find(t, "POST /open-apis/cardkit/v1/cards ", `"type":"card_json"`)
	if mid, _ := w.feishu(t, assistantwire.FeishuPost, map[string]any{"card": feishubot.CardEntityContent(id)})["message_id"].(string); mid == "" {
		t.Fatal("the entity was not sent")
	}
	w.api.find(t, "POST /open-apis/im/v1/messages", `"msg_type":"interactive"`, id)
	w.feishu(t, assistantwire.FeishuCardUpdate, map[string]any{"card_id": id, "card": card, "sequence": 5})
	w.api.find(t, "PUT /open-apis/cardkit/v1/cards/"+id+" ", `"sequence":5`)
	w.feishu(t, assistantwire.FeishuCardStream, map[string]any{"card_id": id, "element": "live", "text": "hello", "sequence": 6})
	w.api.find(t, "PUT /open-apis/cardkit/v1/cards/"+id+"/elements/live/content", `"content":"hello"`, `"sequence":6`)
	w.feishu(t, assistantwire.FeishuCardSettings, map[string]any{"card_id": id, "streaming": false, "sequence": 7})
	w.api.find(t, "PATCH /open-apis/cardkit/v1/cards/"+id+"/settings", `"sequence":7`, `streaming_mode\":false`)
}
