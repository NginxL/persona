package node

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
	"github.com/nwverse/persona/server/internal/shellcmd"
)

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// start launches the selected agent in a new herdr tab and waits for its session: `<launcher> --session-id …` (or
// --resume) for Claude Code, waiting until claude is ready; a Codex launcher of the login shell with the first message
// (codex_start.go); pi, the Cursor CLI and the Antigravity CLI with `--model` and the first message delivered once they
// are up (workagent.go); a compatible CLI, or a launcher the user says runs another agent, without Claude's flags
// (start_agent.go).
// workspaceID puts the tab into an existing space (e.g. "new tab next to this agent"); otherwise the workspace named
// after the directory is reused or created.
// tabName names the herdr tab (hub picks one from the first message when the user gave none); empty: agent-<n>.
// newID is the id a new session gets (startAsync picked it already); empty: a fresh one.
// agentType is the agent the user picked for the launcher (claude, codex, traex, omp, pi, cursor, agy); empty: what
// the launcher runs.
func (n *Node) start(ctx context.Context, cwd, launcher, prompt, resume, workspaceID, mode, effort, tabName, newID, agentType string, options ...modelExecution) (_ *proto.Session, startErr error) {
	execution := executionOption(options)
	if err := checkStartSpeedValue(execution.Speed, resume); err != nil {
		return nil, err
	}
	if proto.IsExternalLauncher(launcher) {
		return nil, externalRefusal(ctx, "adapter_purpose_unsupported")
	}
	if resume != "" {
		if st, ok := n.state.get(resume); ok {
			execution.TaskInstructions = st.TaskInstructions
		}
	}
	// A start named to a failed start's kept tab holds it from here, sync and async alike: newTab uses it, else it is
	// kept again when the start returns (startremedy.go).
	claim, claimErr := n.claimKeptTab(execution.InPane)
	if claimErr != nil {
		return nil, claimErr
	}
	defer claim.release()
	if claim != nil {
		ctx = context.WithValue(ctx, keptClaimKey{}, claim)
	}
	// The effort is checked against the agent this start actually dispatches: an explicit agentType wins over the
	// launcher's name, so a Claude start refuses ultra even on a launcher named codex.
	kind := n.startAgentType(launcher, agentType)
	if err := execution.check(kind); err != nil {
		return nil, err
	}
	var argv []string
	codex := false
	if proto.FasterSpeed(execution.Speed) {
		argv, codex = n.codexLauncher(ctx, launcher)
		if err := execution.applyStartSpeed(launcher, kind, codex); err != nil {
			return nil, err
		}
		if execution.ServiceTier != "" {
			if err := n.checkCodexStartSpeed(ctx, launcher, execution.Model, execution.ServiceTier); err != nil {
				return nil, err
			}
		}
	}
	runs, refused := terminalLauncher(launcher, resume) // the desktop app's launcher runs in no terminal (claudedesktop_new.go)
	if refused != nil {
		return nil, refused
	}
	launcher = runs
	if execution.SecretTicket != "" && resume != "" { // a hand-off goes only to a new start (secretenv.go)
		return nil, refusal(secretEnvAgentCode, secretEnvAgentText, nil)
	}
	if resume != "" && validID(resume) && n.codexPath(resume) != "" && !n.isAssistantSession(resume) {
		if _, live := n.agent(resume); !live { // a Codex thread nothing runs (codex_resume.go)
			return n.resumeCodexSession(ctx, resume, launcher, prompt, mode)
		} else if mode == "plan" {
			return nil, readonlyStartUnsupported() // CLI flags cannot constrain a process that is already running.
		}
	}
	if resume != "" && n.externalCodex(resume) {
		return nil, refusal("codex_read_only", "不能用启动器恢复外部 Codex 会话", nil)
	}
	cwd, launcher, err := n.checkStart(cwd, launcher, resume, mode, effort, kind)
	if err != nil {
		return nil, err
	}
	if kind != "claude" && kind != "codex" && kind != traexAgent && kind != ompAgent && workAgentKind(launcher, kind) == "" {
		return nil, fmt.Errorf("unsupported agent type %q", kind)
	}
	if execution.SecretTicket != "" {
		if err := n.secretStartProblem(kind, launcher, resume, execution.SecretTicket); err != nil {
			return nil, err
		}
		defer func() { // this start took the ticket: failed, nothing else may fetch its values (secretenv.go)
			if startErr != nil {
				n.handoffs().take(execution.SecretTicket, false)
				startErr = withoutAnswers(startErr) // nor can an answer start it again with them (start.continue)
			}
		}()
	}
	if kind == ompAgent { // start_agent.go: omp's own command, and none of a Claude start's options
		if launcher != ompAgent {
			// Only the launcher named omp is known to run omp: another one (a Codex launcher, a provider of no omp
			// command) would type something else into the pane and get the first message.
			return nil, fmt.Errorf("omp starts only with the launcher named omp, not %q", launcher)
		}
		if execution.Model != "" || mode != "" || effort != "" || wantsUltracode(execution.Ultracode) {
			return nil, refusal("omp_options", ompOptionsRefusal, nil)
		}
		if resume != "" {
			if a, ok := n.agent(resume); ok {
				return n.sessionFor(resume, a), nil // already running
			}
			// session.start holds n.recovery.lock(resume) (node.go), as a send to a stopped row does (ompcalls.go): a
			// second start or send waits for this resume and finds the session running above, so no second omp runs.
			// The guards a resume of any other agent passes (below): a session open elsewhere or owned by a runtime
			// is not started again, and not on Orca.
			if r := n.openElsewhereRefusal(resume); r != nil {
				return nil, r
			}
			// Only a runtime still connected owns it: one that listed it and left (its omp exited) does not keep the
			// session from its file.
			if h, _, _ := n.runtimes.belongs(resume); h != nil {
				return nil, refusal("runtime_session", "这个会话属于另一个工具（例如 Orca 的原生对话），请在那里继续，这次没有启动。", nil)
			}
			if n.startKind(ctx) == proto.EngineOrca {
				return nil, refusal("orca_resume", "Orca 上只能从手机继续 Claude 会话：其它 Agent 请在电脑上恢复，这次没有启动。", nil)
			}
			if _, err := n.resumeOmp(ctx, resume, ompResumeParams(ctx, resume, prompt)); err != nil { // ompresume.go
				return nil, err
			}
			if a, ok := n.agent(resume); ok {
				return n.sessionFor(resume, a), nil
			}
			return nil, refusal("not_running", "接着这个 omp 会话后没等到它的会话行，这次没有发送。", nil)
		}
	}
	if !proto.FasterSpeed(execution.Speed) {
		argv, codex = n.codexLauncher(ctx, launcher)
	}
	if codex && execution.SecretTicket != "" { // Codex would start without the values (secretenv.go)
		return nil, refusal(secretEnvAgentCode, secretEnvAgentText, nil)
	}
	if mode == "plan" {
		if codex && kind != traexAgent {
			if argv, err = n.codexReadonlyArgs(launcher, argv, mode); err != nil {
				return nil, err
			}
		} else if kind != "claude" {
			// This launcher path has no verified read-only argument mapping.
			return nil, readonlyStartUnsupported()
		}
	}
	if err := n.checkUltracodeStart(ctx, execution.Ultracode, kind); err != nil {
		return nil, err
	}
	if execution.Model != "" && workAgentKind(launcher, kind) == "" {
		agent, err := n.launcherModelCheck(ctx, launcher, execution.Model)
		if err != nil {
			return nil, err
		}
		if agent != "claude" && agent != "codex" {
			return nil, errors.New("普通 Agent 的启动只支持 Claude Code 或 Codex 启动器")
		}
	}
	if resume != "" {
		if a, ok := n.agent(resume); ok {
			if mode == "plan" && a.Agent == "codex" {
				return nil, readonlyStartUnsupported() // A live thread without a local rollout cannot acquire CLI flags either.
			}
			n.starts.recovered(resume)
			return n.sessionFor(resume, a), nil // already running
		}
		// A claude still alive on the session (Claude's desktop app, another terminal, suspended there) refuses: two
		// writers would corrupt its transcript. Otherwise a Claude session resumes in the start's host, Orca like herdr: a
		// terminal opened in the session's worktree (#5548).
		if r := n.openElsewhereRefusal(resume); r != nil { // open in Claude's desktop app or a terminal (openelsewhere.go)
			return nil, r
		}
		if _, _, known := n.runtimes.belongs(resume); known { // an Orca native chat and the like: its runtime's, not a terminal's
			return nil, refusal("runtime_session", "这个会话属于另一个工具（例如 Orca 的原生对话），请在那里继续，这次没有启动。", nil)
		}
		if n.startKind(ctx) == proto.EngineOrca && kind != "claude" { // the start's host
			return nil, refusal("orca_resume", "Orca 上只能从手机继续 Claude 会话：其它 Agent 请在电脑上恢复，这次没有启动。", nil)
		}
	}

	var instructionFile, instructionChannel string
	if execution.TaskInstructions != "" {
		switch {
		case codex && kind != traexAgent && n.taskCodexChannel(ctx, launcher, cwd, argv) && len(codexTaskConfig(execution.TaskInstructions)) < maxResumeArgv-2048:
			instructionChannel = "codex"
			argv = codexTaskArgs(argv, execution.TaskInstructions)
		case !codex && kind == "claude" && n.taskClaudeChannel(ctx, launcher):
			instructionFile, err = n.taskInstructionFile(execution.TaskInstructions)
			if err != nil {
				return nil, err
			}
			instructionChannel = "claude"
		default:
			previous, _ := n.state.get(resume)
			prompt, err = taskResumeFallback(ctx, prompt, execution.TaskInstructions, resume != "", previous.TaskInstructionChannel)
			if err != nil {
				return nil, err
			}
		}
	}
	prompt = taskFallback(prompt, execution.TaskAttachments)

	// New sessions start in the host the start named, else the default host.
	launch, err := n.launcherFor(n.startKind(ctx))
	if err != nil {
		return nil, err
	}
	ws := workspaceID
	if ws == "" {
		if ws, err = n.workspaceFor(ctx, launch, cwd); err != nil {
			return nil, err
		}
	} else if _, ok := launch.(*herdrHost); ok {
		// A start into a workspace it names (a task's worktree) brings back a herdr server that stopped since, as
		// workspaceFor does for the others: an agent always runs in the engine's terminal (#4754). What herdr then says
		// is reported as before.
		_ = n.ensureHerdr(ctx, "start a session in a workspace")
	}
	if codex && kind != traexAgent { // codex_start.go
		if resume != "" { // a Codex thread is resumed above: this id is not one
			return nil, refusal(codexResumeRefused, "找不到这个 Codex 会话的记录，没法接着它：请在电脑上恢复（codex resume）。", nil)
		}
		// An inherited effort needs no override, which would leave the shared app-server; the record keeps it all the same.
		runs := n.codexOwnEffort(ctx, argv, codexPaneEnv(launch), cwd, execution)
		s, err := n.startCodex(ctx, launch, cwd, launcher, codexExecutionArgs(argv, runs), execution, prompt, ws, tabName, mode)
		if err == nil && strings.TrimSpace(prompt) != "" {
			n.codexGoalAfterStart(s.SessionID, s.PaneID, execution.Goal) // its first message is in (codex_start_goal.go)
		}
		return s, err
	}
	if work := workAgentKind(launcher, kind); work != "" { // workagent.go
		if resume != "" {
			return nil, refusal("work_agent_resume", "这个会话不能用启动器接着：pi 在它原来的标签里继续，cursor 和 agy 请在电脑上恢复。这次没有启动。", nil)
		}
		return n.startWorkAgent(ctx, launch, cwd, launcher, work, execution.Model, effort, prompt, ws, tabName)
	}
	if kind != "claude" { // start_agent.go
		if mode != "" || effort != "" {
			return nil, fmt.Errorf("permission mode and effort are only supported for Claude starts")
		}
		return n.startOtherAgent(ctx, launch, cwd, launcher, kind, prompt, resume, ws, tabName)
	}
	if err := n.codexFunctionLauncher(ctx, launcher, true); err != nil {
		return nil, err
	}
	id := resume
	if id == "" {
		id = newID
	}
	if id == "" {
		id = newUUID()
	}
	// Persona's hooks: a plugin, or in --settings for a claude that loads none (hooksplugin.go).
	settings, hooks := n.claudeStartSettings(ctx, launcher, "claude", cwd)
	// No tier alias on the command line for a gateway provider (#10365): its model for that tier, else its default.
	spec := claudeLaunch{Launcher: launcher, Settings: settings, Hooks: hooks, Display: n.liveRepliesPlugin(hooks), Model: n.claudeModelArg(launcher, execution.Model),
		Plugins: n.claudeLoadsPlugins(ctx), TaskInstructionFile: instructionFile}
	lineSpec := spec
	if execution.FastMode {
		lineSpec.Settings, err = n.claudeFastModeSettings(ctx, launcher, settings, cwd)
		if err != nil {
			return nil, speedRefusal("agent_speed_unsupported", "Claude Code fast mode settings could not be prepared without replacing the launcher's settings; nothing started")
		}
	}
	created, err := n.newTab(ctx, launch, cwd, ws, tabName, id) // retire.go
	if err != nil {
		return nil, err
	}
	pane := created.Pane.String()
	if name := n.starts.tabCreated(id, created.SlotID); name != "" { // named while the tab was being made
		_ = renameSlot(ctx, launch, created.SlotID, name)
	}

	// A trailing refused turn would be merged into the first new prompt of a resume, so it is cut first: the file's
	// last turn is then clean again. A session whose only turn was refused loses its file and starts under its id.
	if resume != "" {
		n.healClaudeRefusalTranscript(id)
	}
	// A session whose first start failed (claude never came up) has no transcript to resume: start it under its id.
	args := "--session-id " + id
	if resume != "" && n.transcriptPath(id) != "" {
		args = "--resume " + id
	}
	planThen := planThenOf(mode, execution.PlanThen)
	// herdr told it is Claude (hostherdr.go): a launcher that wraps another binary is otherwise not seen as one. The
	// node's directory in front, the assistant's environment left out (agentenv.go).
	shells := n.startPaneShells(ctx, launch, created)
	line := n.claudeCommandLine(lineSpec, args+modeFlags(mode, effort)+planThenFlags(mode, planThen), shellQuote)
	if execution.SecretTicket != "" { // through personad launch --ticket; the spec herdr resumes with keeps none (secretenv.go)
		line = n.withSecretTicket(line, launcher, execution.SecretTicket, shellQuote)
	}
	cmd := withHerdrAgent(typedHerdrAgent(launch, "claude", shells...), line)
	cmd = n.agentStartCommand(cmd, shellQuote, shells...)
	if cdFirst(ctx) { // a launcher switch in a shell that is not in the conversation's folder (launcher_switch.go)
		cmd = cdThen(cwd, cmd, shells...)
	}
	n.notePaneStart(launcher) // not in the agent sandbox unless a managed launcher (agentsandbox.go)
	st := sessionState{Pane: pane, Launcher: launcher, Cwd: cwd, StartedAt: time.Now().UTC(), Model: execution.Model, ModelFingerprint: execution.Fingerprint,
		PlanThen: planThen, TaskInstructions: execution.TaskInstructions, TaskInstructionFile: instructionFile, TaskInstructionChannel: instructionChannel}
	if prev, ok := n.state.get(id); ok && resume != "" {
		if !prev.StartedAt.IsZero() { // a branch (fork.go) has none: it starts now
			st.StartedAt = prev.StartedAt
		}
		st.Ultracode = prev.Ultracode
		// A switch to another launcher still waits for the next message (launcher_switch.go): a Resume on the launcher
		// the agent ran on keeps it; the switch's own start leaves it for the switch to clear.
		st.PendingSwitch = prev.PendingSwitch
		st.EngineHistory = prev.EngineHistory   // the conversation's earlier stretches (engine_switch.go)
		st.PendingHandoff = prev.PendingHandoff // a handoff still waiting for a message to go in front of (send.go)
	}
	if legs, ok := engineHistoryOf(ctx); ok { // a switch from the other engine starts this one (engine_switch.go)
		st.EngineHistory = legs
	}
	n.state.put(id, st)
	n.noteModes(id, mode, effort) // shown before the first prompt writes them down
	// The fresh shell at rest first: a question its startup files ask would eat the typed line's head (#2570).
	if err := n.settleShell(ctx, pane); err != nil {
		return nil, n.startTabFailed(ctx, launch, created, ws, id, err)
	}
	n.beginUltracodeProcess(id, resume != "", true, time.Now())
	typed, err := n.typeStart(ctx, launch, created, ws, id, cmd, shells...) // typedstart.go
	if err != nil {
		return nil, err
	}
	probe := n.probeFor(ctx, id, launcher, shells)
	probe.typed(typed)
	probe.acceptBypass = mode == proto.ModeBypass || planThen == proto.ModeBypass
	a, err := n.waitReady(ctx, pane, probe)
	if rejected := (*startLineRejected)(nil); errors.As(err, &rejected) {
		// Nothing of it ran: typed once more as it was, once the shell is at rest again. A second refusal ends the start.
		n.logf("start %s: %v; typing it again", id, err)
		err = n.retypeStart(ctx, pane, typed)
		if err == nil {
			a, err = n.waitReady(ctx, pane, probe)
		}
	}
	typed.done()
	if err != nil {
		return nil, n.startTabFailed(ctx, launch, created, ws, id, err)
	}
	n.bindPane(pane, id)
	n.starts.recovered(id)
	_ = launch.NameAgent(ctx, created.Pane, "j-"+id[:6])
	n.holdResume(ctx, launch, pane, id, spec) // herdr's restore resumes it through the launcher (herdrresume.go)
	// The goal goes after the first prompt is in (startgoal.go): only a new Claude's here; a Codex's in startCodex's
	// caller above.
	goal := ""
	if kind == "claude" && resume == "" {
		goal = execution.Goal
	}
	// Before the first prompt; never a failed start: Claude is up, and ultracode_note says why it is not on.
	if n.startUltracode(ctx, id, pane, execution.Ultracode) && prompt != "" {
		// The command may still sit in the input box: the first prompt would be typed after it and sent with it. Left
		// to the user as one held back by a dialog is (firstPrompt).
		if err := n.ultracodeBoxClear(ctx, pane); err != nil {
			why := lostUltracode
			if rpcCode(err) == screen.DialogOpen {
				why = lostDialog
			}
			n.logf("start %s: first message held back after ultracode: %v", id, err)
			n.lostFirst.set(id, why, time.Now())
			prompt = ""
		}
	}
	if prompt == "" && goal != "" {
		n.logf("start %s: no first message went in, so no goal is set", id)
	}
	if prompt != "" {
		// Checked against the transcript and typed again when it was dropped; one that never goes in leaves the
		// session needing the user, with why, not idle (firstprompt.go).
		if err := n.firstPrompt(ctx, pane, id, prompt, a, goal); err != nil {
			if adoptedPane(ctx) != nil && !errors.Is(err, host.ErrNotDelivered) && !errors.Is(err, host.ErrNoAgent) {
				return nil, unconfirmedStart(err) // a switch's message (engine_switch.go) may be in the claude
			}
			return nil, err
		}
	}
	return n.sessionFor(id, *a), nil
}

