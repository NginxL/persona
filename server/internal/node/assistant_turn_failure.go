package node

import (
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/modelretry"
	"github.com/nwverse/persona/server/internal/proto"
)

// The scheduler owns a strict task result after the node's bounded recovery. A generic Continue would run
// without the task's execution constraints. Keep the original failure text for the schedule and the user.
func classifyTaskTurnFailure(s *proto.Session) {
	if s.Status != proto.StatusIdle && s.Status != proto.StatusDone {
		return
	}
	text := s.TurnError
	if text == "" && assistantwire.CutOff(s.LastText) {
		text = s.LastText
	}
	if text != "" && (s.TurnErrorKind == proto.TurnErrorTransient || s.TurnErrorKind == "" && modelretry.Temporary(text)) {
		s.TurnError, s.TurnErrorKind = text, proto.TurnErrorUser
	}
}

// claudeTurnFailure is how a Claude assistant turn failed, read off its stream-json result (#4669): its text (secrets,
// URLs and addresses taken out) and kind, "" when it did not fail. It failed when the result is flagged is_error with
// words in it, or when its whole answer is a model-service error line (assistantwire.ErrorOnlyText: "Invalid API key ·
// Fix external API key" can come as an ordinary result). An interrupt the user asked for is no failure, nor is an
// is_error result with no words (nothing to say what went wrong). kinds is what the transcript said of the turn
// (claudeResultKinds); its kind wins, else an auth error is the user's to fix.
func claudeTurnFailure(isError bool, result string, kinds []string) (text, kind string) {
	r := strings.TrimSpace(result)
	if r == "" || interruptedResult(r) {
		return "", ""
	}
	if !isError && !assistantwire.ErrorOnlyText(r) {
		return "", ""
	}
	text = clipText(redactCheck(r), 400)
	if len(kinds) > 0 && kinds[0] != "" {
		return text, kinds[0]
	}
	if assistantwire.AuthErrorText(r) {
		return text, proto.TurnErrorUser
	}
	return text, ""
}

// interruptedResult: a result that says the user (or Persona on their behalf) stopped the turn.
func interruptedResult(r string) bool {
	t := strings.ToLower(r)
	for _, w := range []string{"interrupted by user", "request was aborted", "aborted by user", "user aborted"} {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}
