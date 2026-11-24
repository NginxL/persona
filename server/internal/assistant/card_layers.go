package assistant

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/proto"
)

// The card ledger: rule 4 of the decision-rights design (2026-11-23, "who decides what"). The owner decides only six
// kinds of things; every card or push the manager sends to the owner's phone is recorded with which of the six it is
// (or none), its source and its owner, counted per day of the owner's time zone. Once a day, after the morning
// briefing's time, the main conversation (it supervises everything) is told the day before's count: how many, how many
// of each kind, and how many were none of the six, by source. Every push that is none of the six is a bug. The report
// goes to the main conversation without waking it (relay_later.go) and is never pushed to the owner. The ledger only
// records: which cards are pushed is decided where they are made.
//
// The six kinds:
//
//	1 goals and direction: whether to do it, what it should become, what counts as good, how directions trade off
//	2 what only the owner knows: their preferences, the facts of their life and of the people in it
//	3 outward as the owner: messages, comments and mail others see, the owner's identity at work
//	4 money: a new paid account or subscription, more seats
//	5 irreversible and never said before: deleting their data, stopping what they use (a deploy with a backup that
//	  rolls itself back is reversible: not this)
//	6 only the owner in person: a login, a second factor, a password or a form, the experience check
//	0 none of the six: a bug each time
//
// A card's kind is fixed in code by its source (cardCategories), never by a model: the source is a constant named
// where the card is made (withCard on the push's ctx, deliverCard, or inputCardSource for a card).

const (
	cardCatNone         = 0 // none of the six: every such push is a bug
	cardCatDirection    = 1
	cardCatOwnerKnows   = 2
	cardCatOutward      = 3
	cardCatMoney        = 4
	cardCatIrreversible = 5
	cardCatInPerson     = 6
)

