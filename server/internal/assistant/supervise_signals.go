package assistant

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- signals ----

const (
	sigReview        = "review"         // the work looks finished: review it
	sigIdle          = "idle"           // its agents stopped, and the work is not finished
	sigUnjudged      = "unjudged"       // its agents stopped, and the judge could not say whether the work is finished
	sigNeedsYou      = "needs_you"      // an agent waits for an answer
	sigTimedOut      = "timed_out"      // terminal agents stopped after unattended lack of progress
	sigStalled       = "stalled"        // working, but nothing moved for long
	sigNoProgress    = "no_progress"    // working, its session moves, but only waits and repeats: no real progress (supervise_progress.go)
	sigAllStopped    = "all_stopped"    // every current participant stopped with nothing scheduled to resume it
	sigInterrupted   = "interrupted"    // the model service cut its turn off
	sigFailed        = "failed"         // its agent did not start
	sigStarted       = "started"        // its agent started after the supervisor's retry (start_remedy.go)
	sigPRConflict    = "pr_conflict"    // the task's current open PR has merge conflicts at this head
	sigDraftPR       = "draft_pr"       // an own linked PR is still draft, not waiting for upstream review
	sigPREvent       = "pr_event"       // a current-head PR CI failure or review fell back from direct Agent routing
	sigReport        = "report"         // its agent reported to the supervisor (report_to_supervisor, agent_control.go)
	sigEscalated     = "escalated"      // its leader conversation judged it needs the user (supervise_leader.go): told the main one
	sigOrphaned      = "orphaned"       // nobody works on it: its agents are closed or offline and nothing brings one back (tasks_orphan.go)
	sigAsk           = "ask"            // its agent's card came to the supervisor before the user (ask_route.go)
	sigDecide        = "decide"         // a task leader's worker needs a decision its leader cannot make: told the conversation above the leader (task_leader.go)
	sigDecisionReply = "decision_reply" // the owner wrote in a task's Agent chat; its pending decision needs confirmation
	// sigLeaderStuck: a leader's task is stuck (leader_reports.go): the main conversation says so in one line, no push
	// (the owner's ruling after H1, #3609). Detail is the direction's name and where it is stuck.
	sigLeaderStuck = "leader_stuck"
	// sigCheck: a recurring schedule linked to the task (schedule_todo.go) has news of it, the hub's and never its
	// Agent's (#11587). Report says which: its check saw no progress, could not save what it saw, or the owner
	// answered the decision it raised (checkDecided), which did not go to the Agent.
	sigCheck = "check"
	// sigTodoReady: an open, unstarted dependent to-do has all of its after prerequisites satisfied (todo_after_ready.go, #12606).
	sigTodoReady = "todo_ready"
	// sigPRMerged: an unparked open supervised task's delivery PR merged with no other delivery PR open (pr_merge_wake.go, #13100).
	sigPRMerged = "pr_merged"
	// sigParentCloseout: an open parent to-do with no active Agent has all part_of children done (todo_parent_closeout.go, #13100).
	sigParentCloseout = "parent_closeout"
)

// sigCheck's kinds of news (superviseSignal.Report).
const (
	checkNoProgressReport = "no_progress"
	checkErrorReport      = "error"
	checkDecidedReport    = "decided"
)

var (
	// superviseGather is how long a first signal waits for others to go out with it.
	superviseGather = 15 * time.Second
	// superviseEvery is the least time between two supervision messages; superviseUrgentEvery when one waits on a decision.
	superviseEvery       = 10 * time.Minute
	superviseUrgentEvery = 2 * time.Minute
	// superviseNoMainLogEvery is how often a digest that still leaves signals waiting for a main conversation (none is
	// set and no conversation that dispatched them can be told, or no machine) says so in the log: the watch's every
	// pass (superviseTick) tries again, and each said it once before.
	superviseNoMainLogEvery = time.Hour
	// superviseNeedsAfter is how long an agent waits for an answer before the supervisor hears of it (the user was
	// pushed; a quiet task's at once: the user was not).
	superviseNeedsAfter = 3 * time.Minute
	// superviseStallMin is the least time without progress that counts as stuck (or 1.5× what the task should take).
	superviseStallMin = 20 * time.Minute
	// superviseSilentMin is the least time with nothing new in an agent's transcript, in any state, that is told.
	superviseSilentMin = 2 * time.Hour
	// superviseTick is how often the tasks are looked at.
	superviseTick = 30 * time.Second
	// superviseHoldMax bounds how long the main conversation's turns stay unpushed after a supervision message went in.
	superviseHoldMax = 2 * time.Hour
)

// superviseSignal is one thing about one task the supervisor should know. Key names the stretch of state it is about:
// the same signal with the same key is told once.
type superviseSignal struct {
	Todo, Kind, Key string
	// Handoff is an idle-shaped report that an owner's decision or review rework did not reach the agent.
	// It is not a turn-end stop, so a native wakeup must not defer it.
	Handoff bool
	// PendingPredecessors: keys of earlier stretches of this id replaced before their delivery or queue acknowledgment.
	// An ACK removes exactly its own Key; a still-present predecessor's dedup reservation is not delivery proof.
	PendingPredecessors []string
	PRURL               string // draft_pr: the current or earlier followed PR, revalidated before delivery
	// Unreachable: this decision exists because the task leader's agent could not be reached, not because the worker
	// waits for the user. It stays a decision even though the worker's ledger is not needs_user (task_leader.go).
	Unreachable      bool
	At               time.Time
	Machine, Session string
	// Launcher is the actual launcher a failed start used. Recipient is the final conversation or task-leader
	// destination, and NotBefore holds a repeated failure until its persistent deduplication window expires.
	Launcher  string
	Recipient string
	NotBefore time.Time
	Remedying bool
	// Generation identifies a failed-start event independently of its queue position. failedClaim is the local
	// manager's delivery claim; Claims in the durable batch document carries it between managers.
	Generation  time.Time `json:"generation,omitzero"`
	failedClaim string
	Detail, Ask string
	Minutes     int
	// A report's (sigReport): its kind (progress, question, blocked, done), evidence and the step it is on.
	SourcePermission proto.SourcePermission
	Report           string
	Evidence         []string
	Step, Steps      int
	// no_progress: the tool calls the hub saw in the stretch, and how many were waits or repeats (supervise_progress.go).
	Calls, Waits int
	// Orphan: why nobody works on it (sigOrphaned): orphanError, orphanClosed or orphanOffline.
	Orphan string
	// To is the conversation it goes to when that is not digestTo's to say: a card's rung (sigAsk), the main
	// conversation for a leader's escalated cards.
	To string
	// OriginOnly records that this signal reached its dispatcher through the no-main fallback. The queue preserves
	// that delivery provenance so restoring it after main returns cannot turn it into a legacy closeout grant.
	OriginOnly bool `json:",omitempty"`
	// Cards: the exact rendered cards in an ask delivery, or the cards handed to the owner by ask_escalate.
	Cards []prompts.SuperviseAsk
	// Decision: the decision a recheck or an owner_cannot_answer signal is about, as it was asked (decision.go); for
	// owner_cannot_answer Detail is the owner's answer (decide_yourself, not_what_i_asked, explain_first).
	Decision *prompts.SuperviseDecision
	// Told: a check's decided signal whose answer is already in the task Agent's chat (decisionTold, checkDecided).
	Told bool
	// Inbox: the Inbox item an inbox signal names, and how many new messages it tells (supervise_inbox.go).
	Inbox *prompts.SuperviseInbox
	// Optional routine batching keeps these in memory only, after routing and before delivery. They are never part of
	// a persisted relay payload: once admitted, the existing queue owns delivery.
	batchTo    string
	batchUntil time.Time
	// Breaker: why the background supervisor's circuit breaker handed this signal once to the main conversation;
	// digestEntry puts it above the entry in the digest's language (breakerNote).
	Breaker *breakerFallback `json:"breaker,omitempty"`
	// BreakerNote is that fact as finished text, as a signal queued by an older hub carries it: shown as it is.
	BreakerNote string `json:"breaker_note,omitempty"`
	// ExternalSource names the source of external information arriving during reviewNeedsUser (#13112).
	ExternalSource string `json:"external_source,omitempty"`
	// JudgeDown: an unjudged stop because the judge itself failed (no model set up, or the service kept failing), not
	// because it read the turn and could not tell. It is not urgent (urgent): such stops are told together.
	JudgeDown bool `json:"judge_down,omitempty"`
}

