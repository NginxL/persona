package recording

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// fakeHub is the hub as the assistant sees it in unit tests: its hello, the machines with their sessions, a node on
// each that starts and continues conversations (s1, s2…; resume_only refuses one it doesn't know, like a transcript
// that was cleaned up), a model (a Messages API the hello's "ai" points at, fakeModel), and what the assistant pushed
// and reported. settings are the assistant's own a test service starts with (newTestService…).
type fakeHub struct {
	mu                 sync.Mutex
	revokedDevices     map[string]bool
	credentialCheckErr error
	inboxHook          func(ctx context.Context) // runs first in InboxChanged, no lock held (#4924)
	itemReads          int                       // session.items reads since the hooks were set
	onDevicesList      func()                    // runs once, then clears, before the next devices.list; no lock held
	watcherGets        map[int]string            // index in calls of a session.get a run watcher made -> its goroutine
	onTell             func()                    // runs after each snapshot or event the service gave the hub was recorded; no lock held
	// beforeSend runs once, then clears, when session.send is called, before it is answered (what happens on the
	// agent's computer while the send is out); afterList, by machine, once after that machine's next sessions.list made
	// with a listHooked context was answered. No lock held. sendHangs: session.send is never answered while the link
	// stays up, and ends with the call's context, after beforeSend (a request that went out).
	beforeSend func()
	// beforeAssistantSend blocks a relay before its reply, without holding the fake's lock.
	beforeAssistantSend func()
	// beforeSessionGet runs once before checking an answer's destination, with no fake lock held.
	beforeSessionGet func()
	afterList        map[string]func()
	sendHangs        bool
	hello            Hello
	settings         *Settings
	launcherList     []proto.Launcher            // the nodes' launchers.list (nil: the list fails, so it can't be told)
	zshrc            map[string][]proto.Launcher // a machine\'s own launchers.list (over launcherList)
	// hostLaunchers: a machine's launchers.list for a start in its engine host (the call names a host), answered as an
	// engine that runs only its own launchers would (host_runtime, #8140).
	hostLaunchers       map[string][]proto.Launcher
	rcFiles             map[string]string                  // a machine's launchers.list rc_file (its shell follows: bash or zsh)
	slowLaunchers       map[string]bool                    // a machine whose launchers.list answers only when the call gives up
	launcherGate        chan struct{}                      // when set, every launchers.list answer waits for this gate
	failLauncherRefresh bool                               // launchers.list with refresh:true fails, ordinary reads still answer
	launcherListParams  []map[string]any                   // launchers.list params in order
	agentModels         map[string][]proto.AgentModel      // the nodes' launchers.models, by launcher
	modelsLocked        map[string]string                  // launchers.models's locked, by launcher
	managed             map[string][]proto.ManagedLauncher // what each node was given (launchers.managed)
	// catalogNodes: the nodes take launchers.catalog (a node from before it answers unknown method); catalog is what each got.
	catalogNodes   bool
	catalog        map[string][]proto.ManagedLauncher
	ownServices    map[string]proto.OwnServiceState // launchers.own_service by machine/agent (none: unknown method)
	presets        []assistantwire.LauncherPreset
	voice          []assistantwire.VoicePlugin
	modelExts      []assistantwire.ModelExtension // plugins.models: the extensions granted models (model_rpc.go)
	needTriage     []string
	ownModels      bool                         // the test sets hello.Models itself: no fake model
	infoTagModel   bool                         // the service tags with information categories (Service.noInfoTag off)
	devices        []Device                     // devices.list answers these when set (else one per machine with sessions)
	devicesFull    bool                         // devices.list says its list is complete (a machine not in it was removed)
	devicesOther   []string                     // devices.list's ids kept out of it (pending, leaving, absorbed)
	sessions       map[string][]proto.Session   // by machine: the machines there are
	sessionsFail   map[string]int               // by machine: sessions.list fails offline this many more times (then recovers)
	itemsFail      int                          // session.items fails this many more times (then recovers)
	itemsFailPlain int                          // one-turn completion reads without lazy_images fail this many times
	items          []proto.Item                 // session.items answers this
	page           func(turns int) []proto.Item // when set, session.items answers with this instead (a real transcript's PageTurns)
	pageTurns      bool                         // and only the last n turns of it to "turns": n, as the node does (else all of it)
	itemsPage      int                          // > 0: session.items with after_seq pages them by seq, this many a page
	calls          []DeviceCall
	known          map[string]bool  // conversations the nodes have
	made           int              // conversations started, for the next id
	sends          []map[string]any // assistant.send params, and {"rename": params}
	prepared       []map[string]any // assistant.prepare params
	guestPrepared  []map[string]any // assistant.guest_prepare params
	noGuest        bool             // the node is older than guest conversations: assistant.guest_prepare is unknown
	larkCLI        bool
	slowSnapshots  time.Duration // the connectors' live states come this late (nothing: there are none)
	liveStates     []any         // the connectors' live states (connectors.snapshots), when they come at once
	liveDelay      time.Duration // liveStates come this late instead (the ask's deadline still holds)
	snapshotAsks   int           // connectors.snapshots calls
	hungChannel    string        // the channel whose program never answers a send (hangChannel)
	replies        []string      // the model's answers, in turn (the last one again when they run out)
	answers        []fakeAnswer  // how each of those answers comes, in turn (none: at once and whole)
	modelDown      bool          // the model's gateway answers 502 to the calls replies would answer: asked, never answering
	noModel        bool
	asked          []modelCall
	pushes         []Notification
	failNotify     int              // notify.request fails this many more times as when the hub is away and nothing was kept
	refuseNotify   error            // notify.request is the hub's refusal (answered) while set
	notifyResult   NotifyResult     // the held/reason answer notify.request returns once it succeeds
	events         []string         // the type of each event the apps were sent
	onBroadcast    func()           // runs once an event is recorded, while Broadcast has not returned yet
	liveEvents     []map[string]any // the meeting_live events as sent
	snaps          []Snapshot

	sessionsContextCheck bool // sessions.list rejects an expired caller context, as a real RPC does

	// Agents a task starts (session.start a1, a2…) and what was sent to them; a machine offline, a node refusing.
	starts       []map[string]any
	agentSent    []map[string]any
	agents       int
	offline      map[string]bool
	failStart    error
	refuseUltra  error            // session.start's answer to a start that asks for ultracode (a node refusing it, #3596)
	refuseGoal   error            // session.start's answer to a start that carries a goal (a node too old for it)
	startFailed  string           // the StartError of the sessions session.start answers (the launcher failed in its new tab)
	worktrees    []map[string]any // worktree.prepare / worktree.create params, {"method": …} added
	failWorktree error            // worktree.create's answer when set
	slowStart    bool             // session.start blocks until the call's context ends, then answers with its error (a node that builds the session before answering late)
	// startReply shapes session.start's answer: what the session runs with, and what the engine ran in place of what
	// was asked (#8140).
	startReply   func(params map[string]any, s *proto.Session) *proto.StartInstead
	judged       int                       // the done judge's calls to the model
	judge        string                    // what they answer ("": an error)
	judgeFails   int                       // the done judge's next calls that fail before f.judge answers
	judgeDelay   time.Duration             // how long the done judge waits before answering (its caller can time out)
	judgeAsked   []string                  // what the done judge was given (its input)
	judgeBy      func(input string) string // when set, what the done judge answers to its input (instead of f.judge)
	outcome      string                    // what the outcome model answers (todo_outcome.go; "": an error)
	outcomeAsked []modelCall
	lessons      string // what the lessons model answers (task_lessons.go; "": an error)
	lessonsAsked []modelCall
	retired      []string         // the session ids of session.retire and of session.stop with retire (the done and idle passes)
	retireArgs   []map[string]any // what each of those calls asked
	stopped      []string         // session.stop's session ids without retire (a cancelled task's agent)
	retireErr    error
	tokens       map[string][]map[string]map[string]string // todo.sync_session_tokens, by machine, in order

	// The hub's other apps as a test answers them (answerApp), and the calls they got.
	apps     map[string]appAnswer
	blocking map[string]bool // app methods whose answer runs outside the lock (answerAppBlocking)
	appCalls []AppCall

	// The Agents business's models (agents_*.go): the triage's answer ("": it fails) and the tab names', what they
	// were asked; the sessions whose pushes are off, by machine.
	triage      string
	triageAsked []modelCall
	// infoTag answers the information tagging (info_tag.go): {"categories":[]} when empty; infoTagFails fails it.
	infoTag      string
	infoTagFails bool
	infoTagAsked []modelCall
	// captureSort answers sorting a quick capture (capture_sort.go): it fails when empty.
	captureSort      string
	captureSortAsked []modelCall
	// The Progress row's paraphrase of a report (report_line.go): its answer (none: an error), and what was asked.
	reportLine      string
	reportLineAsked []modelCall
	// The delegate's model (delegate.go): its answer ("": it fails), and what it was asked.
	delegate      string
	delegateAsked []modelCall
	tabReply      string
	tabAsked      []string
	// The todo titles' model (todo.title): its answer, an error instead, and what it was asked.
	todoTitleReply string
	todoTitleErr   error
	todoTitleAsked []modelCall
	muted          map[string][]string
	hubCards       []proto.Interaction // interactions.list: what the hub has up
	told           []told              // the manager's fleet.* events (Tell)
	failSend       error               // session.send fails with it
	sentOnce       []map[string]any    // session.send_once params, in order
	failSendOnce   map[string]error    // session.send_once fails with these, by "machine/session"
	failInterrupt  error               // session.interrupt fails with it
	failAssistant  error               // assistant.send fails with it (a launcher that cannot start)
	// refuseConfigDir: assistant.send or assistant.warm with a config_dir fails as the hub refuses it for a node too old
	// for one
	refuseConfigDir bool
	// elsewhere: the conversations in another profile than meetingProfile, by id: a resume that asks for a config_dir
	// fails, as the node refuses to move a conversation to another profile
	elsewhere map[string]bool

	// The launchers' health (launcher_health.go): launchers.check's answers by "machine/launcher" (none: it works),
	// the checks and repairs asked for, launchers.repair's answer (nil: the node has no such method) and what a repair
	// does to the checks.
	checks  map[string]proto.LauncherCheck
	checked []string
	onCheck func(f *fakeHub) // runs (under the fake's lock) inside each launchers.check
	// onRename runs (under the fake's lock) inside each session rename, where a node's reader loop would be waiting.
	onRename func()
	// checkWait, when set, runs before each launchers.check is answered, outside the fake's lock, with the call's
	// context: a check that takes its time; its error is the call's.
	checkWait func(ctx context.Context) error
	checkedAt []time.Time

	// checkParams are the launchers.check params in order; oldCheck: the node runs the plain check whatever it is asked
	// (no assistant_turn in its answer), as one without launcher_check_assistant_turn
	checkParams []map[string]any
	oldCheck    bool

	repairs    []map[string]any
	repairRes  *proto.LauncherRepair
	onRepair   func(f *fakeHub)
	onSend     func(f *fakeHub, p map[string]any) // runs (under the fake's lock) after each session.send
	onSendOnce func(f *fakeHub, p map[string]any) // runs under the fake's lock when a once-only send is accepted
	onStart    func(f *fakeHub, p map[string]any) // runs (under the fake's lock) when session.start arrives, before slowStart blocks
	down       map[string]bool                    // machines listed as not connected (devices.list) while f.devices is nil
	downLists  map[string]int                     // and for how many more devices.list answers a machine is listed so
	onList     func(f *fakeHub, machine string)   // runs (under the fake's lock) before a sessions.list is answered, which then sends the list as it left it
	// onAssistantSend runs (under the fake's lock) when assistant.send arrives, before it is answered
	onAssistantSend func(f *fakeHub, p map[string]any)
	// assistantAnswer adds to assistant.send's answer (under the fake's lock): what a node says of a supervision round
	// (node/assistant_round.go), for example.
	assistantAnswer func(p map[string]any) map[string]any
	// doctor is each machine's node.doctor claude-version detail (none: the report has no such check).
	doctor      map[string]string
	doctorState map[string]string // each machine's claude-version state ("unanswered": no version came this time)
	// auth is claude.auth_status's answer by "machine/launcher" (nil: the node has no such method), and the
	// questions it got.
	auth      map[string]proto.ClaudeAuthStatus
	authAsked []string
	// authHold, when set, holds claude.auth_status's answer until it is closed (a node running a slow launcher).
	authHold chan struct{}
	// nodes answer node methods on any machine, by method, before the answers below (answerNode).
	nodes map[string]nodeAnswer
}