// The sources: one constant per code point that pushes to the owner. Each has its kind in cardCategories, and the
// test reads this block to hold every constant to having one.
const (
	cardSecretForm         = "secret_form"          // the manager's own secret or form card (secret_run, secret_store, secret_handoff)
	cardAgentSecretForm    = "agent_secret_form"    // an Agent's card asking for a password, fields or an MCP form (elicitation)
	cardConfirmOutbound    = "confirm_outbound"     // health.go confirmOutbound: a conversation hands sensitive words to another Agent
	cardOutboundWrite      = "outbound_write"       // a connector's write the gate left to the owner (mail_send, a chat message, a comment)
	cardLeaderNeedsUser    = "leader_needs_user"    // a task waited on the owner (needs_user); before decision rights it was pushed straight to them, kept for the history it counts
	cardDecision           = "decision"             // todo_ask.go pushDecision: a to-do's decision with a recommendation
	cardTodoRemind         = "todo_remind"          // todo_plan.go: a reminder the owner set
	cardHealthWater        = "health_water"         // health_water.go: the water reminders the owner switched on
	cardMinutesReady       = "minutes_ready"        // kb_minutes.go: a recording's minutes
	cardDayRecap           = "day_recap"            // kb_day.go: the day's recap
	cardBriefFallback      = "brief_fallback"       // briefing_fallback.go: the briefing itself, when its run failed
	cardAgentDone          = "agent_done"           // fleet_push.go: a turn finished (the devices' "done" pushes setting)
	cardAskEscalated       = "ask_escalated"        // ask_route.go: a supervisor's ask_escalate rang an Agent's card to the owner
	cardPlanWaiting        = "plan_waiting"         // tasks_plan.go: a plan waits for Start
	cardPlanMoney          = "plan_money"           // tasks_plan.go: a plan waits for Start: a task spends money
	cardPlanOutward        = "plan_outward"         // tasks_plan.go: a plan waits for Start: a task speaks outward as the owner
	cardPlanIrreversible   = "plan_irreversible"    // tasks_plan.go: a plan waits for Start: an irreversible step not agreed
	cardAgentPlan          = "agent_plan"           // an Agent's plan approval card
	cardTaskFailed         = "task_failed"          // supervise_events.go notifyTaskFailed
	cardAgentPermission    = "agent_permission"     // an Agent's tool approval
	cardAgentQuestion      = "agent_question"       // an Agent's question or screen dialog
	cardAgentReply         = "agent_reply"          // agents_reply.go: an Agent still waits for an answer
	cardRelayRefusal       = "relay_refusal"        // relay_refusal.go
	cardAskHeld            = "ask_held"             // ask_route.go quietRemind
	cardAskUser            = "ask_user"             // a conversation's ask_user question
	cardManagerQuestion    = "manager_question"     // another question card of the manager's own tools
	cardToolPermission     = "tool_permission"      // an approval card of the manager's own tools (ask)
	cardSettingsChange     = "settings_change"      // persona_settings (settings_tool.go, extmgr self_settings.go)
	cardRepositoryAdd      = "repository_add"       // repository_add_tool.go
	cardSubscriptionSave   = "subscription_save"    // extmgr subscription_tools.go
	cardDeviceControl      = "device_control"       // extmgr: iPhone, computer or browser control asked on the phone
	cardExtensionApproval  = "extension_approval"   // extmgr: any other approval of an extension in this process
	cardNotifyUser         = "notify_user"          // tools.go: notify_user
	cardUrgentCall         = "urgent_call"          // urgent_call_tool.go
	cardDecisionDeadline   = "decision_deadline"    // decision_deadline.go: an ask_user decision past its deadline
	cardScheduleFailed     = "schedule_failed"      // schedule.go: a scheduled run failed
	cardScheduleSkipped    = "schedule_skipped"     // schedule.go tellWaitingSkip
	cardScheduleStreak     = "schedule_streak"      // schedule_streak.go
	cardScheduleLimitRetry = "schedule_limit_retry" // schedule_limit_retry.go
	cardMemoryMerge        = "memory_merge"         // memory_merge.go
	cardCaptureSorted      = "capture_sorted"       // capture_sort.go
	cardKBHistory          = "kb_history"           // kbhistory_service.go
	cardUntagged           = "untagged"             // a push no source was named for
)