// breakerFallback is what a circuit-breaker fallback note says (prompts.BreakerNote): its kind and their facts.
type breakerFallback struct {
	Kind   string `json:"kind"`             // prompts.BreakerAuth or prompts.BreakerAllStopped
	Tool   string `json:"tool,omitempty"`   // the denied tool (BreakerAuth)
	Rounds int    `json:"rounds,omitempty"` // no-progress rounds in a row (BreakerAllStopped)
	Reason string `json:"reason,omitempty"` // why the breaker tripped (BreakerAllStopped)
}

// breakerNote is g's circuit-breaker fallback note in language (answerLanguage), or "" when it has none.
func breakerNote(g superviseSignal, language string) string {
	if b := g.Breaker; b != nil {
		return prompts.RenderBreakerNote(prompts.BreakerNote{Language: language, Kind: b.Kind, Tool: b.Tool,
			Rounds: b.Rounds, Reason: b.Reason})
	}
	return g.BreakerNote
}

const (
	supervisionInfo   = "info"
	supervisionTriage = "triage"
	supervisionUser   = "user"
)

func (g superviseSignal) level() string {
	if g.Kind == sigParkedSummary {
		return supervisionInfo
	}
	if g.Kind == sigReport && g.Report == upReportProgress {
		return supervisionInfo
	}
	return supervisionTriage
}

// id names what a newer signal replaces while it waits: the task's signal of that kind (a report: of that kind of
// report, so a progress report does not push out a question).
func (g superviseSignal) id() string {
	if g.Kind == sigDraftPR {
		return g.Todo + "|" + g.Kind + "@" + g.PRURL
	}
	if g.Kind == sigPRMerged {
		return g.Todo + "|" + g.Kind + "@" + normalizePRKey(g.PRURL)
	}
	if g.Kind == sigPREvent {
		return g.Todo + "|" + g.Kind + "@" + g.PRURL + ":" + g.Report
	}
	if g.Kind == sigReport || g.Kind == sigCheck {
		return g.Todo + "|" + g.Kind + ":" + g.Report
	}
	if g.Kind == sigAsk { // its cards with each rung are an entry of their own
		return g.Todo + "|" + g.Kind + "@" + g.To
	}
	if g.Kind == sigEscalated && len(g.Cards) > 0 { // a leader's escalated cards, apart from its needs_user
		return g.Todo + "|" + g.Kind + ":cards"
	}
	if g.Kind == sigInbox && g.Inbox != nil && g.Inbox.ToAgent == "sent" {
		return g.Todo + "|" + g.Kind + ":agent_copy" // delivered copies never absorb an earlier failed delivery
	}
	if g.Kind == sigInbox && g.Inbox != nil && g.Inbox.ToAgent == "unhandled" {
		return g.Todo + "|" + g.Kind + ":unhandled@" + g.Key
	}
	if g.Kind == sigNeedsUserExternalInfo {
		return g.Todo + "|" + g.Kind + "@" + g.Key
	}
	return g.Todo + "|" + g.Kind
}

// urgent: a signal that waits on a decision goes out sooner than the rest.
func (g superviseSignal) urgent() bool {
	switch g.Kind {
	case sigUnjudged:
		// A stop the judge could not look at because it is down (no model set up, or it kept failing) is not news
		// that needs the supervisor soon: every stopped task says the same until the model is back, and each used to
		// wake the main conversation on its own. They go out together, on the ordinary schedule.
		return !g.JudgeDown
	case sigNeedsYou, sigIdle, sigAllStopped, sigFailed, sigInterrupted, sigStarted, sigPRConflict, sigDraftPR, sigPREvent, sigEscalated, sigAsk, sigOrphaned, sigDecide, // started: the user waits to hear it
		sigRecheck, sigUndecidable, sigDeliveryHold, sigOwnerCannot, sigEstimateDue, sigTimedOut, // the owner, an automatic deadline or a stopped task waits
		sigParkedDue, sigParkDecided, sigDecisionReply, sigHandoffResult, sigCardlessWait, sigTodoReady, sigPRMerged, sigParentCloseout, sigNeedsUserExternalInfo:
		return true
	case sigReport:
		return g.Report != upReportProgress
	case sigCheck: // the owner answered: what they chose is acted on soon
		return g.Report == checkDecidedReport
	}
	return false
}

// immediate signals need a decision and wait only for gathering, never the shared spacing or routine cooldown.
// An explicit NotBefore (a deferred failed-start batch) still applies.
func (g superviseSignal) immediate() bool { return supervisionBatchImmediate(g) }