type modelCall struct{ System, User string }

func newFakeHub() *fakeHub {
	// "Notify tasks from the main chat only in the main chat" is on by default (tasks from the main conversation are quiet, #1731).
	return &fakeHub{sessions: map[string][]proto.Session{}, sessionsFail: map[string]int{}, known: map[string]bool{},
		offline: map[string]bool{}, tokens: map[string][]map[string]map[string]string{}, managed: map[string][]proto.ManagedLauncher{}}
}

// withMachine adds the assistant's machine "server" (no sessions yet).
func (f *fakeHub) withMachine() *fakeHub {
	f.sessions["server"] = nil
	f.hello.Assistant.MachineID = "server"
	return f
}

func (f *fakeHub) Hello(context.Context) (Hello, error) {
	f.mu.Lock()
	h := f.hello
	f.mu.Unlock()
	return h, nil
}

// deviceCallsForTest records calls made with the test's context under fakeHub.mu.
// Background refreshes can reach the same node independently of the action under test.
type deviceCallsForTest struct{}

// afterListCall marks the context of the call a test arms afterList for (listHooked).
type afterListCall struct{}

// listHooked is ctx for the call under test: afterList runs only after a sessions.list made with it, never after one
// a background goroutine makes meanwhile. A change to a task's agents has the todo-token sync list their machine
// (syncTodoTokens), and on a busy machine that list can still be out when the test arms the hook.
func listHooked(ctx context.Context) context.Context {
	return context.WithValue(ctx, afterListCall{}, true)
}

