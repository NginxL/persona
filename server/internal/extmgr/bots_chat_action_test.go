package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// Each of the owner's plain messages reaches the assistant with a fresh, one-use owner action, like a card press.
func TestBotMessageCarriesTheOwnersAction(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	if _, err := w.h.botEvent(w.ctx, botMessageEvent("om_ask", "ou_me", "text", textContent("2"), nil)); err != nil {
		t.Fatal(err)
	}
	var msg assistantwire.FeishuMessage
	eventually(t, 5*time.Second, "handed with its action", func() bool {
		for _, p := range w.a.got(assistantwire.MethodFeishuMessage) {
			if json.Unmarshal(p, &msg) == nil && strings.Contains(string(msg.Event), "om_ask") {
				return true
			}
		}
		return false
	})
	if !strings.HasPrefix(msg.Action, "oa_") {
		t.Fatalf("no owner action: %+v", msg)
	}
	take := func(action string) (err error) {
		p, _ := json.Marshal(map[string]any{"interaction_id": "ix_q1", "action": action, "channel": "feishu", "kind": "question"})
		_, _, err = w.h.AppCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppInteractions,
			Method: assistantwire.InteractionsOwnerAction, Params: p})
		return
	}
	if err := take(msg.Action); err != nil {
		t.Fatalf("the message's action refused: %v", err)
	}
	if err := take(msg.Action); !errors.Is(err, errNoOwnerAction) {
		t.Fatalf("it worked twice: %v", err)
	}
}
