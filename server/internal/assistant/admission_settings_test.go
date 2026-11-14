package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// assistant.admission is one of persona_settings' own settings, so the assistant sees it in action list and can read
// and (after the phone approval card) change it.
func TestAdmissionSettingIsListedAndReadable(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	r := s.Tool(context.Background(), ToolCall{Name: "persona_settings",
		Arguments: json.RawMessage(`{"action":"list"}`)})
	if !strings.Contains(r.Content[0].Text, admissionSettingsKey) {
		t.Fatalf("the settings list does not name %s: %s", admissionSettingsKey, r.Content[0].Text)
	}
	r = s.Tool(context.Background(), ToolCall{Name: "persona_settings",
		Arguments: json.RawMessage(`{"action":"get","key":"assistant.admission"}`)})
	var got AdmissionSettings
	if err := json.Unmarshal([]byte(r.Content[0].Text), &got); err != nil {
		t.Fatalf("get must return a valid AdmissionSettings JSON object: %v (%q)", err, r.Content[0].Text)
	}
}

// A non-positive line is refused before any approval card: the positiveOr* fallback must not silently ignore it.
func TestAdmissionSettingRejectsNonPositiveLines(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	bad := []string{
		`{"direction_cap":0}`,
		`{"direction_cap_by_direction":{"dir-1":-2}}`,
		`{"machines":{"m1":{"max_claude_procs":0}}}`,
	}
	for _, v := range bad {
		r := s.Tool(context.Background(), ToolCall{Name: "persona_settings", MachineID: "mac", SessionID: "main-1",
			Arguments: json.RawMessage(`{"action":"set","key":"assistant.admission","value":` + v + `}`)})
		if !r.IsError || !strings.Contains(r.Content[0].Text, "positive") {
			t.Fatalf("value %s must be refused as non-positive, got %q", v, r.Content[0].Text)
		}
	}
}

// A valid change reaches the phone as an approval card and, on the user's allow, is stored and read by the next gate
// decision: the settings cache is dropped instead of serving the pre-approval lines.
func TestAdmissionSettingChangeAfterApproval(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	setSettings(t, s, func(v *Settings) { v.Main, v.Leaders = "main-1", nil })
	// Prime the cache with the code defaults.
	before := s.admissionSettings(context.Background())

	done := make(chan string, 1)
	go func() {
		r := s.Tool(context.Background(), ToolCall{Name: "persona_settings", MachineID: "mac", SessionID: "main-1",
			Arguments: json.RawMessage(`{"action":"set","key":"assistant.admission","value":{"direction_cap_by_direction":{"dir-9":2}}}`)})
		done <- r.Content[0].Text
	}()
	card, ok := awaitCard(s)
	if !ok {
		t.Fatal("no approval card")
	}
	yes := true
	if _, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: card.ID,
		Answer: json.RawMessage(mustMarshal(proto.Resolution{Allow: &yes}))}); err != nil {
		t.Fatal(err)
	}
	out := resultOf(t, done, 5*time.Second)
	if !strings.Contains(out, "updated") {
		t.Fatalf("the approved change was not applied: %q", out)
	}
	// The cache now carries the approved lines and resolves the direction's cap from its new map.
	got := s.admissionSettings(context.Background())
	if got.DirectionCapByDirection["dir-9"] != 2 {
		t.Fatalf("approved lines not read back: %+v (before: %+v)", got, before)
	}
	if cap := s.directionCap(context.Background(), "dir-9", "", got); cap != 2 {
		t.Fatalf("the gate must use the approved direction line, got %d", cap)
	}
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