func (f *fakeHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method == DeviceSessionsList {
		f.mu.Lock()
		check := f.sessionsContextCheck
		f.mu.Unlock()
		if check && ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if p.Method == DeviceSessionGet {
		f.mu.Lock()
		hook := f.beforeSessionGet
		f.beforeSessionGet = nil
		f.mu.Unlock()
		if hook != nil {
			hook()
		}
	}
	if p.Device == DeviceAll && p.Method == DeviceDevicesList {
		f.mu.Lock()
		hook := f.onDevicesList
		f.onDevicesList = nil
		f.mu.Unlock()
		if hook != nil {
			hook() // what happens while a card is being made: its "where" reads the machines
		}
	}
	if p.Method == DeviceSessionSend {
		f.mu.Lock()
		hook, hangs := f.beforeSend, f.sendHangs
		f.beforeSend = nil
		f.mu.Unlock()
		if hook != nil {
			hook()
		}
		if hangs {
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
	if p.Method == DeviceAssistantSend {
		f.mu.Lock()
		hook := f.beforeAssistantSend
		f.beforeAssistantSend = nil
		f.mu.Unlock()
		if hook != nil {
			hook()
		}
	}
	if p.Method != DeviceSessionItems {
		out, err := f.deviceCall(ctx, p)
		if p.Method == DeviceSessionsList && ctx.Value(afterListCall{}) != nil {
			f.mu.Lock()
			hook := f.afterList[p.Device]
			delete(f.afterList, p.Device)
			f.mu.Unlock()
			if hook != nil {
				hook()
			}
		}
		return out, err
	}
	f.mu.Lock()
	f.itemReads++
	fail := f.itemsFail > 0
	if fail {
		f.itemsFail--
	}
	if f.itemsFailPlain > 0 {
		var args struct {
			Turns      int  `json:"turns"`
			LazyImages bool `json:"lazy_images"`
		}
		_ = json.Unmarshal(p.Params, &args)
		if args.Turns == 1 && !args.LazyImages {
			f.itemsFailPlain--
			fail = true
		}
	}
	f.mu.Unlock()
	if fail {
		return nil, errors.New("the node did not answer")
	}
	result, err := f.deviceCall(ctx, p)
	return result, err
}

func (f *fakeHub) deviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method == assistantwire.DeviceLaunchersCheck {
		f.mu.Lock()
		wait := f.checkWait
		f.mu.Unlock()
		if wait != nil {
			if err := wait(ctx); err != nil {
				return nil, err
			}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, p)
	if p.Method == DeviceSessionGet { // the fake answers on the caller's goroutine: note a run watcher's poll
		if g, ok := runWatcherGoroutine(); ok {
			if f.watcherGets == nil {
				f.watcherGets = map[int]string{}
			}
			f.watcherGets[len(f.calls)-1] = g
		}
	}
	if trace, ok := ctx.Value(deviceCallsForTest{}).(*[]DeviceCall); ok {
		*trace = append(*trace, p)
	}
	if p.Method == DeviceAssistantWarm && f.refuseConfigDir && strings.Contains(string(p.Params), `"config_dir"`) {
		return nil, &rpc.Error{Code: proto.CodeNodeOutdated, Message: "update Persona on this computer"}
	}
	if p.Method == DeviceAssistantWarm {
		var w struct {
			Launcher  string `json:"launcher"`
			ConfigDir string `json:"config_dir"`
		}
		_ = json.Unmarshal(p.Params, &w)
		if w.ConfigDir != "" && launcherAgent(w.Launcher) != "claude" { // node/assistant_spare.go's preflight
			return nil, errors.New(assistantwire.ConfigDirClaudeOnly)
		}
	}
	if p.Method == "session.interrupt" && f.failInterrupt != nil {
		return nil, f.failInterrupt
	}
	if p.Device == DeviceAll && p.Method == DeviceDevicesList {
		devices := f.devices
		if devices == nil {
			devices = []Device{}
			for id := range f.sessions {
				up := !f.down[id] && f.downLists[id] == 0
				if f.downLists[id] > 0 {
					f.downLists[id]--
				}
				devices = append(devices, Device{ID: id, Name: id, Online: up, Reachable: up})
			}
		}
		return json.Marshal(map[string]any{"devices": devices, "complete": f.devicesFull, "others": f.devicesOther})
	}
	if p.Device == DeviceAll && p.Method == DeviceInteractionsList {
		return json.Marshal(map[string]any{"interactions": f.hubCards})
	}
	list, ok := f.sessions[p.Device]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrNoDevice, p.Device)
	}
	if answer := f.nodes[p.Method]; answer != nil {
		out, err := answer(p.Device, p.Params)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
	var a map[string]any
	_ = json.Unmarshal(p.Params, &a)
	switch p.Method {
	case "fs.stat":
		var stats []map[string]string
		for _, path := range a["paths"].([]any) {
			stats = append(stats, map[string]string{"path": path.(string), "kind": "dir"})
		}
		return json.Marshal(map[string]any{"stats": stats})
	case DeviceSessionSendOnce:
		if sid, _ := a["session_id"].(string); f.failSendOnce[p.Device+"/"+sid] != nil {
			return nil, f.failSendOnce[p.Device+"/"+sid]
		}
		f.sentOnce = append(f.sentOnce, a)
		if id, _ := a["client_msg_id"].(string); strings.HasPrefix(id, "model-retry:") {
			for _, prior := range f.agentSent {
				if prior["client_msg_id"] == id {
					return json.RawMessage(`{}`), nil
				}
			}
			f.agentSent = append(f.agentSent, a)
			if f.onSend != nil {
				f.onSend(f, a)
			}
		}
		if f.onSendOnce != nil {
			f.onSendOnce(f, a)
		}
		return json.RawMessage(`{}`), nil
	case DeviceSessionsList:
		if f.sessionsFail[p.Device] > 0 { // a node that answers offline for a few tries, then recovers
			f.sessionsFail[p.Device]--
			return nil, fmt.Errorf("%s: %w", p.Device, apierr.ErrOffline)
		}
		if f.onList != nil {
			f.onList(f, p.Device)
			list = f.sessions[p.Device]
		}
		return json.Marshal(map[string]any{"sessions": slices.Clone(list), "muted": f.muted[p.Device]})
	case DeviceSessionGet:
		for _, s := range list {
			if s.SessionID == a["session_id"] {
				return json.Marshal(map[string]any{"session": s, "ok": true})
			}
		}
		return json.Marshal(map[string]any{"ok": false})
	case DeviceSessionItems:
		if after, ok := a["after_seq"].(float64); ok && f.itemsPage > 0 {
			page := []proto.Item{}
			for _, it := range f.items {
				if it.Seq > int(after) {
					page = append(page, it)
				}
			}
			more := len(page) > f.itemsPage
			if more {
				page = page[:f.itemsPage]
			}
			return json.Marshal(map[string]any{"items": page, "more_after": more})
		}
		items := f.items
		if n, _ := a["turns"].(float64); f.pageTurns && n > 0 {
			items = lastTurns(items, int(n))
		}
		if f.page != nil { // a real transcript pages (transcriptChat)
			n, _ := a["turns"].(float64)
			items = f.page(max(int(n), 1))
			if limit, _ := a["limit"].(float64); limit > 0 && len(items) > int(limit) {
				items = items[len(items)-int(limit):]
			}
		}
		return json.Marshal(map[string]any{"items": items})
	case DeviceSessionRename, assistantwire.DeviceSessionRenameTabIfCurrent:
		f.sends = append(f.sends, map[string]any{"rename": a})
		if f.onRename != nil {
			f.onRename()
		}
		return json.RawMessage(`{"ok":true}`), nil
	case assistantwire.DeviceLaunchersCheck:
		name, _ := a["name"].(string)
		f.checked = append(f.checked, p.Device+"/"+name)
		f.checkParams = append(f.checkParams, a)
		if f.onCheck != nil {
			f.onCheck(f)
		}
		f.checkedAt = append(f.checkedAt, time.Now())
		turn := a["assistant_turn"] == true && !f.oldCheck && launcherAgent(name) == "claude"
		if c, ok := f.checks[p.Device+"/"+name]; ok {
			c.AssistantTurn = c.AssistantTurn || turn
			return json.Marshal(c)
		}
		return json.Marshal(proto.LauncherCheck{OK: true, Agent: launcherAgent(name), Model: "m", Answer: "ok", AssistantTurn: turn})
	case assistantwire.DeviceLaunchersRepair:
		f.repairs = append(f.repairs, a)
		if f.repairRes == nil {
			return nil, errors.New("unknown method launchers.repair")
		}
		if f.onRepair != nil {
			f.onRepair(f)
		}
		return json.Marshal(f.repairRes)
	case DeviceLaunchersProbe: // a launcher named after the agent it starts
		name, _ := a["name"].(string)
		return json.Marshal(map[string]any{"detail": proto.LauncherDetail{Agent: launcherAgent(name)}})
	case assistantwire.DeviceLaunchersModelCheck:
		name, _ := a["name"].(string)
		return json.Marshal(map[string]any{"agent": launcherAgent(name)})
	case DeviceLaunchersList:
		f.launcherListParams = append(f.launcherListParams, a)
		if a["source"] == proto.ExternalLauncherSource {
			list, ok := f.zshrc[p.Device]
			if !ok {
				list = f.launcherList
			}
			external := []proto.Launcher{}
			for _, l := range list {
				if proto.IsExternalLauncher(l.Name) {
					external = append(external, l)
				}
			}
			return json.Marshal(map[string]any{"source": proto.ExternalLauncherSource, "launchers": external})
		}
		if f.launcherGate != nil {
			gate := f.launcherGate
			f.mu.Unlock()
			select {
			case <-gate:
			case <-ctx.Done():
				f.mu.Lock()
				return nil, ctx.Err()
			}
			f.mu.Lock()
		}
		if f.slowLaunchers[p.Device] {
			f.mu.Unlock()
			<-ctx.Done()
			f.mu.Lock()
			return nil, ctx.Err()
		}
		if a["refresh"] == true && f.failLauncherRefresh {
			return nil, errors.New("launcher refresh failed")
		}
		if host, _ := a["host"].(string); host != "" {
			if l, ok := f.hostLaunchers[p.Device]; ok {
				return json.Marshal(map[string]any{"launchers": l, "host": host, "host_runtime": true})
			}
		}
		if l, ok := f.zshrc[p.Device]; ok {
			if rc := f.rcFiles[p.Device]; rc != "" {
				shell := map[bool]string{true: "bash", false: "zsh"}[strings.HasSuffix(rc, "bashrc")]
				return json.Marshal(map[string]any{"launchers": l, "shell": shell, "rc_file": rc})
			}
			return json.Marshal(map[string]any{"launchers": l})
		}
		if f.launcherList == nil {
			return nil, errors.New("no launcher list here")
		}
		return json.Marshal(map[string]any{"launchers": f.launcherList})
	case assistantwire.DeviceLaunchersModels:
		name, _ := a["name"].(string)
		models := f.agentModels[name]
		if models == nil {
			models = []proto.AgentModel{}
		}
		return json.Marshal(proto.LauncherModels{Agent: launcherAgent(name), Models: models, Locked: f.modelsLocked[name]})
	case assistantwire.DeviceLaunchersManaged:
		var m struct {
			Launchers []proto.ManagedLauncher `json:"launchers"`
		}
		_ = json.Unmarshal(p.Params, &m)
		f.managed[p.Device] = m.Launchers
		return json.RawMessage(`{"ok":true}`), nil
	case assistantwire.DeviceClaudeAuthStatus:
		name, _ := a["name"].(string)
		f.authAsked = append(f.authAsked, p.Device+"/"+name)
		if hold := f.authHold; hold != nil {
			f.mu.Unlock()
			select {
			case <-hold:
			case <-ctx.Done():
			}
			f.mu.Lock()
		}
		if f.auth == nil {
			return nil, errors.New("unknown method claude.auth_status")
		}
		st, ok := f.auth[p.Device+"/"+name]
		if !ok {
			st = proto.ClaudeAuthStatus{State: proto.ClaudeAuthUnknown, Reason: proto.ClaudeAuthUnreadable}
		}
		return json.Marshal(st)
	case assistantwire.DeviceAssistantDirectory:
		return json.Marshal(map[string]any{"cwd": a["path"]})
	case DeviceAssistantPrepare:
		f.prepared = append(f.prepared, a)
		return json.Marshal(map[string]any{"cwd": "/home/me/.persona/assistant"})
	case DeviceAssistantGuestPrepare:
		if f.noGuest {
			return nil, fmt.Errorf("unknown method %s", p.Method)
		}
		f.guestPrepared = append(f.guestPrepared, a)
		return json.Marshal(map[string]any{"cwd": "/home/me/.persona/guest"})
	case "worktree.status":
		return json.Marshal(proto.WorktreeStatus{})
	case "workspace.resolve":
		return json.Marshal(proto.Workspace{Cwd: a["path"].(string), Available: true})
	case DeviceWorktreePrepare, DeviceWorktreeCreate: // the machine's engine: checkouts under /wt/<repo>
		a["method"] = p.Method
		f.worktrees = append(f.worktrees, a)
		if p.Method == DeviceWorktreePrepare {
			return json.Marshal(proto.WorktreePrepare{Root: "/code/app", RepoName: "app", Branch: "main", Path: "/wt/app"})
		}
		if f.failWorktree != nil {
			return nil, f.failWorktree
		}
		return json.Marshal(proto.WorktreeCreateResult{WorkspaceID: "ws-new", Path: a["path"].(string), Branch: a["branch"].(string)})
	case DeviceSessionStart:
		if f.offline[p.Device] {
			return nil, fmt.Errorf("%s: %w", p.Device, apierr.ErrOffline)
		}
		f.starts = append(f.starts, a)
		if f.refuseUltra != nil && a["ultracode"] == true {
			return nil, f.refuseUltra
		}
		if g, _ := a["goal"].(string); f.refuseGoal != nil && g != "" {
			return nil, f.refuseGoal
		}
		if f.onStart != nil {
			f.onStart(f, a) // the node changes state (for example its session list) while handling this start
		}
		if f.slowStart {
			<-ctx.Done() // the node builds the session and launches the agent while the caller's wait runs out
			return nil, ctx.Err()
		}
		if f.failStart != nil {
			return nil, f.failStart
		}
		f.agents++
		sess := proto.Session{SessionID: fmt.Sprintf("s%d", f.agents), Cwd: a["cwd"].(string), Agent: "claude", StartError: f.startFailed}
		var instead *proto.StartInstead
		if f.startReply != nil {
			instead = f.startReply(a, &sess)
		}
		return json.Marshal(map[string]any{"session": sess, "instead": instead})
	case "session.interrupt": // a node whose agent's turn ended (settled); tests answer otherwise through f.nodes
		return json.RawMessage(`{"ok":true,"settled":true}`), nil
	case DeviceSessionSend:
		if f.failSend != nil {
			return nil, f.failSend
		}
		f.agentSent = append(f.agentSent, a)
		if f.onSend != nil {
			f.onSend(f, a)
		}
		return json.RawMessage(`{}`), nil
	case DeviceAssistantSend:
		if f.onAssistantSend != nil {
			f.onAssistantSend(f, a)
		}
		if f.failAssistant != nil {
			return nil, f.failAssistant
		}
		if _, set := a["config_dir"]; set && f.refuseConfigDir {
			return nil, &rpc.Error{Code: proto.CodeNodeOutdated, Message: "update Persona on this computer"}
		}
		if l, _ := a["launcher"].(string); a["config_dir"] != nil && launcherAgent(l) != "claude" { // node/assistant.go's preflight
			return nil, errors.New(assistantwire.ConfigDirClaudeOnly)
		}
		if sid, _ := a["session_id"].(string); a["config_dir"] != nil && f.elsewhere[sid] {
			return nil, errors.New("config_dir cannot move an existing conversation; start a new conversation instead")
		}
		f.sends = append(f.sends, a)
		id, _ := a["session_id"].(string)
		if want, _ := a["new_session_id"].(string); id == "" && want != "" { // a new conversation under the id asked for
			if f.known[want] {
				return json.Marshal(map[string]any{"session_id": want, "existed": true})
			}
			id = want
			f.known[id] = true
		}
		if id == "" { // a new conversation
			f.made++
			id = fmt.Sprintf("s%d", f.made)
			f.known[id] = true
		}
		if a["resume_only"] == true && !f.known[id] {
			return nil, fmt.Errorf("conversation %s is gone", id)
		}
		out := map[string]any{"session_id": id}
		if f.assistantAnswer != nil {
			maps.Copy(out, f.assistantAnswer(a))
		}
		return json.Marshal(out)
	}
	return json.RawMessage(`{}`), nil
}

func (f *fakeHub) AppCall(ctx context.Context, p AppCall) (json.RawMessage, error) {
	if p.AppID == AppConnectors && p.Method == ConnectorsSnapshots {
		f.mu.Lock()
		f.snapshotAsks++
		slow, live, delay := f.slowSnapshots, f.liveStates, f.liveDelay
		f.mu.Unlock()
		if slow == 0 && delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if slow == 0 {
			if live == nil {
				live = []any{}
			}
			return json.Marshal(map[string]any{"snapshots": live})
		}
		select {
		case <-time.After(slow):
			return nil, errors.New("too late")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	if key := p.AppID + " " + p.Method; f.blocking[key] {
		answer := f.apps[key]
		f.appCalls = append(f.appCalls, p)
		f.mu.Unlock()
		out, err := answer(p.Params)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
	defer f.mu.Unlock()
	switch p.AppID + " " + p.Method {
	case AppConnectors + " " + ConnectorsSignedIn:
		return json.Marshal(map[string]any{"connectors": []string{}, "clis": map[string]string{}, "lark_cli": f.larkCLI})
	case AppPlugins + " " + PluginsAssistant:
		if f.apps[p.AppID+" "+p.Method] == nil { // else the test's answer (answerApp), below
			return json.Marshal(map[string]any{"instructions": "", "skills": nil})
		}
	case AppSettings + " " + SettingsList:
		return json.Marshal(map[string]any{"settings": []Setting{{Key: "inbox.rules", Title: "收件箱分拣规则", About: "the rules",
			Value: "a string", Actions: []string{"get", "set"}}}})
	case AppSettings + " " + AppTool:
		if f.apps[p.AppID+" "+p.Method] == nil { // else the test's answer (answerApp), below
			return json.Marshal(proto.TextResult("the hub's: "+string(p.Params), false))
		}
	case AppPlugins + " " + assistantwire.PluginsPresets:
		return json.Marshal(map[string]any{"presets": f.presets})
	case AppPlugins + " " + assistantwire.PluginsVoice:
		return json.Marshal(map[string]any{"plugins": f.voice})
	case AppPlugins + " " + assistantwire.PluginsModels:
		return json.Marshal(map[string]any{"extensions": f.modelExts})
	case AppPlugins + " " + assistantwire.PluginsNeedTriage:
		return json.Marshal(map[string]any{"names": f.needTriage})
	}
	if answer := f.apps[p.AppID+" "+p.Method]; answer != nil {
		f.appCalls = append(f.appCalls, p)
		out, err := answer(p.Params)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
	return nil, fmt.Errorf("unexpected %s %s", p.AppID, p.Method)
}

func (f *fakeHub) NotifyRequest(_ context.Context, p NotifyRequest) (NotifyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch p.Notification.Kind {
	case NotifyPush, assistantwire.NotifyDone, assistantwire.NotifyInput, assistantwire.NotifyReply, assistantwire.NotifyDecide, assistantwire.NotifyHealthWater:
	default:
		return NotifyResult{}, fmt.Errorf("kind %q", p.Notification.Kind)
	}
	if f.refuseNotify != nil {
		return NotifyResult{}, f.refuseNotify
	}
	if f.failNotify > 0 {
		f.failNotify--
		return NotifyResult{}, errors.New("the hub is away")
	}
	f.pushes = append(f.pushes, p.Notification)
	return f.notifyResult, nil
}

func (f *fakeHub) Broadcast(_ context.Context, b Broadcast) {
	var ev struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(b.Message, &ev)
	f.mu.Lock()
	f.events = append(f.events, ev.Type)
	if ev.Type == "meeting_live" {
		var live map[string]any
		_ = json.Unmarshal(b.Message, &live)
		f.liveEvents = append(f.liveEvents, live)
	}
	during := f.onBroadcast
	f.mu.Unlock()
	if during != nil {
		during()
	}
}

func (f *fakeHub) CredentialRevoked(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.revokedDevices[id], f.credentialCheckErr
}

func (f *fakeHub) InboxChanged(ctx context.Context, c assistantwire.InboxChanged) {
	f.mu.Lock()
	hook := f.inboxHook
	f.mu.Unlock()
	if hook != nil {
		hook(ctx)
	}
	f.mu.Lock()
	f.events = append(f.events, "inbox")
	f.mu.Unlock()
}

func (f *fakeHub) Snapshot(_ context.Context, s Snapshot) (SnapshotResult, error) {
	f.mu.Lock()
	f.snaps = append(f.snaps, s)
	hook := f.onTell
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return SnapshotResult{OK: true, Seq: s.Seq}, nil
}

func (f *fakeHub) modelCalls() []modelCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]modelCall(nil), f.asked...)
}

func (f *fakeHub) lastSend(t *testing.T) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) == 0 {
		t.Fatal("nothing was sent")
	}
	return f.sends[len(f.sends)-1]
}

func (f *fakeHub) sent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

func (f *fakeHub) forget(id string) {
	f.mu.Lock()
	delete(f.known, id)
	f.mu.Unlock()
}

// setSessions replaces the sessions machine has.
func (f *fakeHub) setSessions(machine string, list ...proto.Session) {
	f.mu.Lock()
	f.sessions[machine] = slices.Clone(list)
	f.mu.Unlock()
}

func (f *fakeHub) setItems(items ...proto.Item) {
	f.mu.Lock()
	f.items = items
	f.mu.Unlock()
}

func (f *fakeHub) lastSnapshot() Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.snaps) == 0 {
		return Snapshot{}
	}
	return f.snaps[len(f.snaps)-1]
}