// cardCategories is each source's kind, 1-6, or 0 for none of the six. A source missing here is 0 too.
var cardCategories = map[string]int{
	// 6, only the owner in person: a password, fields or a form are typed by them.
	cardSecretForm:      cardCatInPerson,
	cardAgentSecretForm: cardCatInPerson,

	// 3, outward as the owner: a connector's write (mail, a chat message, a comment) goes out under their name.
	cardOutboundWrite: cardCatOutward,
	cardPlanOutward:   cardCatOutward, // a planned task says it speaks outward as the owner

	// 4, money; 5, irreversible and never said: a planned task says so (task_plan owner_step).
	cardPlanMoney:        cardCatMoney,
	cardPlanIrreversible: cardCatIrreversible,

	// 1, goals and direction: a supervisor's review judged the owner must decide, and the decision it raises.
	cardLeaderNeedsUser: cardCatDirection,
	cardDecision:        cardCatDirection,
	// 1, an escalation: the supervisor handed the card up explicitly (ask_escalate). Its why_owner is not carried to
	// the push, so the kind cannot be read from it: counted as direction, the supervisor's call to hand it up.
	cardAskEscalated: cardCatDirection,
	// 1, what the owner set up or asked for themself: their own reminders and subscriptions are their direction, so
	// their pushes are theirs to get.
	cardTodoRemind:    cardCatDirection, // a reminder the owner set
	cardHealthWater:   cardCatDirection, // a reminder the owner switched on
	cardMinutesReady:  cardCatDirection, // the minutes of a recording the owner made
	cardDayRecap:      cardCatDirection, // the recap the owner subscribed to
	cardBriefFallback: cardCatDirection, // the briefing the owner subscribed to, when its run failed
	cardAgentDone:     cardCatDirection, // the owner's own "done" pushes setting

	// 0, none of the six: a bug each time it reaches the owner.
	cardConfirmOutbound:    cardCatNone, // work handed to another Agent: nobody outside sees it, the owner's identity is not used
	cardPlanWaiting:        cardCatNone, // a plan's start: the supervisor's (the owner asked for the work)
	cardAgentPlan:          cardCatNone, // an Agent's plan: its supervisor approves it
	cardTaskFailed:         cardCatNone, // a failed start: the supervisor retries or reports it
	cardAgentPermission:    cardCatNone, // an Agent's tool approval: its supervisor's
	cardAgentQuestion:      cardCatNone, // an Agent's question: its supervisor answers
	cardAgentReply:         cardCatNone, // an Agent waiting for an answer: its supervisor's
	cardRelayRefusal:       cardCatNone, // a relay refused: between conversations
	cardAskHeld:            cardCatNone, // a held card's reminder: the supervisor did not handle it
	cardAskUser:            cardCatNone, // a conversation's own question: not by kind; one of the six goes through a decision
	cardManagerQuestion:    cardCatNone, // the manager's own question card
	cardToolPermission:     cardCatNone, // the manager's own tool approval
	cardSettingsChange:     cardCatNone, // the main conversation's own settings change (a merge or deploy grant registered for the owner)
	cardRepositoryAdd:      cardCatNone, // registering a repository: the main conversation's own bookkeeping
	cardSubscriptionSave:   cardCatNone, // saving a subscription the owner asked for: asking again is not theirs
	cardDeviceControl:      cardCatNone, // the agent drives the owner's device: execution, not one of the six
	cardExtensionApproval:  cardCatNone, // another extension approval: not by kind
	cardNotifyUser:         cardCatNone, // the conversation chose to push: results go in the briefing
	cardUrgentCall:         cardCatNone, // an urgent call: the conversation's call, not by kind
	cardDecisionDeadline:   cardCatNone, // an ask_user decision past its deadline: as ask_user
	cardScheduleFailed:     cardCatNone, // a scheduled run failed: the main conversation's to fix
	cardScheduleSkipped:    cardCatNone, // a scheduled run skipped: the main conversation's
	cardScheduleStreak:     cardCatNone, // scheduled runs failing in a row: the main conversation's
	cardScheduleLimitRetry: cardCatNone, // a run waiting out a usage limit: the main conversation's
	cardMemoryMerge:        cardCatNone, // the memory merged: housekeeping
	cardCaptureSorted:      cardCatNone, // a capture filed: housekeeping, the briefing says it
	cardKBHistory:          cardCatNone, // the knowledge base's history import: housekeeping
	cardUntagged:           cardCatNone, // nobody named the source: a bug until it is named
}

// cardCategoryOf is source's kind, 0 for none of the six or a source the table does not know.
func cardCategoryOf(source string) int { return cardCategories[source] }

type cardSourceKey struct{}

// withCard is ctx for a push (push, pushResult) or a card (ask) made at source.
func withCard(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, cardSourceKey{}, source)
}

// cardSourceOf is the source withCard put on ctx, "" for none.
func cardSourceOf(ctx context.Context) string {
	s, _ := ctx.Value(cardSourceKey{}).(string)
	return s
}

// ownCardSource is the source of a card the manager asks itself (ask) when its caller named none.
func ownCardSource(ix proto.Interaction) string {
	switch {
	case secretOrForm(ix):
		return cardSecretForm
	case ix.ToolName == "ask_user":
		return cardAskUser
	case ix.Kind == proto.IxPermission:
		return cardToolPermission
	}
	return cardManagerQuestion
}

// secretOrForm: ix asks for a password, for fields or is an MCP server's form (projection cardKind's secret and form).
func secretOrForm(ix proto.Interaction) bool {
	return ix.Secret || len(ix.Fields) > 0 || ix.Kind == proto.IxElicitation
}