type superviseBook struct {
	routingMu   sync.RWMutex // moves and card creation exclude routing/delivery; ordinary deliveries may run concurrently
	mu          sync.Mutex
	writeMu     sync.Mutex        // one ledger write at a time (updateSupervision)
	roundEpochs map[string]uint64 // by todo, protected by writeMu; distinguishes a new round from a scope's state reset
	digestMu    sync.Mutex        // one whole digest, including todo and durable-pending restoration; teardown drains it
	dedupMu     sync.Mutex        // serializes digest reservations and delivery
	dedup       map[string]superviseFingerprint
	// failedBatchMu serializes the durable leader-failure cooldown document. failedBatches is its memory-only
	// fallback when this manager has no store; failedBatchesLoaded says whether deferred members were restored.
	failedBatchMu       sync.Mutex
	failedBatches       failedStartBatchDoc
	failedBatchesLoaded bool
	pending             map[string]superviseSignal   // by todo|kind
	pendingLoadMu       sync.Mutex                   // one durable pending restoration at a time
	pendingLoaded       bool                         // the durable pending has been merged into pending
	sent                map[string]string            // todo|kind → the key last told
	asks                map[string]string            // todo → what its agent last asked (InteractionRaised)
	last                time.Time                    // when the last supervision message went out
	queued              map[string][]superviseSignal // conversation → the signals of the message still waiting in its queue
	batch               map[string]time.Time         // conversation → its routine batch's first-signal deadline
	// legacyBackground: legacyBackgroundKey(conversation, todo) → an old notice reached this background conversation,
	// so it may finish that task. relayState persists the same grant across manager restarts.
	legacyBackground map[string]bool
	batchInterval    time.Duration        // last scheduled optional interval, to release it when disabled
	told             map[string]time.Time // conversation → last accepted supervision message
	timer            *time.Timer
	timerAt          time.Time

	resuming map[[2]string]struct{} // fingerprints of offline leader sends in flight, under dedupMu

	// noMainSince is when digests started leaving signals waiting for a main conversation (zero: the last one left none
	// waiting), noMainLogged when that was last logged (sendDigest).
	noMainSince, noMainLogged time.Time
	hold                      map[string]time.Time // conversation → when a supervision message went in: its turn's finish is not pushed
	visible                   map[string]bool      // an explicit user action makes the current triage turn visible
	// failSeen: tasks whose failed start a supervision message carried, when, and for which conversation
	// (start_remedy.go remedyTurnEnded)
	failSeen map[string]failSeen
	// closeoutScanned bounds startup reconciliation; durable Automatic.Turn prevents replay across restarts.
	closeoutScanned map[string]bool
	// lines are the reports being paraphrased for the Progress row (report_line.go; the tests wait on them).
	lines sync.WaitGroup
}

func (b *superviseBook) init() {
	if b.pending == nil {
		b.pending, b.sent, b.asks, b.hold = map[string]superviseSignal{}, map[string]string{}, map[string]string{}, map[string]time.Time{}
		b.visible = map[string]bool{}
		b.legacyBackground = map[string]bool{}
	}
	if b.failSeen == nil {
		b.failSeen = map[string]failSeen{}
	}
	if b.batch == nil {
		b.batch = map[string]time.Time{}
	}
}

// restoreSupervisePending merges the durable outbox into the in-memory gather exactly once. A signal that arrived
// meanwhile wins; a failed read is retried by the digest timer rather than marking the outbox loaded. Already-told
// provisional starts are retracted before the snapshot: startup recovery reaches this path before any session list.
func (s *Service) restoreSupervisePending(ctx context.Context) {
	b := &s.supervise
	b.mu.Lock()
	loaded := b.pendingLoaded
	b.mu.Unlock()
	if loaded {
		return
	}
	if !s.retractToldStartUnknowns(ctx) {
		b.mu.Lock()
		b.init()
		s.retryDigestLocked(s.superviseTimings().urgent)
		b.mu.Unlock()
		return
	}
	b.pendingLoadMu.Lock()
	defer b.pendingLoadMu.Unlock()
	b.mu.Lock()
	loaded = b.pendingLoaded
	b.mu.Unlock()
	if loaded {
		return
	}
	r := &s.relays
	r.mu.Lock()
	if !s.relayLocked(ctx) {
		r.mu.Unlock()
		b.mu.Lock()
		b.init()
		s.retryDigestLocked(s.superviseTimings().urgent)
		b.mu.Unlock()
		return
	}
	// A crash between an older queue write and its ACK could leave both. The exact queued stretch wins, even after
	// the shorter deduplication window has expired; it must not be restored for a second queue admission.
	var queued []superviseSignal
	for _, queue := range r.st.Queue {
		for _, msg := range queue {
			if msg.Tool != superviseTool {
				continue
			}
			if signals, structured := decodeDigestSignals(msg.Signals); structured {
				queued = append(queued, signals...)
			}
		}
	}
	before := len(r.st.SupervisePending)
	ackSupervisePendingLocked(r.st.SupervisePending, queued)
	if len(r.st.SupervisePending) != before {
		if len(r.st.SupervisePending) == 0 {
			r.st.SupervisePending = nil
		}
		s.saveRelayLocked()
	}
	persisted := make(map[string]superviseSignal, len(r.st.SupervisePending))
	for id, g := range r.st.SupervisePending {
		persisted[id] = g
	}
	r.mu.Unlock()
	b.mu.Lock()
	b.init()
	for id, g := range persisted {
		if _, newer := b.pending[id]; !newer {
			b.pending[id] = g
		}
	}
	b.pendingLoaded = true
	if len(b.pending) > 0 {
		s.scheduleDigestLocked(time.Now())
	}
	b.mu.Unlock()
}

// updateSupervisePending changes the durable outbox. Callers may hold supervise.mu: relay code never takes that lock
// while holding relays.mu. Keeping this per signal, rather than replacing it from pending, preserves in-flight digests.
// A failed write rolls the in-memory durable state back too; delivery checks supervisePendingDurably before it can ACK.
func (s *Service) updateSupervisePending(change func(map[string]superviseSignal)) {
	r := &s.relays
	r.mu.Lock()
	defer r.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return
	}
	previous := maps.Clone(r.st.SupervisePending)
	next := maps.Clone(previous)
	if next == nil {
		next = map[string]superviseSignal{}
	}
	change(next)
	if len(next) == 0 {
		next = nil
	}
	r.st.SupervisePending = next
	if err := s.saveRelayLocked(); err != nil {
		r.st.SupervisePending = previous
	}
}

// supervisePendingDurably reports whether every signal that is about to be delivered still has its exact durable
// outbox entry. A failed initial write must never turn into a successful send and told receipt before a retry lands it.
func (s *Service) supervisePendingDurably(list []superviseSignal) bool {
	if s.store() == nil {
		return true
	}
	r := &s.relays
	r.mu.Lock()
	defer r.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return false
	}
	for _, g := range list {
		if cur, ok := r.st.SupervisePending[g.id()]; !ok || cur.Key != g.Key {
			return false
		}
	}
	return true
}

// ackSupervisePendingLocked removes exactly these stretches while relays.mu is held and the queue is saved atomically.
func ackSupervisePendingLocked(pending map[string]superviseSignal, list []superviseSignal) {
	for _, g := range list {
		if cur, ok := pending[g.id()]; ok {
			if cur.Key == g.Key {
				delete(pending, g.id())
				continue
			}
			// The newer stretch remains, but this predecessor really reached delivery or the durable queue. Its key can
			// no longer make the replacement retry an unchanged fingerprint; other unacknowledged predecessors remain.
			keys := slices.DeleteFunc(slices.Clone(cur.PendingPredecessors), func(key string) bool { return key == g.Key })
			if len(keys) != len(cur.PendingPredecessors) {
				if len(keys) == 0 {
					keys = nil
				}
				cur.PendingPredecessors = keys
				pending[g.id()] = cur
			}
		}
	}
}

// ackSupervisePending removes exactly the stretches delivered or admitted to the persisted relay queue. A newer
// signal of the same kind with another Key is left for its own digest.
func (s *Service) ackSupervisePending(list []superviseSignal) {
	s.updateSupervisePending(func(pending map[string]superviseSignal) {
		ackSupervisePendingLocked(pending, list)
	})
}

const startUnknownDetail = "启动结果还没确认；待办会按 creation_id 补联，不要重复开 Agent" // i18n-ignore: legacy supervisor diagnostic