func (f *fakeHub) pushed() []Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Notification(nil), f.pushes...)
}

// started is a copy of the session.start parameters so far: a task starts its agent on a goroutine of its own, which
// appends to f.starts under f.mu.
func (f *fakeHub) started() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.starts)
}

// itemsReadAfter reports whether a one-item session.items read (waitTurnEnd's lastItemID) came after the first
// `from` calls.
func (f *fakeHub) itemsReadAfter(from int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls[min(from, len(f.calls)):] {
		var a map[string]any
		_ = json.Unmarshal(c.Params, &a)
		if c.Method == DeviceSessionItems && a["limit"] == float64(1) {
			return true
		}
	}
	return false
}

// watcherReadAfter is the goroutine ("goroutine N") of the first run watcher (watchScheduledRun, through
// waitTurnEnd's poll) that read sid's session after the first `from` calls, or "" when none did yet. Calls are logged
// under the lock the sessions are, so that read saw every change made before `from` was taken.
func (f *fakeHub) watcherReadAfter(from int, sid string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := min(from, len(f.calls)); i < len(f.calls); i++ {
		var a map[string]any
		_ = json.Unmarshal(f.calls[i].Params, &a)
		if g := f.watcherGets[i]; g != "" && f.calls[i].Method == DeviceSessionGet && a["session_id"] == sid {
			return g
		}
	}
	return ""
}