// outboundVerbs are the write verbs (connector.WriteVerb) of a connector's tool that put words out under the owner's
// name: such a write left to the owner is kind 3.
var outboundVerbs = []string{"send", "reply", "forward", "comment", "publish", "share", "invite", "react"}

// hubCardSource is the source of a permission card one of the manager's own tools or extensions made outside ask
// (askHub, waitPreparedCard: SessionTitle "Persona"), by its tool; "" for a card that is not one.
func hubCardSource(ix proto.Interaction) string {
	switch ix.ToolName {
	case "persona_settings":
		return cardSettingsChange
	case "repository_add":
		return cardRepositoryAdd
	case "subscription_save":
		return cardSubscriptionSave
	}
	if ix.Kind != proto.IxPermission || ix.SessionTitle != "Persona" {
		return ""
	}
	for _, p := range []string{"iphone_", "computer_", "browser_"} {
		if strings.HasPrefix(ix.ToolName, p) {
			return cardDeviceControl
		}
	}
	if slices.Contains(outboundVerbs, connector.WriteVerb(ix.ToolName)) {
		return cardOutboundWrite
	}
	return cardExtensionApproval
}

// inputCardSource is the source of card ix's push (notifyInteractionAt): the manager's own card by what ask was told
// or by its tool, an Agent's by its kind. escalated: ask_route rang it up its ladder.
func (s *Service) inputCardSource(ix proto.Interaction, escalated bool) string {
	if v, ok := s.cardAsks.Load(ix.ID); ok {
		return v.(string)
	}
	if ix.ToolName == "ask_user" { // ask_user with wait false (ask_later.go) does not go through ask
		return cardAskUser
	}
	if src := hubCardSource(ix); src != "" { // before the form check: persona_settings may carry fields
		return src
	}
	switch {
	case secretOrForm(ix):
		return cardAgentSecretForm
	case escalated:
		return cardAskEscalated
	case ix.Kind == proto.IxPermission:
		return cardAgentPermission
	case ix.Kind == proto.IxPlan:
		return cardAgentPlan
	}
	return cardAgentQuestion
}

// cardLedgerKey keeps the counts (Store.Setting), cardLedgerDays days of them; cardLedgerToldKey the last day told to
// the main conversation.
const (
	cardLedgerKey     = "assistant.card_ledger"
	cardLedgerToldKey = "assistant.card_ledger_told"
	cardLedgerDays    = 8
)

// cardCount is how many cards one source pushed for one owner in a day.
type cardCount struct {
	Source   string `json:"source"`
	Category int    `json:"category"`
	Owner    string `json:"owner,omitempty"`
	N        int    `json:"n"`
}

// cardLedger is the counts by the owner's local day (2006-01-02). Without a database they are kept in memory only and
// start again with the manager.
type cardLedger struct {
	mu     sync.Mutex
	loaded bool
	days   map[string][]cardCount
	told   string // the last day told to the main conversation (reportCardLedger)
}

// noteCard records push n going to the owner: its source (source, else the one on ctx, else untagged), the kind the
// table gives it and its owner, the session it opens ("" for none). It takes no lock of the caller's, so it can be
// called wherever a push is sent.
func (s *Service) noteCard(ctx context.Context, source string, n Notification) {
	s.noteCardOwner(ctx, source, n.SessionID)
}

// supervisingOwner is the owner of an Agent's push n (deliverCard): the conversation supervising the task of the
// session it opens, else that session. It reads the to-dos: not for a caller that may hold their lock.
func (s *Service) supervisingOwner(ctx context.Context, n Notification) string {
	if n.DeviceID != "" && n.SessionID != "" {
		if t, ok := s.todoOfSession(ctx, n.DeviceID, n.SessionID); ok && t.Supervision != nil && t.Supervision.Supervisor != "" {
			return t.Supervision.Supervisor
		}
	}
	return n.SessionID
}

