package assistant

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The strip over an empty composer displays Session.Suggestion. An agent can offer a specific next step
// when it finishes a turn. Otherwise a Claude Code terminal can supply its native hint, or an opted-in
// small model can propose one for a watched chat. All three go through the same risk check. The fill and
// send actions belong to the owner (docs/assistant/assistant.md "Next-message suggestions").

const purposeSuggestNext = "suggest.next"

const (
	suggestMaxMessages = 8
	suggestMaxChars    = 12000
	suggestMaxRunes    = 200 // a longer answer is not one message the user would send as it is
	suggestTurns       = 4   // turns read back: enough for suggestMaxMessages user and assistant messages
	suggestMaxTokens   = 1024
)

// suggestCallTimeout bounds the whole suggestion (reading the messages, the model call); tests shorten it.
var suggestCallTimeout = 45 * time.Second

type suggestedReply struct {
	text     string
	proposed bool // explicitly offered in the agent's final work report
}

// nextSuggest holds one session's visible suggestion or its in-flight read.
type nextSuggest struct {
	suggestedReply
	cancel context.CancelFunc // the call still on its way; nil once it answered
}

type nextSuggestBook struct {
	mu sync.Mutex
	m  map[[2]string]*nextSuggest // machine, session
	// Tests: watched stands in for the hub's watches, done sees every call end (its suggestion, "" for none).
	watched func(machine, sid string) bool
	done    func(machine, sid, text string)
}

// nextSuggestionsOn is the switch (next_suggestions, off by default).
func (s *Service) nextSuggestionsOn(ctx context.Context) bool {
	v := s.settings(ctx).NextSuggestions
	return v != nil && *v
}

// Standalone chats can offer a reply; a child agent cannot speak for its parent's turn.
func standaloneSuggestion(sess *proto.Session) bool {
	return !sess.IsSubagent && sess.ParentThreadID == "" && !strings.Contains(sess.SessionID, "@")
}

// claudeInTerminal: Claude Code in a terminal pane, whose suggestion the node reads off the screen.
func claudeInTerminal(sess *proto.Session) bool {
	return sess.PaneID != "" && (sess.Agent == "" || sess.Agent == "claude")
}

// sessionWatched says whether an app has the session open: the hub watches it then (session.watch).
func (s *Service) sessionWatched(machine, sid string) bool {
	b := &s.nextSug
	b.mu.Lock()
	f := b.watched
	b.mu.Unlock()
	if f != nil {
		return f(machine, sid)
	}
	return s.devs != nil && s.devs.Watched(machine, sid)
}

// suggestDrop forgets the session's suggestion and cancels a call still on its way: a turn started or a message went
// in. It reports whether there was one to drop.
func (s *Service) suggestDrop(machine, sid string) bool {
	b := &s.nextSug
	k := [2]string{machine, sid}
	b.mu.Lock()
	st := b.m[k]
	delete(b.m, k)
	b.mu.Unlock()
	if st == nil {
		return false
	}
	if st.cancel != nil {
		st.cancel()
	}
	return st.text != ""
}

// suggestTurnEnded is told of every turn that ended (working to done or idle, fleet_sessions.go setSessions): it starts
// the turn's one suggestion when the rules say so, and reports whether it did.
func (s *Service) suggestTurnEnded(machine string, sess proto.Session) bool {
	ctx := context.Background()
	if !standaloneSuggestion(&sess) || sess.PendingInteractions > 0 || sess.Starting {
		return false
	}
	if text, _ := latestTurnError(sess); text != "" { // a failed turn
		return false
	}
	// An explicit proposal belongs to this turn even if its owner opens the app later.
	// Only an opted-in, watched chat without its own terminal hint may call the model.
	model := !claudeInTerminal(&sess) && s.sessionWatched(machine, sess.SessionID) && s.nextSuggestionsOn(ctx)
	cctx, cancel := context.WithTimeout(ctx, suggestCallTimeout)
	st := &nextSuggest{cancel: cancel}
	k := [2]string{machine, sess.SessionID}
	b := &s.nextSug
	b.mu.Lock()
	if b.m == nil {
		b.m = map[[2]string]*nextSuggest{}
	}
	old := b.m[k]
	b.m[k] = st
	b.mu.Unlock()
	if old != nil && old.cancel != nil {
		old.cancel()
	}
	go func() {
		defer cancel()
		reply := s.makeSuggestion(cctx, machine, sess.SessionID, model)
		b.mu.Lock()
		if b.m[k] != st {
			reply = suggestedReply{} // a later turn owns the entry
		} else if cctx.Err() != nil || reply.text == "" {
			delete(b.m, k)
			reply = suggestedReply{}
		} else {
			st.suggestedReply, st.cancel = reply, nil
		}
		done := b.done
		b.mu.Unlock()
		if reply.text != "" {
			s.publishSessions(machine) // the apps get it with the list
		}
		if done != nil {
			done(machine, sess.SessionID, reply.text)
		}
	}()
	return true
}

