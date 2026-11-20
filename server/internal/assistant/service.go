package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/saved"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/remoteimage"
	"github.com/nwverse/persona/server/internal/secretkey"
	"github.com/nwverse/persona/server/internal/taskworkspace"
)

// Options are what a Service runs with.
type Options struct {
	Hub Hub
	// Store is read on every use: nil, or a func returning nil, means no database (unit tests); the state then lives
	// in memory only.
	Store func() Store
	Logf  func(format string, args ...any)
	// KB is the knowledge base it keeps (kb.go); nil: none.
	KB *KBOptions
	// Devices is the manager's own connections to the computers: with them, what comes from the nodes is its own
	// (fleet.go); nil (unit tests): no fleet.
	Devices Devices
	// Trust says which computers wait to be confirmed (machine_trust.go): the devices, from the hub's handover; nil
	// (unit tests): none.
	Trust MachineTrust
	// AgentDirs keeps each computer's hidden Agent folders (hidden_agent_dirs.go): the devices; nil: nothing hidden.
	AgentDirs AgentDirs
	// SecretKey is the path of the key the model providers' keys are sealed with (the manager's own secret.key,
	// models_launchers.go); "" makes one for this run only (tests).
	SecretKey string
	// Build is this manager's version, kept with what the north star records (north_star.go); "" in tests.
	Build string
	// ID is the manager this assistant is ("" and assistantwire.AssistantID: the built-in one). Its directories on a
	// computer are named after it (identity.AssistantDirName, docs/assistant/managers.md "Directories on a computer").
	ID string
	// RepositoryConfig is this manager's operator configuration file. Only its
	// explicit repository_trusted_targets can opt private destinations in.
	RepositoryConfig string
	// RemoteImageCache is where the remote image route keeps the pictures it fetched (remote_image.go; a cache the
	// directory of which may be deleted at any time); "" keeps them in no file (tests).
	RemoteImageCache string
}

