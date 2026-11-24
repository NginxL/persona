package transcript

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/modelretry"
	"github.com/nwverse/persona/server/internal/proto"
)

// A turn that ended on a model API error, as Claude Code's own record says it (Meta.TurnError…, #1710): the reply it
// writes itself for a request that failed (an assistant line with isApiErrorMessage, model <synthetic>) names the
// error's kind, and a subscription's limit when it resets (quotaLimits). Its notices say whether it goes on by itself
// once the limit resets. Codex's record is read in codex.go (task_complete's error, the rollout's rate limits).

// ClaudeTurnStarted: a Claude turn starts, with a message or a background task waking the session: the last turn's
// error is over. Claude Code's word that it goes on by itself after a limit (TurnErrorResumes) outlasts a wake, which
// fails again at once with no new notice, but not a message: typing into the session cancels its continuation without
// a word. The node calls it for a prompt line too big to parse.
func (m *Meta) ClaudeTurnStarted(wake bool) {
	if m.ModelFallback != nil && m.ModelFallback.Scope != "session" {
		m.clearModelFallback()
	}
	resumes := m.TurnErrorResumes && wake
	m.clearTurnError()
	m.TurnErrorResumes, m.turnWorked, m.turnSucceeded, m.turnErrorCounted = resumes, false, false, false
	m.turnParts = nil
	m.resumesSaid = m.resumesSaid || !wake
}

// TurnErrorResumesSaid: a line read said whether Claude Code goes on by itself after its limit (its notices, a
// message typed, a reply, another error). False when none did: the lines read start after its notice (a tail of the
// transcript, read into a Meta of its own: the first prompt in its head says nothing of a wait at the end), and what
// an earlier read found stands.
func (m *Meta) TurnErrorResumesSaid() bool { return m.resumesSaid }

// TurnSucceeded reports that the latest Claude turn closed after at least one visible reply from the model, not a
// synthetic runtime notice, and that no model error followed it. A prompt, an unclosed reply, or a process exit is not
// evidence of success. The reply itself stays in the transcript; this is only the latest turn's outcome.
func (m *Meta) TurnSucceeded() bool {
	return !m.Codex && m.TurnClosed && m.turnSucceeded && m.TurnError == ""
}

func (m *Meta) clearTurnError() {
	m.TurnError, m.TurnErrorKind, m.TurnErrorResetsAt = "", "", time.Time{}
	m.TurnErrorCode = ""
	m.TurnErrorResumes, m.TurnErrorAfterWork, m.turnErrorLine = false, false, ""
}

// claudeReply notes what an assistant line says of the turn; blocks is its content, shown whether it showed anything.
// A reply from the model ends whatever failed before (a limit included: it is over); Claude Code's own reply for a
// failed request is the turn's error. In a session's transcript a subagent's lines (isSidechain) never get here.
func (m *Meta) claudeReply(e *entry, blocks []block, shown bool) {
	if !e.IsAPIErrorMessage {
		if shown && e.Message.Model != "<synthetic>" {
			if len(blocks) > 0 && blocks[0].Type == "text" && assistantwire.CutOff(blocks[0].Text) {
				allText := true
				var parts []string
				for _, b := range blocks {
					if b.Type != "text" {
						allText = false
						break
					}
					parts = append(parts, b.Text)
				}
				if allText {
					joined := strings.TrimSpace(strings.Join(parts, "\n"))
					// Older Claude Code wrote cut-off "API Error: …" lines without isApiErrorMessage or <synthetic>:
					// treat no-work upstream 5xx cut-offs as part of the streak rather than substantive model work.
					if !m.turnWorked && modelretry.UpstreamServerError(joined) {
						m.addUpstreamFailure(e.UUID, joined)
					} else {
						m.resetUpstreamStreak()
					}
					return
				}
			}
			m.clearTurnError()
			m.turnWorked, m.resumesSaid = true, true
			m.resetUpstreamStreak()
		}
		return
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	var code string
	_ = json.Unmarshal(e.Error, &code)
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		text = code
	}
	if text == "" {
		return
	}
	kind, reset := claudeErrorKind(code, e)
	if kind == "" {
		kind, reset = GatewayErrorKind(text, e.Timestamp)
	}
	m.TurnErrorCode = ""
	if m.ModelFallback != nil && (code == "model_not_found" || e.APIErrorStatus == 404 && fallbackModelError(text, m.ModelFallback.FallbackModel)) {
		m.TurnErrorCode, kind = proto.TurnErrorModelFallbackUnavailable, proto.TurnErrorUser
	}
	if kind != proto.TurnErrorUsageLimit {
		m.TurnErrorResumes, m.resumesSaid = false, true // only a limit's wait ends in Claude Code's own continuation
	}
	m.TurnError, m.TurnErrorKind, m.TurnErrorResetsAt = text, kind, reset
	m.TurnErrorAfterWork, m.turnErrorLine = m.turnWorked, e.UUID
	m.noteUpstreamTurnError(text, kind, e.UUID)
}

