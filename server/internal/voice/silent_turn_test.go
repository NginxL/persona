package voice

import (
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// The hub's word on a task Agent's turn with no output (#10187): Persona's own Stop ends a run as stopped, never as a
// model service failure; a turn the model service answered nothing for is still one, said as that (no_output).
func TestHubTurnVerdictsInVoiceRuns(t *testing.T) {
	stopped := &proto.Session{TurnError: "Stopped with its conversation before it had any output", TurnErrorKind: proto.TurnErrorStopped}
	if turnFailed(stopped) || stopOf(stopped) != transcript.InterruptedNotice || (&voiceRun{}).failureOf(stopped) != "" {
		t.Fatalf("stopped: failed %v, stop %q, failure %q", turnFailed(stopped), stopOf(stopped), (&voiceRun{}).failureOf(stopped))
	}
	silent := &proto.Session{TurnError: "This turn had no output", TurnErrorKind: proto.TurnErrorNoOutput}
	if !turnFailed(silent) || (&voiceRun{}).failureOf(silent) != "no_output" {
		t.Fatalf("no output: failed %v, failure %q", turnFailed(silent), (&voiceRun{}).failureOf(silent))
	}
	cut := &proto.Session{TurnError: "API Error: 529 overloaded"}
	if !turnFailed(cut) || stopOf(cut) == transcript.InterruptedNotice {
		t.Fatalf("a model error: failed %v, stop %q", turnFailed(cut), stopOf(cut))
	}
}