// noteCardOwner is noteCard with the owner given.
func (s *Service) noteCardOwner(ctx context.Context, source, owner string) {
	source = cmp.Or(source, cardSourceOf(ctx), cardUntagged)
	cat := cardCategoryOf(source)
	if s.logf != nil {
		s.logf("assistant: card to the owner: kind %d, source %s, owner %q", cat, source, owner)
	}
	s.countCard(ctx, time.Now().In(s.location(ctx)), cardCount{Source: source, Category: cat, Owner: owner, N: 1})
}

// countCard adds c to the day of at, drops the days past cardLedgerDays and saves.
func (s *Service) countCard(ctx context.Context, at time.Time, c cardCount) {
	b := &s.cards
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.cardsLoadedLocked(ctx) {
		return
	}
	day := at.Format(time.DateOnly)
	rows := b.days[day]
	if i := slices.IndexFunc(rows, func(r cardCount) bool { return r.Source == c.Source && r.Owner == c.Owner }); i >= 0 {
		rows[i].N += c.N
		rows[i].Category = c.Category // the table may have changed since
	} else {
		rows = append(rows, c)
	}
	b.days[day] = rows
	oldest := at.AddDate(0, 0, -cardLedgerDays).Format(time.DateOnly)
	for d := range b.days {
		if d < oldest {
			delete(b.days, d)
		}
	}
	if st := s.cardStore(); st != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := st.PutSetting(ctx, cardLedgerKey, b.days); err != nil && s.logf != nil {
			s.logf("assistant: save the card ledger: %v", err)
		}
	}
}

// cardsLoadedLocked reads the counts once; b.mu is held. False while they cannot be read (nothing is saved over them).
func (s *Service) cardsLoadedLocked(ctx context.Context) bool {
	b := &s.cards
	if b.loaded {
		return true
	}
	m := map[string][]cardCount{}
	var told string
	if st := s.cardStore(); st != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := st.Setting(ctx, cardLedgerKey, &m); err != nil {
			if s.logf != nil {
				s.logf("assistant: load the card ledger: %v", err)
			}
			return false
		}
		if _, err := st.Setting(ctx, cardLedgerToldKey, &told); err != nil {
			if s.logf != nil {
				s.logf("assistant: load the card ledger's last report: %v", err)
			}
			return false
		}
	}
	if m == nil {
		m = map[string][]cardCount{}
	}
	b.days, b.told, b.loaded = m, told, true
	return true
}

// cardStore is the database the counts are kept in, nil for none.
func (s *Service) cardStore() Store {
	if s.store == nil {
		return nil
	}
	return s.store()
}

// cardsOn is a copy of the counts of day (2006-01-02).
func (s *Service) cardsOn(ctx context.Context, day string) []cardCount {
	b := &s.cards
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.cardsLoadedLocked(ctx) {
		return nil
	}
	return slices.Clone(b.days[day])
}

// cardTally is one day's count: in all, of each of the six kinds (index 1-6; 0 is none of the six), and of the pushes
// that were none of the six, by source.
type cardTally struct {
	Total  int
	ByKind [7]int
	Bugs   map[string]int
}

// tallyCards counts rows, each by the kind the table gives its source now (a row saved before a change of the table
// is counted as the table says today).
func tallyCards(rows []cardCount) cardTally {
	t := cardTally{Bugs: map[string]int{}}
	for _, r := range rows {
		cat := cardCategoryOf(r.Source)
		t.Total += r.N
		t.ByKind[cat] += r.N
		if cat == cardCatNone {
			t.Bugs[r.Source] += r.N
		}
	}
	return t
}

// cardKindNames are the six kinds as the report names them.
var cardKindNames = [7]string{"none of the six", "goals and direction", "only the owner knows", "outward as the owner",
	"money", "irreversible, never said", "only the owner in person"}