// runWatcherGoroutine is the calling goroutine ("goroutine N") when it runs a scheduled run's watcher.
func runWatcherGoroutine() (string, bool) {
	buf := make([]byte, 1<<14)
	stack := string(buf[:runtime.Stack(buf, false)])
	header, _, _ := strings.Cut(stack, " [")
	return header, strings.Contains(stack, ".watchScheduledRun(")
}

func (f *fakeHub) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeHub) eventTypes() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.events, " ")
}

// newTestService is an assistant on f with an in-memory store.
// testLogf is t.Logf until the test ends, nothing after: the service's goroutines (a run's watcher, a snapshot being
// published) can still log when the test is over, and t.Logf then panics and fails the whole package.
func testLogf(t testing.TB) func(string, ...any) {
	var mu sync.Mutex
	done := false
	t.Cleanup(func() {
		mu.Lock()
		done = true
		mu.Unlock()
	})
	return func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		if !done {
			t.Logf(format, args...)
		}
	}
}

// ---- the hub's knowledge base, interaction queue and inbox, as the tools on them call them (tools_apps.go) ----

// appAnswer is a test's answer to an app method's params. It runs under the fake's lock.
type appAnswer func(params json.RawMessage) (any, error)

// nodeAnswer is a test's answer to a node method's params on machine. It runs under the fake's lock.
type nodeAnswer func(machine string, params json.RawMessage) (any, error)