// hubSuggestions snapshots the machine's current suggestions by session.
func (s *Service) hubSuggestions(machine string) map[string]suggestedReply {
	b := &s.nextSug
	b.mu.Lock()
	defer b.mu.Unlock()
	var out map[string]suggestedReply
	for k, st := range b.m {
		if k[0] == machine && st.text != "" {
			if out == nil {
				out = map[string]suggestedReply{}
			}
			out[k[1]] = st.suggestedReply
		}
	}
	return out
}

// A reported next step takes precedence over a guess from the terminal or the small model.
// Each source uses the same fill-only treatment for a risky suggestion.
func decorateSuggestion(sess *proto.Session, hub map[string]suggestedReply) {
	if r := hub[sess.SessionID]; r.text != "" && standaloneSuggestion(sess) &&
		(sess.Status == proto.StatusDone || sess.Status == proto.StatusIdle) && sess.PendingInteractions == 0 &&
		(r.proposed || sess.Suggestion == "" && !claudeInTerminal(sess)) {
		sess.Suggestion = r.text
	}
	sess.SuggestionRisky = sess.Suggestion != "" && suggestionRisky(sess.Suggestion)
}

// makeSuggestion reads one completed turn for an offered next step. A chat eligible for a
// model suggestion reads its normal context once and uses it only if the agent offered none.
func (s *Service) makeSuggestion(ctx context.Context, machine, sid string, model bool) suggestedReply {
	var res struct {
		Items []proto.Item `json:"items"`
	}
	count := 1
	if model {
		count = suggestTurns
	}
	if err := s.call(ctx, machine, DeviceSessionItems, map[string]any{"session_id": sid, "turns": count, "lazy_images": true}, &res); err != nil {
		s.logf("suggest %s: %v", short(sid), err)
		return suggestedReply{}
	}
	if step := s.reportedNextStep(ctx, machine, sid, res.Items); step != "" {
		return suggestedReply{text: step, proposed: true}
	}
	if !model || ctx.Err() != nil {
		return suggestedReply{}
	}
	turns := suggestionTurns(res.Items)
	if len(turns) == 0 {
		return suggestedReply{}
	}
	system := prompts.RenderNextMessage(prompts.NextMessage{Language: answerLanguage(s.hello(ctx).Locale)})
	r, err := s.purposeModel(purposeSuggestNext).Chat(ctx, ai.Request{System: system,
		Messages: []ai.Message{{Role: ai.RoleUser, Content: prompts.RenderNextMessageInput(turns)}}, MaxTokens: suggestMaxTokens})
	if err != nil {
		if ctx.Err() == nil && !ai.IsPaused(err) {
			s.logf("suggest %s: %v", short(sid), err)
		}
		return suggestedReply{}
	}
	if r.Truncated {
		return suggestedReply{}
	}
	return suggestedReply{text: cleanSuggestion(r.Text)}
}

// reportedNextStep only reads the most recent assistant answer, after the last user input,
// tool or thought. A truncated preview cannot establish where its work report ends.
func (s *Service) reportedNextStep(ctx context.Context, machine, sid string, items []proto.Item) string {
	_, last := splitLastTurn(items)
	for i := len(last) - 1; i >= 0; i-- {
		it := last[i]
		if it.Kind == turnDurationKind || it.Kind == proto.KindNotice && !it.IsError && it.Text != userStoppedNotice && it.ModelRefusal == nil {
			continue
		}
		if !proposalAnswer(it) {
			return ""
		}
		if it.FullSize > 0 {
			if it.ID == "" {
				return ""
			}
			full, err := (busTranscript{s: s, machine: machine, session: sid}).Full(ctx, it.ID)
			if err != nil || full.ID != it.ID || full.FullSize > 0 || !proposalAnswer(full) {
				return ""
			}
			it = full
		}
		if record := deliveryCloseout(it.Text); record != "" {
			c, _, _ := closeout.Parse(record)
			return c.Next
		}
		return ""
	}
	return ""
}