// retypeStart types a start's line again into pane once its shell is at rest (settleShell), after leaving the
// continuation prompt a cut quote left it at (Ctrl-C).
func (n *Node) retypeStart(ctx context.Context, pane string, typed *typedStart) error {
	if err := n.settleShell(ctx, pane); err != nil {
		return err
	}
	if shown, err := n.readPane(ctx, pane, "visible", 60); err == nil && shellContinuation.MatchString(lastLine(shown)) {
		_ = n.sendKeys(ctx, pane, "ctrl+c")
		if err := n.settleShell(ctx, pane); err != nil {
			return err
		}
	}
	return typed.again(ctx)
}

// startTabFailed is the error of a start that failed in the tab it made, created, and closes the tab: an agent that
// came up after the start gave up would sit there unbound, and a retry opens another. Two failures keep it for
// keptStartWait instead, with where it is (withKeptTab): a shell waiting on its startup question (shell_waiting),
// which someone may answer, and a claude whose own screen stopped before its input box (agent_start_stalled), which
// someone may look at. A line the shell did not take twice is start_line_rejected. The tab's last lines go with the
// error: in its data (screen) for a coded one, else after its text.
func (n *Node) startTabFailed(ctx context.Context, launch host.Launcher, created *host.Created, ws, id string, err error) error {
	pane := created.Pane.String()
	shown, _ := n.readPane(ctx, pane, "recent-unwrapped", 30)
	tail := lastLines(shown, 12)
	keep := func(e error) error {
		if adoptedPane(ctx) != nil { // the user's own pane ("Run Agent Here"): theirs to keep, never closed later
			return e
		}
		return withKeptTab(e, pane, n.keepStartTab(launch, created, ws, id))
	}
	var stalled *claudeStalled
	var rejected *startLineRejected
	switch {
	case rpcCode(err) == proto.CodeShellWaiting:
		return keep(err)
	case errors.As(err, &stalled):
		return keep(refusal(proto.CodeAgentStartStalled, fmt.Sprintf("%v\n%s", err, tail), map[string]any{"screen": tail}))
	case errors.As(err, &rejected):
		n.closeNewTab(ctx, launch, created, ws)
		return refusal(proto.CodeStartLineRejected, fmt.Sprintf("%v, twice; the tab is closed\n%s", err, tail),
			map[string]any{"line": rejected.line, "screen": tail})
	}
	n.closeNewTab(ctx, launch, created, ws)
	if rpcCode(err) != "" { // the tab's shell said why (shellmiss.go): its tail would only repeat the command
		return err
	}
	return fmt.Errorf("%w\n%s", err, tail)
}