// Service is the assistant. It implements Handler.
type Service struct {
	// agentDirs keeps each computer's hidden Agent folders (hidden_agent_dirs.go); nil: none.
	agentDirs AgentDirs
	// remoteImages fetches the pictures replies name by address for the apps (remote_image.go).
	remoteImages *remoteimage.Fetcher
	// inboxExt is what the extensions wrote for the Inbox, read at most once a minute (extension_context.go).
	inboxExt inboxExtensionCache
	// extTr are the extensions' transcription jobs running now (extension_transcribe.go).
	extTr extTranscriptions
	// id is Options.ID: the manager this assistant is.
	id               string
	repositoriesMu   sync.Mutex
	repositoryConfig string
	repositoryCheck  func(context.Context, repository.Identity) error // tests only; production uses the guarded checker
	// repoExt is the extensions' repository providers (SetExtensions; repository_providers.go); nil: none.
	repoExt repository.Extensions
	// repoStarted is closed once repoExt lists its providers (its manager has started); nil: it always does.
	repoStarted <-chan struct{}
	// reportedLinks are the reported links a provider is asked about off the event loops (todo_repository_providers.go).
	reportedLinks reportedLinkBook
	// build is Options.Build.
	build string
	// northStarOn: the north star's recording start is saved (north_star.go).
	northStarOn atomic.Bool
	// nodeAnswering: the computers' requests being answered now, but the ones that wait on a person (idle.go).
	nodeAnswering atomic.Int64
	// taskStarting counts queued and running task starts through linking or failure bookkeeping (idle.go).
	taskStarting atomic.Int64
	// leaderStarts counts leaders through linking: only legacy one-shot discovery waits for them (task_leader.go).
	leaderStarts atomic.Int64
	// mcpChanges wakes tool discovery when task membership or leader rights change.
	mcpChanges mcpToolChanges
	// ext is the extensions' tools in this process (extensions.go SetExtensions); nil: none (tests).
	ext ExtensionTools
	// startDefaultsFor answers a task start's defaults from the extension layer (start_defaults.go); nil: no defaults.
	startDefaultsFor func(ctx context.Context, facts StartFacts) (StartDefaults, bool, error)
	// refs is the provider extensions' values for launcher keys that are references (launcher_secrets.go); nil: none.
	refs launcherSecrets
	// trans reaches the transcription contributions (meeting_transcribe_ext.go): the extensions' when they offer it
	// (SetExtensions); nil: none (tests).
	trans              transcribers
	transcriptionReady transcriptionReadiness
	// heard is told when the owner speaks in a conversation (extensions.go ownerHeard): the extensions' when they
	// listen; nil: none.
	heard ownerHeard
	// humanSent: the messages a person's own app sent, as the hub vouched (secret_handoff.go).
	humanSent humanSends

	hub   Hub
	store func() Store
	// trustSrc is Options.Trust.
	trustSrc MachineTrust
	logf     func(format string, args ...any)
	// runRetry is how long after a failed run the retry goes (0: runRetryDelay; tests shorten it).
	runRetry time.Duration
	// turnPoll is how often a run's watcher reads its turn (0: turnPollInterval; tests shorten it).
	turnPoll time.Duration
	// taskRetry is how long a task waits before its one retry after a dropped link (0: taskStartRetryDelay; tests
	// shorten it).
	taskRetry time.Duration
	// continuePolled, when set (tests), is told each time a sent "Continue"'s guard begins its next read of the agent
	// (n from 1), after it handled the read before: a test waits on the guard with it (continueTask).
	continuePolled func(n int)
	// leaderResume bounds each attempt to put a supervision message into a closed task leader agent, and is the first
	// wait of the retries after one failed (0: leaderResumeWait; tests shorten it, task_leader.go leaderResumeTime).
	leaderResume time.Duration
	// superviseTimes are how supervision messages gather and space out (zero fields: the package's; tests shorten
	// them, supervise_signals.go superviseTimings).
	superviseTimes superviseTimings
	// todoAskAfter is how long a linked to-do goes without progress before its recurring check asks the owner (0:
	// scheduleAskAfter's floor; tests shorten it, schedule_todo_evidence.go).
	todoAskAfter time.Duration
	// retryNow is the clock a due retry checks the task's time against (retryStillDue; nil: time.Now, tests set it).
	retryNow func() time.Time

	mux    *http.ServeMux
	routes []string

	standingJudgeModel jsonModel  // nil: the configured agent.delegate model; tests can supply a deterministic model.
	standingMu         sync.Mutex // standing_authorizations.go: revocation and automatic answers serialize

	// rules: the answers to suggested rules, and the last count of what earns one (rule_suggest.go).
	rules ruleSuggestState
	// setupMu serializes the reads and the one write of the weekly setup block's book (setup_suggest.go).
	setupMu sync.Mutex

	// prepared is what this hub last gave a machine's node for a directory (key: machine id NUL directory), the
	// CLAUDE.md and the plugin skills, by prepareSpaceIf; refreshInstructions gives again what differs.
	prepareLocks cardLocks
	preparedMu   sync.Mutex
	prepared     map[string]preparedState

	epoch string
	// born is when this manager started: a background supervisor started before it may be in a profile with no login
	// (backgroundSignedOut).
	born  time.Time
	pubMu sync.Mutex
	// urgentWake wakes urgentLoop (urgent_call_loop.go): a call rang.
	urgentWake chan struct{}
	seq        uint64

	startingAssistants assistantStarts // machine_privacy.go

	// running is Run's context: it ends when the service stops (runCtx).
	running atomic.Pointer[context.Context]

	// interrupts: the interrupted receipts re-sent when their tasks change (interruptions.go).
	interrupts interruptBook

	titles           titleBook             // titles.go
	phoneKept        keptPhoneTools        // fleet_tools.go: the phone's tools as the hub last listed them
	agentNames       agentNamingBook       // agent_naming.go
	prefs            settingsBook          // profile.go
	main             mainBook              // start.go
	archived         archiveBook           // conversations.go
	tags             conversationTagsBook  // conversation_tags.go
	directionFilings directionSessionsBook // direction_sessions.go
	origins          originBook            // origins.go
	guests           guestBook             // guest.go
	relays           relayBook             // conversations.go
	chatReplies      chatReplyBook         // chat_reply.go: what conversations that wrote as the user were told of the chats
	undelivered      undeliveredBook       // undelivered.go: the owner's messages that could not go in
	quotes           quoteBook             // relay_quote.go: the owner's messages relays carried, verified
	avail            availBook             // availability.go
	quiet            quietBook             // quiet.go
	taskFileBook     taskFileBook          // task_files.go: the files tasks handed over, by todo id
	supervise        superviseBook         // supervise_signals.go
	progress         progressBook          // supervise_progress.go
	preempt          preemptBook           // owner_preempt.go
	tbriefs          taskBriefBook         // task_brief.go
	search           searchIndex           // search_index.go

	deletedConversations conversationDeleteBook // conversation_delete.go
	conversationWrites   sync.RWMutex

	modelPicks modelPickBook // model_pick.go
	// noBackground: there is no background supervisor whatever the setting says, so the main conversation gets its
	// own tasks' signals. Only the tests of what the main conversation's own supervision does set it; the hub never does.
	noBackground bool
	// briefsOptional: task_plan takes a task without done_when / expect_minutes (briefRequired). Only the tests of
	// what a plan does once it exists set it; the hub never does.
	briefsOptional bool
	background     backgroundBook    // supervise_background.go
	autoRuns       autoReviewBook    // supervise_autoreview.go
	sessionReviews sessionReviewBook // agents_session_review.go
	reports        reportBook        // reports.go
	upReports      reportRate        // agent_control.go: report_to_supervisor
	noteRate       reportRate        // todo_decisions.go: todo_decision_add
	dispatch       dispatchBook      // dispatch.go: dispatch_start, dispatch_read
	admission      admissionBook     // admission.go: execution-agent resource and direction gate
	adoptMu        sync.Mutex        // agent_control.go: one agent_supervise at a time
	placeBook      placeBook         // places.go
	accept         acceptBook        // acceptance.go
	pushQuiet      quietSwitch       // push_quiet.go: the hub's low-distraction switch as last read
	deploy         deployProbe       // deploy_check.go: is the work in the running hub
	parkReplies    parkReplyBook     // park_reply.go: bounded source reads for explicitly bound person replies

	reviewDismissals taskReviewDismissalBook // task_review_dismissals.go
	repoRootsMu      sync.Mutex              // pr_number_ref.go: one write of the repository roots at a time
	// ownerVerifiesAll: every deployed task asks the owner to verify, not only the big ones (ownerVerifies; tests).
	ownerVerifiesAll         bool
	later                    laterBook    // later.go: questions that can wait for a briefing
	brief                    briefBook    // briefing.go
	reporters                reporterBook // reporters.go
	sched                    schedule     // schedule.go
	delayedMu                sync.Mutex   // delayed_waits.go: one local writer to the durable wait backlog
	delayedRecoveryMu        sync.Mutex   // one restore pass; tracks entries already armed by this incarnation
	delayedReceiptsRecovered bool         // initial receipt-claim requeue has completed for this incarnation
	delayedRestored          map[string]bool
	tasks                    tasksState    // tasks.go
	taskSpaces               taskSpaceBook // task_space.go: herdr leader-task placement and ownership
	questions                questions     // tools.go (ask_user)
	// deadlineSweeping: a sweep of the decisions past their deadline is running (decision_deadline.go).
	deadlineSweeping atomic.Bool
	askTodos         askTodoBook       // ask_todo.go: the questions nobody answered, now to-dos
	askLaters        askLaterBook      // ask_later.go: the questions asked without waiting, still out
	approvals        approvalBook      // approval_clarify.go: clarified prepared approvals and their result delivery
	detached         detachedGrantBook // approval_detached.go: one-time continuation grants for command gates
	// minutesMu is held while confirmed action items of meeting minutes become to-dos (kb_minutes.go): one
	// confirmation at a time reads and records what the others made.
	minutesMu  sync.Mutex
	attend     attendState       // meeting_feed.go: feeding the meetings Persona attends, and their turns
	room       roomState         // meeting_room_tools.go: the meetings' rooms, their asks and sources
	meetLive   meetingLiveEvents // meeting_events.go: the meeting_live events, at most one a second per recording
	justMe     justMeBook        // meeting_justme.go: the Just me turns
	meetLocks  meetingLocks      // meeting_followup.go: one creation at a time per recording
	live       contextState      // context.go
	todos      todoBook          // todos.go
	work       workBook          // agents_work.go: the Agents business
	reply      replyBook         // agents_reply.go: the reminders of questions left unanswered
	chans      channelsBook      // channels.go
	fbot       feishuBook        // feishu.go
	tposts     threadPoster      // feishu_thread_posts.go: what a todo tells its chat thread
	tevents    todoEvents        // todo_events.go: what the extensions are told of the todos
	tpush      todosPush         // todos.go: the {"type":"todos"} waiting for the rest of its write
	directions directionBook     // directions.go: the leaders' records and what the extensions were told of them
	lreports   leaderReportBook  // leader_reports.go: what the leaders' tasks were reported as
	dtree      directionTreeBook // direction_tree.go: the direction tree the apps were last told of
	tview      todoViewBook      // todo_view_push.go: what the apps were told each to-do is filed under
	offers     routeBook         // route.go: the directions offered for New chats
	standing   standingBoard     // feishu_board.go: the standing "waiting for you" card
	workbench  workbenchState    // feishu_workbench_pin.go: the pinned workbench
	companions companionState    // feishu_companions.go: the notices for a companion bot
	kb         kbState           // kb.go
	inbox      inboxBook         // inbox_book.go
	board      boardBook         // workspace_board.go: the workspace board
	viewEvents viewNotifications // views.go: only coalesced notifications; contents are stored first
	viewMemo   viewMemo          // views.go: encoded cards for the feed snapshot, checked against the database

	devs          Devices // the computers (devices.go); nil: no fleet
	mcpWaitMu     sync.Mutex
	mcpWaits      map[string]*mcpWaitReporter // live opted-in mcp.call requests, by their validated call id
	agentArchives agentArchiveBook
	orphans       orphanBook      // tasks_orphan.go: the open to-dos nobody works on
	agentNative   agentNativeBook // the tools' own archives (agent_archive_native.go)
	switchQ       switchQueue     // agents_switch's switches waiting for their agents' turns to end (agents_switch.go)
	fleet         fleetBook       // what comes from the nodes (fleet.go)
	nextSug       nextSuggestBook // suggest_next.go: the hub-made next-message suggestions
	askUp         escalationBook  // cards that reached the user through an escalation (ask_route.go), under fleet.mu
	// delegateHook stands in for Delegate in tests (fleet_delegate.go).
	delegateHook func(context.Context, DelegateParams) (DelegateResult, error)
	// fleetEnded, when set, sees every card's end the channels are told (tests).
	fleetEnded func(ix proto.Interaction, r *proto.Resolution)
	// ixResolved, when set, sees every answered card the assistant is told of (tests).
	ixResolved func(InteractionResolved)
	models     modelsState   // models.go: the model configuration
	health     healthBook    // launcher_health.go: each machine's launchers
	trust      trustBook     // machine_trust.go: the computers waiting to be confirmed
	ready      readinessBook // readiness.go: the main conversation's readiness

	// pieceWriteHook, when set, runs as a write of part_of/after (editTodo, insertTodo) has passed its caller's check
	// and is about to take todoBook.writing (tests: two writes held there both passed against the same graph).
	pieceWriteHook func(id string)
	// editWriteHook, when set, runs as any edit (editTodo) is about to take todos.writing, after pieceWriteHook: tests
	// use it to change the to-do in between (a DELETE racing the edit).
	editWriteHook func(id string)
	// acceptSweepHook, when set, runs in the acceptance sweeps' unlocked window, after a todo is read (tests).
	acceptSweepHook func(id string)
	// sendsSettleHook, when set, runs in sendsSettle once the task is opened again, before the turn held for it is
	// taken up (tests).
	sendsSettleHook func()
	// askRetryHook, when set, runs as the asks route's Retry has found the failed start its ask names, before the start
	// reads the to-do again (tests: the to-do reopens or fails anew in between).
	askRetryHook func(id string)
	// sessionsSeen, when set, sees every machine's list SessionsChanged is handed (tests).
	sessionsSeen func(DeviceSessions)

	// asks are the questions of attended meetings being answered (meeting_ask.go); meetingModel stands in for the
	// meeting.answer model in tests (nil: the configured one).
	asks         meetingAsks
	meetingModel kb.Model
	// hubTr is the meetings transcribed on the hub (meeting_transcribe.go); sockets makes socketMux, ServeSocket's
	// (meeting_audio.go).
	hubTr     hubRuns
	sockets   sync.Once
	socketMux *http.ServeMux
	// navMu: one change of the user's navigation choices at a time, each read and written whole (navigation.go)
	navMu sync.Mutex
}

