package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Agent folder rules: per computer, folders whose sessions are blocked (the node collects nothing of them, #10053) or
// hidden (collected, left out of Agents and the assistant's view: agents_list, live snapshots, tasks, pushes). The rules
// live with the computer beside its blocked Claude config folders (internal/devices agentdirs.go); this file decides
// what the assistant leaves out: every rule switched on, blocking ones included, so the sessions of a node that does
// not block yet are hidden at least. The authoritative pending interaction inventory is never filtered by them
// (pendingInteractions): hiding is a view, not a lifecycle change.

const hiddenAgentDirsKey = "agents.hidden_dirs"

// AgentDirs is where each computer's hidden Agent folders are kept (internal/devices.Devices).
type AgentDirs interface {
	// HiddenAgentDirs are machine id's rules and its home ("" before its node said); an unknown machine has none.
	HiddenAgentDirs(ctx context.Context, id string) ([]proto.HiddenAgentDir, string, error)
	// EditHiddenAgentDirs replaces machine id's rules by what edit makes of the current ones, one edit at a time.
	EditHiddenAgentDirs(ctx context.Context, id string, edit func([]proto.HiddenAgentDir) ([]proto.HiddenAgentDir, error)) ([]proto.HiddenAgentDir, error)
}

// HiddenAgentDirsChanged shows the machine's sessions again by its new rules (devices.Options.HiddenAgentDirsChanged):
// session rows reach the clients before their pending cards are replayed, so a card of a session shown again finds
// its row.
func (s *Service) HiddenAgentDirsChanged(machine string) {
	s.snapshotStale()
	s.sessionsChanged(machine)
	s.refreshPendingCards()
	s.boardChanged()
}

// Active supervised tasks stay visible: hiding ambient activity must never sever
// a task's supervision. Merely having been started by Persona is not an exemption.
func (s *Service) hiddenByDirectory(machine string, sess proto.Session) bool {
	if sess.Assistant || s.agentDirs == nil {
		return false
	}
	// A failed read (retried by the devices after a few seconds) hides nothing: the rules are off by default, and
	// hiding is a view, so a database hiccup must not take every session of the computer out of the lists. Once read,
	// the rules stay cached (only this manager writes them), so this is only before the first successful read.
	rules, home, err := s.agentDirs.HiddenAgentDirs(context.Background(), machine)
	patterns := proto.HiddenAgentDirPatterns(rules)
	if err != nil || len(patterns) == 0 {
		return false
	}
	m, _ := s.fleetMachine(machine)
	if m.isAssistant(sess) {
		return false
	}
	if home == "" {
		home = m.Home
	}
	matched := proto.DirPatternMatchedOn(sess.Cwd, home, m.HomeReal, patterns) != ""
	return matched && !s.directorySessionSupervised(machine, sess.SessionID)
}

func (s *Service) directoryHiddenSession(machine, sid string) bool {
	sess, ok := s.nodeSession(machine, sid)
	return ok && s.hiddenByDirectory(machine, sess)
}

func visibleDirectorySessions(list []proto.Session) []proto.Session {
	return slices.DeleteFunc(slices.Clone(list), func(c proto.Session) bool { return c.HiddenByDirectory() })
}

// Read persisted supervision before deciding visibility; publishing its links is
// queued outside decoration to avoid reentering the session-list pipeline.
func (s *Service) directorySessionSupervised(machine, sid string) bool {
	loaded, err := s.loadTodoInventory(context.Background())
	if err != nil {
		s.logf("directory visibility: supervision unavailable: %v", err)
		return true
	}
	if loaded {
		s.inOrder(func() { s.syncLinkedTodoTokens(); s.sweepSourceReports() })
	}
	key := [2]string{s.canonical(machine), sid}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	t := s.todos.todos[s.todos.activeBySession[key]]
	return t != nil && t.DoneAt == nil && t.Supervision != nil && t.Supervision.Supervisor != ""
}

// hiddenDirSetting is persona_settings agents.hidden_dirs, the same get / add / remove as
// agents.blocked_claude_dirs beside it; add also switches an existing rule on or off.
func (s *Service) hiddenDirSetting() ownSetting {
	return ownSetting{Setting: Setting{Key: hiddenAgentDirsKey, Title: i18n.T(i18n.Default, "assistant-hidden_dirs.title"), Actions: []string{"get", "add", "remove"},
		About: "per machine, folders whose Agent sessions (every runtime) Persona blocks or hides. block (the default for a new rule): " +
			"the computer collects nothing of them, so no app, list, count, notification, Inbox item or your view has them, and their " +
			"permission / question requests are left to the agent's own interface. hide: they stay collected but are left out of Agents and your view " +
			"(agents_list, snapshots, tasks, notifications); active supervised tasks stay visible. A rule switched off keeps its place and does nothing. " +
			"get lists each machine's rules, their action, and how many of its listed sessions each matches now (a blocking rule's are 0 " +
			"once that computer's node keeps them out; an older node's are only hidden)",
		Value: `add: {"machine": machine id, "pattern": a path or glob starting with / or ~/ (~ is that machine's home), or a folder name glob ` +
			`starting with *, e.g. "~/.automation/workspaces/*", "action": "block" (default for a new rule) or "hide" (an existing rule keeps ` +
			`its action unless given), "enabled": true (default) or false to switch an existing rule off}; remove: "<machine id>:<pattern>"`},
		get: s.getHiddenAgentDirs, change: s.changeHiddenAgentDirs}
}

func (s *Service) getHiddenAgentDirs(ctx context.Context) (string, error) {
	if s.agentDirs == nil {
		return "No machines.", nil
	}
	var b strings.Builder
	for _, m := range s.fleetMachines() {
		rules, home, err := s.agentDirs.HiddenAgentDirs(ctx, m.ID)
		if err != nil {
			return "", err
		}
		if home == "" {
			home = m.Home
		}
		fmt.Fprintf(&b, "%s (%s):", m.ID, m.Name)
		if len(rules) == 0 {
			b.WriteString(" no hidden folders\n")
			continue
		}
		b.WriteByte('\n')
		var sessions []proto.Session
		if s.devs != nil {
			sessions, _ = s.devs.Sessions(m.ID)
		}
		for _, r := range rules {
			if !r.Enabled {
				fmt.Fprintf(&b, "  - %s (%s, off)\n", r.Pattern, r.Action)
				continue
			}
			n := 0
			for _, sess := range sessions {
				// What the rule hides now: assistant conversations and active supervised tasks are exempt.
				if proto.DirPatternMatchedOn(sess.Cwd, home, m.HomeReal, []string{r.Pattern}) != "" && s.hiddenByDirectory(m.ID, sess) {
					n++
				}
			}
			fmt.Fprintf(&b, "  - %s (%s, on; matches %d sessions)\n", r.Pattern, r.Action, n)
		}
	}
	if b.Len() == 0 {
		return "No machines.", nil
	}
	return b.String(), nil
}

func (s *Service) changeHiddenAgentDirs(ctx context.Context, action string, v json.RawMessage) (*ownChange, error) {
	if s.agentDirs == nil {
		return nil, errors.New("hidden Agent folders are kept with the computers, and this manager carries none")
	}
	var machine, pattern, ruleAction string // ruleAction: "" leaves an existing rule's, a new one blocks
	enabled := true
	switch action {
	case "add":
		var a struct {
			Machine string `json:"machine"`
			Pattern string `json:"pattern"`
			Enabled *bool  `json:"enabled"`
			Action  string `json:"action"`
		}
		if err := json.Unmarshal(v, &a); err != nil {
			return nil, errors.New(`value must be {"machine", "pattern", "action"?, "enabled"?}`)
		}
		machine, pattern, ruleAction = a.Machine, a.Pattern, strings.TrimSpace(a.Action)
		if ruleAction != "" && ruleAction != proto.AgentDirBlock && ruleAction != proto.AgentDirHide {
			return nil, fmt.Errorf(`action must be %q or %q, not %q`, proto.AgentDirBlock, proto.AgentDirHide, a.Action)
		}
		if a.Enabled != nil {
			enabled = *a.Enabled
		}
	case "remove":
		var id string
		if err := json.Unmarshal(v, &id); err != nil {
			return nil, errors.New(`remove takes "<machine id>:<pattern>"`)
		}
		var ok bool
		if machine, pattern, ok = strings.Cut(id, ":"); !ok {
			return nil, fmt.Errorf(`remove takes "<machine id>:<pattern>", got %q`, id)
		}
	}
	m, ok := s.fleetMachine(strings.TrimSpace(machine))
	if !ok {
		return nil, fmt.Errorf("no machine %q", machine)
	}
	p, ok := proto.CleanDirPattern(pattern)
	if !ok || !proto.ValidDirPattern(p) {
		return nil, fmt.Errorf("bad pattern %q: a path or glob starting with / or ~/, or a folder name glob starting with *", pattern)
	}
	rules, _, err := s.agentDirs.HiddenAgentDirs(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	at := slices.IndexFunc(rules, func(r proto.HiddenAgentDir) bool { return r.Pattern == p })
	name := m.Name
	if name == "" {
		name = m.ID
	}
	args := i18n.Args{"machine": name, "path": p}
	var key string
	var edit func([]proto.HiddenAgentDir) ([]proto.HiddenAgentDir, error)
	want := ruleAction
	switch {
	case want != "":
	case at >= 0:
		want = rules[at].Action
	default:
		want = proto.AgentDirBlock
	}
	switch {
	case action == "remove" && at < 0:
		return nil, fmt.Errorf("%s is not an Agent folder rule on %s (get lists the rules)", p, m.ID)
	case action == "remove":
		key = "remove"
		edit = func(now []proto.HiddenAgentDir) ([]proto.HiddenAgentDir, error) {
			return slices.DeleteFunc(now, func(r proto.HiddenAgentDir) bool { return r.Pattern == p }), nil
		}
	case at >= 0 && rules[at].Enabled == enabled && rules[at].Action == want:
		return &ownChange{summary: fmt.Sprintf("%s is already an Agent folder rule on %s: %s, %s.", p, m.ID, want, map[bool]string{true: "on", false: "off"}[enabled])}, nil
	default:
		switch {
		case !enabled && at < 0:
			key = "add_off"
		case at < 0:
			key = want // "block" or "hide"
		case rules[at].Action != want:
			key = "to_" + want // the action switched, the rule on or off as asked
		default:
			key = map[bool]string{true: "switch_on", false: "switch_off"}[enabled]
		}
		edit = func(now []proto.HiddenAgentDir) ([]proto.HiddenAgentDir, error) {
			if i := slices.IndexFunc(now, func(r proto.HiddenAgentDir) bool { return r.Pattern == p }); i >= 0 {
				now[i].Enabled, now[i].Action = enabled, want
				return now, nil
			}
			return append(now, proto.HiddenAgentDir{Pattern: p, Enabled: enabled, Action: want}), nil
		}
	}
	return &ownChange{summary: "Agent folder rule " + p + " on " + m.ID + ": " + key,
		cardText: func(l i18n.Lang) string { return i18n.T(l, "assistant-hidden_dirs."+key, args) },
		apply: func(ctx context.Context) (string, error) {
			// The edit is made on the rules as they are then, so a change made meanwhile is kept.
			if _, err := s.agentDirs.EditHiddenAgentDirs(ctx, m.ID, edit); err != nil {
				return "", err
			}
			return "Done (" + key + " " + p + " on " + m.ID + "). The same action with the opposite switch, or remove, undoes it.", nil
		}}, nil
}