// legacyStartUnknownMessage identifies only an unambiguous legacy provisional-start notice. A mixed rendered digest
// has multiple task entries and cannot be withdrawn wholesale without risking a genuine later stop.
func legacyStartUnknownMessage(text, todoID string) bool {
	return strings.Contains(text, todoID) && strings.Contains(text, startUnknownDetail) && strings.Count(text, "任务「") <= 1 // i18n-ignore: legacy rendered entry marker
}

// legacyIdleMessage identifies only a one-entry pre-structured idle. A mixed legacy digest has no exact way to rebuild
// its other members, so it stays queued rather than risking an unrelated stop or a genuine later decision.
func legacyIdleMessage(text, todoID string) bool {
	return strings.Contains(text, todoID) && strings.Contains(text, "Agent 停下了，但没做完") && strings.Count(text, "任务「") <= 1 // i18n-ignore: legacy rendered idle and entry markers
}

// startUnknownArmed reports whether any durable or in-memory form still needs retraction. The read-only check keeps
// repeated sightings of an already-recognized done session from writing an empty acknowledgment on every sync.
func (s *Service) startUnknownArmed(g superviseSignal) bool {
	exact := func(candidate superviseSignal) bool {
		return candidate.Todo == g.Todo && candidate.Kind == g.Kind && candidate.Key == g.Key
	}
	pendingMatch := func(candidate superviseSignal) bool {
		return exact(candidate) || slices.Contains(candidate.PendingPredecessors, g.Key)
	}
	b := &s.supervise
	b.mu.Lock()
	armed := pendingMatch(b.pending[g.id()])
	for _, queue := range b.queued {
		for _, candidate := range queue {
			armed = armed || exact(candidate)
		}
	}
	b.mu.Unlock()
	if armed {
		return true
	}

	r := &s.relays
	r.mu.Lock()
	if !s.relayLocked(context.Background()) {
		r.mu.Unlock()
		return true // an unreadable store cannot prove that retraction is a no-op
	}
	armed = pendingMatch(r.st.SupervisePending[g.id()])
	for _, queue := range r.st.Queue {
		for _, msg := range queue {
			signals, structured := decodeDigestSignals(msg.Signals)
			if !structured {
				// A legacy text queue has no structured key: only its unambiguous provisional-start text is retractable.
				armed = armed || legacyStartUnknownMessage(msg.Text, g.Todo)
				continue
			}
			for _, candidate := range signals {
				armed = armed || exact(candidate)
			}
		}
	}
	r.mu.Unlock()
	if armed {
		return true
	}

	turns := &s.tasks.turns
	turns.mu.Lock()
	defer turns.mu.Unlock()
	if !s.turnsReadLocked(context.Background()) {
		return true // an unloaded or unreadable ledger cannot prove that retraction is a no-op
	}
	if entry, ok := turns.doc.Turns[g.Todo]; ok && entry.Signal != nil {
		sig := entry.Signal
		return sig.Kind == g.Kind && sig.Key == g.Key && sig.Told == nil
	}
	return false
}

// retractStartUnknown retires the provisional start result once its exact session has been linked. A newer unjudged
// stop on the same task has another key and remains pending; queued mixed digests keep and regather their other signals.
func (s *Service) retractStartUnknown(ctx context.Context, todoID string) bool {
	return s.retractStartUnknowns(ctx, []string{todoID})
}

// retractStartUnknowns serializes a whole confirmation batch with durable restoration and digest admission. Otherwise
// restoreSupervisePending can snapshot the old outbox before cleanup and insert that snapshot after it, or a digest can
// slip between two confirmed ids. pendingLoadMu precedes dedupMu, matching simulateSupervisionRestart.
func (s *Service) retractStartUnknowns(ctx context.Context, todoIDs []string) bool {
	b := &s.supervise
	b.pendingLoadMu.Lock()
	defer b.pendingLoadMu.Unlock()
	b.dedupMu.Lock()
	defer b.dedupMu.Unlock()
	for _, todoID := range todoIDs {
		if !s.retractStartUnknownLocked(ctx, todoID) {
			return false
		}
	}
	return true
}

func (s *Service) retractStartUnknownLocked(ctx context.Context, todoID string) bool {
	g := superviseSignal{Todo: todoID, Kind: sigUnjudged, Key: "start-unknown@" + todoID}
	// sendDigest holds dedupMu through queueDigest. Confirmation therefore either precedes a new digest's snapshot or
	// waits for its queue admission and retracts that message too; a post-confirmation queue cannot slip between them.
	if !s.startUnknownArmed(g) {
		return true
	}
	// Persist the exact ledger receipt before discarding every other durable form. A read/write failure leaves the
	// outbox, queue and in-memory signal available for a later exact-session sync or watchTurn to retry.
	if !s.turnsToldDurably(ctx, []superviseSignal{g}) {
		return false
	}
	s.retractSuperviseSignalLocked(g)
	return true
}

// retractSuperviseSignalLocked removes exactly g from pending and queue-admitted supervision. The caller holds
// dedupMu, so no new digest can be admitted between the key scan and the queue write.
func (s *Service) retractSuperviseSignalLocked(g superviseSignal) {
	b := &s.supervise
	b.mu.Lock()
	b.init()
	ackSupervisePendingLocked(b.pending, []superviseSignal{g})
	for to, queue := range b.queued {
		found := false
		for _, candidate := range queue {
			found = found || candidate.Todo == g.Todo && candidate.Kind == g.Kind && candidate.Key == g.Key
		}
		if found {
			b.setQueuedLocked(to, nil)
		}
	}
	b.mu.Unlock()

	// Queue removal, exact outbox acknowledgment and regathering the rest share each relay-state write. Do not hold
	// supervise.mu across that I/O; a newer durable stretch with the same id wins over anything regathered here.
	replay := map[string]superviseSignal{}
	update := func(pending map[string]superviseSignal, _ []superviseSignal, regather []superviseSignal) bool {
		before, had := pending[g.id()]
		beforeLen := len(pending)
		ackSupervisePendingLocked(pending, []superviseSignal{g})
		after, ok := pending[g.id()]
		changed := len(pending) != beforeLen || had && (!ok || !slices.Equal(before.PendingPredecessors, after.PendingPredecessors))
		for _, candidate := range regather {
			cur, exists := pending[candidate.id()]
			if !exists {
				cur = candidate
				pending[candidate.id()] = cur
				changed = true
			}
			replay[candidate.id()] = cur
		}
		return changed
	}
	r := &s.relays
	r.mu.Lock()
	var conversations []string
	if s.relayLocked(context.Background()) {
		for to := range r.st.Queue {
			conversations = append(conversations, to)
		}
	}
	r.mu.Unlock()
	if len(conversations) == 0 {
		s.updateSupervisePending(func(pending map[string]superviseSignal) { update(pending, nil, nil) })
	}
	for _, to := range conversations {
		_, _ = s.dropQueuedSupervisionSignalAtomic(to, g, update)
	}

	b.mu.Lock()
	for id, candidate := range replay {
		if _, exists := b.pending[id]; !exists {
			b.pending[id] = candidate
		}
	}
	if len(b.pending) > 0 {
		s.scheduleDigestLocked(time.Now())
	}
	b.mu.Unlock()
}

