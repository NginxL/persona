package node

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestRequestedModelSurvivesStateReloadAndGatewayEcho(t *testing.T) {
	for _, model := range []string{"auto/large", ""} {
		t.Run(model, func(t *testing.T) {
			// The hub persists this state, not the gateway's reply name.
			saved, err := json.Marshal(map[string]sessionState{"chat": {Launcher: "gw", Model: model}})
			if err != nil {
				t.Fatal(err)
			}
			var restored map[string]sessionState
			if err = json.Unmarshal(saved, &restored); err != nil {
				t.Fatal(err)
			}
			n := &Node{state: newState()}
			n.state.merge(restored)
			row := proto.Session{SessionID: "chat", Model: "group/auto-large"}
			n.decorateState(&row, true)
			wire, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			var got proto.Session
			if err = json.Unmarshal(wire, &got); err != nil {
				t.Fatal(err)
			}
			if got.RequestedModel == nil || *got.RequestedModel != model || got.Model != "group/auto-large" {
				t.Fatalf("model identities lost: %+v", got)
			}
			n.state.put("chat", sessionState{Launcher: "gw", Model: "new/model"})
			if *row.RequestedModel != model {
				t.Fatal("earlier snapshot changed with session state")
			}
		})
	}
	n := &Node{state: newState()}
	row := proto.Session{SessionID: "observed", Model: "reported/model"}
	n.decorateState(&row, true)
	if row.RequestedModel != nil {
		t.Fatal("an observed session has no requested model evidence")
	}
}
