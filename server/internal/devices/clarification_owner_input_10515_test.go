package devices

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

// Clarifications use session.send_once. Only the devices boundary may turn the
// hub's verified device/human submission into owner_input; a manager, an
// unverified caller, or another submission kind cannot forge that attestation.
func TestClarificationOwnerInputAtDevicesBoundary10515(t *testing.T) {
	const question = "  Why is this change needed?\nPlease answer without approving it.  "
	device := &devicewire.Caller{Kind: "device", CredentialID: "owner-device"}
	human := &devicewire.Submission{ID: "clarification-submission-10515", Kind: "human_input", OriginalText: question}
	for _, tc := range []struct {
		name       string
		call       devicewire.NodeCall
		params     string
		wantOwner  bool
		wantClient string
	}{
		{
			name: "hub verified clarification proof",
			call: devicewire.NodeCall{MachineID: "box", Method: "session.send_once", Caller: device, Submission: human},
			params: `{"session_id":"s1","text":"  Why is this change needed?\nPlease answer without approving it.  ",` +
				`"client_msg_id":"owner-question-10515"}`,
			wantOwner: true, wantClient: "owner-question-10515",
		},
		{
			name: "manager cannot forge verified proof",
			call: devicewire.NodeCall{MachineID: "box", Method: "session.send_once",
				Caller: &devicewire.Caller{Kind: "system"}, Submission: human},
			params:    `{"session_id":"s1","text":"question","client_msg_id":"manager-forgery","owner_input":true,"OWNER_INPUT":true}`,
			wantOwner: false, wantClient: "manager-forgery",
		},
		{
			name:      "device without human submission cannot forge proof",
			call:      devicewire.NodeCall{MachineID: "box", Method: "session.send_once", Caller: device},
			params:    `{"session_id":"s1","text":"question","client_msg_id":"device-forgery","owner_input":true}`,
			wantOwner: false, wantClient: "device-forgery",
		},
		{
			name: "relay submission is not owner input",
			call: devicewire.NodeCall{MachineID: "box", Method: "session.send_once", Caller: device,
				Submission: &devicewire.Submission{ID: "relay-10515", Kind: "relay", OriginalText: question}},
			params:    `{"session_id":"s1","text":"question","client_msg_id":"relay-forgery","owner_input":true}`,
			wantOwner: false, wantClient: "relay-forgery",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDevices(t)
			n := &testNode{info: &proto.NodeInfo{Version: "test", Protocol: nodeproto.Version, Capabilities: nodeproto.Announced}}
			connectedMachine(t, d, "box", n)
			tc.call.Params = json.RawMessage(tc.params)
			if _, err := d.Call(context.Background(), tc.call); err != nil {
				t.Fatalf("clarification send_once: %v", err)
			}
			calls := n.seen("session.send_once ")
			if len(calls) != 1 {
				t.Fatalf("node received %d clarification sends, want one", len(calls))
			}
			raw := strings.TrimPrefix(calls[0], "session.send_once ")
			var fields map[string]json.RawMessage
			var asNode struct {
				OwnerInput bool   `json:"owner_input"`
				Text       string `json:"text"`
				ClientID   string `json:"client_msg_id"`
			}
			if json.Unmarshal([]byte(raw), &fields) != nil || json.Unmarshal([]byte(raw), &asNode) != nil {
				t.Fatalf("node received invalid params: %s", raw)
			}
			marks := 0
			for key := range fields {
				if strings.EqualFold(key, "owner_input") {
					marks++
				}
			}
			if asNode.OwnerInput != tc.wantOwner || (tc.wantOwner && (marks != 1 || string(fields["owner_input"]) != "true")) || (!tc.wantOwner && marks != 0) {
				t.Errorf("node owner-input attestation = %v with %d marks, want owner=%v: %s", asNode.OwnerInput, marks, tc.wantOwner, raw)
			}
			if asNode.ClientID != tc.wantClient {
				t.Errorf("one-shot client_msg_id = %q, want %q", asNode.ClientID, tc.wantClient)
			}
			if tc.wantOwner && asNode.Text != question {
				t.Errorf("the owner's verbatim question changed before the node: %q", asNode.Text)
			}
		})
	}
}