// retractSupervisedIdle retires every idle stretch that exists when an agent resumes. The turn-ledger receipt lands
// before any other durable form is discarded, so a restart cannot replay an idle that was only recorded there. A later
// idle has a new key and stays pending; mixed queue messages keep and regather their unrelated signals. False leaves
// every durable form available for retry when the ledger cannot be read or written.
func (s *Service) retractSupervisedIdle(ctx context.Context, todoID string) bool {
	b := &s.supervise
	// A restore holds pendingLoadMu from its durable snapshot through insertion. Take it before dedupMu, matching
	// retractStartUnknowns and simulateSupervisionRestart, so an older snapshot cannot land after this retraction.
	b.pendingLoadMu.Lock()
	defer b.pendingLoadMu.Unlock()
	b.dedupMu.Lock()
	defer b.dedupMu.Unlock()
	keys := map[string]struct{}{}
	collect := func(g superviseSignal) {
		if g.Todo == todoID && g.Kind == sigIdle {
			keys[g.Key] = struct{}{}
		}
	}
	b.mu.Lock()
	if g, ok := b.pending[todoID+"|"+sigIdle]; ok {
		collect(g)
	}
	for _, queue := range b.queued {
		for _, g := range queue {
			collect(g)
		}
	}
	b.mu.Unlock()
	r := &s.relays
	r.mu.Lock()
	if s.relayLocked(context.Background()) {
		if g, ok := r.st.SupervisePending[todoID+"|"+sigIdle]; ok {
			collect(g)
		}
		for _, queue := range r.st.Queue {
			for _, msg := range queue {
				signals, structured := decodeDigestSignals(msg.Signals)
				if !structured {
					if legacyIdleMessage(msg.Text, todoID) {
						collect(superviseSignal{Todo: todoID, Kind: sigIdle, Key: "legacy-idle@" + todoID, At: msg.At})
					}
					continue
				}
				for _, g := range signals {
					collect(g)
				}
			}
		}
	}
	r.mu.Unlock()
	turns := &s.tasks.turns
	turns.mu.Lock()
	if !s.turnsReadLocked(ctx) {
		turns.mu.Unlock()
		return false
	}
	if entry, ok := turns.doc.Turns[todoID]; ok && entry.Signal != nil {
		sig := entry.Signal
		collect(superviseSignal{Todo: todoID, Kind: sig.Kind, Key: sig.Key, Machine: sig.Machine})
	}
	turns.mu.Unlock()
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	slices.Sort(ordered)
	old := make([]superviseSignal, 0, len(ordered))
	for _, key := range ordered {
		old = append(old, superviseSignal{Todo: todoID, Kind: sigIdle, Key: key})
	}
	if !s.turnsToldDurably(ctx, old) {
		return false
	}
	for _, g := range old {
		s.retractSuperviseSignalLocked(g)
	}
	return true
}

// toldStartUnknownIDs returns provisional starts whose confirmation receipt survived the old process. The caller
// decides whether to reconcile only these durable receipts or add current exact creation ids too. Before the ledger's
// first ordinary read, inspect its stored document without loading the book: loading also restores quiet-turn marks
// and publishes their rows, while this recovery scan must not change that lazy projection's ordering.
func (s *Service) toldStartUnknownIDs(ctx context.Context) ([]string, bool) {
	turns := &s.tasks.turns
	turns.mu.Lock()
	defer turns.mu.Unlock()
	doc := turns.doc
	if !turns.loaded {
		st := s.store()
		if st == nil {
			return nil, true
		}
		found, err := st.Setting(ctx, taskTurnsKey, &doc)
		if err != nil {
			s.logf("task turns: %v", err)
			return nil, false
		}
		if !found {
			return nil, true
		}
	}
	var ids []string
	for id, entry := range doc.Turns {
		if sig := entry.Signal; sig != nil && sig.Kind == sigUnjudged && sig.Key == "start-unknown@"+id && sig.Told != nil {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, true
}

// retractToldStartUnknowns runs before restoreSupervisePending takes its snapshot. Taking the same
// pendingLoadMu → dedupMu order as every other retraction keeps a concurrent restore or digest from reinserting it.
func (s *Service) retractToldStartUnknowns(ctx context.Context) bool {
	ids, ok := s.toldStartUnknownIDs(ctx)
	return ok && s.retractStartUnknowns(ctx, ids)
}

// retractConfirmedStartUnknowns reconciles every start whose exact outcome is already known before a session-list
// drain or digest can deliver an old signal. A persisted told receipt is confirmation even when the task Agent is
// listed by another computer or no relay message is queued; a non-offline creation id also confirms before the
// asynchronous auto-link write. False holds the drain so an unreadable relay or ledger cannot get a second chance to
// deliver the stale start result before retraction.
func (s *Service) retractConfirmedStartUnknowns(ctx context.Context, sessions []proto.Session) bool {
	r := &s.relays
	r.mu.Lock()
	loaded := s.relayLocked(context.Background())
	r.mu.Unlock()
	if !loaded {
		return false // drainRelays must not get a second, successful read before retraction.
	}
	ids, ok := s.toldStartUnknownIDs(ctx)
	if !ok {
		return false
	}
	confirmed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		confirmed[id] = struct{}{}
	}
	for _, session := range sessions {
		if session.Status == proto.StatusOffline {
			continue
		}
		if id := session.CreationID(); id != "" {
			confirmed[id] = struct{}{}
		}
	}
	ids = ids[:0]
	for id := range confirmed {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return s.retractStartUnknowns(ctx, ids)
}

// replaceSupervisePending atomically turns an undelivered leader signal into the escalation that replaces it.
func (s *Service) replaceSupervisePending(old, next superviseSignal) {
	s.updateSupervisePending(func(pending map[string]superviseSignal) {
		if cur, ok := pending[old.id()]; ok && cur.Key == old.Key {
			delete(pending, old.id())
			pending[next.id()] = next
		}
	})
}

// forgetSupervisePending removes signals answered or silenced elsewhere, whether or not they were restored in memory.
// A working/review sweep calls this even when the outbox has no matching entry; do not turn those no-ops into writes.
func (s *Service) forgetSupervisePending(keys ...string) {
	r := &s.relays
	r.mu.Lock()
	defer r.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return
	}
	removed := false
	for _, key := range keys {
		if _, ok := r.st.SupervisePending[key]; ok {
			delete(r.st.SupervisePending, key)
			removed = true
		}
	}
	if !removed {
		return
	}
	if len(r.st.SupervisePending) == 0 {
		r.st.SupervisePending = nil
	}
	s.saveRelayLocked()
}

// forgetSupervisePendingPrefix removes any durable outbox entries whose signal id begins with prefix.
func (s *Service) forgetSupervisePendingPrefix(prefix string) {
	r := &s.relays
	r.mu.Lock()
	defer r.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return
	}
	removed := false
	for key := range r.st.SupervisePending {
		if strings.HasPrefix(key, prefix) {
			delete(r.st.SupervisePending, key)
			removed = true
		}
	}
	if !removed {
		return
	}
	if len(r.st.SupervisePending) == 0 {
		r.st.SupervisePending = nil
	}
	s.saveRelayLocked()
}

