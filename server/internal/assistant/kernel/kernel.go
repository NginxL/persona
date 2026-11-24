// Package kernel is the host side of the module contract (internal/modapi; docs/architecture/core-and-modules.md):
// it loads the built-in modules that are compiled in and switched on, serves modapi.Host to each, and answers the
// core's questions about what they added. The core asks the kernel, never a module's package: a module that is not
// loaded is absent (no setting, no schedule contribution, nothing).
package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"

	"github.com/nwverse/persona/server/internal/modapi"
)

var (
	builtinMu sync.Mutex
	builtin   func() []modapi.Module
)

// Provide sets where Builtin comes from. Only the composition root calls it (internal/modules/all, from its init),
// so a binary has exactly the modules that root lists and its build tags keep.
func Provide(list func() []modapi.Module) {
	builtinMu.Lock()
	defer builtinMu.Unlock()
	builtin = list
}

// Builtin are the modules compiled into this binary, in the composition root's order; none when nothing links
// internal/modules/all (a core-only test binary).
func Builtin() []modapi.Module {
	builtinMu.Lock()
	list := builtin
	builtinMu.Unlock()
	if list == nil {
		return nil
	}
	return slices.Clone(list())
}

// served are the manifest slots this host serves: a module that declares any other slot is not loaded, so nothing
// it expects goes missing quietly. The rest of modapi.Info is declared but not yet served.
func served(info modapi.Info) error {
	if info.Feature != "" && info.Feature != "module."+info.ID {
		return fmt.Errorf("feature %q must name its own module as %q", info.Feature, "module."+info.ID)
	}
	unserved := map[string][]string{"prompt_sections": info.PromptSections,
		"shell_places": info.ShellPlaces, "chat_card_kinds": info.ChatCardKinds, "composer_actions": info.ComposerActions,
		"chat_accessories": info.ChatAccessories, "db_migrations": info.DBMigrations,
		"i18n_namespaces": info.I18nNamespaces, "node_programs": info.NodePrograms, "channel_kinds": info.ChannelKinds,
		"frame_handlers": info.FrameHandlers, "push_categories": info.PushCategories}
	var names []string
	for name, list := range unserved {
		if len(list) > 0 {
			names = append(names, name)
		}
	}
	for _, s := range info.Schedules {
		if s != modapi.ScheduleFactsName {
			names = append(names, "schedules "+s)
		}
	}
	if len(names) == 0 {
		return nil
	}
	slices.Sort(names)
	return fmt.Errorf("declares slots this host does not serve yet: %v", names)
}

// Kernel is the loaded modules and what they added.
type Kernel struct {
	loaded     []modapi.Info
	skipped    map[string]string // id → why it is not loaded
	settings   map[string]modapi.Setting
	facts      *modapi.ScheduleFacts
	routes     map[string]http.HandlerFunc
	routeOrder []string
	tools      map[string]modapi.MCPTool
	toolOrder  []string
	jobs       map[string]modapi.BackgroundJob
	jobOrder   []string
	check      func(routes, tools []string) error
}

// LoadOptions are the host's load-time rules beyond the module contract.
type LoadOptions struct {
	Off  func(id string) bool
	Logf func(format string, args ...any)
	// Check validates and reserves one module's route and tool names against the host's core and previously accepted
	// modules. It runs in load order after every kernel check; a nil result commits the module without further errors.
	Check func(routes, tools []string) error
}

// New loads mods, leaving out those off says are switched off (off may be nil), those written for another API or
// not built in, those that declare a slot this host does not serve, those whose needs do not load (whatever their
// place in the list; a cycle of needs loads none of them), and those whose Register fails. A module registers after
// the modules it needs, and after those it wants that load; otherwise list order holds. An id listed again is
// ignored after its first entry. logf may be nil.
func New(mods []modapi.Module, off func(id string) bool, logf func(format string, args ...any)) *Kernel {
	return NewWithOptions(mods, LoadOptions{Off: off, Logf: logf})
}