// noteUpstreamTurnError updates UpstreamErrorStreak and marks TurnErrorUpstreamUnavailable when a turn failed on an
// exhausted upstream account pool or across >= 2 consecutive no-work upstream 5xx turns (#13372).
func (m *Meta) noteUpstreamTurnError(text, kind, lineID string) {
	if !m.turnWorked && kind != proto.TurnErrorUsageLimit && kind != proto.TurnErrorUnrecoverable &&
		m.TurnErrorCode == "" && proto.UnrecoverableTurnErrorCode(text) == "" && modelretry.UpstreamServerError(text) {
		m.addUpstreamFailure(lineID, text)
	} else {
		m.resetUpstreamStreak()
	}
	if m.TurnErrorCode == "" && (modelretry.AccountExhausted(text) || m.UpstreamErrorStreak >= 2) {
		m.TurnErrorCode = proto.TurnErrorUpstreamUnavailable
	}
}

func (m *Meta) addUpstreamFailure(lineID, text string) {
	if m.turnErrorCounted {
		return
	}
	anchor := lineID
	if anchor == "" {
		anchor = m.Updated.UTC().Format(time.RFC3339Nano) + "|" + text
	}
	m.UpstreamErrorStreak++
	m.turnErrorCounted = true
	m.upstreamErrorLines = append(m.upstreamErrorLines, anchor)
}

func (m *Meta) resetUpstreamStreak() {
	m.UpstreamErrorStreak = 0
	m.upstreamStreakReset = true
	if len(m.upstreamErrorLines) > 0 {
		m.upstreamErrorLines = m.upstreamErrorLines[:0]
	}
}

// LastUpstreamLine returns the dedupe anchor of the latest counted upstream failure across Claude Code, legacy cut-off,
// and Codex rollout formats.
func (m *Meta) LastUpstreamLine() string {
	if n := len(m.upstreamErrorLines); n > 0 {
		return m.upstreamErrorLines[n-1]
	}
	return m.turnErrorLine
}

// ResolveUpstreamStreak combines this tail scan's consecutive upstream error count with the previous read's count when
// a large line pushed earlier failed turns before off without any intervening reset.
func (m *Meta) ResolveUpstreamStreak(off int64, prevStreak int, prevLine string) int {
	if prevStreak > 0 && prevLine != "" {
		for i := len(m.upstreamErrorLines) - 1; i >= 0; i-- {
			if m.upstreamErrorLines[i] == prevLine {
				return prevStreak + (len(m.upstreamErrorLines) - 1 - i)
			}
		}
	}
	if off > 0 && !m.upstreamStreakReset && prevStreak > 0 {
		return prevStreak + m.UpstreamErrorStreak
	}
	return m.UpstreamErrorStreak
}

// A 404 alone can be an endpoint error. Without the runtime's model_not_found code, require the error to name the
// fallback model or explicitly describe a missing model before attributing it to the refusal's switch.
func fallbackModelError(text, model string) bool {
	t := strings.ToLower(text)
	return model != "" && strings.Contains(t, strings.ToLower(model)) || strings.Contains(t, "model_not_found") ||
		strings.Contains(t, "model not found") || strings.Contains(t, "model does not exist") || strings.Contains(t, "selected model")
}

// claudeErrorKind sorts Claude Code's error field (2.1.2xx+): a subscription's limit (rate_limit with the account's
// quota rejected) and when it resets; sign-in and the model are the user's to fix; the service failing is worth a
// "Continue". Anything else gives no kind: an older version wrote no field, a gateway's 429 is a rate_limit without
// quotaLimits (its words say what it is), unknown is anything.
func claudeErrorKind(code string, e *entry) (kind string, reset time.Time) {
	switch code {
	case "rate_limit":
		if q := e.QuotaLimits; q != nil && q.Status == "rejected" {
			reset, _ := ParseResetsAt(q.ResetsAt)
			return proto.TurnErrorUsageLimit, reset
		}
	case "authentication_failed", "model_not_found":
		return proto.TurnErrorUser, time.Time{}
	case "server_error":
		return proto.TurnErrorTransient, time.Time{}
	}
	return "", time.Time{}
}

// ParseResetsAt reads a reset time given as a number (epoch seconds, or milliseconds when it is that large), a
// numeric string or an RFC 3339 string: Claude Code's quotaLimits.resetsAt, the usage windows' resets_at
// (node/usagewindows.go).
func ParseResetsAt(raw json.RawMessage) (time.Time, bool) {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" || s == "null" {
		return time.Time{}, false
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
		if f > 1e11 {
			return time.UnixMilli(int64(f)).UTC(), true
		}
		return time.Unix(int64(f), 0).UTC(), true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

// claudeLimitNotice reads Claude Code's notices on going on by itself after a usage limit (system/informational,
// 2.1.282+; the texts can change by remote config, the words matched are the ones that stay). "Usage limit reached ·
// continuing automatically at 4:20pm · esc or type to cancel" (and "… reached again …") say it will; "Automatic
// continue cancelled / was turned off / stopped / did not run …" and "Usage limit has reset · Press enter to continue"
// that it will not. "Usage limit reset · continuing automatically" and "Usage limit available again · continuing now"
// are it doing so: nothing changes until its first reply clears the error.
func (m *Meta) claudeLimitNotice(text string) {
	t := strings.ToLower(strings.TrimSpace(text))
	switch {
	case strings.HasPrefix(t, "automatic continue"), strings.Contains(t, "press enter to continue"):
		m.TurnErrorResumes, m.resumesSaid = false, true
	case strings.HasPrefix(t, "usage limit reset"), strings.HasPrefix(t, "usage limit available again"):
	case strings.Contains(t, "continuing automatically"):
		m.TurnErrorResumes, m.resumesSaid = true, true
	}
}