// superviseTimings are how supervision messages gather and space out (Service.superviseTimes; a zero field: the
// package's superviseGather, superviseEvery or superviseUrgentEvery).
type superviseTimings struct {
	gather, every, urgent time.Duration
	// cooldown: how long routine signals for a conversation wait after a supervision message went to it (zero:
	// routineCooldown's default).
	cooldown time.Duration
}

// superviseTimings are s's own timings, the package's for each one it does not set. Tests set them on their Service,
// so that a digest timer left over from one test never reads what another test's cleanup writes.
func (s *Service) superviseTimings() superviseTimings {
	p := s.superviseTimes
	if p.gather <= 0 {
		p.gather = superviseGather
	}
	if p.every <= 0 {
		p.every = superviseEvery
	}
	if p.urgent <= 0 {
		p.urgent = superviseUrgentEvery
	}
	return p
}

// digestWait is how long until the pending signals are due (false: none): each waits p.gather after it came, and
// p.every (p.urgent for an urgent one) after the last message (last; zero: none yet), except an immediate one.
func digestWait(now, last time.Time, pending []superviseSignal, p superviseTimings) (time.Duration, bool) {
	var due time.Time
	for _, g := range pending {
		at := g.At.Add(p.gather)
		if g.NotBefore.After(at) {
			at = g.NotBefore
		}
		if !last.IsZero() && !g.immediate() {
			gap := p.every
			if g.urgent() {
				gap = p.urgent
			}
			if next := last.Add(gap); next.After(at) {
				at = next
			}
		}
		if !supervisionBatchImmediate(g) && g.batchUntil.After(at) {
			at = g.batchUntil // held for its recipient's routine cooldown (gateDigestBatches)
		}
		if due.IsZero() || at.Before(due) {
			due = at
		}
	}
	if due.IsZero() {
		return 0, false
	}
	return max(0, due.Sub(now)), true
}

// signal records g for the supervisor, unless the same stretch of state was told already, and sees that the message
// goes out when it is due.
func (s *Service) signal(g superviseSignal) {
	s.signalReplacing(nil, g)
}

// signalReplacing records g and, when old is still pending, atomically replaces that older stretch in the outbox.
func (s *Service) signalReplacing(old *superviseSignal, g superviseSignal) {
	if g.Todo != "" {
		if t, ok := s.todoNow(g.Todo); ok && s.reporterStopped(context.Background(), t.Origin) {
			return // /stop silences the reporting task's notices on every route until the user speaks again (#3308)
		}
	}
	if g.level() == supervisionInfo && g.Kind != sigParkedSummary {
		return // Progress is already recorded on the task card; it never wakes the assistant.
	}
	if g.At.IsZero() {
		g.At = time.Now()
	}
	s.restoreSupervisePending(context.Background())
	b := &s.supervise
	if turnSignalKind(g) && s.turnSignalled(g) {
		// How its turn ended was told already, by this manager or by the one before a restart (task_turns.go).
		b.mu.Lock()
		b.init()
		b.sent[g.id()] = g.Key
		b.mu.Unlock()
		return
	}
	s.queueSignalReplacing(old, g)
}

// queueSignal only adds a validated signal to the in-memory digest. It does not change the turn ledger.
func (s *Service) queueSignal(g superviseSignal) {
	s.queueSignalReplacing(nil, g)
}

// preserveIdleHandoff lets a newer turn idle replace the handoff's key without erasing the undelivered decision.
func preserveIdleHandoff(cur, next superviseSignal) superviseSignal {
	if cur.Kind == sigIdle && next.Kind == sigIdle && cur.Handoff && !next.Handoff {
		next.Handoff = true
		next.Detail = cur.Detail
	}
	return next
}

// reportStopHold is how long a report its Agent makes before it stops (report_to_supervisor: done, blocked, a
// question) waits for that stop: the turn's end signal (idle, unjudged, review, interrupted) of the same task comes
// seconds later, and the two then go out as one entry of one supervision message (joinTurnReports) instead of two
// messages a couple of minutes apart. An Agent that goes on working past it has its report told on its own.
var reportStopHold = 2 * time.Minute

// releaseReportsLocked: g ends its task's turn, so the reports of that task held for it (reportStopHold) are due now
// and go out with it. It returns their ids for the durable outbox; s.supervise.mu is held.
func (s *Service) releaseReportsLocked(g superviseSignal) []string {
	if !turnSignalKind(g) || g.Todo == "" {
		return nil
	}
	b := &s.supervise
	var ids []string
	for id, cur := range b.pending {
		if cur.Kind == sigReport && cur.Todo == g.Todo && !cur.NotBefore.IsZero() {
			cur.NotBefore = time.Time{}
			b.pending[id] = cur
			ids = append(ids, id)
		}
	}
	return ids
}

// queueSignalReplacing also removes the older stretch it supersedes from the durable outbox.
func (s *Service) queueSignalReplacing(old *superviseSignal, g superviseSignal) {
	if g.Kind != sigAsk {
		g = s.currentSupervisorSignal(g)
	}
	if g.At.IsZero() {
		g.At = time.Now()
	}
	b := &s.supervise
	b.mu.Lock()
	b.init()
	// This early cache is not an unhandled source's delivery receipt: an owner wait
	// can discard the entry before dispatch. Its durable fingerprint deduplicates delivery.
	unhandled := g.Kind == sigInbox && g.Inbox != nil && g.Inbox.ToAgent == "unhandled"
	if b.sent[g.id()] == g.Key && !unhandled {
		b.mu.Unlock()
		return
	}
	if cur, ok := b.pending[g.id()]; ok {
		if g.Kind == sigFailed {
			if cur.Key != g.Key && newerFailedSignal(cur, g) {
				b.mu.Unlock()
				return // a restored or delayed old attempt cannot replace the newer pending one
			}
			if cur.Key == g.Key {
				g.At = cur.At // the same failed attempt keeps its queue position and delivery generation
				if g.Generation.IsZero() {
					g.Generation = cur.Generation
				}
				if g.NotBefore.IsZero() {
					g.NotBefore = cur.NotBefore
				}
			}
		} else if g.Kind != sigParkedSummary || g.Key == cur.Key {
			g.At = cur.At // still waiting: it keeps its place; a new summary day keeps its own date
		}
		if g.Ask == "" {
			g.Ask = cur.Ask
		}
		if g.Detail == "" && cur.Key == g.Key {
			g.Detail = cur.Detail // the watch's sight of what an event told already
		}
		g = preserveIdleHandoff(cur, g)
		if g.Kind == sigReport && cur.Key != g.Key {
			g = joinReports(cur, g) // two questions before the message went: both are asked
		}
		if g.Kind == sigInbox {
			if cur.Key != g.Key {
				g = joinInbox(cur, g) // more messages before the message went: the newest, with how many came
			} else {
				g.Inbox = cur.Inbox // the same message again: counted once
			}
		}
		if g.Kind == sigReport && !cur.NotBefore.IsZero() && (g.NotBefore.IsZero() || cur.NotBefore.Before(g.NotBefore)) {
			g.NotBefore = cur.NotBefore // a second report does not hold the first one longer (reportStopHold)
		}
		g.batchTo, g.batchUntil = cur.batchTo, cur.batchUntil
	}
	held := s.releaseReportsLocked(g)
	s.updateSupervisePending(func(pending map[string]superviseSignal) {
		for _, id := range held {
			if cur, ok := pending[id]; ok {
				cur.NotBefore = time.Time{}
				pending[id] = cur
			}
		}
		// sendDigest may already have taken the older stretch out of b.pending. Its durable outbox entry remains until
		// real delivery or queue admission, so it is the reliable evidence that this different Key replaces an unACKed one.
		if cur, ok := pending[g.id()]; ok {
			g.PendingPredecessors = slices.Clone(cur.PendingPredecessors)
			if cur.Key != g.Key && !slices.Contains(g.PendingPredecessors, cur.Key) {
				g.PendingPredecessors = append(g.PendingPredecessors, cur.Key)
			}
		}
		if old != nil {
			if cur, ok := pending[old.id()]; ok && cur.Key == old.Key {
				delete(pending, old.id())
			}
		}
		pending[g.id()] = g
	})
	b.pending[g.id()] = g
	s.scheduleDigestLocked(time.Now())
	b.mu.Unlock()
}