var _ Handler = (*Service)(nil)

// New makes the assistant; nothing runs until the hub calls it.
func New(o Options) *Service {
	s := &Service{hub: o.Hub, store: o.Store, logf: o.Logf, mux: http.NewServeMux(), epoch: randomHex(8), tasks: newTasksState(), born: time.Now(),
		devs: o.Devices, trustSrc: o.Trust, agentDirs: o.AgentDirs, id: o.ID, build: o.Build, repositoryConfig: o.RepositoryConfig, urgentWake: make(chan struct{}, 1)}
	s.inbox.lang = func() i18n.Lang { return s.lang(context.Background()) }
	s.remoteImages = remoteimage.New(remoteimage.Options{CacheDir: o.RemoteImageCache, Logf: o.Logf, DoH: s.remoteImageDoH})
	if s.store == nil {
		s.store = func() Store { return nil }
	}
	if s.logf == nil {
		s.logf = func(string, ...any) {}
	}
	// The kept old keys too while a rotation keeps them (keyrotate.go).
	s.models.secret = secretkey.NewWithFallback(o.SecretKey)
	s.archiveRoutes()
	s.conversationTagRoutes()
	s.conversationDeleteRoutes()
	s.modelPickRoutes()
	s.agentArchiveRoutes()
	s.agentNamingRoutes() // agent_naming_request.go
	s.alwaysRoutes()
	s.undeliveredRoutes()
	s.standingAuthorizationRoutes()
	s.ruleSuggestionRoutes()
	s.northStarRoutes()
	s.urgentCallRoutes() // urgent_call.go
	s.reporterRoutes()
	s.directionRoutes()
	s.dispatchPolicyRoutes() // dispatch_policy_routes.go
	s.startPresetRoutes()
	s.startDefaultsRoutes() // start_defaults_routes.go
	s.directionTreeRoutes()
	s.directionSessionRoutes()
	s.routeRoutes()
	s.agentRouteRoutes()
	s.directionNameRoutes()
	s.directionNotesRoutes()
	s.originRoutes()
	s.profileRoutes()
	s.skillImportRoutes()
	s.readinessRoutes()
	s.availabilityRoutes()
	s.startRoutes()
	s.skillRoutes()
	s.workoutRoutes()
	s.scheduleRoutes()
	s.scheduleGrantRoutes()
	s.placeRoutes()
	saved.Register(saved.Host{Store: func() any { return s.store() }, Event: s.event, Handle: s.handle, Full: fullRole, BodyErr: writeBodyErr})
	s.repositoryRoutes()
	s.taskRoutes()
	s.viewActionRoutes()
	s.todayRoutes()
	s.briefRoutes()
	s.todoRoutes()
	s.agentRoutes()
	s.sessionReviewRoutes()
	s.workspaceBoardRoutes()
	s.navigationRoutes() // navigation.go: the page the apps open on
	s.channelRoutes()
	s.companionRoutes()
	s.kbInit(o.KB)
	s.kbRoutes()
	s.searchRoutes()
	s.inboxRoutes()
	s.modelConfigRoutes()
	s.modelProfileRoutes()
	s.launcherRoutes()
	s.machineLauncherRoutes()
	s.workSessionStartRoutes()
	s.launcherQuotaRoutes()
	s.providerProbeRoutes()
	s.providerCatalogRoutes()
	s.launcherImportRoutes()
	s.launcherHealthRoutes()
	s.remoteImageRoutes()
	return s
}

