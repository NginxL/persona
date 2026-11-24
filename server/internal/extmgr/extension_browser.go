package extmgr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// DefaultBrowserBackend is the built-in ego lite backend name.
const (
	DefaultBrowserBackend = "ego"
	maxBrowserTimeoutMs   = 120000
)

var browserSlotRe = lazyre.New(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// browserSessionName maps (extID, slot) to an unambiguous browser task session name.
// Hashing extID prevents collisions when both extension IDs and slots contain hyphens.
func browserSessionName(extID, slot string) string {
	sum := sha256.Sum256([]byte(extID))
	return "ext-" + hex.EncodeToString(sum[:12]) + "-" + slot
}

// BrowserBackend is a pluggable cross-machine browser automation driver exposed to extensions granted browser.run
// (#12866). The default implementation is ego lite (egoBrowserBackend); additional backends such as CDP or Playwright
// can implement this interface and register via Manager.RegisterBrowserBackend.
type BrowserBackend interface {
	Name() string
	Machines(ctx context.Context) ([]wire.BrowserMachine, error)
	Run(ctx context.Context, extID string, p wire.BrowserRunParams) (wire.BrowserRunResult, error)
}

type browserRegistry struct {
	mu       sync.RWMutex
	backends map[string]BrowserBackend
}

func (r *browserRegistry) register(b BrowserBackend) {
	if b == nil || strings.TrimSpace(b.Name()) == "" {
		return
	}
	r.mu.Lock()
	if r.backends == nil {
		r.backends = map[string]BrowserBackend{}
	}
	r.backends[strings.ToLower(strings.TrimSpace(b.Name()))] = b
	r.mu.Unlock()
}

func (r *browserRegistry) get(name string) BrowserBackend {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.backends[strings.ToLower(strings.TrimSpace(name))]
}

func (r *browserRegistry) names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.backends))
	for k := range r.backends {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RegisterBrowserBackend registers a pluggable BrowserBackend (e.g. a future CDP backend) on the manager.
func (h *Manager) RegisterBrowserBackend(b BrowserBackend) {
	h.browserBackends.register(b)
}

func (h *Manager) ensureDefaultBrowserBackend() {
	if h.browserBackends.get(DefaultBrowserBackend) == nil {
		h.browserBackends.register(&egoBrowserBackend{h: h})
	}
}

// egoBrowserBackend drives Persona's built-in ego lite browser plugin across paired machines.
type egoBrowserBackend struct {
	h *Manager
}

func (e *egoBrowserBackend) Name() string { return DefaultBrowserBackend }

func (e *egoBrowserBackend) Machines(context.Context) ([]wire.BrowserMachine, error) {
	out := []wire.BrowserMachine{}
	if e.h == nil {
		return out, nil
	}
	for _, m := range e.h.machinesNow() {
		b := e.h.browserOf(m)
		if b == nil || !b.Enabled {
			continue
		}
		out = append(out, wire.BrowserMachine{
			MachineID: m.cfg.ID,
			Name:      m.name(),
			Backend:   DefaultBrowserBackend,
			Online:    m.isOnline(),
			Ready:     m.isOnline() && b.Ready,
		})
	}
	return out, nil
}

func (e *egoBrowserBackend) Run(ctx context.Context, extID string, p wire.BrowserRunParams) (wire.BrowserRunResult, error) {
	m, err := e.h.browserMachine(p.MachineID)
	if err != nil {
		return wire.BrowserRunResult{}, &rpc.Error{Code: "browser_unavailable", Message: err.Error()}
	}
	in := map[string]any{
		"session": browserSessionName(extID, p.Slot),
		"script":  p.Script,
	}
	if p.TimeoutMs > 0 {
		in["timeout_ms"] = p.TimeoutMs
	}
	var res proto.ComputerResult
	if err := m.callBuiltin(ctx, "browser.call", in, &res); err != nil {
		return wire.BrowserRunResult{}, &rpc.Error{Code: "browser_failed", Message: err.Error()}
	}
	out := wire.BrowserRunResult{
		Backend: DefaultBrowserBackend,
		Output:  string(res.Output),
	}
	var parsed struct {
		Error string `json:"error"`
	}
	if len(res.Output) > 0 && json.Unmarshal(res.Output, &parsed) == nil && strings.TrimSpace(parsed.Error) != "" {
		out.Error = strings.TrimSpace(parsed.Error)
	}
	return out, nil
}

// extensionBrowserCall handles browser.machines and browser.run from extension id (#12866).
func (ps *extensions) extensionBrowserCall(ctx context.Context, id, method string, params json.RawMessage) (any, error) {
	if ps.reg == nil || !ps.reg.HubScopeAllowed(id, extension.ScopeBrowserRun) {
		data, _ := json.Marshal(map[string]string{"scope": extension.ScopeBrowserRun})
		return nil, &rpc.Error{Code: extension.CodePermissionDenied, Message: method + ": requires " + extension.ScopeBrowserRun, Data: data}
	}
	if ps.h == nil {
		return nil, &rpc.Error{Code: assistantwire.CodeAssistantOffline, Message: errAssistantOffline.Error()}
	}
	ps.h.ensureDefaultBrowserBackend()
	switch method {
	case "browser.machines":
		var p wire.BrowserMachinesParams
		if len(bytes.TrimSpace(params)) > 0 {
			dec := json.NewDecoder(bytes.NewReader(params))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&p); err != nil {
				return nil, &rpc.Error{Code: "invalid_params", Message: err.Error()}
			}
		}
		filter := strings.ToLower(strings.TrimSpace(p.Backend))
		var machines []wire.BrowserMachine
		for _, name := range ps.h.browserBackends.names() {
			if filter != "" && name != filter {
				continue
			}
			b := ps.h.browserBackends.get(name)
			if b == nil {
				continue
			}
			list, err := b.Machines(ctx)
			if err != nil {
				return nil, err
			}
			machines = append(machines, list...)
		}
		if machines == nil {
			machines = []wire.BrowserMachine{}
		}
		return wire.BrowserMachinesResult{Machines: machines}, nil
	case "browser.run":
		var p wire.BrowserRunParams
		if len(bytes.TrimSpace(params)) > 0 {
			dec := json.NewDecoder(bytes.NewReader(params))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&p); err != nil {
				return nil, &rpc.Error{Code: "invalid_params", Message: err.Error()}
			}
		}
		p.MachineID = strings.TrimSpace(p.MachineID)
		p.Slot = strings.TrimSpace(p.Slot)
		if p.MachineID == "" || !browserSlotRe.MatchString(p.Slot) || strings.TrimSpace(p.Script) == "" {
			return nil, &rpc.Error{Code: "invalid_params", Message: "machine_id, valid slot ([a-z0-9_-]{1,32}) and script are required"}
		}
		if p.TimeoutMs < 0 || p.TimeoutMs > maxBrowserTimeoutMs {
			return nil, &rpc.Error{Code: "invalid_params", Message: "timeout_ms must be between 0 and 120000"}
		}
		backendName := strings.ToLower(strings.TrimSpace(p.Backend))
		if backendName == "" {
			backendName = DefaultBrowserBackend
		}
		b := ps.h.browserBackends.get(backendName)
		if b == nil {
			return nil, &rpc.Error{Code: "unknown_backend", Message: "unknown browser backend: " + backendName}
		}
		return b.Run(ctx, id, p)
	default:
		return nil, &rpc.Error{Code: extension.CodeUnknownMethod, Message: "unknown method " + method}
	}
}