// scheduleDigestLocked sets the timer for when the pending signals are due; s.supervise.mu is held.
func (s *Service) scheduleDigestLocked(now time.Time) {
	if s.runCtx().Err() != nil {
		return
	}
	b := &s.supervise
	batch := s.supervisionBatchInterval(context.Background())
	b.syncBatchInterval(batch)
	list := make([]superviseSignal, 0, len(b.pending))
	for _, g := range b.pending {
		list = append(list, g)
	}
	wait, ok := digestWaitWithBatch(now, b.last, list, s.superviseTimings(), batch)
	if !ok {
		return
	}
	at := now.Add(wait)
	if b.timer != nil && !b.timerAt.After(at) {
		return // set already, for no later
	}
	if b.timer != nil {
		b.timer.Stop()
	}
	b.timerAt = at
	b.timer = time.AfterFunc(wait, func() { s.sendDigest(s.runCtx()) })
}

// retryDigestLocked tries the digest again in d, unless one is due sooner (b.mu held).
func (s *Service) retryDigestLocked(d time.Duration) {
	if s.runCtx().Err() != nil {
		return
	}
	b := &s.supervise
	at := time.Now().Add(d)
	if b.timer != nil && !b.timerAt.After(at) {
		return
	}
	if b.timer != nil {
		b.timer.Stop()
	}
	b.timerAt = at
	b.timer = time.AfterFunc(d, func() { s.sendDigest(s.runCtx()) })
}

// watchSignals are what t and its agents show now that the supervisor should hear of, by their state alone: a task
// waiting for review (told once per review: the turns' ledger keeps that it was told across a restart of the
// assistant, task_turns.go; a review no turn on the ledger raised is told again after one), an
// agent waiting for an answer (at once for a quiet task), working without progress for long, or in any other state
// with nothing new in its transcript for longer still (silentAfter, #5124). Nothing more for a
// task waiting for review or for the user, only that its time came for a parked one, and nothing for one done or not
// supervised. A task nobody works on any more
// is told from what the minute pass found (orphanSignal, tasks_orphan.go), since that asks whether its computers answer.
func watchSignals(t Todo, agents []proto.Session, now time.Time, groups ...superviseGroup) []superviseSignal {
	v := t.Supervision
	if v == nil || t.DoneAt != nil {
		return nil
	}
	switch v.state(t) {
	case reviewClosing, reviewUserCheck:
		return nil
	case reviewTimedOut:
		return []superviseSignal{{Todo: t.ID, Kind: sigTimedOut, Key: reviewKey(v)}}
	case reviewPending:
		if h := v.handoffResult(); h != nil {
			if h.ToldAt.IsZero() {
				return []superviseSignal{handoffResultSignal(t.ID, h)}
			}
			return nil
		}
		return []superviseSignal{{Todo: t.ID, Kind: sigReview, Key: reviewKey(v)}}
	case reviewParked: // nothing until its time, then that it came (parked.go)
		if g, ok := parkedDueSignal(t, now); ok {
			return []superviseSignal{g}
		}
		return nil
	}
	group := superviseGroup{tasks: []Todo{t}, sessions: agents}
	if len(groups) > 0 {
		group = groups[0]
	}
	if g, ok := allStoppedSignal(t, group.tasks, group.sessions, now); ok && !group.unknown {
		return []superviseSignal{g} // one group diagnosis, not another silent signal for each member
	}
	if v.state(t) == reviewNeedsUser {
		return nil
	}
	var out []superviseSignal
	// An agent the task was handed over from is not watched: it would raise stalled from its last update forever (#6483).
	for _, c := range currentTaskAgents(agents, supersededAgents(t, agents)) {
		base := superviseSignal{Todo: t.ID, Machine: c.MachineID, Session: c.SessionID}
		switch {
		case c.State == proto.StateNeedsYou || c.PendingInteractions > 0:
			since := cmp.Or(c.StateSince, c.UpdatedAt)
			if wait := now.Sub(since); t.Quiet || wait >= superviseNeedsAfter {
				g := base
				g.Kind, g.Key, g.Minutes = sigNeedsYou, c.SessionID+"@"+since.UTC().Format(time.RFC3339), int(wait.Minutes())
				out = append(out, g)
			}
		case c.Assistant && c.State != proto.StateWorking:
			// An assistant conversation (a task leader) waits on its workers, each told of on its own; a message always reaches it.
		default:
			moved := c.UpdatedAt
			if c.ActivityAt != nil && c.ActivityAt.After(moved) {
				moved = *c.ActivityAt
			}
			// Working: stuck past stallAfter. Any other state (waiting on background work, a wake, stopped or gone)
			// is told past silentAfter, so a state that should end by itself and never does is still heard of (#5124).
			after := silentAfter(v.ExpectMinutes)
			if c.State == proto.StateWorking {
				after = stallAfter(v.ExpectMinutes)
			}
			if still := v.activeTimeSince(moved, now); !moved.IsZero() && still >= after {
				g := base
				g.Kind, g.Key, g.Minutes = sigStalled, c.SessionID+"@"+moved.UTC().Format(time.RFC3339), int(still.Minutes())
				g.Detail = strings.TrimSpace(c.Activity)
				out = append(out, g)
			}
		}
	}
	return out
}

// reviewKey names one review a task waits for: when it began to wait.
func reviewKey(v *Supervision) string {
	if r := v.externalPRWake(); r != nil {
		return externalPRReviewPrefix + r.At.UTC().Format(time.RFC3339Nano)
	}
	return "review@" + v.UpdatedAt.UTC().Format(time.RFC3339Nano)
}

// stallAfter is how long without progress counts as stuck for a task that should take expect minutes.
func stallAfter(expect int) time.Duration {
	return max(superviseStallMin, time.Duration(expect)*time.Minute*3/2)
}

// silentAfter is how long a supervised task's agent may write nothing to its transcript, whatever its state, before
// the supervisor hears of it: the fallback for a wait nothing ends (#5124).
func silentAfter(expect int) time.Duration {
	return max(superviseSilentMin, time.Duration(expect)*time.Minute*3/2)
}