// handle adds one of the routes the hub relays.
func (s *Service) handle(pattern string, fn http.HandlerFunc) {
	s.routes = append(s.routes, pattern)
	s.mux.HandleFunc(pattern, fn)
}

func (s *Service) Routes() []string { return append([]string(nil), s.routes...) }

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := s.deletedConversationRequest(r); err != nil {
		apierr.Call(w, err)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// fullRole: the request came from a device that may do everything (RoleHeader; none: a test's own request).
func fullRole(r *http.Request) bool {
	role := r.Header.Get(RoleHeader)
	return role == "" || role == roleFull
}

// todoWriter says whether r may change todos: a full device, or an extension token. The hub relays an extension's
// request only to the routes its granted scopes open (extwire.RouteScopes: todos.write), so reaching a todo write
// here already means the extension holds todos.write.
func todoWriter(r *http.Request) bool {
	return fullRole(r) || r.Header.Get(RoleHeader) == roleExtension
}

// runCtx ends when the service stops: Run's context; background before Run (tests).
func (s *Service) runCtx() context.Context {
	if p := s.running.Load(); p != nil {
		return *p
	}
	return context.Background()
}

// owner is how the assistant calls the user: manager.json's assistant.owner, else the Chinese word for "user".
func (s *Service) owner(ctx context.Context) string {
	if o := strings.TrimSpace(s.hello(ctx).Owner); o != "" {
		return o
	}
	return "用户"
}

// hello is the hub's answer to manager.hello (Hub.Hello); an error leaves it empty.
func (s *Service) hello(ctx context.Context) Hello {
	h, err := s.hub.Hello(ctx)
	if err != nil {
		s.logf("assistant: hello: %v", err)
	}
	return h
}

// lang is the user's language (the hub's hello, docs/protocol/i18n.md "Protocol"): what the assistant's own texts for people are
// written in when no request is in front of them (task notices, cards, records in a conversation).
func (s *Service) lang(ctx context.Context) i18n.Lang {
	return i18n.Lang(s.hello(ctx).Locale).Or(i18n.Default)
}

// call is a DeviceCall whose answer is read into out (nil: not read).
func (s *Service) call(ctx context.Context, device, method string, params, out any) error {
	if conversationWrite(method) {
		s.conversationWrites.RLock()
		defer s.conversationWrites.RUnlock()
	}
	if method != DeviceSessionGet && method != "session.unwatch" {
		if err := s.conversationParamsAvailable(ctx, device, params); err != nil {
			return beforeNodeWrite(method, err)
		}
	}
	taken := false // the node took this send (or may have): a direction's notes block it carried counts as given
	if p, ok := params.(map[string]any); ok && (method == DeviceAssistantSend || method == DeviceAssistantWarm || method == DeviceSessionSend ||
		method == DeviceSessionSendOnce || method == assistantwire.DeviceAssistantAnswerAgain) {
		id, _ := p["session_id"].(string)
		pctx, carry := withNotesCarry(ctx)
		// Settled however this returns: a send refused before it left was not taken (direction_notes.go).
		defer func() { s.notesSendOutcome(ctx, carry, taken) }()
		if err := s.putSupervisionInstructions(pctx, device, id, p); err != nil {
			return beforeNodeWrite(method, err)
		}
		if method == DeviceAssistantSend || method == DeviceSessionSend || method == DeviceSessionSendOnce {
			// Initial assistant.send starts do not pass through SendParams, and Inbox continuations use session.send.
			// The hub forwards session.send_once to the same node entry, so attest it here before that forwarding too.
			// Attest the explicit ordinary/unknown decision on every manager-owned node send.
			newConversation := method == DeviceAssistantSend && id == ""
			startOrigin := ""
			if newConversation {
				startOrigin, _ = p["origin"].(string)
			} else {
				delete(p, "origin") // an existing conversation trusts only the manager's origin book
			}
			origin, originKnown := s.managerOriginDecision(ctx, device, id, startOrigin, newConversation)
			delete(p, "origin_known")
			if originKnown {
				p["origin_known"] = true
			}
			if origin != "" {
				p["origin"] = origin
			}
			if !originKnown || (origin != "" && origin != OriginTask) || p["is_main"] == true {
				delete(p, devicewire.LegacyTaskExecution)
			}
			if method == DeviceSessionSend || method == DeviceSessionSendOnce {
				cfg := s.settings(ctx)
				if id != "" && id == cfg.Main && device == cfg.mainRouteMachine() {
					p["is_main"] = true // internal sends must not restart main on ordinary-chat compaction settings
				}
			}
		}
	}
	roundKind := "" // the round the message starts, when it is one the owner did not start (owner_preempt.go)
	if p, ok := params.(map[string]any); ok && (method == DeviceAssistantSend || method == DeviceSessionSend || method == DeviceSessionSendOnce) {
		if text, ok := p["text"].(string); ok {
			source, _ := p["message_source"].(*proto.MessageSource)
			if id, _ := p["session_id"].(string); source != nil && method == DeviceAssistantSend {
				// What the round it starts is spent on (availability.go). Only a source the sender named: a relay
				// without one is noted by deliverRelay, and one read off the text below is not what started it.
				s.availInput(device, id, source.Kind, time.Now())
			}
			if source == nil {
				source = prompts.ItemSource(proto.Item{Role: "user", Kind: proto.KindText, Text: text})
			}
			roundKind = backgroundRoundKind(text, source)
			if source != nil {
				p["message_source"] = source
				p["text"] = prompts.MessageSourceText(text, source)
			}
		}
	}
	if p, ok := params.(map[string]any); ok && (method == DeviceSessionSend || method == DeviceSessionSendOnce) {
		if id, _ := p["session_id"].(string); id != "" {
			s.workTold(device, id) // the turn it starts is this manager's doing, not the owner's hand (setup_suggest.go)
		}
	}
	if p, ok := params.(map[string]any); ok && method == DeviceAssistantSend {
		// The node reads a missing is_main as false, and is_main is part of what a process runs: a relay into the main
		// conversation (deliverRelay) without it started that conversation, or restarted it when idle, on the ordinary
		// compaction limits (#3028). Only this manager knows which conversation is main; what a sender set stands.
		if _, set := p["is_main"]; !set {
			id, _ := p["session_id"].(string)
			cfg := s.settings(ctx)
			p["is_main"] = id != "" && cfg.Main == id && cfg.mainRouteMachine() == device
		}
		if _, set := p["interject_background"]; !set && s.interjectBackground(ctx) {
			p["interject_background"] = true // what the node does with a message that comes while it answers
		}
	}
	if p, ok := params.(map[string]any); ok && (method == DeviceAssistantSend || method == DeviceSessionSend || method == DeviceSessionSendOnce) && p["is_main"] == true {
		p["idle_compaction"] = assistantwire.IdleCompaction{
			Enabled: s.idleAutoCompact(ctx), IdleMinutes: s.idleAutoCompactMinutes(ctx), ContextTokens: s.idleAutoCompactTokens(ctx),
		}
	}
	if p, ok := params.(map[string]any); ok && (method == DeviceAssistantSend || method == DeviceAssistantWarm || method == DeviceSessionSend || method == DeviceSessionSendOnce) {
		// The window the main conversation is held to (main_compact_window.go), as sendModel puts it: a start, a relay,
		// an internal message and the spare warmed for its next start carry it with is_main, or the node starts it again
		// on its launcher's window (and never adopts the spare).
		if _, set := p["main_compact_window"]; !set && p["is_main"] == true {
			putMainCompactWindow(s.settings(ctx), p)
		}
		// Interactive Claude Code (#10058) is part of what a process runs: every send and warm carries it, or the node
		// restarts a conversation between the two kinds as messages without it come.
		if _, set := p["claude_interactive"]; !set && s.claudeInteractive(ctx) {
			p["claude_interactive"] = true
		}
	}
	if p, ok := params.(map[string]any); ok && startScopeMethods[method] {
		// A Persona-managed provider out of this computer's scope does not run there (models_launchers_scope.go).
		if name, _ := p["launcher"].(string); name != "" {
			if err := s.launcherScopeErr(ctx, device, name); err != nil {
				if method == DeviceAssistantSend {
					s.recordManagerSend(device, p, nil, err) // a failed start or resume all the same (north_star.go)
				}
				return beforeNodeWrite(method, err)
			}
		}
	}
	if p, ok := params.(map[string]any); ok && method == DeviceAssistantSend && p[holdPushesParam] != true && !meetingSource(p) {
		// A meeting's batch is no one speaking there either: its turn is a side exchange (meeting_turns.go).
		if id, _ := p["session_id"].(string); id != "" && s.supervising(id) {
			s.releasePushes(ctx, id) // something else speaks there: its turns are pushed again (a supervision message holds them anew)
		}
	}
	var round *roundStart
	if p, ok := params.(map[string]any); ok && method == DeviceAssistantSend && !meetingSource(p) {
		round = s.roundStarting(device, p, roundKind) // read before the send: its conversation is working right after
	}
	taskRoot, taskSpaceHeld := taskSpaceRootFromContext(ctx)
	if taskRoot == "" && (method == DeviceSessionSend || method == deviceTaskStart) {
		if p, ok := params.(map[string]any); ok && (method == DeviceSessionSend || p["resume_session_id"] != nil) {
			taskRoot = s.attachTaskSpaceResume(ctx, device, method, p)
		}
	}
	if taskRoot != "" && !taskSpaceHeld && (method == DeviceSessionSend || method == deviceTaskStart) {
		unlock := s.lockTaskSpace(taskRoot)
		defer unlock()
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return beforeNodeWrite(method, err)
	}
	p := DeviceCall{Device: device, Method: method, Params: raw}
	if fields, ok := params.(map[string]any); ok && method == DeviceSessionStart {
		if _, managed := fields["backend_workspace"].(taskworkspace.Request); managed {
			p.Method = devicewire.MethodStartBackendSession
		}
	}
	res, ok, err := s.localFleetDeviceCall(ctx, p) // what the hub answered before the nodes' events were the manager's
	if !ok {
		res, err = s.hub.DeviceCall(ctx, p)
	}
	// Not taken: refused before anything was typed, or it never reached the computer (relay_refusal.go).
	taken = err == nil || !sendNotTaken(err)
	if taskRoot != "" && (method == DeviceSessionSend || method == deviceTaskStart) {
		s.noteTaskSpaceResult(ctx, taskRoot, device, res)
	}
	if err == nil && method != DeviceSessionGet && method != "session.unwatch" {
		err = afterNodeWrite(method, s.conversationParamsAvailable(ctx, device, params))
	}
	if err == nil {
		round.sent(s, res) // whose round it is, for the owner's next message (owner_preempt.go)
	}
	if sent, ok := params.(map[string]any); ok && method == DeviceAssistantSend {
		s.recordManagerSend(device, sent, res, err) // the north star's availability (north_star.go)
	}
	if err != nil || out == nil || len(res) == 0 {
		return err
	}
	return afterNodeWrite(method, json.Unmarshal(res, out))
}

// appCall is an AppCall whose answer is read into out (nil: not read).
func (s *Service) appCall(ctx context.Context, app, method string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	p := AppCall{AppID: app, Method: method, Params: raw}
	res, ok, err := s.localFleetAppCall(ctx, p) // the interaction queue (fleet_routes.go)
	if !ok {
		res, err = s.hub.AppCall(ctx, p)
	}
	if err != nil || out == nil || len(res) == 0 {
		return err
	}
	return json.Unmarshal(res, out)
}

// sessions are a machine's sessions as the apps get them; ErrNoDevice when the hub has no such machine.
func (s *Service) sessions(ctx context.Context, machine string) ([]proto.Session, error) {
	list, _, err := s.sessionsMuted(ctx, machine)
	return s.deletionRows(ctx, machine, list, false), err
}

// sessionsMuted is sessions, and which of them are muted (the agent or its space: pushes off), by session id, as
// sessions.list says.
func (s *Service) sessionsMuted(ctx context.Context, machine string) ([]proto.Session, map[string]bool, error) {
	if list, muted, ok, err := s.localSessions(machine); ok {
		return list, muted, err
	}
	var res struct {
		Sessions []proto.Session `json:"sessions"`
		Muted    []string        `json:"muted"`
	}
	err := s.call(ctx, machine, DeviceSessionsList, map[string]any{}, &res)
	muted := make(map[string]bool, len(res.Muted))
	for _, id := range res.Muted {
		muted[id] = true
	}
	return res.Sessions, muted, err
}

// session is one session as the node listed it.
func (s *Service) session(ctx context.Context, machine, id string) (proto.Session, bool) {
	var res struct {
		Session proto.Session `json:"session"`
		OK      bool          `json:"ok"`
	}
	if err := s.call(ctx, machine, DeviceSessionGet, map[string]any{"session_id": id}, &res); err != nil {
		return proto.Session{}, false
	}
	return res.Session, res.OK
}

// devices are the user's machines.
func (s *Service) devices(ctx context.Context) []Device {
	devices, _, _ := s.devicesAndPhone(ctx)
	return devices
}

// devicesAndPhone are the user's machines, whether the phone's app is connected (phone_* tools work now) and the
// Persona apps' versions as they last said (client_apps.go).
func (s *Service) devicesAndPhone(ctx context.Context) ([]Device, bool, []ClientApp) {
	var res struct {
		Devices     []Device    `json:"devices"`
		PhoneOnline bool        `json:"phone_online"`
		Apps        []ClientApp `json:"apps"`
	}
	if err := s.call(ctx, DeviceAll, DeviceDevicesList, map[string]any{}, &res); err != nil {
		s.logf("assistant: devices: %v", err)
	}
	return res.Devices, res.PhoneOnline, res.Apps
}

// device is the machine by id; false when the hub has none.
func (s *Service) device(ctx context.Context, id string) (Device, bool) {
	for _, d := range s.devices(ctx) {
		if d.ID == id {
			return d, true
		}
	}
	return Device{}, false
}

// startParams are what a new conversation starts with.
type startParams struct {
	TaskModel           *ModelBinding
	MachineID           string
	Cwd                 string
	TaskExecution       bool
	LegacyTaskExecution bool
	Text                string               `json:"text"`
	PermissionMode      string               `json:"permission_mode,omitempty"`
	Purpose             string               `json:"purpose,omitempty"` // the model configuration's purpose; "" is the assistant's
	Space               string               `json:"space,omitempty"`   // "health" starts it in the health space (docs/assistant/health.md)
	Origin              string               `json:"origin,omitempty"`  // where it comes from (origins.go); "" is the user's
	OriginRef           string               `json:"origin_ref,omitempty"`
	Source              *proto.MessageSource `json:"source,omitempty"`
	// NewSessionID starts the conversation under this id (a Claude Code one; another agent picks its own).
	NewSessionID string `json:"new_session_id,omitempty"`
}

// kbReady: there is a knowledge base (kb.go; on, opened or about to).
func (s *Service) kbReady(ctx context.Context) bool { return s.kbOn(ctx) }

// push asks the hub to push n to the user (notify.request).
func (s *Service) push(ctx context.Context, n Notification) {
	_, _ = s.pushResult(ctx, n)
}

// pushResult is push with the hub's answer: Held/reason say ln mode kept the notice for the briefing instead of
// alerting the phone (notify_user shows that to the model).
func (s *Service) pushResult(ctx context.Context, n Notification) (NotifyResult, error) {
	n.ID, n.Kind = "ntf_"+randomHex(8), NotifyPush
	if n.Asked { // the conversation pushed what matters itself: its own finished push is left out (fleet_push.go)
		s.noteAsked(n.SessionID)
	}
	res, err := s.hub.NotifyRequest(ctx, NotifyRequest{Notification: n})
	if err != nil {
		s.logf("assistant: push: %v", err)
	}
	return res, err
}

// event tells the apps what changed (ws.broadcast: the hub sends ev to them as it is).
func (s *Service) event(ctx context.Context, ev map[string]any) {
	if kind, _ := ev["type"].(string); conversationContentEvent(kind) {
		id, _ := ev["conversation"].(string)
		if err := s.conversationIDAvailable(ctx, id); err != nil {
			return
		}
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	s.hub.Broadcast(ctx, Broadcast{Message: raw})
}

// ---- the snapshot ----

// publish reports the whole snapshot (manager.snapshot) after a change; the hub shows it.
func (s *Service) publish(ctx context.Context) {
	s.pubMu.Lock()
	snap := s.snapshot(ctx)
	s.seq++
	snap.Epoch, snap.Seq, snap.At = s.epoch, s.seq, time.Now().UTC()
	s.pubMu.Unlock()
	if s.fleetOn() {
		s.fleetTakeSnapshot(snap) // the lists' decoration, its own cards on the phone (fleet_snapshot.go)
		snap.Interactions = nil
	}
	if _, err := s.hub.Snapshot(ctx, snap); err != nil && !errors.Is(err, ErrStaleSnapshot) {
		s.logf("assistant: snapshot: %v", err)
	}
	s.directionsSoon()    // a leader, its title or its tasks may have changed (directions.go)
	s.directionTreeSoon() // and so may the direction tree (direction_tree.go)
}

func (s *Service) Resync(ctx context.Context) { s.publish(ctx) }

// snapshot is what the assistant shows now; parts that could not be read are left out and the snapshot is partial.
func (s *Service) snapshot(ctx context.Context) Snapshot {
	views := map[string]*SessionView{}
	view := func(id, machine string) *SessionView {
		v := views[id]
		if v == nil {
			v = &SessionView{MachineID: machine, SessionID: id}
			views[id] = v
		}
		return v
	}
	var snap Snapshot
	titles, ok := s.conversationTitles()
	snap.Partial = snap.Partial || !ok
	for id, t := range titles {
		view(id, t.machine).Title = t.title
	}
	archived, ok := s.archivedSet()
	snap.Partial = snap.Partial || !ok
	origins, ok := s.conversationOrigins()
	snap.Partial = snap.Partial || !ok
	cfg := s.settings(ctx)
	for id, o := range origins {
		v := view(id, o.machine)
		v.Origin, v.OriginRef, v.OpenedBy = o.origin, o.ref, o.ext
		// Inbox processing belongs to its item, not Recent. Keep the transcript reachable through the item and
		// archived history, including after an instruction or a restart, without changing its saved origin.
		v.Archived = o.origin == OriginInbox && id != cfg.Main
	}
	for id, machine := range archived {
		view(id, machine).Archived = true
	}
	tagRecords, ok := s.conversationTagsRecords()
	snap.Partial = snap.Partial || !ok
	for id, rec := range tagRecords {
		if len(rec.Tags) > 0 {
			view(id, rec.MachineID).Tags = slices.Clone(rec.Tags)
		}
	}
	picks, ok := s.modelPicksSet()
	snap.Partial = snap.Partial || !ok
	for id, p := range picks {
		view(id, p.MachineID).ModelPick = p.wire()
	}
	directions, ok := s.directionViews(ctx)
	snap.Partial = snap.Partial || !ok
	leadersAt := cfg.MachineID // a leader conversation is on the assistant's machine
	for id, d := range directions {
		view(id, leadersAt).Direction = d
	}
	if len(cfg.Leaders) > 0 {
		// The same canonical name as the direction tree is the active conversation's
		// identity on every client. Keep its stored ordinary title for when it ends.
		names := s.directionSessionNames(ctx, cfg, titles)
		for id, name := range names {
			if name != "" {
				view(id, leadersAt).Title = name
			}
		}
	}
	snap.Tasks = s.taskViews(ctx)
	snap.Partial = snap.Partial || snap.Tasks == nil
	snap.Todos = s.todoReport(ctx)
	snap.Partial = snap.Partial || snap.Todos == nil
	snap.Profile = s.profile(ctx, snap.Tasks, snap.Todos)
	snap.Partial = snap.Partial || snap.Profile == nil
	snap.Channels = s.channelsReport()
	snap.Partial = snap.Partial || snap.Channels == nil
	here := cfg.MachineID
	queued, ok := s.queuedRelays()
	snap.Partial = snap.Partial || !ok
	for id, q := range queued {
		view(id, here).Queued = q // a conversation sent to is on the assistant's machine
	}
	kept, ok := s.undeliveredViews(ctx)
	snap.Partial = snap.Partial || !ok
	for id, q := range kept {
		for _, k := range q {
			v := view(id, k.Machine)
			v.Undelivered = append(v.Undelivered, k.wire()) // the owner's messages that could not go in (undelivered.go)
		}
	}
	for _, id := range s.heldConversations() {
		view(id, here).Supervising = true // a supervision message started its turn: not pushed (supervise_digest.go)
	}
	if cfg.Main != "" { // not snap.Profile: that is left out while a settings read fails
		view(cfg.Main, cfg.MachineID).Title = mainTitle // never its first message, a model's name or a rename (#1313)
	}
	snap.Inbox = s.inboxReport(ctx)
	snap.Partial = snap.Partial || snap.Inbox == nil
	snap.Work = s.workReport(ctx) // before the cards: they are read back with it
	snap.Partial = snap.Partial || snap.Work == nil
	snap.Interactions = append(s.pendingQuestions(), s.workCards()...)
	reviewInteractions, reviewOK := s.sessionReviewInteractions(ctx)
	snap.Interactions = append(snap.Interactions, reviewInteractions...)
	snap.Partial = snap.Partial || !reviewOK
	snap.Sessions = make([]SessionView, 0, len(views))
	for _, v := range views {
		snap.Sessions = append(snap.Sessions, *v)
	}
	sort.Slice(snap.Sessions, func(i, j int) bool { return snap.Sessions[i].SessionID < snap.Sessions[j].SessionID })
	return snap
}

// ---- what the hub saw ----

// UserSpoke: the user is talking in the conversation again: back in the list, and it works for them, not for a chain
// of conversations.
func (s *Service) UserSpoke(ctx context.Context, r SessionRef) {
	s.userSpokeText(ctx, r, "")
}

func (s *Service) userSpokeText(ctx context.Context, r SessionRef, text string) {
	s.availOwnerSpoke(r, time.Now()) // before anything else: what the conversation is doing as the message comes (availability.go)
	if exitText(text) {
		s.retireTodoWork([2]string{r.MachineID, r.SessionID}) // /exit or /quit starts no turn that could cancel the wake
	}
	s.agentDecisionSpoke(ctx, r, time.Now(), text)
	s.ownerHeard(r.SessionID)
	s.clearToolLoop([2]string{r.MachineID, r.SessionID}, loopUserInput)
	s.progressMade(r.MachineID, r.SessionID, time.Now()) // told something: a new stretch (supervise_progress.go)
	// A meeting's turn under way is the user's from here on (meeting_turns.go).
	s.meetingTurnUserSpoke(r.MachineID, r.SessionID)
	s.releasePushes(ctx, r.SessionID) // the user speaks: its turns are pushed as ever (supervise_digest.go)
	s.withdrawAskUser(r.SessionID)    // a question it waits on is taken back: the user's words are not left behind its turn
	s.askLaterSpoke(ctx, r.SessionID) // one asked without waiting stays up; that they wrote is kept (ask_later.go)
	s.unarchiveOnSend(r.MachineID, r.SessionID)
	if s.suggestDrop(r.MachineID, r.SessionID) { // a message went in: its suggestion is spent (suggest_next.go)
		s.publishSessions(r.MachineID)
	}
	s.userSpoke(r.SessionID)
	if s.reporterStopped(ctx, r.SessionID) { // talked to again: its tasks are told to it again (#3308)
		s.setReporterStopped(ctx, r.SessionID, "")
	}
}

// TurnStarted: one of a task's agents is working; what was going to be done about an agent's last turn is off.
func (s *Service) TurnStarted(ctx context.Context, r SessionRef) {
	s.approvalAnswerTurnStarted(r)
	if s.meetingTurnStarted(r.MachineID, r.SessionID, true) {
		return // a meeting's batch started it: a side exchange (meeting_turns.go)
	}
	s.turnStartedObserved(ctx, r, true)
}

func (s *Service) turnStartedObserved(ctx context.Context, r SessionRef, newTurn bool) {
	s.quietTurnOver(r.MachineID, r.SessionID) // a turn with no output before this one is over (silent_turns.go)
	s.taskWorking(ctx, r.MachineID, r.SessionID)
	s.workStartedObserved(ctx, r.MachineID, r.SessionID, time.Now(), newTurn)
	s.availTurnStarted(r.MachineID, r.SessionID, newTurn, time.Now())
}

// TurnEnded: a task's agent may be done (in the background: it waits for the agents to settle), an agent's turn is
// triaged; a new conversation is named after its first turn.
func (s *Service) TurnEnded(ctx context.Context, r SessionRef) {
	s.approvalAnswerTurnEnded(r)
	s.turnEnded(ctx, r, s.meetingTurnEnded(r.MachineID, r.SessionID))
}

// turnEnded is TurnEnded; meeting: the turn was a meeting's batch's (meeting_turns.go), a side exchange that is
// neither a task's progress nor triaged, mirrored or a reason to name the conversation.
func (s *Service) turnEnded(ctx context.Context, r SessionRef, meeting bool) {
	s.stopTurnEnded(r.MachineID, r.SessionID, time.Now()) // the turn a Stop interrupted, if one did (silent_turns.go)
	s.clearToolLoop([2]string{r.MachineID, r.SessionID}, loopTurnChanged)
	go s.registerInlineTurn(context.WithoutCancel(ctx), r)
	s.afterSettle(context.WithoutCancel(ctx), s.registerInlineTurnSettled, r.MachineID, r.SessionID)
	if !meeting {
		s.availTurnEnded(ctx, r.MachineID, r.SessionID, time.Now()) // an assistant conversation's round (availability.go)
		s.afterSettle(context.WithoutCancel(ctx), s.taskTurnSettled, r.MachineID, r.SessionID)
		// A review's reviewer answered (supervise_autoreview.go, agents_session_review.go).
		go s.autoReviewTurnEnded(context.WithoutCancel(ctx), r.MachineID, r.SessionID)
		go s.sessionReviewTurnEnded(context.WithoutCancel(ctx), r.MachineID, r.SessionID)
		go s.workTurnEnded(context.WithoutCancel(ctx), r.MachineID, r.SessionID)    // its triage (agents_work_turns.go)
		go s.channelTurnEnded(context.WithoutCancel(ctx), r.MachineID, r.SessionID) // a channel's answer, the sync mirror
	}
	// A usage limit marks the launcher there until it resets (launcher_health.go).
	s.afterSettle(context.WithoutCancel(ctx), s.launcherLimitTurnSettled, r.MachineID, r.SessionID)
	if !meeting {
		// An assistant turn that failed is logged; a conversation conversation_new opened tells its parent (spawned.go).
		s.afterSettle(context.WithoutCancel(ctx), s.assistantTurnSettled, r.MachineID, r.SessionID)
		s.afterSettle(context.WithoutCancel(ctx), s.inboxAgentTurnSettled, r.MachineID, r.SessionID)
	}
	if !meeting {
		s.titleAfterTurn(ctx, r.MachineID, r.SessionID)
	}
}

// afterSettle runs settled for machine's session sid once its turn stayed still (settleDelay, read now), on a timer:
// a goroutine asleep through the window would hold a stack per pending turn end (#2472). Each call is its own run.
func (s *Service) afterSettle(ctx context.Context, settled func(context.Context, string, string), machine, sid string) {
	time.AfterFunc(s.settleDelay(ctx), func() { settled(ctx, machine, sid) })
}

// InteractionResolved: the user dealt with a card of a task's agent: the conversation keeps a record of it, and the
// todo of the agent the calls made in it (its decisions).
func (s *Service) InteractionResolved(ctx context.Context, r InteractionResolved) {
	if f := s.ixResolved; f != nil {
		f(r)
	}
	s.availAskEnded(r, time.Now()) // who answered it, and after how long (availability.go)
	s.taskPlanApproval()
	s.taskAnswered(ctx, r.Interaction, r.Resolution, r.Status)
	s.cardDecisions(ctx, r.Interaction, r.Resolution, r.Status)
	s.escalatedResolved(ctx, r.Interaction, r.Resolution, r.Status) // an approval the user gave on an escalation (ask_route.go)
	// A quiet task's waiting notice may still be in its delay window or queued behind a turn: drop it now unless the
	// task still waits on another card. An agent machine the recheck cannot reach is not an answer, so the notice
	// is left for the drain-time recheck.
	if t, ok := s.taskOf(ctx, r.Interaction.MachineID, r.Interaction.SessionID); r.Status != "failed" && ok && t.Quiet {
		if waits, known := s.taskNeedsRecheck(ctx, t); known && !waits {
			if s.dropQueuedNeeds(t.ID) {
				s.publish(ctx)
			}
		}
	}
}

// SessionsChanged: messages from other conversations go to those no longer running; the todos follow the list
// (todo_sessions.go).
func (s *Service) SessionsChanged(ctx context.Context, d DeviceSessions) {
	if f := s.sessionsSeen; f != nil {
		f(d)
	}
	s.toolLoopsChanged(d)
	s.progressSessions(d, time.Now()) // what the working agents do, for no_progress (supervise_progress.go)
	s.drainRelays(d.MachineID, d.Sessions)
	s.undeliveredSeen(d)                      // kept owner messages go once their conversation runs again (undelivered.go)
	s.originsChanged(d.MachineID, d.Sessions) // conversations from before origins were noted (origins.go)
	s.todosSessionsChanged(ctx, d)
	s.taskSpacesSessionsChanged(ctx, d)
	s.todoRunsChanged(d.MachineID) // a to-do's view may file it elsewhere now (todo_view_push.go)
	s.taskStageSessions(ctx, d)
	s.nativeArchivesListed(d.MachineID) // agent_archive_native.go
	s.switchQueueListed(d.MachineID)    // a queued launcher switch whose turn ended (agents_switch.go)
	s.followStarts(ctx, d)              // the app's starts, for launcher health (launcher_health_starts.go)
	s.directionTreeSessions(d)          // an Agent of the direction tree may have moved (direction_tree.go)
	s.agentNamesChanged(d)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
