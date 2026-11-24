package extmgr

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

// This synthetic schema-2.0 event follows the Open Platform's bot_p2p_chat_entered example.
func TestBotEnteredChatOffersPairing(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	payload := []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt_welcome","event_type":"im.chat.access_event.bot_p2p_chat_entered_v1",
			"create_time":"1608725989000","token":"verification_example","app_id":"cli_test","tenant_key":"tenant_example"},
		"event":{"chat_id":"oc_welcome","operator_id":{"open_id":"ou_welcome"},
			"last_message_id":"om_example","last_message_create_time":"1608725988000"}
	}`)
	if _, err := w.h.botEvent(w.ctx, payload); err != nil {
		t.Fatal(err)
	}
	call := w.api.find(t, "POST /open-apis/im/v1/messages", "oc_welcome")
	var sent struct {
		ReceiveID string `json:"receive_id"`
		MsgType   string `json:"msg_type"`
		Content   string `json:"content"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(call, " ", 3)[2]), &sent); err != nil {
		t.Fatal(err)
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(sent.Content), &content); err != nil {
		t.Fatal(err)
	}
	if sent.ReceiveID != "oc_welcome" || sent.MsgType != "text" || content.Text != i18n.T(w.h.userLang(), "hub-bots_chat.pair_hint") {
		t.Fatalf("welcome = %+v, content = %+v", sent, content)
	}
	if set := w.settings(); set.Owner != nil {
		t.Fatalf("opening the chat paired an owner: %+v", set.Owner)
	}
	if got := w.handed(); len(got) != 0 {
		t.Fatalf("welcome reached the assistant: %v", got)
	}
}
