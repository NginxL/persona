package proto

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSourcePermissionDigestSurvivesRPCEncoding(t *testing.T) {
	raw := json.RawMessage("{\n \"text\": \"a < b & c\", \"nested\": {\"a\": 1} }")
	p := SourcePermission{At: time.Now(), Session: "session", Generation: "process", Class: "bypass", Digest: SourceCallDigest("agent_send", raw)}
	envelope, _ := json.Marshal(map[string]any{"args": raw})
	var received struct {
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(envelope, &received); err != nil {
		t.Fatal(err)
	}
	if !p.Matches("session", "agent_send", received.Args) {
		t.Fatalf("RPC changed digest: %s", envelope)
	}
	if p.Matches("another", "agent_send", received.Args) || p.Matches("session", "other_tool", received.Args) || p.Matches("session", "agent_send", json.RawMessage(`{"text":"other"}`)) {
		t.Fatal("proof reused for a different call")
	}
}
