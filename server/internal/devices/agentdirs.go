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

// Hidden Agent folders (proto/agentdirs.go): per computer, the folders whose sessions stay collected but are left out
// of Agents and the assistant's view. The list sits beside the blocked Claude config folders (claudedirs.go): same
// per-machine setting, same pattern syntax, same routes' shape. The node is not told; the assistant reads the rules
// (HiddenAgentDirs) and decides what is shown, and is told after every change (Options.HiddenAgentDirsChanged).

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
		out = append(out, proto.HiddenAgentDir{Pattern: p, Enabled: r.Enabled})
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
	c.rules = slices.Clone(next)
	c.mu.Unlock()
	m.d.logf("[%s] hidden agent folders: %v", m.cfg.ID, next)
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
	rules, err := m.editHiddenAgentDirs(r.Context(), func([]proto.HiddenAgentDir) ([]proto.HiddenAgentDir, error) { return in.Rules, nil })
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
