package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Why a call rings (urgent_call's why).
const (
	urgentWhyOwner = "owner_asked" // the owner asked for a call about this matter
	urgentWhyRule  = "rule"        // it matches a "what counts as urgent" rule
	urgentWhyTest  = "test"        // Settings › Urgent calls › Test call
)

// Why a call did not ring (the gates; docs/assistant/urgent-call.md "Hard gates"). The matter went out as a P0 push.
const (
	skipOff           = "off"
	skipNoQuote       = "no_quote"
	skipNoRule        = "no_rule"
	skipNoHappening   = "no_happening"
	skipNoAction      = "no_action"
	skipAlreadyCalled = "already_called"
	skipQuiet         = "quiet"
	skipCap           = "cap"
	skipSpacing       = "spacing"
	skipNoPhone       = "no_phone"
)

// Limits of the gates.
const (
	urgentTitleMax      = 30 // runes: what the lock screen shows
	urgentHappeningMin  = 8
	urgentHappeningMax  = 120
	urgentQuoteMin      = 4
	urgentQuoteWindow   = 7 * 24 * time.Hour
	urgentQuoteTurns    = 50
	urgentSpacing       = 10 * time.Minute
	urgentRingTimeout   = 75 * time.Second // the phone reports a missed call after 45 s; this is for one that never reports
	urgentRingTTL       = 30               // seconds APNs keeps a ring
	urgentRemindFirst   = 10 * time.Minute // after two missed rings
	urgentRemindSecond  = 20 * time.Minute // after the first reminder: 30 minutes in all
	urgentCallEvent     = "urgent_call"    // work event: a call rang (north_star.go)
	urgentSkippedEvent  = "urgent_call_skipped"
	urgentReminderLimit = 2
)

// urgentAsk is urgent_call's arguments.
type urgentAsk struct {
	Title         string `json:"title"`
	Body          string `json:"body"`
	Why           string `json:"why"`
	OwnerQuote    string `json:"owner_quote"`
	Rule          string `json:"rule"`
	Happening     string `json:"happening"`
	InteractionID string `json:"interaction_id"`
	// matter is the matter's key kept from its own title when only its words, not its track, redacted it: redacted
	// titles all read alike and would make unrelated matters one (urgentMatter).
	matter string
}

// toolUrgentCall is urgent_call: ring the owner's phone about a matter, when the gates let it; otherwise the matter
// goes out as a P0 push and the answer says why it did not ring.
func (s *Service) toolUrgentCall(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	var a urgentAsk
	if err := json.Unmarshal(args, &a); err != nil {
		return "", errors.New("bad arguments")
	}
	a.Title = strings.Join(strings.Fields(a.Title), " ")
	a.Body = strings.TrimSpace(a.Body)
	a.Happening = strings.Join(strings.Fields(a.Happening), " ")
	a.InteractionID = strings.TrimSpace(a.InteractionID)
	if a.Title == "" {
		return "", errors.New("title is required")
	}
	if a.Why != urgentWhyOwner && a.Why != urgentWhyRule {
		return "", errors.New(`why must be "owner_asked" or "rule"`)
	}
	if s.justMeNow(machine, self) {
		return justMeNotice(s.lang(ctx)), nil
	}
	// The lock screen hears there is news, not what it is, when what it would say is redacted (health.go).
	if space, _ := s.selfSpace(ctx, self); s.exitPolicy(ctx, a.Title+"\n"+a.Body, self, space).Redact {
		if !s.trackPolicy(ctx, space).Redact {
			a.matter = urgentMatter(a)
		}
		a.Title, a.Body = s.redactedNotice(ctx, s.lang(ctx), space)
	}
	a.Title = clipRunesTo(a.Title, urgentTitleMax)
	st := s.urgentStore()
	set, err := s.urgentSettings(ctx)
	if st == nil || err != nil {
		return s.urgentFallback(ctx, machine, self, a, skipOff, "")
	}
	now := time.Now()
	rule, reason, err := s.urgentGate(ctx, st, machine, self, a, set, now)
	if err != nil {
		return "", err
	}
	if reason != "" {
		return s.urgentFallback(ctx, machine, self, a, reason, rule.ID)
	}
	c := db.UrgentCall{ID: "call_" + randomHex(8), MachineID: machine, Conversation: self, Matter: urgentMatter(a),
		Title: a.Title, Body: a.Body, Why: a.Why, Happening: a.Happening, OwnerQuote: a.OwnerQuote, InteractionID: a.InteractionID,
		State: callRinging, Attempt: 1, CreatedAt: now}
	if a.Why == urgentWhyRule {
		c.RuleID, c.RuleText = rule.ID, urgentRuleText(rule, s.urgentLanguage(ctx))
	}
	res, err := s.ringNew(ctx, st, &c, now)
	if err != nil {
		return "", err
	}
	if res.Devices == 0 {
		// Nothing could ring: the call is failed and the matter goes as a P0 push now (ringNew recorded it).
		s.urgentPush(ctx, c, c.Title, c.Body)
		return fmt.Sprintf("Could not ring the owner's phone (%s); sent it as a P0 notification instead.", res.Reason), nil
	}
	return fmt.Sprintf("Ringing the owner's phone now (call %s). If they don't answer it rings once more in %d minutes, then "+
		"falls back to notifications; keep the card open. What they decide on the call comes back to this conversation.",
		c.ID, set.RetryMinutes), nil
}

// urgentMatter is what makes two calls the same matter: the card, else the title.
func urgentMatter(a urgentAsk) string {
	if a.InteractionID != "" {
		return "ix:" + a.InteractionID
	}
	if a.matter != "" {
		return a.matter
	}
	return "title:" + strings.ToLower(a.Title)
}

// urgentGate is why the matter may not ring ("" when it may) and the rule it matched (why rule).
func (s *Service) urgentGate(ctx context.Context, st urgentStore, machine, self string, a urgentAsk, set urgentCallSettings, now time.Time) (urgentRule, string, error) {
	var rule urgentRule
	if !set.Enabled {
		return rule, skipOff, nil
	}
	switch a.Why {
	case urgentWhyOwner:
		said, err := s.ownerSaid(ctx, machine, self, a.OwnerQuote, now)
		if err != nil {
			return rule, "", err
		}
		if !said {
			return rule, skipNoQuote, nil
		}
	case urgentWhyRule:
		i := slices.IndexFunc(set.Rules, func(r urgentRule) bool { return r.ID == strings.TrimSpace(a.Rule) })
		if i < 0 {
			return rule, skipNoRule, nil
		}
		rule = set.Rules[i]
		if n := utf8.RuneCountInString(a.Happening); n < urgentHappeningMin || n > urgentHappeningMax {
			return rule, skipNoHappening, nil
		}
	}
	if a.InteractionID != "" {
		if _, pending := s.pendingIx(a.InteractionID); !pending {
			return rule, skipNoAction, nil
		}
	} else if a.Body == "" {
		return rule, skipNoAction, nil
	}
	calls, err := st.UrgentCallsSince(ctx, s.startOfDay(ctx, now).Add(-24*time.Hour))
	if err != nil {
		return rule, "", err
	}
	matter := urgentMatter(a)
	for _, c := range calls {
		if c.Why != urgentWhyTest && c.Matter == matter && c.State != callFailed {
			return rule, skipAlreadyCalled, nil
		}
	}
	if set.Quiet.inQuiet(now.In(s.location(ctx))) {
		return rule, skipQuiet, nil
	}
	today := s.startOfDay(ctx, now)
	var todays []db.UrgentCall
	for _, c := range calls {
		if !c.CreatedAt.Before(today) {
			todays = append(todays, c)
		}
	}
	if countedMatters(todays) >= set.DailyCap {
		return rule, skipCap, nil
	}
	for _, c := range calls {
		if c.Why != urgentWhyTest && c.State != callFailed && now.Sub(c.CreatedAt) < urgentSpacing {
			return rule, skipSpacing, nil
		}
	}
	return rule, "", nil
}

// countedMatters is how many of calls count toward the day's cap: every matter that rang once, a test call and one
// that could not ring aside. A matter's retry is the same row, so it is never counted again.
func countedMatters(calls []db.UrgentCall) int {
	n := 0
	for _, c := range calls {
		if c.Why != urgentWhyTest && c.State != callFailed {
			n++
		}
	}
	return n
}

// ownerSaid: quote appears word for word in a message the owner wrote in this conversation within the last week (not
// a forwarded message, an inbox item or a scheduled run's prompt: ItemSource names those).
func (s *Service) ownerSaid(ctx context.Context, machine, self, quote string, now time.Time) (bool, error) {
	quote = strings.Join(strings.Fields(quote), " ")
	if utf8.RuneCountInString(quote) < urgentQuoteMin {
		return false, nil
	}
	var res struct {
		Items []proto.Item `json:"items"`
	}
	if err := s.call(ctx, machine, DeviceSessionItems, map[string]any{"session_id": self, "turns": urgentQuoteTurns}, &res); err != nil {
		return false, fmt.Errorf("reading the conversation to check owner_quote: %w", err)
	}
	return ownerWrote(res.Items, quote, now), nil
}

// ownerWrote is ownerSaid's check over a conversation's items.
func ownerWrote(items []proto.Item, quote string, now time.Time) bool {
	for _, it := range items {
		if it.Role != "user" || it.Kind != proto.KindText || it.From != "" || prompts.ItemSource(it) != nil {
			continue
		}
		if !it.TS.IsZero() && now.Sub(it.TS) > urgentQuoteWindow {
			continue
		}
		if strings.Contains(strings.Join(strings.Fields(it.Text), " "), quote) {
			return true
		}
	}
	return false
}

// urgentFallback sends a matter that did not ring as a P0 push, notes why it did not (north_star.go excludes it) and
// tells the model.
func (s *Service) urgentFallback(ctx context.Context, machine, self string, a urgentAsk, reason, rule string) (string, error) {
	s.northStarNoted()
	s.workEvent(machine, self, urgentSkippedEvent, map[string]any{"reason": reason, "rule": rule})
	body := a.Body
	if body == "" {
		body = a.Happening
	}
	res, err := s.pushResult(withCard(ctx, cardUrgentCall), Notification{Subtitle: "Persona", DeviceID: machine, SessionID: self, Title: a.Title, Body: body,
		Asked: true, Level: assistantwire.LevelP0, RelatedKey: "urgent:" + urgentMatter(a)})
	if err != nil {
		return "", err
	}
	if !res.Held {
		s.channelsCopied(machine, self, a.Title, body)
	}
	s.recordNotify(ctx, machine, self, notifyMissed(res))
	return fmt.Sprintf("Did not ring the owner's phone (%s: %s). Sent it as a P0 notification instead; don't send it again.",
		reason, skipReasonText(reason)), nil
}

// skipReasonText explains a gate to the model.
func skipReasonText(reason string) string {
	switch reason {
	case skipOff:
		return "urgent calls are off"
	case skipNoQuote:
		return "owner_quote is not the owner's own words in this conversation from the last 7 days"
	case skipNoRule:
		return "rule is not one of the current rules"
	case skipNoHappening:
		return "happening must say in 8 to 120 characters what is happening now"
	case skipNoAction:
		return "nothing for the owner to do: the card is not pending, or there is no body"
	case skipAlreadyCalled:
		return "this matter already rang"
	case skipQuiet:
		return "inside the owner's quiet hours for calls"
	case skipCap:
		return "the day's limit of calls is reached"
	case skipSpacing:
		return "another matter rang less than 10 minutes ago"
	}
	return reason
}

// ringNew keeps c and rings it: the row first (so the phone can read it as soon as it rings), then call.ring. A ring
// no phone took leaves the row failed, so it is not counted; the caller pushes the matter instead.
func (s *Service) ringNew(ctx context.Context, st urgentStore, c *db.UrgentCall, now time.Time) (assistantwire.CallRingResult, error) {
	timeout := now.Add(urgentRingTimeout)
	c.RangAt, c.NextAt, c.Step = &now, &timeout, stepRingTimeout
	if err := st.InsertUrgentCall(ctx, *c); err != nil {
		return assistantwire.CallRingResult{}, err
	}
	res := s.ring(ctx, *c)
	s.northStarNoted()
	if res.Devices == 0 {
		c.State, c.NextAt, c.Step = callFailed, nil, ""
		if ok, err := st.UpdateUrgentCall(ctx, *c); err == nil && ok {
			c.Version++
		}
		s.workEvent(c.MachineID, c.Conversation, urgentSkippedEvent, map[string]any{"reason": skipNoPhone, "call": c.ID, "test": c.Why == urgentWhyTest})
		return res, nil
	}
	s.workEvent(c.MachineID, c.Conversation, urgentCallEvent, map[string]any{"call": c.ID, "why": c.Why, "rule": c.RuleID, "test": c.Why == urgentWhyTest})
	s.wakeUrgent()
	return res, nil
}

// ring has the hub ring the owner's phones once for c.
func (s *Service) ring(ctx context.Context, c db.UrgentCall) assistantwire.CallRingResult {
	r, ok := s.hub.(callRinger)
	if !ok {
		return assistantwire.CallRingResult{Reason: assistantwire.CallNoPhone}
	}
	res, err := r.CallRing(ctx, assistantwire.CallRing{ID: c.ID, Title: c.Title, ConversationID: c.Conversation, Attempt: c.Attempt, TTL: urgentRingTTL})
	if err != nil {
		s.logf("assistant: urgent call %s: ring: %v", c.ID, err)
		return assistantwire.CallRingResult{Reason: assistantwire.CallRefused}
	}
	return res
}

// urgentPush pushes a call's matter at P0 (a missed call, a declined one, a reminder, one that could not ring), to
// the phone and the Feishu bot and channels, opening the conversation.
func (s *Service) urgentPush(ctx context.Context, c db.UrgentCall, title, body string) {
	s.push(withCard(ctx, cardUrgentCall), Notification{Subtitle: "Persona", DeviceID: c.MachineID, SessionID: c.Conversation, Title: title, Body: body,
		Level: assistantwire.LevelP0, RelatedKey: "urgent:" + c.ID + ":" + randomHex(2)})
	s.channelsPushed(c.MachineID, c.Conversation, title, body)
}

// missedText is the push after a call nobody answered.
func missedText(l i18n.Lang, c db.UrgentCall) (string, string) {
	body := c.Body
	if body == "" {
		body = c.Happening
	}
	return i18n.T(l, "assistant-urgent_call.missed_title", i18n.Args{"title": c.Title}), body
}