// answerNode has the machines' nodes answer method with answer.
func (f *fakeHub) answerNode(method string, answer nodeAnswer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nodes == nil {
		f.nodes = map[string]nodeAnswer{}
	}
	f.nodes[method] = answer
}

// answerApp has the hub answer app's method with answer.
func (f *fakeHub) answerApp(app, method string, answer appAnswer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.apps == nil {
		f.apps = map[string]appAnswer{}
	}
	f.apps[app+" "+method] = answer
}

// answerAppBlocking is answerApp for an answer that waits on the test (a post held until the test lets it land): it
// runs outside the fake's lock, so the service's other calls go on meanwhile. Under the lock, a goroutine of a
// synctest bubble that calls the fake would wait on the mutex, which is not a durable block: the bubble's clock would
// stop, and a timer the test waits for would never fire (#8924). The answer must not touch the fake's fields.
func (f *fakeHub) answerAppBlocking(app, method string, answer appAnswer) {
	f.answerApp(app, method, answer)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.blocking == nil {
		f.blocking = map[string]bool{}
	}
	f.blocking[app+" "+method] = true
}

// appParams are the params of each call to app's method, in order.
func (f *fakeHub) appParams(app, method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.appCalls {
		if c.AppID == app && c.Method == method {
			var m map[string]any
			_ = json.Unmarshal(c.Params, &m)
			out = append(out, m)
		}
	}
	return out
}

