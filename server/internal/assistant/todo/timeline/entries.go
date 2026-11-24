package timeline

import (
	"cmp"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// Values the stores write that the timeline reads, as the manager writes them.
const (
	feedRouted   = "routed"
	feedReceipt  = "receipt"
	feedAnswered = "answered"
	feedTask     = "task"
	feedReport   = "report"
	feedReview   = "review"

	routedFromAssistant = "assistant"
	answeredByAssistant = "assistant"

	receiptDone        = "done"
	receiptFailed      = "failed"
	receiptInterrupted = "interrupted"

	verdictNeedsUser = "needs_user"
	verdictAccepted  = "accepted"
	verdictCancelled = "cancelled"
	verdictParked    = "parked"
	verdictRework    = "rework"
	byJudge          = "judge"
	byReviewer       = "reviewer"

	sourceManual   = "manual"
	sourceCard     = "card"
	sourceOutcome  = "outcome"
	sourceAgent    = "agent"
	sourceThread   = "thread"
	sourceReported = "reported"
	// An extension's outcome names it as its machine; the hub's own outcome of a completion names that completion as
	// its message (outcome:<to-do>:<done_at>#o for the outcome line, #d<n> and #p<n> for what it decided and hit).
	extensionMachine = "extension:"
	outcomeMessage   = "outcome:"
	outcomeLine      = "o"

	briefByOwner       = "owner"
	briefEventScope    = "scope"
	briefEventEstimate = "estimate" // the assistant's briefEventEstimate: an estimate change without new criteria
	briefEventOverdue  = "overdue"

	outcomeDecide = "decide"
	outcomeCannot = "cannot"

	doneByAuto      = "auto"
	doneByCancelled = "cancelled"
	doneByReported  = "reported"
	doneByReview    = "review" // the list's done_by of a review's close, a cancellation's included
)

// The line keys the timeline writes (spec/fixtures/todo-rounds/README.md "Line keys"). interrupted, parked and cannot
// are also row keys of the view (spec/fixtures/todo-view/README.md), with the same meaning and arguments.
const (
	lineRunStarted        = "run_started"
	lineContinues         = "continues"
	lineRunEnded          = "run_ended"
	lineRunFailed         = "run_failed"
	lineInterrupted       = "interrupted"
	lineReview            = "review"
	lineParked            = "parked"
	lineOverdue           = "overdue"
	lineScope             = "scope"
	lineClosed            = "closed"
	lineCannot            = "cannot"
	lineReport            = "report"
	lineAnsweredByPersona = "answered_by_persona"
	lineOwnerResponse     = "owner_response"
)

// Who closed a to-do and how, as the closed line says it.
const (
	closedByOwner   = "owner"
	closedByPersona = "persona"
	closedByJudge   = "judge"

	closedDone     = "done"
	closedCanceled = "canceled"
	closedReported = "reported"
)

// How far apart in time two rows of one act are, at most.
const (
	// pairWindow: the two rows one owner act leaves (R1): an answered card and its card decisions, a relayed choice and
	// its decision. Both are written in one go.
	pairWindow = 10 * time.Second
	// relayWindow: a decision recorded before its relay (the owner's reply to its card in Lark) is relayed once its run
	// took it, which the hub waits for up to 90 seconds, or 5 minutes for a run it resumes first.
	relayWindow = 5 * time.Minute
	// reviewWindow: a review's feed row is written after its ledger entry, once the review's other writes are done (R6).
	reviewWindow = time.Minute
	// wakeWindow: a task card that says how a run came to work for the task, and the run's link.
	wakeWindow = 2 * time.Minute
	// startWindow: a failed start's receipt and the to-do's start error; closeWindow: the review that closed the to-do
	// and its done_at.
	startWindow = 2 * time.Minute
	closeWindow = 2 * time.Minute
	// askWindow: the to-do's open ask is asked once the needs_user review that raised it is written.
	askWindow = 10 * time.Minute
)

// What an item was read from, for the pairs of R1.
const (
	fromOther = iota
	fromInstruction
	fromAnswered
	fromRelayAnswer
	fromManual
	fromCard
	fromLink
)

// item is one entry as it is read, with what grouping it into rounds needs.
type item struct {
	e    assistantwire.TodoTimelineEntry
	from int
	head bool   // the owner's instruction or answer: it opens a round (R1)
	bg   bool   // supervision nobody asked for: the day's Background round (R3)
	join string // the id of the header it folds under, whatever its time (R1)
	turn *turn  // a turn of a run, one line per run per round (R4)
	// key is what R1 pairs it by: an instruction's feed id, a decision's source message, a link's run; started the run
	// an instruction started (machine/session).
	key, started string
	needsUser    bool // a needs_user review: it may be the to-do's open ask
	closes       bool // an accepting or cancelling review: it may be what closed the to-do
	judge        bool // given by the done judge
	failedStart  bool // a failed start: a receipt, or a run whose start failed
	result       *assistantwire.TodoResult
}

// turn is a receipt as a turn of its run: which run ("" when none can be named), and whether it was cut off.
type turn struct {
	key         string
	run         *assistantwire.TodoRunRef
	interrupted bool
}

// reader reads one to-do's stores into items.
type reader struct {
	in      *Input
	items   []*item
	request *assistantwire.TodoTimelineEntry // the owner's request that started the work: the start round's header
	changes map[string]change                // the PRs its receipts named, by URL
}

// change is the first receipt that named a PR: when, and its run.
type change struct {
	at  time.Time
	run *assistantwire.TodoRunRef
}

func (r *reader) add(id string, at time.Time, typ, actor string) *item {
	it := &item{e: assistantwire.TodoTimelineEntry{ID: id, Type: typ, At: at.UTC(),
		Actor: assistantwire.TodoActor{Kind: actor}}}
	r.items = append(r.items, it)
	return it
}

// read reads every store, then marks what needs the others read first: wakes on links, the start error on its
// receipt, the open ask on its review, the close.
func (r *reader) read() {
	var tasks, reviews []FeedItem
	for _, f := range r.in.Feed {
		switch f.Kind {
		case feedRouted:
			r.routed(f)
		case feedReceipt:
			r.receipt(f)
		case feedAnswered:
			r.answered(f)
		case feedReport:
			r.report(f)
		case feedTask:
			tasks = append(tasks, f)
		case feedReview:
			reviews = append(reviews, f)
		}
	}
	r.reviews(reviews)
	for _, d := range r.in.Decisions {
		r.decision(d)
	}
	r.links()
	r.wakes(tasks)
	r.brief()
	r.continues()
	r.failedStart()
	r.closed()
	r.openAsk()
}

// decode reads a feed item's body into v; false when it cannot, and the item is left out.
func decode(raw json.RawMessage, v any) bool {
	return len(raw) > 0 && json.Unmarshal(raw, v) == nil
}

func feedID(f FeedItem) string { return "feed:" + f.ID }

// nano is a time in an id: UTC, to the nanosecond.
func nano(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func runKey(machine, session string) string { return machine + "/" + session }

// session is the to-do's run machine/session as the session list has it; nil when it does not list it.
func (r *reader) session(machine, session string) *proto.Session {
	for i := range r.in.Sessions {
		if s := &r.in.Sessions[i]; s.MachineID == machine && s.SessionID == session {
			return s
		}
	}
	return nil
}

// ref is run machine/session, its title the session list's, else its link's; nil when either id is missing.
func (r *reader) ref(machine, session string) *assistantwire.TodoRunRef {
	if machine == "" || session == "" {
		return nil
	}
	out := &assistantwire.TodoRunRef{MachineID: machine, SessionID: session}
	if s := r.session(machine, session); s != nil && s.Title != "" {
		out.Title = s.Title
	} else if i := slices.IndexFunc(r.in.Todo.Agents, func(a Agent) bool {
		return a.MachineID == machine && a.SessionID == session
	}); i >= 0 {
		out.Title = r.in.Todo.Agents[i].Title
	}
	return out
}

func title(ref *assistantwire.TodoRunRef) string {
	if ref == nil {
		return ""
	}
	return ref.Title
}

// line is key with its arguments as name, value pairs; empty values are left out, and so are args without any.
func line(key string, kv ...string) *assistantwire.TodoLine {
	l := &assistantwire.TodoLine{Key: key}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			continue
		}
		if l.Args == nil {
			l.Args = map[string]string{}
		}
		l.Args[kv[i]] = kv[i+1]
	}
	return l
}

