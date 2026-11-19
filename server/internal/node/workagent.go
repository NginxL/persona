package node

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// Work agents (docs/integrations/launchers.md "Work agents"): pi, the Cursor CLI (cursor-agent) and the Antigravity
// CLI (agy). They are Agents the user works with, started in a herdr pane the way Claude and Codex are, with a model
// the start names (`--model`) and a first message. agy also takes an explicit `--effort`. Permission modes and
// ultracode are unsupported, and the first message is never typed into the pane. pi's goes through its Persona extension
// (session.send); cursor-agent's and agy's go through herdr's agent.prompt, which only reaches an agent herdr detects.

const (
	cursorAgent             = "cursor"
	agyAgent                = "agy"
	workAgentPaneLockPrefix = "run-agent-in-pane:"
)

// workAgent is one such agent: its herdr name, and the program a start types when the launcher is the agent itself.
func workAgent(kind string) (herdrName, program string, ok bool) {
	switch kind {
	case piAgent:
		return piAgent, "pi", true
	case cursorAgent:
		return cursorAgent, "cursor-agent", true
	case agyAgent:
		return agyAgent, "agy", true
	}
	return "", "", false
}

// workAgentKind is the work agent a launcher starts: the agent the hub named, else the launcher's own name when that
// name is the agent (pi, cursor, agy). "" for a launcher that starts Claude, Codex or another agent.
func workAgentKind(launcher, agentType string) string {
	if _, _, ok := workAgent(agentType); ok {
		return agentType
	}
	if agentType == "" {
		if _, _, ok := workAgent(launcher); ok {
			return launcher
		}
	}
	return ""
}

// workAgentCommand is what a start types into the pane: the launcher (the program, for the agent's own name) with the
// model when one was picked and the effort when picked for agy. The first message is delivered once the agent is up.
// pi also gets --approve: in a directory with project-local resources (.agents/, AGENTS.md, .pi/) it otherwise stops at
// its Trust prompt, which nobody at the pane answers, and the start is abandoned (#9462). The flag trusts the directory
// the start chose for this run only; it writes nothing to ~/.pi/agent/trust.json, so a pi the user runs keeps asking.
func workAgentCommand(launcher, program, model, effort string) string {
	cmd := launcher
	if launcher == "" || launcher == program || workAgentKind(launcher, "") == launcher {
		cmd = program
	}
	if model != "" {
		cmd += " --model " + shellQuote(model)
	}
	if program == agyAgent && effort != "" {
		cmd += " --effort " + shellQuote(effort)
	}
	if program == "pi" {
		cmd += " --approve"
	}
	return cmd
}

// workAgentResume is how herdr resumes one Persona started: the same command with the session it was, when the agent
// has a session id Persona can hand back. Pi uses the session file reported by herdr's pi integration. Agy uses a
// verified native conversation ID when available. Without one, agy starts a new conversation. Cursor starts fresh.
func workAgentResume(kind, cmd, id string) string {
	if kind == piAgent && id != "" {
		return cmd + " --session " + dquote(id)
	}
	if kind == agyAgent && uuidRe.MatchString(id) {
		return cmd + " --conversation " + id
	}
	return cmd
}