// NewWithOptions is New with the host's core-namespace checks.
func NewWithOptions(mods []modapi.Module, o LoadOptions) *Kernel {
	off, logf := o.Off, o.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	k := &Kernel{skipped: map[string]string{}, settings: map[string]modapi.Setting{}, routes: map[string]http.HandlerFunc{}, tools: map[string]modapi.MCPTool{}, jobs: map[string]modapi.BackgroundJob{}, check: o.Check}
	skip := func(id, why string) {
		k.skipped[id] = why
		if why != "switched off" {
			logf("module %s not loaded: %s", id, why)
		}
	}
	type candidate struct {
		m    modapi.Module
		info modapi.Info
	}
	var pending []candidate
	seen := map[string]bool{}
	for _, m := range mods {
		info := m.Info()
		why := ""
		switch {
		case info.ID == "":
			logf("module without an id not loaded")
			continue
		case seen[info.ID]:
			logf("module %s listed twice: the later entry is ignored", info.ID)
			continue
		case info.API != modapi.API:
			why = fmt.Sprintf("written for module API %d, this host serves %d", info.API, modapi.API)
		case info.Origin != modapi.OriginBuiltin:
			why = fmt.Sprintf("origin %q: only built-in modules load here", info.Origin)
		case off != nil && off(info.ID):
			why = "switched off"
		}
		seen[info.ID] = true
		if why == "" {
			if err := served(info); err != nil {
				why = err.Error()
			}
		}
		if why != "" {
			skip(info.ID, why)
			continue
		}
		pending = append(pending, candidate{m, info})
	}
	waiting := func(id string) bool {
		return slices.ContainsFunc(pending, func(c candidate) bool { return c.info.ID == id })
	}
	// Each round registers the first module whose needs are loaded and whose wants are not still waiting; when none
	// is (wants in a cycle), the first whose needs are loaded. A module whose needs can no longer load is left out.
	for len(pending) > 0 {
		next := -1
		for _, strict := range []bool{true, false} {
			next = slices.IndexFunc(pending, func(c candidate) bool {
				return !slices.ContainsFunc(c.info.Needs, func(n string) bool { return !k.Loaded(n) }) &&
					(!strict || !slices.ContainsFunc(c.info.Wants, waiting))
			})
			if next >= 0 {
				break
			}
		}
		if next < 0 {
			for _, c := range pending {
				for _, n := range c.info.Needs {
					if !k.Loaded(n) {
						skip(c.info.ID, "needs "+n+", which is not loaded")
						break
					}
				}
			}
			break
		}
		c := pending[next]
		pending = slices.Delete(pending, next, next+1)
		h := &host{info: c.info, settings: map[string]modapi.Setting{}, routes: map[string]http.HandlerFunc{}, tools: map[string]modapi.MCPTool{}, jobs: map[string]modapi.BackgroundJob{}, logf: logf}
		if err := c.m.Register(h); err != nil {
			skip(c.info.ID, "register: "+err.Error())
			continue
		}
		if err := k.take(h); err != nil {
			skip(c.info.ID, err.Error())
			continue
		}
		k.loaded = append(k.loaded, c.info)
	}
	return k
}

// take keeps what h was given, all of it or nothing.
func (k *Kernel) take(h *host) error {
	for key := range h.settings {
		if _, ok := k.settings[key]; ok {
			return fmt.Errorf("setting %s is another module's", key)
		}
	}
	if h.facts != nil && k.facts != nil {
		return errors.New("another module gives the routine facts")
	}
	for pattern := range h.routes {
		if _, ok := k.routes[pattern]; ok {
			return fmt.Errorf("route %s is another module's", pattern)
		}
	}
	for name := range h.tools {
		if _, ok := k.tools[name]; ok {
			return fmt.Errorf("tool %s is another module's", name)
		}
	}
	for name := range h.jobs {
		if _, ok := k.jobs[name]; ok {
			return fmt.Errorf("background job %s is another module's", name)
		}
	}
	if k.check != nil {
		if err := k.check(h.routeOrder, h.toolOrder); err != nil {
			return err
		}
	}
	for key, s := range h.settings {
		k.settings[key] = s
	}
	if h.facts != nil {
		k.facts = h.facts
	}
	for pattern, handler := range h.routes {
		k.routes[pattern] = handler
	}
	k.routeOrder = append(k.routeOrder, h.routeOrder...)
	for name, tool := range h.tools {
		k.tools[name] = tool
	}
	k.toolOrder = append(k.toolOrder, h.toolOrder...)
	for name, job := range h.jobs {
		k.jobs[name] = job
	}
	k.jobOrder = append(k.jobOrder, h.jobOrder...)
	return nil
}

// Loaded says whether module id is loaded.
func (k *Kernel) Loaded(id string) bool {
	return k != nil && slices.ContainsFunc(k.loaded, func(i modapi.Info) bool { return i.ID == id })
}

// Modules are the loaded modules' manifests, in load order.
func (k *Kernel) Modules() []modapi.Info {
	if k == nil {
		return nil
	}
	return slices.Clone(k.loaded)
}

// Skipped is why module id, compiled in, is not loaded ("" when it is loaded or not compiled in).
func (k *Kernel) Skipped(id string) string {
	if k == nil {
		return ""
	}
	return k.skipped[id]
}

// Setting is the loaded module setting key, if any.
func (k *Kernel) Setting(key string) (modapi.Setting, bool) {
	if k == nil {
		return modapi.Setting{}, false
	}
	s, ok := k.settings[key]
	return s, ok
}

// ScheduleFacts is the loaded routine-facts contribution, if any.
func (k *Kernel) ScheduleFacts() (modapi.ScheduleFacts, bool) {
	if k == nil || k.facts == nil {
		return modapi.ScheduleFacts{}, false
	}
	return *k.facts, true
}

// HubRoutes are the loaded modules' route patterns in registration order.
func (k *Kernel) HubRoutes() []string {
	if k == nil {
		return nil
	}
	return slices.Clone(k.routeOrder)
}

