package extmgr

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botport"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
)

// Obtain status, then change the actual routing snapshot before AppCall: the assistant-side status check alone
// cannot authorize a send to the new recipient. No real provider or account is contacted.
func TestFeishuReminderBindingAtActualRoute(t *testing.T) {
	needsLarkModule(t)
	for _, change := range []string{"none", "owner", "generation", "pushes off", "explicit chat"} {
		t.Run(change, func(t *testing.T) {
			api := newFakeOpenAPI(t)
			h := newTestManager(t)
			h.bots.loaded = true
			h.bots.api = botport.New("cli_test", "synthetic", api.srv.URL)
			h.bots.set = FeishuBotSettings{Enabled: true, Owner: &BotOwner{OpenID: "ou_a", ChatID: "oc_a", At: time.Unix(100, 0)}, Features: BotFeatures{Pushes: true}}
			status, err := h.feishuCall(context.Background(), assistantwire.FeishuStatusGet, nil)
			if err != nil {
				t.Fatal(err)
			}
			st := status.(assistantwire.FeishuStatus)
			params := map[string]any{"card": botcard.NewCard("", "", "Reminder").Markdown("Synthetic notes").JSON(), "uuid": "todo-bound-card0", "todo_reminder_pairing": map[string]any{"owner": st.Owner, "paired_at": st.PairedAt}}
			h.bots.mu.Lock()
			switch change {
			case "owner":
				h.bots.set.Owner = &BotOwner{OpenID: "ou_b", ChatID: "oc_b", At: st.PairedAt}
			case "generation":
				h.bots.set.Owner.At = st.PairedAt.Add(time.Second)
			case "pushes off":
				h.bots.set.Features.Pushes = false
			case "explicit chat":
				params["chat_id"] = "oc_a"
			}
			h.bots.mu.Unlock()
			raw, _ := json.Marshal(params)
			_, handled, err := h.AppCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppFeishu, Method: assistantwire.FeishuPost, Params: raw})
			if !handled {
				t.Fatal("real AppCall route not reached")
			}
			api.mu.Lock()
			calls := append([]string(nil), api.calls...)
			api.mu.Unlock()
			if change == "none" {
				if err != nil || len(calls) == 0 {
					t.Fatalf("matching reminder refused: %v %v", err, calls)
				}
			} else if err == nil || len(calls) != 0 {
				t.Fatalf("changed authority reached transport: err=%v calls=%v", err, calls)
			}
		})
	}
}