// checkStart validates a start's arguments and returns the directory (~ expanded, home when empty) and launcher.
// agentType is the agent the start dispatches (start's kind: an explicit agent_type wins); empty: the launcher's own.
func (n *Node) checkStart(cwd, launcher, resume, mode, effort, agentType string) (string, string, error) {
	if proto.IsExternalLauncher(launcher) {
		return "", "", externalRefusal(context.Background(), "adapter_purpose_unsupported")
	}
	if launcher == "" {
		launcher = "claude"
	}
	if !shellcmd.NameRe.MatchString(launcher) {
		return "", "", fmt.Errorf("invalid launcher %q", launcher)
	}
	if resume != "" && !validID(resume) {
		return "", "", fmt.Errorf("invalid session id %q", resume)
	}
	kind := agentType
	if kind == "" {
		kind = launcherAgentType(launcher)
	}
	if err := validModesFor(kind, mode, effort); err != nil {
		return "", "", err
	}
	cwd = proto.ExpandHome(cwd, n.home)
	if cwd == "" {
		cwd = n.home
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		return "", "", fmt.Errorf("directory %s does not exist", cwd)
	}
	return cwd, launcher, nil
}

// tabLabels are the names of the workspace's tabs.
func (n *Node) tabLabels(ctx context.Context, launch host.Launcher, ws string) map[string]bool {
	taken := map[string]bool{}
	h, ok := launch.(host.Host)
	if !ok {
		return taken
	}
	if snap, err := h.Snapshot(ctx); err == nil {
		for _, t := range snap.Slots {
			if t.SpaceID == ws {
				taken[t.Label] = true
			}
		}
	}
	return taken
}