// fakeQueue is the hub's interaction queue (interactions.get / interactions.answer): the pending interactions, how
// the others ended, and the answers taken. raced are pending when read but answered by someone else (with that
// status) before the assistant's answer comes.
type fakeQueue struct {
	pending  map[string]proto.Interaction
	ended    map[string]string
	raced    map[string]string
	answered map[string]map[string]string
}

// withQueue gives the hub an interaction queue with ixs pending.
func (f *fakeHub) withQueue(ixs ...proto.Interaction) *fakeQueue {
	q := &fakeQueue{pending: map[string]proto.Interaction{}, ended: map[string]string{}, raced: map[string]string{},
		answered: map[string]map[string]string{}}
	for _, ix := range ixs {
		q.pending[ix.ID] = ix
	}
	type call struct {
		InteractionID string            `json:"interaction_id"`
		Answers       map[string]string `json:"answers"`
	}
	f.answerApp(AppInteractions, assistantwire.InteractionsGet, func(params json.RawMessage) (any, error) {
		var a call
		_ = json.Unmarshal(params, &a)
		if ix, ok := q.pending[a.InteractionID]; ok {
			return map[string]any{"interaction": ix, "ok": true}, nil
		}
		return map[string]any{"ok": false, "status": q.ended[a.InteractionID]}, nil
	})
	f.answerApp(AppInteractions, assistantwire.InteractionsAnswer, func(params json.RawMessage) (any, error) {
		var a call
		_ = json.Unmarshal(params, &a)
		if status, ok := q.raced[a.InteractionID]; ok {
			return map[string]any{"ok": false, "status": status}, nil
		}
		if _, ok := q.pending[a.InteractionID]; !ok {
			return map[string]any{"ok": false, "status": q.ended[a.InteractionID]}, nil
		}
		delete(q.pending, a.InteractionID)
		q.answered[a.InteractionID] = a.Answers
		return map[string]any{"ok": true}, nil
	})
	return q
}