// at is a time as a line's argument: absolute RFC 3339, "" for none.
func at(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// ---- the feed ----

// routed: words that went to a run. The owner's own open a round and say delivered: the item is written only once its
// run took them. Persona's and the hub's are relays.
func (r *reader) routed(f FeedItem) {
	var b routedBody
	if !decode(f.Body, &b) {
		return
	}
	machine, session := b.MachineID, b.SessionID
	if session == "" && b.NewAgent != "" { // written before #10465: the run it started is the run it reached
		machine, session = b.NewAgentMachine, b.NewAgent
	}
	it := r.add(feedID(f), f.At, assistantwire.TodoEntryTypeRelay, assistantwire.TodoActorKindPersona)
	it.e.Text, it.e.Run = b.Text, r.ref(machine, session)
	switch {
	case b.From == routedFromAssistant:
	case b.Relay != "":
		it.e.Actor.Kind, it.from = assistantwire.TodoActorKindHub, fromRelayAnswer
	default:
		it.e.Type, it.e.Actor.Kind = assistantwire.TodoEntryTypeInstruction, assistantwire.TodoActorKindOwner
		it.e.State, it.head, it.from, it.key = assistantwire.TodoMessageStateDelivered, true, fromInstruction, f.ID
		if b.NewAgent != "" {
			it.started = runKey(b.NewAgentMachine, b.NewAgent)
		}
	}
}

// receipt: a turn of a run ended (one line per run per round, R4), or a start failed. A PR it names is a result.
func (r *reader) receipt(f FeedItem) {
	var b receiptBody
	if !decode(f.Body, &b) {
		return
	}
	switch b.State {
	case receiptFailed: // no run started: the hub's words, as the conversation shows them, until a start error says why
		it := r.add(feedID(f), f.At, assistantwire.TodoEntryTypeRunFailed, assistantwire.TodoActorKindHub)
		it.e.Text, it.failedStart = b.Summary, true
	case receiptDone, receiptInterrupted:
		key, run := r.receiptRun(b, f.At)
		it := r.add(feedID(f), f.At, assistantwire.TodoEntryTypeRun, assistantwire.TodoActorKindRun)
		it.e.Actor.Run, it.e.Run = run, run
		it.turn = &turn{key: key, run: run, interrupted: b.State == receiptInterrupted}
		if it.turn.interrupted {
			it.e.Type = assistantwire.TodoEntryTypeInterrupted
		} else {
			it.e.Text = b.Summary
		}
		if c, ok := r.changes[b.PR]; b.PR != "" && (!ok || f.At.Before(c.at)) {
			r.changes[b.PR] = change{at: f.At, run: run}
		}
	}
}

// receiptRun is the run a receipt is a turn of: the one it names (#10465); on an older receipt, the one link whose
// computer has the receipt's machine name, whose run started, and that was on the to-do at its time. "" when none or
// several are: the line is then "A run".
func (r *reader) receiptRun(b receiptBody, at time.Time) (string, *assistantwire.TodoRunRef) {
	if b.MachineID != "" && b.SessionID != "" {
		return runKey(b.MachineID, b.SessionID), r.ref(b.MachineID, b.SessionID)
	}
	var found *Agent
	for i := range r.in.Todo.Agents {
		a := &r.in.Todo.Agents[i]
		if b.Machine == "" || r.in.Machines[a.MachineID] != b.Machine || a.StartError != "" || a.LinkedAt.After(at) ||
			a.ReleasedAt != nil && !at.Before(*a.ReleasedAt) {
			continue
		}
		if found != nil {
			return "", nil
		}
		found = a
	}
	if found == nil {
		return "", nil
	}
	return runKey(found.MachineID, found.SessionID), r.ref(found.MachineID, found.SessionID)
}

// answered: someone dealt with a run's card. The owner's answer opens a round; Persona's in their place is its call.
// Each question's answer is the answer itself, whole; the summary line is clipped, and a card without questions (a
// permission, a plan) has only that line, in the hub's words.
func (r *reader) answered(f FeedItem) {
	var b answeredBody
	if !decode(f.Body, &b) {
		return
	}
	var answers []string
	for _, i := range b.Items {
		if i.Answer != "" {
			answers = append(answers, i.Answer)
		}
	}
	it := r.add(feedID(f), f.At, assistantwire.TodoEntryTypeAnswer, assistantwire.TodoActorKindOwner)
	it.e.Question = b.Question
	if b.By != answeredByAssistant {
		it.e.Text, it.e.Via = cmp.Or(strings.Join(answers, "\n"), b.Answer), assistantwire.TodoEntryViaCard
		it.head, it.from = true, fromAnswered
		return
	}
	// The summary line says "Persona answered" in the hub's words: the key says that, with the answers when there are.
	it.e.Type, it.e.Actor.Kind = assistantwire.TodoEntryTypeDecision, assistantwire.TodoActorKindPersona
	it.e.Reason = b.Reason
	it.e.Line = line(lineAnsweredByPersona, "answer", strings.Join(answers, "\n"))
}

// report: a run reported to its supervisor (question, blocked, done): supervision nobody asked for (R3).
func (r *reader) report(f FeedItem) {
	var b reportBody
	if !decode(f.Body, &b) {
		return
	}
	it := r.add(feedID(f), f.At, assistantwire.TodoEntryTypeReport, assistantwire.TodoActorKindRun)
	it.e.Text, it.e.Line, it.bg = b.Text, line(lineReport, "kind", b.Kind), true
}

// review is one acceptance review, read from the feed, the ledger or both (R6), with the owner's fixed answer to its
// decision and the ledger's time of it, which that answer's id is formed from.
type review struct {
	id, verdict, by, note, reason, response string
	at, until, ledgerAt, responseAt         time.Time
	round                                   int
}

// reviews reads the reviews (R6): a feed row and the ledger entry it was written after (same verdict, at most
// reviewWindow before it) are one review, under the feed row's id and time, with what the ledger alone keeps (who gave
// it, a decision's question, a park's reason); the ledger adds the reviews no row has (a to-do no conversation follows,
// one from before it had an origin).
func (r *reader) reviews(feed []FeedItem) {
	var out []review
	for _, f := range feed {
		var b reviewBody
		if decode(f.Body, &b) {
			out = append(out, review{id: feedID(f), at: f.At, verdict: b.Verdict, note: b.Note, round: b.Round,
				until: b.Until})
		}
	}
	slices.SortFunc(out, func(a, b review) int { return cmp.Or(a.at.Compare(b.at), strings.Compare(a.id, b.id)) })
	var ledger []Review
	if v := r.in.Todo.Supervision; v != nil {
		ledger = slices.Clone(v.Reviews)
		slices.SortStableFunc(ledger, func(a, b Review) int { return a.At.Compare(b.At) })
	}
	used := make([]bool, len(ledger))
	for i := range out {
		f, best := &out[i], -1
		for j, l := range ledger {
			if fits := !l.At.Before(f.at.Add(-reviewWindow)) && !l.At.After(f.at.Add(time.Second)); fits && !used[j] &&
				l.Verdict == f.verdict {
				best = j // the latest that fits: they are sorted
			}
		}
		if best < 0 {
			continue
		}
		l := ledger[best]
		used[best] = true
		f.by, f.reason, f.note, f.round = l.By, l.Reason, cmp.Or(f.note, l.Note), cmp.Or(f.round, l.Round)
		f.ledgerAt, f.response, f.responseAt = l.At, l.OwnerResponse, l.OwnerResponseAt
		if f.until.IsZero() {
			f.until = l.Until
		}
	}
	for j, l := range ledger {
		if !used[j] {
			out = append(out, review{id: "review:" + nano(l.At), at: l.At, verdict: l.Verdict, by: l.By, note: l.Note,
				reason: l.Reason, round: l.Round, until: l.Until, ledgerAt: l.At, response: l.OwnerResponse,
				responseAt: l.OwnerResponseAt})
		}
	}
	for _, rv := range out {
		r.review(rv)
	}
}

// review: a needs_user review asks the owner; an accepting or cancelling one ends the work its round asked for, and
// stays in that round (the to-do's close is its own entry: an acceptance that wants the owner to check, or one of a
// concern, closes nothing); every other (rework, parked, pending_review) is supervision nobody asked for (R3). An ask
// stays in its round whoever gave it, the background supervisor included: the owner has to see it where the work is.
func (r *reader) review(rv review) {
	actor := assistantwire.TodoActorKindPersona
	switch rv.by {
	case byJudge:
		actor = assistantwire.TodoActorKindHub
	case byReviewer:
		actor = assistantwire.TodoActorKindRun
	}
	it := r.add(rv.id, rv.at, assistantwire.TodoEntryTypeReview, actor)
	switch rv.verdict {
	case verdictNeedsUser:
		it.e.Type, it.e.Text, it.needsUser = assistantwire.TodoEntryTypeAsk, cmp.Or(rv.reason, rv.note), true
		r.ownerResponse(rv, it.e.Text)
		return
	case verdictParked:
		it.e.Type, it.bg = assistantwire.TodoEntryTypeParked, true
		it.e.Line = line(lineParked, "reason", cmp.Or(rv.reason, rv.note), "until", at(rv.until))
		return
	case verdictAccepted, verdictCancelled:
		it.closes, it.judge = true, rv.by == byJudge
	default:
		it.bg = true
	}
	round := "" // the ledger's rework count rides on every later review; only a rework starts a round
	if rv.round > 0 && rv.verdict == verdictRework {
		round = strconv.Itoa(rv.round)
	}
	it.e.Text, it.e.Line = rv.note, line(lineReview, "verdict", rv.verdict, "round", round)
}

// ownerResponse: the fixed answer the owner gave a needs_user review's decision instead of an option (decide it
// yourself, not what I asked, explain first) is their answer, and opens a round. The ledger keeps it on the review
// alone: no decision and no feed item says it.
func (r *reader) ownerResponse(rv review, question string) {
	if rv.response == "" || rv.responseAt.IsZero() {
		return
	}
	it := r.add("review:"+nano(rv.ledgerAt)+":response", rv.responseAt, assistantwire.TodoEntryTypeAnswer,
		assistantwire.TodoActorKindOwner)
	it.e.Question, it.e.Line, it.head = question, line(lineOwnerResponse, "response", rv.response), true
}

// ---- decisions ----

// decision: the owner's own (by hand or in a conversation, on a run's card, in the chat topic) open a round, unless
// R1 pairs them; the others are calls Persona, a run, the hub or an extension made, or what the work came to.
func (r *reader) decision(d Decision) {
	it := r.add("dec:"+d.ID, d.CreatedAt, assistantwire.TodoEntryTypeAnswer, assistantwire.TodoActorKindOwner)
	it.e.Text, it.e.Question, it.e.Reason = d.Choice, d.Question, d.Reason
	it.e.Decision, it.e.SupersededBy, it.key = d.ID, d.SupersededBy, d.SourceMessage
	run := r.ref(d.SourceMachine, d.SourceSession)
	byRun := func() { // the run it names, else the hub
		if run != nil {
			it.e.Actor = assistantwire.TodoActor{Kind: assistantwire.TodoActorKindRun, Run: run}
		} else {
			it.e.Actor.Kind = assistantwire.TodoActorKindHub
		}
	}
	switch d.Source {
	case sourceManual:
		it.head, it.from = true, fromManual
		if d.SourceSession != "" { // recorded from the owner's words in a conversation (task_decided)
			it.e.Via = assistantwire.TodoEntryViaChat
			if d.Reason != "" { // which are its reason: the header says them, and the option they chose beside
				it.e.Text, it.e.Choice, it.e.Reason = d.Reason, d.Choice, ""
			}
		}
	case sourceCard:
		it.head, it.from, it.e.Via = true, fromCard, assistantwire.TodoEntryViaCard
	case sourceThread:
		it.head, it.e.Via = true, assistantwire.TodoEntryViaThread
	case sourceAgent:
		it.e.Type, it.e.Actor.Kind = assistantwire.TodoEntryTypeDecision, assistantwire.TodoActorKindPersona
		switch {
		case d.SourceMachine == "" && d.SourceSession == "" && d.SourceMessage == "":
			// The hub took the recommendation at its deadline; its reason is the hub's sentence saying so, which via says.
			it.e.Via, it.e.Reason = assistantwire.TodoEntryViaDefault, ""
		case run != nil && d.SourceMessage == "": // a run noted its own call (todo_decision_add)
			it.e.Actor = assistantwire.TodoActor{Kind: assistantwire.TodoActorKindRun, Run: run}
		}
	case sourceOutcome:
		it.e.Type = assistantwire.TodoEntryTypeResult
		mark, ours := strings.CutPrefix(d.SourceMessage, outcomeMessage+r.in.Todo.ID+":")
		_, part, _ := strings.Cut(mark, "#")
		switch {
		case strings.HasPrefix(d.SourceMachine, extensionMachine):
			it.e.Actor.Kind = assistantwire.TodoActorKindExtension
		case ours && part != outcomeLine: // what a completion decided and the pitfalls it hit
			it.e.Type = assistantwire.TodoEntryTypeDecision
			byRun()
		case ours:
			byRun()
		} // else the owner wrote the outcome by hand
		if it.e.Type == assistantwire.TodoEntryTypeResult {
			it.result = report(it.e)
		}
	case sourceReported: // what the main conversation told the owner of a concern, which closed it
		it.e.Type, it.e.Actor.Kind = assistantwire.TodoEntryTypeResult, assistantwire.TodoActorKindPersona
		it.result = report(it.e)
	default:
		it.e.Type = assistantwire.TodoEntryTypeDecision
		byRun()
	}
}

// ---- runs ----

// links: each run's link starts it, or says it did not start, and its release ends it. Read oldest first, so two runs
// that failed to start near one receipt take it in that order. A run the owner took off the to-do is not listed.
func (r *reader) links() {
	agents := slices.Clone(r.in.Todo.Agents)
	slices.SortFunc(agents, func(a, b Agent) int {
		return cmp.Or(a.LinkedAt.Compare(b.LinkedAt), strings.Compare(a.MachineID, b.MachineID),
			strings.Compare(a.SessionID, b.SessionID))
	})
	for _, a := range agents {
		key, run := runKey(a.MachineID, a.SessionID), r.ref(a.MachineID, a.SessionID)
		id := "run:" + key
		if a.StartError != "" { // the failed start's receipt written with it is the same failure
			it := r.nearest(a.LinkedAt, startWindow, func(it *item) bool { return it.failedStart && it.e.Run == nil })
			if it == nil {
				it = r.add(id+":failed", a.LinkedAt, assistantwire.TodoEntryTypeRunFailed, assistantwire.TodoActorKindHub)
				it.failedStart = true
			}
			it.e.Run, it.e.Line, it.e.Text = run, line(lineRunFailed, "run", title(run), "error", a.StartError), ""
			continue
		}
		it := r.add(id+":linked", a.LinkedAt, assistantwire.TodoEntryTypeRunStarted, assistantwire.TodoActorKindRun)
		it.e.Actor.Run, it.e.Run, it.e.Line = run, run, line(lineRunStarted, "run", title(run))
		it.from, it.key = fromLink, key
		if end := a.ReleasedAt; end != nil {
			it := r.add(id+":ended", *end, assistantwire.TodoEntryTypeRunEnded, assistantwire.TodoActorKindHub)
			it.e.Run, it.e.Line = run, line(lineRunEnded, "run", title(run))
		}
	}
}

// wakes: a task card that says how a run the task did not start came to work for it (active, resumed) is not an entry
// of its own; it says so on that run's start, the link nearest in time. A task that started its run says nothing more
// than the link does.
func (r *reader) wakes(tasks []FeedItem) {
	slices.SortFunc(tasks, func(a, b FeedItem) int { return cmp.Or(a.At.Compare(b.At), strings.Compare(a.ID, b.ID)) })
	for _, f := range tasks {
		var b taskBody
		if !decode(f.Body, &b) || b.Wake == "" {
			continue
		}
		if it := r.nearest(f.At, wakeWindow, func(it *item) bool { return it.from == fromLink }); it != nil {
			it.e.Line.Args = line(lineRunStarted, "run", it.e.Line.Args["run"], "wake", b.Wake).Args
		}
	}
}

// nearest is the item ok takes nearest to t, within d either way (the earlier, then the lesser id, on a tie); nil when
// none is.
func (r *reader) nearest(t time.Time, d time.Duration, ok func(*item) bool) *item {
	var best *item
	var bestGap time.Duration
	for _, it := range r.items {
		gap := it.e.At.Sub(t).Abs()
		if !ok(it) || gap > d {
			continue
		}
		if best == nil || gap < bestGap || gap == bestGap && byTime(it, best) < 0 {
			best, bestGap = it, gap
		}
	}
	return best
}

// failedStart: the to-do's own start error (the latest failed start) says why on that failure's line, the failed
// start nearest its done_at (a receipt, or a run whose start failed); a to-do with neither (no conversation follows
// it, and no run was linked) gets the failure as an entry of its own.
func (r *reader) failedStart() {
	t := r.in.Todo
	if t.StartError == "" || t.DoneAt == nil {
		return
	}
	it := r.nearest(*t.DoneAt, startWindow, func(it *item) bool { return it.failedStart })
	if it == nil {
		it = r.add("todo:failed@"+nano(*t.DoneAt), *t.DoneAt, assistantwire.TodoEntryTypeRunFailed,
			assistantwire.TodoActorKindHub)
	}
	it.e.Line, it.e.Text = line(lineRunFailed, "run", title(it.e.Run), "error", t.StartError), ""
}

// ---- the to-do itself ----

// brief: the owner's request that started the work heads the start round; the brief's log on this to-do adds its
// scope changes and the overdue mark (a brief is shared by every to-do of one piece of work: a log entry that names no
// to-do counts from this one's creation).
func (r *reader) brief() {
	b, t := r.in.Todo.Brief, r.in.Todo
	if b == nil {
		return
	}
	if b.StartedBy == briefByOwner && b.Request != nil {
		r.request = &assistantwire.TodoTimelineEntry{ID: "brief:" + b.ID + ":request",
			Type: assistantwire.TodoEntryTypeInstruction, At: b.Request.At.UTC(),
			Actor: assistantwire.TodoActor{Kind: assistantwire.TodoActorKindOwner}, Text: b.Request.Text,
			State: assistantwire.TodoMessageStateDelivered}
	}
	for i, ev := range b.Log {
		if ev.Todo != "" && ev.Todo != t.ID || ev.Todo == "" && ev.At.Before(t.CreatedAt) {
			continue
		}
		id := "brief:" + b.ID + ":" + strconv.Itoa(i)
		switch ev.Kind {
		case briefEventScope, briefEventEstimate:
			it := r.add(id, ev.At, assistantwire.TodoEntryTypeScope, assistantwire.TodoActorKindPersona)
			it.e.Line = line(lineScope, "change", ev.Change, "why", ev.Why)
		case briefEventOverdue:
			it := r.add(id, ev.At, assistantwire.TodoEntryTypeOverdue, assistantwire.TodoActorKindHub)
			it.e.Line, it.bg = line(lineOverdue), true
		}
	}
}

// continues: a to-do that took the work over from another says so where it began (its lineage step's start); the
// other's rounds stay on the other (stage 3's activity keeps them on one timeline).
func (r *reader) continues() {
	t := r.in.Todo
	i := slices.IndexFunc(t.Lineage, func(s LineageStep) bool { return s.Current && s.Continues != "" })
	if i < 0 {
		return
	}
	step := t.Lineage[i]
	from := ""
	if j := slices.IndexFunc(t.Lineage, func(s LineageStep) bool { return s.TodoID == step.Continues }); j >= 0 {
		from = t.Lineage[j].Title
	}
	it := r.add("lineage:"+step.Continues, cmp.Or(step.Started, t.CreatedAt), assistantwire.TodoEntryTypeRunStarted,
		assistantwire.TodoActorKindHub)
	it.e.Line = line(lineContinues, "todo", step.Continues, "title", from)
}

// closed: the to-do's close (the stores keep only the latest: a to-do reopened since has none until it closes again),
// by whom its done_by says: the owner, the done judge, or a review, the done judge's when the accepting review nearest
// the close was its own. A cancellation is read off the ledger, as the view reads it: the list sends a cancelled
// to-do's done_by as review, and its ledger says cancelled; the owner's close of a once cancelled to-do is theirs.
func (r *reader) closed() {
	t := r.in.Todo
	if t.DoneAt == nil || t.StartError != "" {
		return
	}
	actor, by, outcome := assistantwire.TodoActorKindPersona, closedByPersona, closedDone
	switch t.DoneBy {
	case "":
		actor, by = assistantwire.TodoActorKindOwner, closedByOwner
	case doneByAuto:
		actor, by = assistantwire.TodoActorKindHub, closedByJudge
	case doneByReported:
		outcome = closedReported
	default:
		if rv := r.nearest(*t.DoneAt, closeWindow, func(it *item) bool { return it.closes }); rv != nil && rv.judge {
			actor, by = assistantwire.TodoActorKindHub, closedByJudge
		}
	}
	if t.CancelledAt != nil || t.DoneBy == doneByCancelled ||
		t.DoneBy == doneByReview && t.Supervision != nil && t.Supervision.State == verdictCancelled {
		outcome = closedCanceled
	}
	it := r.add("todo:closed@"+nano(*t.DoneAt), *t.DoneAt, assistantwire.TodoEntryTypeClosed, actor)
	it.e.Line = line(lineClosed, "by", by, "outcome", outcome)
}

// openAsk: the to-do's own open decision, under the id its view gives it, on the needs_user review that raised it
// (the latest at most askWindow before it was asked); a decision no review raised (an Agent's conclusion) is an ask of
// its own. Don't handle takes it off the view, and its id with it.
func (r *reader) openAsk() {
	t := r.in.Todo
	a := t.Ask
	dismissed := t.DecisionReview != nil && t.DecisionReview.DismissalID != ""
	if a == nil || a.Outcome != outcomeDecide && a.Outcome != outcomeCannot || dismissed {
		return
	}
	id := "todo:decision@" + a.AskedAt.Format(time.RFC3339Nano) // as the view forms it, from the time as listed
	var it *item
	for _, c := range r.items {
		raised := c.needsUser && !c.e.At.After(a.AskedAt) && !c.e.At.Before(a.AskedAt.Add(-askWindow))
		if raised && (it == nil || byTime(c, it) > 0) {
			it = c
		}
	}
	if it == nil {
		it = r.add("todo:ask@"+nano(a.AskedAt), a.AskedAt, assistantwire.TodoEntryTypeAsk, assistantwire.TodoActorKindPersona)
		it.e.Text = a.Question
	}
	it.e.Ask = id
	if a.Outcome == outcomeCannot {
		it.e.Line = line(lineCannot)
	}
}