// tabLabel is the name a new tab gets: the given one (one line, at most 32 characters), else agent-<n>; a name the
// workspace already has gets -2, -3….
func tabLabel(name string, taken map[string]bool) string {
	name = strings.Join(strings.Fields(name), " ")
	if r := []rune(name); len(r) > 32 {
		name = strings.TrimSpace(string(r[:32]))
	}
	if name == "" {
		for i := 1; ; i++ {
			if l := fmt.Sprintf("agent-%d", i); !taken[l] {
				return l
			}
		}
	}
	if !taken[name] {
		return name
	}
	for i := 2; ; i++ {
		if l := fmt.Sprintf("%s-%d", name, i); !taken[l] {
			return l
		}
	}
}

// workspaceFor reuses the workspace named after the project directory (the user keeps one workspace per project).
func (n *Node) workspaceFor(ctx context.Context, launch host.Launcher, cwd string) (string, error) {
	if _, ok := launch.(*herdrCoreHost); ok {
		// The app's folder identity is resolved on each layout retry. Passing an id here would turn an automatic
		// choice into an explicit destination and miss a space created concurrently for the same directory.
		return "", nil
	}
	var startErr error
	if _, ok := launch.(*herdrHost); ok {
		startErr = n.ensureHerdr(ctx, "start or resume a session") // herdrstart.go
	}
	wss, err := launch.Spaces(ctx)
	if err != nil {
		// Nothing was started or typed: say so, and what to do, instead of herdr's own words (the app showed
		// "herdr workspace list: exec: \"herdr\": executable file not found").
		name := n.defaultEngineName()
		if k := n.startKind(ctx); proto.EngineNames[k] != "" && k != n.defaultKind() {
			name = proto.EngineNames[k] // a start that named its host
		}
		msg := fmt.Sprintf("这台电脑上的 %s 没在运行（或没装），不能在这里新开或接着会话，这次没有发送。"+
			"要从手机接着电脑上开着的会话，在那边继续；要从手机新开，先在这台电脑上装好并打开 %s。", name, name)
		if startErr != nil && !errors.Is(startErr, errHerdrNotInstalled) {
			msg = fmt.Sprintf("这台电脑上的 %s 没在运行，自动启动也没成功，不能在这里新开或接着会话，这次没有发送。"+
				"请在这台电脑上打开 %s 再试；原因见详情。", name, name)
		}
		return "", refusal(engineMissing, msg,
			map[string]any{"detail": herdrMissingDetail(err, startErr)})
	}
	base := filepath.Base(cwd)
	for _, w := range wss {
		if w.Label == base {
			return w.ID, nil
		}
	}
	return "", nil
}