// Tell keeps what the manager told the hub of the nodes (fleet.*).
func (f *fakeHub) Tell(event string, params any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.told = append(f.told, told{event, b})
	hook := f.onTell
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

type told struct {
	event  string
	params json.RawMessage
}

// launcherAgent is the agent a launcher starts in these tests: the one its name starts with, else claude.
func launcherAgent(name string) string {
	for _, other := range []string{"codex", "gemini"} {
		if strings.HasPrefix(name, other) {
			return other
		}
	}
	return "claude"
}

// lastTurns is the last n turns of items, as the node pages a transcript (transcript.File.PageTurns): a turn opens
// with a prompt the node counts (nodePagePrompt), so the page also holds the turns before it that opened with an
// injected prompt (a scheduled run's, a wake, a supervision prompt). Counting those as turns made the page of a reused
// scheduled chat hold only the current run, which is how #8874 got past the tests.
func lastTurns(items []proto.Item, n int) []proto.Item {
	start := len(items)
	for seen := 0; start > 0 && seen < n; {
		start--
		if nodePagePrompt(items[start]) {
			seen++
		}
	}
	return items[start:]
}

// nodePagePrompt is transcript.pagePrompt (not exported): a user question, not a turn Persona woke. TestLastTurnsPagesLikeTheNode
// holds the two together on a real transcript.
func nodePagePrompt(it proto.Item) bool {
	if it.Role != "user" || it.Kind != proto.KindText || it.Queued || it.Wake || it.Injected != nil {
		return false
	}
	return it.ClientMsgID != "" || it.From != "" || prompts.ParseInjected(it.Text) == nil
}

// fakeAnswer is how the fake model gives one answer: after delay (a caller that gives up first gets none), stopping for
// stop ("": end_turn; "max_tokens": cut off).
type fakeAnswer struct {
	delay time.Duration
	stop  string
}