// superviseLoop looks at the supervised tasks every superviseTick, and lets the supervision messages go out.
func (s *Service) superviseLoop(ctx context.Context) {
	t := time.NewTicker(superviseTick)
	defer t.Stop()
	s.turnOf(ctx, "") // the turns' ledger is kept from the manager's start, not from its first tick (task_turns.go)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.rehomeLeaderlessWorkers(ctx) // a worker whose leader's task closed is rehomed even with no signal of its own
		s.handOverFormerMainTasks(ctx) // what a replaced main conversation still holds goes to the main one (main_handover.go)
		s.backgroundStranded(ctx)      // what waits for a background supervisor that is gone is told again (supervise_background.go)
		s.superviseWatch(ctx, time.Now())
		s.drainHeldRelays(ctx, time.Now()) // nothing held for the owner is left behind (relay_presence.go)
	}
}

// superviseWatch records what the supervised tasks' agents show now (watchSignals), hands a review a side
// conversation left waiting to the main conversation's follow-up (handOverUnreviewed), and lets go of a hold on the
// main conversation's pushes that outlived superviseHoldMax.
func (s *Service) superviseWatch(ctx context.Context, now time.Time) {
	s.autoReviewTick(ctx, now)
	s.sessionReviewTick(ctx, now)
	s.askTick(now)                 // cards held past their time go up a rung (ask_route.go)
	s.handOverUnreviewed(ctx, now) // a review a side conversation left waiting goes to the main one's follow-up (side_handover.go)
	routed := s.routedSessions()
	s.pruneTurns(ctx) // the turns' ledger: read before the first signal (what was told before a restart is not told again), closed tasks off it
	s.recoverTaskInboxEvents(ctx)
	// Only the open tasks: everything below passes the finished ones by, and there can be thousands of them.
	tasks := s.todosWhere(ctx, "*", func(t *Todo) bool { return t.DoneAt == nil })
	s.recoverOutsideDirectionDigests(ctx)
	s.pruneAllStoppedDedup(ctx)
	s.prunePRMergeWakes(ctx)
	s.sweepTodoAfterReady(ctx)
	s.sweepParentCloseout(ctx)
	lists := machineSessions{}     // each machine listed once for all the supervised tasks (#3029)
	history := s.planHistoryOnce() // and the plans read once, when a task's start needs them (start_remedy.go)
	s.backfillBriefs(ctx, tasks)   // tasks from before briefs get one from what they still have (task_brief.go)
	groups := allStoppedTaskGroups(tasks)
	backfilled := 0
	for _, t := range tasks {
		if t.Supervision == nil && t.DoneAt == nil {
			// A legacy completion judged while no main existed deliberately stayed unjudged. Once main is configured,
			// recover that exact ledger turn instead of leaving it outside every future supervised sweep (#5732).
			main := s.settings(ctx).Main
			if main == "" {
				continue
			}
			e, ok := s.turnOf(ctx, t.ID)
			if !ok || e.State != turnDone || e.Judged != nil {
				continue
			}
			if err := s.superviseTodo(ctx, t.ID, main, nil, 0); err != nil {
				continue
			}
			current, ok := s.todoNow(t.ID)
			if !ok || current.DoneAt != nil {
				continue
			}
			t = current
			s.watchTurn(ctx, t, s.taskAgentsIn(ctx, t, lists))
			continue
		}
		if t.Supervision == nil || t.DoneAt != nil {
			continue
		}
		s.remedyWatch(ctx, t, now, history) // a failed start's kept tab expired (start_remedy.go)
		t = s.checkTaskHandoff(ctx, t, now)
		agents := s.taskAgentsIn(ctx, t, lists)
		s.closeStalledTask(ctx, t, agents, now)
		if current, ok := s.todoNow(t.ID); ok {
			t = current
		} else {
			continue
		}
		if backfilled < closeoutBackfillMost {
			var scanned bool
			t, scanned = s.backfillCloseout(ctx, t, lists, now)
			if scanned {
				backfilled++
			}
		}
		group := s.allStoppedGroup(ctx, t, groups[t.ID], lists)
		allStopped := false
		stalled := map[string]bool{}
		for _, g := range watchSignals(t, agents, now, group) {
			allStopped = allStopped || g.Kind == sigAllStopped
			stalled[g.Session] = stalled[g.Session] || g.Kind == sigStalled
			if g.Kind == sigNeedsYou && routed[[2]string{g.Machine, g.Session}] {
				continue // its card is with the supervisor as an ask already
			}
			s.signal(g)
		}
		if !allStopped && t.Supervision.state(t) == "" { // going on: neither parked nor with the owner nor waiting for review
			for _, c := range currentTaskAgents(agents, supersededAgents(t, agents)) {
				if g, ok := s.noProgressSignal(t, c, now); ok && !stalled[c.SessionID] {
					s.signal(g)
				}
			}
		}
		if g, ok := s.orphanSignal(t); ok && !allStopped { // a fresh complete group outranks the minute pass's orphan snapshot
			s.signal(g)
		}
		ownerEvidencePending := s.parkDecidedRetry(ctx, t) // a stored owner answer outranks legacy reconciliation
		t = s.reconcileLegacyOwnerPriority(ctx, t, ownerEvidencePending)
		if g, ok := s.automaticParkSummary(ctx, t, now.In(s.location(ctx))); ok {
			s.signal(g)
		}
		if g, ok := parkDecidedSignal(t); ok { // a decision ended its parking, and no message carried that yet (parked.go)
			s.signal(g)
		}
		if !ownerEvidencePending { // a stored or unreadable answer must not be replaced with another question
			if g, ok := s.undecidableSignal(ctx, t, now); ok {
				s.signal(g)
			}
			s.signalCardlessWait(ctx, t, agents, lists, now) // a review the owner cannot answer (#12648)
		}
		if g, ok := s.deliveryHoldSignal(ctx, t, now); ok { // an owner hold with no card goes to its supervisor first
			s.signal(g)
		}
		s.briefClock(ctx, t, now)   // past its brief's clock with no end (task_brief_scope.go)
		s.watchTurn(ctx, t, agents) // a turn that ended with no working heard, or was left half handled (task_turns.go)
	}
	b := &s.supervise
	b.mu.Lock()
	changed := false
	for id, at := range b.hold {
		if now.Sub(at) > superviseHoldMax {
			delete(b.hold, id)
			delete(b.visible, id)
			changed = true
		}
	}
	b.mu.Unlock()
	if changed {
		s.publish(ctx)
	}
}

// SetSuperviseTimings makes supervision messages gather for gather and come at most every every (urgent: urgent), and
// answers what puts them back: for the hub's end-to-end tests, which cannot reach the package's own.
func SetSuperviseTimings(gather, every, urgent time.Duration) (restore func()) {
	g, e, u := superviseGather, superviseEvery, superviseUrgentEvery
	superviseGather, superviseEvery, superviseUrgentEvery = gather, every, urgent
	return func() { superviseGather, superviseEvery, superviseUrgentEvery = g, e, u }
}