// HubRoute is a loaded module's handler for pattern.
func (k *Kernel) HubRoute(pattern string) (http.HandlerFunc, bool) {
	if k == nil {
		return nil, false
	}
	handler, ok := k.routes[pattern]
	return handler, ok
}

// MCPTools are the loaded modules' tools in registration order.
func (k *Kernel) MCPTools() []modapi.MCPTool {
	if k == nil {
		return nil
	}
	out := make([]modapi.MCPTool, 0, len(k.toolOrder))
	for _, name := range k.toolOrder {
		out = append(out, k.tools[name])
	}
	return out
}

// MCPTool is a loaded module's tool by name.
func (k *Kernel) MCPTool(name string) (modapi.MCPTool, bool) {
	if k == nil {
		return modapi.MCPTool{}, false
	}
	tool, ok := k.tools[name]
	return tool, ok
}

// BackgroundJobs are the loaded modules' loops in registration order.
func (k *Kernel) BackgroundJobs() []modapi.BackgroundJob {
	if k == nil {
		return nil
	}
	out := make([]modapi.BackgroundJob, 0, len(k.jobOrder))
	for _, name := range k.jobOrder {
		out = append(out, k.jobs[name])
	}
	return out
}

// host is modapi.Host for one module while it registers: it takes only what the module's manifest declares.
type host struct {
	info       modapi.Info
	settings   map[string]modapi.Setting
	facts      *modapi.ScheduleFacts
	routes     map[string]http.HandlerFunc
	routeOrder []string
	tools      map[string]modapi.MCPTool
	toolOrder  []string
	jobs       map[string]modapi.BackgroundJob
	jobOrder   []string
	logf       func(format string, args ...any)
}

func (h *host) Logf(format string, args ...any) { h.logf(h.info.ID+": "+format, args...) }

func (h *host) AddSetting(s modapi.Setting) error {
	switch {
	case !slices.Contains(h.info.Settings, s.Key):
		return fmt.Errorf("setting %q is not in the manifest's settings", s.Key)
	case s.Value == nil || s.Check == nil || s.Describe == nil:
		return fmt.Errorf("setting %q needs Value, Check and Describe", s.Key)
	}
	if _, ok := h.settings[s.Key]; ok {
		return fmt.Errorf("setting %q added twice", s.Key)
	}
	h.settings[s.Key] = s
	return nil
}

func (h *host) AddScheduleFacts(f modapi.ScheduleFacts) error {
	switch {
	case !slices.Contains(h.info.Schedules, modapi.ScheduleFactsName):
		return fmt.Errorf("schedules %q is not in the manifest", modapi.ScheduleFactsName)
	case f.Waits == nil:
		return errors.New("schedule facts need Waits")
	case f.Setting != "" && !slices.Contains(h.info.Settings, f.Setting):
		return fmt.Errorf("schedule facts read setting %q, which is not in the manifest", f.Setting)
	case h.facts != nil:
		return errors.New("schedule facts added twice")
	}
	h.facts = &f
	return nil
}

func (h *host) AddHubRoute(pattern string, handler http.HandlerFunc) error {
	switch {
	case !slices.Contains(h.info.HubRoutes, pattern):
		return fmt.Errorf("route %q is not in the manifest's hub_routes", pattern)
	case handler == nil:
		return fmt.Errorf("route %q needs a handler", pattern)
	}
	if _, ok := h.routes[pattern]; ok {
		return fmt.Errorf("route %q added twice", pattern)
	}
	h.routes[pattern] = handler
	h.routeOrder = append(h.routeOrder, pattern)
	return nil
}

func (h *host) AddMCPTool(name, description string, schema json.RawMessage, run func(context.Context, string, string, json.RawMessage) (string, error)) error {
	switch {
	case !slices.Contains(h.info.MCPTools, name):
		return fmt.Errorf("tool %q is not in the manifest's mcp_tools", name)
	case run == nil:
		return fmt.Errorf("tool %q needs a handler", name)
	}
	if _, ok := h.tools[name]; ok {
		return fmt.Errorf("tool %q added twice", name)
	}
	h.tools[name] = modapi.MCPTool{Name: name, Description: description, InputSchema: slices.Clone(schema), Run: run}
	h.toolOrder = append(h.toolOrder, name)
	return nil
}

func (h *host) AddBackgroundJob(name string, run func(context.Context) error) error {
	switch {
	case !slices.Contains(h.info.BackgroundJobs, name):
		return fmt.Errorf("background job %q is not in the manifest's background_jobs", name)
	case run == nil:
		return fmt.Errorf("background job %q needs a run function", name)
	}
	if _, ok := h.jobs[name]; ok {
		return fmt.Errorf("background job %q added twice", name)
	}
	h.jobs[name] = modapi.BackgroundJob{Name: name, Run: run}
	h.jobOrder = append(h.jobOrder, name)
	return nil
}