// cardLedgerReport is the main conversation's message on day's pushes to the owner; "" when none was pushed.
func cardLedgerReport(day string, rows []cardCount) string {
	t := tallyCards(rows)
	if t.Total == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Card ledger for %s (decision rights: the owner decides only six kinds of things). Pushed to the owner's phone: %d.\n", day, t.Total)
	for k := 1; k <= 6; k++ {
		fmt.Fprintf(&b, "%d %s: %d\n", k, cardKindNames[k], t.ByKind[k])
	}
	fmt.Fprintf(&b, "None of the six (each one a bug): %d", t.ByKind[cardCatNone])
	if len(t.Bugs) > 0 {
		sources := make([]string, 0, len(t.Bugs))
		for src := range t.Bugs {
			sources = append(sources, src)
		}
		sort.Slice(sources, func(i, j int) bool { // the most first, then by name
			if t.Bugs[sources[i]] != t.Bugs[sources[j]] {
				return t.Bugs[sources[i]] > t.Bugs[sources[j]]
			}
			return sources[i] < sources[j]
		})
		parts := make([]string, len(sources))
		for i, src := range sources {
			parts[i] = fmt.Sprintf("%s×%d", src, t.Bugs[src])
		}
		fmt.Fprintf(&b, " — %s", strings.Join(parts, ", "))
	}
	b.WriteString(".\nFor you, not the owner: settle what is none of the six with the supervisors or yourself, and do not pass this on to the owner.")
	if len(t.Bugs) > 0 {
		b.WriteString(" Each source above is a platform defect: hand each one, unless a task for it is open already, to the platform's supervisor as a task to fix where the card is made.")
	}
	return b.String()
}

// cardLedgerFrom is where the report comes from (relayMsg.From and Tool), cardLedgerTitle its label above the main
// conversation's composer.
const (
	cardLedgerFrom  = "card_ledger"
	cardLedgerTitle = "Card ledger"
)

// reportCardLedger tells the main conversation, once a day from the morning briefing's time in the owner's zone, the
// day before's count (cardLedgerReport). It is kept for the conversation without waking it (keepRelayLater): it goes
// in with the next message the manager delivers there, starts no turn, and is never pushed to the owner. A day with
// nothing pushed is told nothing. acceptLoop calls it on its tick.
func (s *Service) reportCardLedger(ctx context.Context, now time.Time) {
	loc := s.location(ctx)
	local := now.In(loc)
	hh, mm := s.briefingClock(ctx)
	if local.Hour()*60+local.Minute() < hh*60+mm {
		return
	}
	day := local.AddDate(0, 0, -1).Format(time.DateOnly)
	main := s.settings(ctx).Main
	b := &s.cards
	b.mu.Lock()
	if !s.cardsLoadedLocked(ctx) || b.told >= day {
		b.mu.Unlock()
		return
	}
	rows := slices.Clone(b.days[day])
	b.mu.Unlock()
	text := cardLedgerReport(day, rows)
	if u := unattendedReport(s.unattendedHandedOut()); u != "" {
		text = strings.TrimSpace(text + "\n\n" + u)
	}
	if text != "" {
		if main == "" {
			return // nobody to tell yet: tried again on the next tick
		}
		if _, ok := s.keepRelayLater(main, relayMsg{From: cardLedgerFrom, FromTitle: cardLedgerTitle, Tool: cardLedgerFrom, Text: text, At: now.UTC()}); !ok {
			return // the relay state could not be saved: tried again on the next tick
		}
		s.publish(ctx) // shown waiting above the main conversation's composer
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.told = day
	if st := s.cardStore(); st != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := st.PutSetting(ctx, cardLedgerToldKey, day); err != nil && s.logf != nil {
			s.logf("assistant: save the card ledger's last report: %v", err)
		}
	}
}