func (n *Node) sessionFor(id string, a host.Agent) *proto.Session {
	s := &proto.Session{
		SessionID: id, Cwd: a.Cwd, Status: a.AgentStatus, PaneID: a.PaneID, TabID: a.TabID,
		WorkspaceID: a.WorkspaceID, AgentName: a.Name, Title: a.Title, Agent: a.Agent, UpdatedAt: time.Now().UTC(), // just started
	}
	_ = n.refreshHosts(context.Background())
	n.placeIn(s, a)
	n.decorate(s)
	s.PermissionMode, s.Effort = n.modesOf(id)
	if why := n.lostFirstPrompt(id, s.Status); why != "" { // firstprompt.go
		s.Status, s.AttentionReason = proto.StatusBlocked, why
	}
	return s
}

// close quits any Claude running in the machine's engine (started by Persona or by hand) and closes its pane: herdr
// closes its tab, or in a split tab only its own pane, so the other panes survive; herdr-core ends its terminal.
// Its user-action callers hold the recovery and typing locks, including retire's already-checked turn.
func (n *Node) close(ctx context.Context, id string) error {
	return n.closeWithCause(ctx, id, proto.StopUser)
}

// closeWithCause is close recording cause: the user's (saved and read back before anything is closed), or an
// automatic one (idle, task_completed: #10355), which is recorded only once the close went through, as an automatic
// stop's is (retire.go).
func (n *Node) closeWithCause(ctx context.Context, id string, cause proto.StopCause) (err error) {
	if err := n.switchingRefusal(id); err != nil { // a launcher switch, maybe on the last connection's Node
		return err
	}
	if cause == proto.StopUser {
		if err := n.saveStopCause(ctx, id, proto.StopUser); err != nil {
			return err
		}
		defer n.state.stopped(id, proto.StopUser)
	} else {
		n.state.stopped(id, proto.StopUnknown) // an automatic close that fails grants no recovery
		defer func() {
			if err == nil {
				n.state.stopped(id, cause)
			}
		}()
	}
	a, ok := n.agent(id)
	if !ok {
		if st, mine := n.state.get(id); mine {
			return n.closeAfterExit(ctx, id, st)
		}
		return fmt.Errorf("这个 Agent 不在这台电脑的终端里运行，没法从手机关闭")
	}
	org, ref, err := paneCap[host.Organizer](n, a.PaneID) // a host that cannot close it is not interrupted first
	if err != nil {
		return err
	}
	_ = n.sendKeys(ctx, a.PaneID, "ctrl+c")
	time.Sleep(200 * time.Millisecond)
	_ = n.sendKeys(ctx, a.PaneID, "ctrl+c")
	time.Sleep(500 * time.Millisecond)
	return org.Close(ctx, ref, a.WorkspaceID, a.TabID)
}

// exitCommand: the message is Claude's or Codex's own command to quit (/exit, /quit).
func exitCommand(text string) bool {
	switch strings.TrimSpace(text) {
	case "/exit", "/quit":
		return true
	}
	return false
}

// exitAgent is a message that only says to quit: a running agent is stopped as session.stop does (its tab stays with
// the shell, retire.go), and one already stopped is left as it is rather than resumed in a new tab just to quit there.
// It runs inside send's turn for the session, so it does not take the typing lock again as stop does.
func (n *Node) exitAgent(ctx context.Context, id string) error {
	// Recorded as an exit, not the user's Stop: the session ended (the owner's close-on-end setting, #10355, closes
	// its terminal; a Stop keeps it). Neither permits a supervisor resume.
	if err := n.saveStopCause(ctx, id, proto.StopExit); err != nil {
		return err
	}
	defer n.state.stopped(id, proto.StopExit)
	a, ok := n.agent(id)
	if !ok {
		return nil
	}
	_, err := n.quitAgent(ctx, id, a.PaneID)
	return err
}