// startWorkAgent opens a tab in ws and runs the work agent there, then delivers the first message: to pi through the
// extension that lists the session (finishPiStart), to cursor-agent and agy through herdr's agent.prompt once herdr
// reports the agent, checked until the agent takes it (workAgentFirst). A pi whose extension never lists the session is
// quit; its tab stays at the shell, and typing the message into pi is refused.
func (n *Node) startWorkAgent(ctx context.Context, launch host.Launcher, cwd, launcher, kind, model, effort, prompt, ws, tabName string) (*proto.Session, error) {
	herdrName, program, ok := workAgent(kind)
	if !ok {
		return nil, fmt.Errorf("unsupported agent type %q", kind)
	}
	if kind != agyAgent {
		effort = ""
	}
	created, err := n.newTab(ctx, launch, cwd, ws, tabName, "")
	if err != nil {
		return nil, err
	}
	pane := created.Pane.String()
	if adoptedPane(ctx) == nil {
		// Keep another start from taking this pane while its command or cleanup is still in flight. The adopted caller
		// holds the same lock (runAgentInPane); acquiring it again here would deadlock.
		unlock := n.typing.lock(workAgentPaneLockPrefix + pane)
		defer unlock()
		// The tab may have been claimed or adopted before the lock was acquired. Recheck instead of typing over its agent,
		// using only this pane's host: a successful answer from another host cannot prove this pane is empty.
		h := n.hosts.Get(host.ParsePane(pane).Kind)
		if h == nil {
			return nil, fmt.Errorf("host %s is unavailable", host.ParsePane(pane).Kind)
		}
		if err := n.refreshHosts(ctx, h); err != nil {
			return nil, err
		}
		if id := n.sessionOfPane(pane); id != "" {
			return nil, fmt.Errorf("pane %s already runs agent session %s", pane, id)
		}
	}
	if err := n.settleShell(ctx, pane); err != nil { // the fresh shell at rest first (shellsettle.go, #2570)
		return nil, err
	}
	shells := n.startPaneShells(ctx, launch, created)
	command := launcher
	if kind == agyAgent {
		command = n.launcherCommand(launcher)
	}
	cmd := withHerdrAgent(typedHerdrAgent(launch, herdrName, shells...), workAgentCommand(command, program, model, effort))
	// The command goes through the start file, so a line break in it cannot meet a shell that runs each line on its
	// own (typedstart.go).
	done, err := n.runWorkAgentCommand(ctx, launch, created, ws, "", n.agentStartCommand(cmd, shellQuote, shells...), shells...)
	if err != nil {
		if done == nil {
			return nil, err
		}
		done()
		return nil, n.abandonStart(ctx, created, kind, err)
	}
	defer done()
	if kind == piAgent {
		return n.finishPiStart(ctx, launch, created, cwd, launcher, model, prompt, done)
	}
	id, agent, err := n.waitWorkAgent(ctx, pane, herdrName)
	if err != nil {
		done()
		return nil, n.abandonStart(ctx, created, kind, err)
	}
	n.noteWorkAgent(ctx, launch, pane, id, cwd, launcher, kind, model, effort)
	if prompt != "" {
		// Typed once, then checked in the background until the agent is seen taking it (workagent_first.go, #11345).
		if err := n.workAgentFirst(ctx, pane, id, herdrName, prompt); err != nil {
			return nil, fmt.Errorf("send first message: %w", err)
		}
	}
	session := n.sessionFor(id, agent)
	session.Agent = herdrName
	return session, nil
}

