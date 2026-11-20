package devices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Agent folder rules (proto/agentdirs.go): per computer, folders whose sessions are blocked (not collected) or hidden
// (collected, left out of Agents and the assistant's view). The list sits beside the blocked Claude config folders
// (claudedirs.go): same per-machine setting, same pattern syntax, same routes' shape. The node is told the blocking
// patterns (node.configure agent_dirs_blocked) and keeps those sessions out itself; the assistant reads every rule
// (HiddenAgentDirs), leaves blocked and hidden sessions out of what it shows (so an older node's blocked sessions are
// only hidden), and is told after every change (Options.HiddenAgentDirsChanged).

func hiddenAgentDirsKey(machine string) string { return "agent_dirs_hidden/" + machine }

// hiddenAgentDirsRetry is how long a failed read is answered from the failure before the store is asked again: the
// assistant reads the rules for every session row it shows.
const hiddenAgentDirsRetry = 5 * time.Second

// hiddenDirsCache is the machine's rules as last read or written. Only this manager writes them.
type hiddenDirsCache struct {
	mu       sync.Mutex
	loaded   bool
	rules    []proto.HiddenAgentDir
	err      error
	failedAt time.Time
}

func (m *Machine) hiddenAgentDirs(ctx context.Context) ([]proto.HiddenAgentDir, error) {
	c := &m.hiddenDirs
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := m.loadHiddenAgentDirs(ctx); err != nil {
		return nil, err
	}
	return slices.Clone(c.rules), nil
}

// loadHiddenAgentDirs fills the cache, under its lock.
func (m *Machine) loadHiddenAgentDirs(ctx context.Context) error {
	c := &m.hiddenDirs
	if c.loaded {
		return nil
	}
	if c.err != nil && time.Since(c.failedAt) < hiddenAgentDirsRetry {
		return c.err
	}
	var rules []proto.HiddenAgentDir
	if st := m.d.opts.Store; st != nil {
		if _, err := st.Setting(ctx, hiddenAgentDirsKey(m.cfg.ID), &rules); err != nil {
			c.err, c.failedAt = fmt.Errorf("read hidden agent folders: %w", err), time.Now()
			return c.err
		}
	}
	c.loaded, c.rules, c.err = true, rules, nil
	return nil
}

// cleanHiddenAgentDirs checks and normalizes a whole list as a PUT or an edit leaves it.
func cleanHiddenAgentDirs(rules []proto.HiddenAgentDir) ([]proto.HiddenAgentDir, error) {
	if len(rules) > proto.MaxHiddenAgentDirs {
		return nil, fmt.Errorf("%w: at most %d hidden folders per computer", errBadPattern, proto.MaxHiddenAgentDirs)
	}
	out := make([]proto.HiddenAgentDir, 0, len(rules))
	for _, r := range rules {
		p, ok := proto.CleanDirPattern(r.Pattern)
		if !ok || !proto.ValidDirPattern(p) {
			return nil, fmt.Errorf("%w %q: a path or glob starting with / or ~/, or a folder name glob starting with *", errBadPattern, r.Pattern)
		}
		if slices.ContainsFunc(out, func(o proto.HiddenAgentDir) bool { return o.Pattern == p }) {
			return nil, fmt.Errorf("%w %q: listed twice", errBadPattern, p)
		}
		action, ok := proto.CleanAgentDirAction(r.Action)
		if !ok {
			return nil, fmt.Errorf("%w %q: action %q is neither %q nor %q", errBadPattern, p, r.Action, proto.AgentDirBlock, proto.AgentDirHide)
		}
		out = append(out, proto.HiddenAgentDir{Pattern: p, Enabled: r.Enabled, Action: action})
	}
	return out, nil
}