// Only the agent's own visible text can offer its owner an action.
func proposalAnswer(it proto.Item) bool {
	return it.Role == "assistant" && it.Kind == proto.KindText && !it.IsError && it.Task == nil &&
		it.ModelRefusal == nil && !it.CodexHeartbeatQuiet &&
		it.Injected == nil && it.Source == nil && it.From == "" && !it.Wake && it.ToolUseID == "" && it.Image == nil
}

// suggestionTurns are the visible user and assistant messages the model reads: the last suggestMaxMessages, oldest
// first, suggestMaxChars characters in all (the newest kept whole first, an older one cut from its start).
func suggestionTurns(items []proto.Item) []prompts.NextMessageTurn {
	var picked []prompts.NextMessageTurn
	left := suggestMaxChars
	for i := len(items) - 1; i >= 0 && len(picked) < suggestMaxMessages && left > 0; i-- {
		it := &items[i]
		if it.Kind != proto.KindText || (it.Role != "user" && it.Role != "assistant") || it.Image != nil ||
			it.Injected != nil || it.Wake || it.Compact != nil || it.ToolUseID != "" {
			continue
		}
		text := strings.TrimSpace(it.Text)
		if it.Role == "assistant" {
			text = strings.TrimSpace(wire.AssistantCardText(text))
		}
		if text == "" {
			continue
		}
		if n := utf8.RuneCountInString(text); n > left {
			r := []rune(text)
			text = "…" + string(r[n-left+1:])
		}
		left -= utf8.RuneCountInString(text)
		picked = append(picked, prompts.NextMessageTurn{Role: it.Role, Text: text})
	}
	for i, j := 0, len(picked)-1; i < j; i, j = i+1, j-1 {
		picked[i], picked[j] = picked[j], picked[i]
	}
	return picked
}

// cleanSuggestion is the model's answer as a suggestion: one line, unquoted; "" for NONE, nothing, or more than a
// short message.
func cleanSuggestion(out string) string {
	t := strings.TrimSpace(out)
	if strings.ContainsAny(t, "\r\n") { // more than one line is not one message
		return ""
	}
	// i18n-ignore: quote marks the model may wrap its answer in, matched and stripped, never shown
	for _, q := range [][2]string{{`"`, `"`}, {"“", "”"}, {"「", "」"}, {"'", "'"}, {"`", "`"}} {
		if len(t) >= len(q[0])+len(q[1]) && strings.HasPrefix(t, q[0]) && strings.HasSuffix(t, q[1]) {
			t = strings.TrimSpace(t[len(q[0]) : len(t)-len(q[1])])
		}
	}
	// i18n-ignore: the full stops the model may end its "NONE" with, matched and stripped, never shown
	if t == "" || strings.EqualFold(strings.Trim(t, ".。 "), "none") || utf8.RuneCountInString(t) > suggestMaxRunes {
		return ""
	}
	return t
}

// What makes a suggestion risky beyond riskyWords: a command, a path, a link, a secret-looking string.
var (
	suggestCommand = lazyre.New("(?i)`|(?:^|\\s|\\p{Han})(?:\\$|sudo|rm|git|npm|npx|pnpm|yarn|cargo|curl|wget|ssh|scp|kubectl|docker|helm|terraform|systemctl|bash|python3?|pip|brew|chmod|chown|mkfs|pkill|psql)\\s+\\S")
	suggestPath    = lazyre.New(`(?:^|[\s(])(?:~/|\./|\.\./|/[A-Za-z0-9._-]+/)|[A-Za-z]:\\|\b[\w.-]+/[\w.-]+\.[A-Za-z0-9]{1,6}\b`)
	suggestLink    = lazyre.New(`(?i)\b[a-z][a-z0-9+.-]*://|\bwww\.|\b[\w-]+\.(?:com|net|org|io|dev|app|cn|co)\b`)
	suggestToken   = lazyre.New(`(?i)\b(?:sk-|ghp_|gho_|github_pat_|xox[bpa]-|akia)[A-Za-z0-9_-]{8,}|[A-Za-z0-9+/_=-]{24,}`)
)

// suggestionRisky: the suggestion names a command, a path, a link, a secret-looking string or a risky action
// (riskyWords): the apps fill it in but do not send it with one tap.
func suggestionRisky(t string) bool {
	if risky(t) || suggestCommand.MatchString(t) || suggestPath.MatchString(t) || suggestLink.MatchString(t) {
		return true
	}
	for _, tok := range suggestToken.FindAllString(t, -1) {
		// a key's prefix, or a long run that mixes letters and digits
		if len(tok) < 24 || (strings.ContainsAny(tok, "0123456789") && strings.ContainsAny(strings.ToLower(tok), "abcdefghijklmnopqrstuvwxyz")) {
			return true
		}
	}
	return false
}