// finishPiStart waits for the extension in the pi just started to list its session, then sends the first message
// through it. The agent is quit and its tab is kept when the extension never lists it: the message is not typed into pi.
func (n *Node) finishPiStart(ctx context.Context, launch host.Launcher, created *host.Created, cwd, launcher, model, prompt string, done func()) (*proto.Session, error) {
	pane := created.Pane.String()
	deadline := time.Now().Add(piResumeWait)
	for {
		if id := n.runtimes.sessionInPane(pane, n.ownPane); id != "" {
			if err := n.refreshHosts(ctx); err != nil {
				return nil, err
			}
			n.noteWorkAgent(ctx, launch, pane, id, cwd, launcher, piAgent, model, "")
			if prompt != "" {
				if err := n.sendToPi(ctx, id, prompt); err != nil {
					return nil, fmt.Errorf("send first message: %w", err)
				}
			}
			a, _ := n.agent(id)
			if a.Agent == "" {
				a = host.Agent{Agent: piAgent, PaneID: pane}
			}
			session := n.sessionFor(id, a)
			session.Agent = piAgent
			return session, nil
		}
		if time.Now().After(deadline) {
			screen, _ := n.readPane(ctx, pane, "recent-unwrapped", 30)
			err := refusal("pi_no_extension", fmt.Sprintf("pi 起来了，但 %d 秒内没有 Persona 扩展报这个会话，首条消息没有发送。装上扩展（pi install）再开。\n%s",
				int(piResumeWait.Seconds()), lastLines(screen, 12)), nil)
			return nil, n.abandonStartAfterDone(ctx, created, piAgent, done, err)
		}
		select {
		case <-ctx.Done():
			return nil, n.abandonStartAfterDone(ctx, created, piAgent, done, ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// noteWorkAgent records the session as Persona's, bound to its pane, so herdr resumes it the way it was started.
func (n *Node) noteWorkAgent(ctx context.Context, launch host.Launcher, pane, id, cwd, launcher, kind, model, effort string) {
	n.state.put(id, sessionState{Pane: pane, Launcher: launcher, Cwd: cwd, Model: model, ModelEffort: effort, StartedAt: time.Now().UTC()})
	n.bindPane(pane, id)
	n.holdResume(ctx, launch, pane, id, agentLaunch{Agent: kind, Launcher: launcher, Model: model, Effort: effort, ID: id})
}

// sendToPi delivers text to a pi session through the extension that serves it. Refused, with nothing typed, when no
// extension does.
func (n *Node) sendToPi(ctx context.Context, id, text string) error {
	if _, _, ok := n.runtimes.rowOf(id); !ok {
		return refusal("pi_no_extension", piNoExtension+"。这次没有发送。", nil)
	}
	owner := n.runtimes.ownerOf(id)
	if owner == nil {
		return refusal("pi_no_extension", piNoExtension+"。这次没有发送。", nil)
	}
	params, _ := json.Marshal(map[string]any{"session_id": id, "text": text})
	_, err, _ := n.runtimeHandle(ctx, owner, "session.send", params)
	return err
}

// waitWorkAgent waits until herdr reports an agent of herdrName in pane, and answers its session id (its own when it
// names one, else the pane's terminal row).
func (n *Node) waitWorkAgent(ctx context.Context, pane, herdrName string) (string, host.Agent, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			screen, _ := n.readPane(context.Background(), pane, "recent-unwrapped", 30)
			return "", host.Agent{}, fmt.Errorf("wait for %s: %w\n%s", herdrName, ctx.Err(), lastLines(screen, 12))
		case <-ticker.C:
		}
		if err := n.refreshHosts(ctx); err != nil {
			continue
		}
		n.mu.Lock()
		var found host.Agent
		var id string
		for candidate, a := range n.agents {
			if a.PaneID == pane && a.Agent == herdrName {
				found, id = a, candidate
				break
			}
		}
		n.mu.Unlock()
		if id != "" {
			return id, found, nil
		}
	}
}

// abandonStartWait is how long each cleanup discovery window waits for herdr to report a slow-starting agent.
// It is a variable so tests can shorten the no-agent path.
var abandonStartWait = time.Second

func (n *Node) abandonStartAfterDone(ctx context.Context, created *host.Created, kind string, done func(), err error) error {
	if done != nil {
		done()
	}
	return n.abandonStart(ctx, created, kind, err)
}

// abandonStart gives a slow-starting agent a bounded chance to appear and quits its reported session through quitAgent,
// keeping the terminal for the user. A user-picked pane is left untouched because pane and kind do not prove which
// start owns its foreground process. Discovery failure or ambiguity never sends keys. After each quit, fresh discovery
// runs again so a pi that moved from its pane-derived fallback ID to its real session ID is not mistaken for an exit.
func (n *Node) abandonStart(ctx context.Context, created *host.Created, kind string, err error) error {
	if created == nil || adoptedPane(ctx) != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	pane := created.Pane.String()
	for {
		id, ambiguous, discoverErr := n.waitForAbandonedWorkAgent(cctx, pane, kind)
		if discoverErr != nil {
			log.Printf("abandon start in pane %s: discover %s: %v", pane, kind, discoverErr)
			return err
		}
		if ambiguous {
			log.Printf("abandon start in pane %s: more than one %s agent is reported; leaving them for the user", pane, kind)
			return err
		}
		if id == "" {
			return err
		}
		h := n.hosts.Get(host.ParsePane(pane).Kind)
		if h == nil {
			return err
		}
		stopped, quitErr := n.quitAgentRound(cctx, id, pane, h, true)
		if quitErr != nil {
			log.Printf("abandon start in pane %s: quit %s: %v", pane, kind, quitErr)
			return err
		}
		if !stopped {
			// The agent is still present. Only another fresh discovery may authorize another Ctrl+C pair.
			continue
		}
	}
}

// waitForAbandonedWorkAgent polls fresh host snapshots for one unambiguous work agent in pane. A stale in-memory row
// is never used as proof: only a successful refresh lets reportedWorkAgent inspect n.agents.
func (n *Node) waitForAbandonedWorkAgent(ctx context.Context, pane, kind string) (id string, ambiguous bool, refreshErr error) {
	hostKind := host.ParsePane(pane).Kind
	h := n.hosts.Get(hostKind)
	if h == nil {
		return "", false, fmt.Errorf("host %s is unavailable", hostKind)
	}
	deadline := time.Now().Add(abandonStartWait)
	for {
		if err := n.refreshHosts(ctx, h); err != nil {
			refreshErr = err
		} else {
			refreshErr = nil
			if id, ambiguous = n.reportedWorkAgent(pane, kind); id != "" || ambiguous {
				return id, ambiguous, nil
			}
		}
		if time.Now().After(deadline) {
			return "", false, refreshErr
		}
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// reportedWorkAgent returns the only matching n.agents key. It first counts the raw rows from the latest answered
// host snapshot: duplicate Cursor/agy rows collapse into one n.agents entry, so checking only the merged map would hide
// an unsafe ambiguity. A stale in-memory row alone is never used as proof.
func (n *Node) reportedWorkAgent(pane, kind string) (id string, ambiguous bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	raw := 0
	for _, a := range n.hostAgents[host.ParsePane(pane).Kind] {
		if a.PaneID == pane && a.Agent == kind {
			raw++
		}
	}
	if raw > 1 {
		return "", true
	}
	if raw == 0 {
		return "", false
	}
	for candidate, a := range n.agents {
		if a.PaneID != pane || a.Agent != kind {
			continue
		}
		if id != "" {
			return "", true
		}
		id = candidate
	}
	return id, false
}

// workAgentCalls serves a hub call for a Cursor or agy session herdr runs. A message goes through herdr's
// agent.prompt (never typed blind), and Persona interrupts by sending Esc through herdr. Agy reads the
// verified pane conversation when available. A page for Cursor is a terminal notice. The pane's own calls
// (keys, screen, close) fall through to the node.
func (n *Node) workAgentCalls(ctx context.Context, method, id string, a host.Agent, params json.RawMessage) (any, error, bool) {
	if a.Agent == agyAgent {
		if res, err, handled := n.agyPaneCall(ctx, method, id, a.PaneID, params); handled {
			return res, err, true
		}
	}
	switch method {
	case "session.items":
		notice := fmt.Sprintf("Persona 还不读 %s 的对话：手机上能发消息、看终端。", a.Agent)
		return map[string]any{"items": []proto.Item{{ID: a.Agent + "#0", Seq: 1, Role: "system", Kind: "notice", Text: notice,
			TS: time.Now().UTC()}}, "has_more": false, "history": a.Agent + "-pane:" + id, "to_seq": 1}, nil, true
	case "session.item", "session.image", "session.visualization":
		return nil, fmt.Errorf("no item"), true
	case "session.watch", "session.unwatch":
		return okResult(nil), nil, true
	case "session.send":
		var p struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err, true
		}
		// After the check of a start's first message (workAgentFirst): a message typed meanwhile could meet the first
		// one typed again.
		unlock := n.typing.lock(id)
		defer unlock()
		if err := n.hostPrompt(ctx, a.PaneID, p.Text); err != nil {
			return nil, fmt.Errorf("send: %w", err), true
		}
		n.lostFirst.clear(id) // the message the start could not deliver went in now, or another one
		return okResult(nil), nil, true
	case "session.interrupt":
		return okResult(n.sendKeys(ctx, a.PaneID, "escape")), nil, true
	default:
		return nil, nil, false
	}
}