// editHiddenAgentDirs applies edit to the machine's current rules and stores the result, one edit at a time, then
// tells the assistant. edit returning the rules unchanged writes nothing.
func (m *Machine) editHiddenAgentDirs(ctx context.Context, edit func([]proto.HiddenAgentDir) ([]proto.HiddenAgentDir, error)) ([]proto.HiddenAgentDir, error) {
	st := m.d.opts.Store
	if st == nil {
		return nil, errors.New("no database")
	}
	c := &m.hiddenDirs
	c.mu.Lock()
	if err := m.loadHiddenAgentDirs(ctx); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	next, err := edit(slices.Clone(c.rules))
	if err == nil {
		next, err = cleanHiddenAgentDirs(next)
	}
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	if slices.Equal(next, c.rules) {
		c.mu.Unlock()
		return next, nil
	}
	if err := st.PutSetting(ctx, hiddenAgentDirsKey(m.cfg.ID), next); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	blockedBefore := proto.BlockedAgentDirPatterns(c.rules)
	c.rules = slices.Clone(next)
	c.mu.Unlock()
	m.d.logf("[%s] agent folder rules: %v", m.cfg.ID, next)
	// The node keeps blocked folders out itself: it is told, and lists again without them (or with them, unblocked).
	if !slices.Equal(blockedBefore, proto.BlockedAgentDirPatterns(next)) {
		go m.reconfigureClaudeDirs(ctx)
	}
	if f := m.d.opts.HiddenAgentDirsChanged; f != nil {
		f(m.cfg.ID)
	}
	return next, nil
}

// HiddenAgentDirs are machine id's hidden folder rules and its home (for "~"); an unknown machine has none.
func (d *Devices) HiddenAgentDirs(ctx context.Context, id string) ([]proto.HiddenAgentDir, string, error) {
	m := d.machineByID(id)
	if m == nil {
		return nil, "", nil
	}
	rules, err := m.hiddenAgentDirs(ctx)
	return rules, m.nodeHome(), err
}

// EditHiddenAgentDirs changes machine id's rules (editHiddenAgentDirs).
func (d *Devices) EditHiddenAgentDirs(ctx context.Context, id string, edit func([]proto.HiddenAgentDir) ([]proto.HiddenAgentDir, error)) ([]proto.HiddenAgentDir, error) {
	m := d.machineByID(id)
	if m == nil {
		return nil, errUnknownMachine
	}
	return m.editHiddenAgentDirs(ctx, edit)
}

// blockedAgentDirsParam is node.configure's agent_dirs_blocked: the patterns of the rules that block ([] when none
// do, which clears the node's list). nil (null on the wire) when the rules cannot be read: the node then keeps the
// list it has, across restarts too, rather than unblocking every folder. Once read, the rules are cached and only
// this manager changes them, so a read can only fail before the first one succeeded.
func (m *Machine) blockedAgentDirsParam(ctx context.Context) []string {
	rules, err := m.hiddenAgentDirs(ctx)
	if err != nil {
		m.d.logf("[%s] agent folder rules for the node: %v (the node keeps its blocked folders)", m.cfg.ID, err)
		return nil
	}
	return proto.BlockedAgentDirPatterns(rules)
}

func (m *Machine) hiddenAgentDirsState(rules []proto.HiddenAgentDir) proto.HiddenAgentDirs {
	if rules == nil {
		rules = []proto.HiddenAgentDir{}
	}
	return proto.HiddenAgentDirs{Rules: rules, Home: m.nodeHome()}
}

func (d *Devices) getHiddenAgentDirs(w http.ResponseWriter, r *http.Request, m *Machine) {
	rules, err := m.hiddenAgentDirs(r.Context())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, m.hiddenAgentDirsState(rules))
}

// putHiddenAgentDirs replaces the machine's whole list, like PUT claude-dirs/blocked.
func (d *Devices) putHiddenAgentDirs(w http.ResponseWriter, r *http.Request, m *Machine) {
	var in struct {
		Rules []proto.HiddenAgentDir `json:"rules"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("bad body: %w", err))
		return
	}
	rules, err := m.editHiddenAgentDirs(r.Context(), func(now []proto.HiddenAgentDir) ([]proto.HiddenAgentDir, error) {
		// An app from before blocking sends no actions: a rule it lists that exists keeps its own, so switching
		// another rule there does not turn blocks into hides. A new rule without one hides, as it did then.
		for i, rule := range in.Rules {
			if rule.Action != "" {
				continue
			}
			p, _ := proto.CleanDirPattern(rule.Pattern)
			if j := slices.IndexFunc(now, func(o proto.HiddenAgentDir) bool { return o.Pattern == p }); j >= 0 {
				in.Rules[i].Action = now[j].Action
			}
		}
		return in.Rules, nil
	})
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errBadPattern) {
			code = http.StatusBadRequest
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, m.hiddenAgentDirsState(rules))
}
