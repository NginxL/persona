package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/modapi"
	"github.com/nwverse/persona/server/internal/proto"
)

type testSlotModule struct {
	info     modapi.Info
	register func(modapi.Host) error
}

func (m testSlotModule) Info() modapi.Info { return m.info }
func (m testSlotModule) Register(h modapi.Host) error {
	if m.register == nil {
		return nil
	}
	return m.register(h)
}

func moduleTestInfo(id string) modapi.Info {
	return modapi.Info{ID: id, API: modapi.API, Origin: modapi.OriginBuiltin}
}

func TestModuleFeatureFlagsIncludeProtocolMarker(t *testing.T) {
	m := testSlotModule{info: moduleTestInfo("feature-test")}
	m.info.Feature = "module.feature-test"
	newService := func(mods []modapi.Module, off ...string) *Service {
		f := newFakeHub().withMachine()
		return New(Options{Hub: f, Store: func() Store { return &memStore{} }, Logf: testLogf(t), Modules: mods, ModulesOff: off})
	}
	for _, tc := range []struct {
		name    string
		mods    []modapi.Module
		off     []string
		feature bool
	}{
		{"loaded", []modapi.Module{m}, nil, true},
		{"off", []modapi.Module{m}, []string{"feature-test"}, false},
		{"core only", []modapi.Module{}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := newService(tc.mods, tc.off...).moduleFeatures()
			if !slices.Contains(got, "modules") {
				t.Errorf("module protocol marker is missing: %v", got)
			}
			if slices.Contains(got, "module.feature-test") != tc.feature {
				t.Errorf("module feature presence is wrong: %v", got)
			}
		})
	}
}

func TestModuleFeaturesInManagerHello(t *testing.T) {
	m := testSlotModule{info: moduleTestInfo("feature-test")}
	m.info.Feature = "module.feature-test"
	s := New(Options{Hub: newFakeHub().withMachine(), Store: func() Store { return &memStore{} }, Logf: testLogf(t), Modules: []modapi.Module{m}})
	base := NewBusHub(BusOptions{Hub: "http://127.0.0.1:1", Token: "t"})
	b := NewBusHub(BusOptions{Hub: "http://127.0.0.1:1", Token: "t", Features: s.moduleFeatures})
	baseParams, _ := base.client.HelloParams().(map[string]any)
	params, _ := b.client.HelloParams().(map[string]any)
	features, _ := params["features"].([]string)
	if !slices.Contains(features, "modules") || !slices.Contains(features, "module.feature-test") {
		t.Fatalf("manager hello features = %v", features)
	}
	before, _ := baseParams["capabilities"].([]string)
	after, _ := params["capabilities"].([]string)
	if !slices.Equal(after, before) {
		t.Fatalf("module features changed manager capabilities: before %v, after %v", before, after)
	}
}

func TestModuleRouteFollowsModulePresence(t *testing.T) {
	const pattern = "GET /api/v1/module-slot-test"
	m := testSlotModule{info: moduleTestInfo("route-test"), register: func(h modapi.Host) error {
		return h.AddHubRoute(pattern, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("module route"))
		})
	}}
	m.info.HubRoutes = []string{pattern}
	f := newFakeHub().withMachine()
	newService := func(off ...string) *Service {
		return New(Options{Hub: f, Store: func() Store { return &memStore{} }, Logf: testLogf(t), Modules: []modapi.Module{m}, ModulesOff: off})
	}
	w := httptest.NewRecorder()
	newService().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/module-slot-test", nil))
	if w.Code != http.StatusOK || w.Body.String() != "module route" {
		t.Fatalf("loaded module route = %d %q, want 200 %q", w.Code, w.Body.String(), "module route")
	}
	w = httptest.NewRecorder()
	newService("route-test").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/module-slot-test", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled module route = %d %q, want 404", w.Code, w.Body.String())
	}
}

func TestModuleToolFollowsModulePresence(t *testing.T) {
	const name = "module_echo"
	m := testSlotModule{info: moduleTestInfo("tool-test"), register: func(h modapi.Host) error {
		return h.AddMCPTool(name, "Echo the input", json.RawMessage(`{"type":"object"}`),
			func(_ context.Context, _, _ string, args json.RawMessage) (string, error) {
				return "echo:" + string(args), nil
			})
	}}
	m.info.MCPTools = []string{name}
	f := newFakeHub().withMachine()
	newService := func(off ...string) *Service {
		return New(Options{Hub: f, Store: func() Store { return &memStore{} }, Logf: testLogf(t), Modules: []modapi.Module{m}, ModulesOff: off})
	}
	ctx := context.Background()
	s := newService()
	if !slices.ContainsFunc(s.Tools(ctx), func(tool proto.Tool) bool { return tool.Name == name }) {
		t.Fatal("loaded module tool is not listed")
	}
	got, err := s.runTool(ctx, "server", "main", name, json.RawMessage(`{"x":1}`))
	if err != nil || got != `echo:{"x":1}` {
		t.Fatalf("loaded module tool = %q, %v", got, err)
	}
	off := newService("tool-test")
	if slices.ContainsFunc(off.Tools(ctx), func(tool proto.Tool) bool { return tool.Name == name }) {
		t.Fatal("disabled module tool is still listed")
	}
	if _, err := off.runTool(ctx, "server", "main", name, json.RawMessage(`{}`)); err == nil {
		t.Fatal("disabled module tool still runs")
	}
}

func TestModuleToolConflictWithCoreDropsWholeModule(t *testing.T) {
	const unique = "module_conflict_echo"
	m := testSlotModule{info: moduleTestInfo("tool-conflict")}
	m.info.MCPTools = []string{unique, "notify_user"}
	m.register = func(h modapi.Host) error {
		run := func(context.Context, string, string, json.RawMessage) (string, error) { return "module shadow", nil }
		if err := h.AddMCPTool(unique, "Unique module tool", json.RawMessage(`{"type":"object"}`), run); err != nil {
			return err
		}
		return h.AddMCPTool("notify_user", "Conflicting module tool", json.RawMessage(`{"type":"object"}`), run)
	}
	f := newFakeHub().withMachine()
	s := New(Options{Hub: f, Store: func() Store { return &memStore{} }, Logf: testLogf(t), Modules: []modapi.Module{m}})
	if s.modules.Loaded("tool-conflict") || !strings.Contains(s.modules.Skipped("tool-conflict"), "core") {
		t.Fatalf("core-conflicting module loaded %v, skipped %q", s.modules.Loaded("tool-conflict"), s.modules.Skipped("tool-conflict"))
	}
	if slices.ContainsFunc(s.Tools(context.Background()), func(tool proto.Tool) bool { return tool.Name == unique }) {
		t.Fatal("a failed module's unique tool remains listed")
	}
	_, err := s.runTool(context.Background(), "server", "main", "notify_user", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "title and body") {
		t.Fatalf("core notify_user was replaced: %v", err)
	}
}

func TestModuleRouteConflictWithCoreDropsWholeModule(t *testing.T) {
	f := newFakeHub().withMachine()
	newService := func(mods []modapi.Module) (s *Service) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("module route conflict panicked Service construction: %v", r)
			}
		}()
		return New(Options{Hub: f, Store: func() Store { return &memStore{} }, Logf: testLogf(t), Modules: mods})
	}
	core := newService([]modapi.Module{})
	if len(core.Routes()) == 0 {
		t.Fatal("core has no routes for the conflict fixture")
	}
	const uniqueRoute = "GET /api/v1/module-core-conflict-unique"
	const uniqueTool = "module_core_conflict_echo"
	m := testSlotModule{info: moduleTestInfo("route-core-conflict")}
	m.info.HubRoutes = []string{uniqueRoute, core.Routes()[0]}
	m.info.MCPTools = []string{uniqueTool}
	m.register = func(h modapi.Host) error {
		handler := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("module route")) }
		if err := h.AddHubRoute(uniqueRoute, handler); err != nil {
			return err
		}
		if err := h.AddHubRoute(core.Routes()[0], handler); err != nil {
			return err
		}
		return h.AddMCPTool(uniqueTool, "Unique module tool", json.RawMessage(`{"type":"object"}`), func(context.Context, string, string, json.RawMessage) (string, error) { return "", nil })
	}
	s := newService([]modapi.Module{m})
	if s.modules.Loaded("route-core-conflict") || !strings.Contains(s.modules.Skipped("route-core-conflict"), "route") {
		t.Fatalf("core-conflicting module loaded %v, skipped %q", s.modules.Loaded("route-core-conflict"), s.modules.Skipped("route-core-conflict"))
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/module-core-conflict-unique", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("failed module's unique route = %d, want 404", w.Code)
	}
	if slices.ContainsFunc(s.Tools(context.Background()), func(tool proto.Tool) bool { return tool.Name == uniqueTool }) {
		t.Fatal("route-conflicting module left its tool contribution")
	}
}

func TestModuleRouteSemanticConflictDropsLaterModule(t *testing.T) {
	newModule := func(id, variable string) testSlotModule {
		m := testSlotModule{info: moduleTestInfo(id)}
		pattern := "GET /api/v1/module-semantic-conflict/{" + variable + "}"
		m.info.HubRoutes = []string{pattern}
		m.register = func(h modapi.Host) error {
			return h.AddHubRoute(pattern, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(id)) })
		}
		return m
	}
	a, b := newModule("route-a", "id"), newModule("route-b", "name")
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("semantic module route conflict panicked Service construction: %v", r)
		}
	}()
	s := New(Options{Hub: newFakeHub().withMachine(), Store: func() Store { return &memStore{} }, Logf: testLogf(t), Modules: []modapi.Module{a, b}})
	if !s.modules.Loaded("route-a") || s.modules.Loaded("route-b") || !strings.Contains(s.modules.Skipped("route-b"), "route") {
		t.Fatalf("a %v, b %v, b skipped %q", s.modules.Loaded("route-a"), s.modules.Loaded("route-b"), s.modules.Skipped("route-b"))
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/module-semantic-conflict/value", nil))
	if w.Code != http.StatusOK || w.Body.String() != "route-a" {
		t.Fatalf("first module route = %d %q", w.Code, w.Body.String())
	}
}

func TestModuleSwitchSettingIsListedAndReadable(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	m := testSlotModule{info: moduleTestInfo("switch-test")}
	f := newFakeHub().withMachine()
	s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t), Modules: []modapi.Module{m}}))
	r := s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: json.RawMessage(`{"action":"list"}`)})
	if r.IsError || !strings.Contains(r.Content[0].Text, "- modules (") {
		t.Fatalf("modules switch is not listed: %q", r.Content[0].Text)
	}
	if err := st.PutSetting(ctx, modulesKey, ModuleSwitch{Disabled: []string{"switch-test"}}); err != nil {
		t.Fatal(err)
	}
	r = s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: json.RawMessage(`{"action":"get","key":"modules"}`)})
	var got ModuleSwitch
	if r.IsError || json.Unmarshal([]byte(r.Content[0].Text), &got) != nil || !slices.Equal(got.Disabled, []string{"switch-test"}) {
		t.Fatalf("stored modules switch = %q (error: %v, disabled: %v)", r.Content[0].Text, r.IsError, got.Disabled)
	}
}

func TestModuleSwitchSettingRejectsInvalidIDs(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	m := testSlotModule{info: moduleTestInfo("switch-test")}
	f := newFakeHub().withMachine()
	s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t), Modules: []modapi.Module{m}}))
	for _, tc := range []struct {
		value string
		want  string
	}{
		{`{"disabled":["missing"]}`, `unknown module "missing"`},
		{`{"disabled":["switch-test","switch-test"]}`, `duplicate module "switch-test"`},
	} {
		r := s.Tool(ctx, ToolCall{Name: "persona_settings", MachineID: "server", SessionID: "main",
			Arguments: json.RawMessage(`{"action":"set","key":"modules","value":` + tc.value + `}`)})
		if !r.IsError || !strings.Contains(r.Content[0].Text, tc.want) {
			t.Errorf("%s: result = %q (error: %v), want %q", tc.value, r.Content[0].Text, r.IsError, tc.want)
		}
		if got := len(s.pendingQuestions()); got != 0 {
			t.Errorf("%s: invalid change created %d approval cards", tc.value, got)
		}
		var stored ModuleSwitch
		if found, err := st.Setting(ctx, modulesKey, &stored); err != nil || found {
			t.Errorf("%s: invalid change wrote the settings row (found: %v, error: %v)", tc.value, found, err)
		}
	}
}

func TestModuleSwitchSettingChangeAfterApproval(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	m := testSlotModule{info: moduleTestInfo("switch-test")}
	newService := func(off ...string) *Service {
		f := newFakeHub().withMachine()
		f.hello.Locale = "en"
		return withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t), Modules: []modapi.Module{m}, ModulesOff: off}))
	}
	s := newService()
	set := func(s *Service, value string) {
		t.Helper()
		done := make(chan string, 1)
		go func() {
			r := s.Tool(ctx, ToolCall{Name: "persona_settings", MachineID: "server", SessionID: "main",
				Arguments: json.RawMessage(`{"action":"set","key":"modules","value":` + value + `}`)})
			done <- r.Content[0].Text
		}()
		card, ok := awaitCard(s)
		if !ok {
			t.Fatalf("%s: no approval card; tool returned %q", value, <-done)
		}
		if !strings.Contains(card.Summary, "next manager start") {
			t.Errorf("%s: approval card does not state when the change applies: %q", value, card.Summary)
		}
		yes := true
		if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: card.ID,
			Answer: json.RawMessage(mustMarshal(proto.Resolution{Allow: &yes}))}); err != nil {
			t.Fatal(err)
		}
		if out := resultOf(t, done, 5*time.Second); !strings.Contains(out, "next manager start") {
			t.Errorf("%s: applied result does not state when the change applies: %q", value, out)
		}
	}

	set(s, `{"disabled":["switch-test"]}`)
	if got := ReadModuleSwitch(ctx, st, t.Logf); !slices.Equal(got, []string{"switch-test"}) {
		t.Fatalf("approved disabled list = %v", got)
	}
	if !s.modules.Loaded("switch-test") {
		t.Fatal("the running manager hot-unloaded the module")
	}
	off := newService(ReadModuleSwitch(ctx, st, t.Logf)...)
	if off.modules.Loaded("switch-test") {
		t.Fatal("the next manager start did not apply the disabled list")
	}

	// A module that is off is still a valid id when the next change restores it.
	set(off, `null`)
	if got := ReadModuleSwitch(ctx, st, t.Logf); len(got) != 0 {
		t.Fatalf("null did not restore the default: %v", got)
	}
	set(s, `{"disabled":["switch-test"]}`)
	set(s, `{"disabled":[]}`)
	if got := ReadModuleSwitch(ctx, st, t.Logf); len(got) != 0 {
		t.Fatalf("an empty list did not restore the default: %v", got)
	}
}

func TestModuleBackgroundJobFollowsModulePresence(t *testing.T) {
	started := make(chan struct{}, 2)
	m := testSlotModule{info: moduleTestInfo("job-test"), register: func(h modapi.Host) error {
		return h.AddBackgroundJob("module-job", func(ctx context.Context) error {
			select {
			case started <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return ctx.Err()
		})
	}}
	m.info.BackgroundJobs = []string{"module-job"}
	f := newFakeHub().withMachine()
	newService := func(off ...string) *Service {
		return New(Options{Hub: f, Store: func() Store { return &memStore{} }, Logf: testLogf(t), Modules: []modapi.Module{m}, ModulesOff: off})
	}
	ctx, cancel := context.WithCancel(context.Background())
	go newService().Run(ctx)
	select {
	case <-started:
	case <-time.After(200 * time.Millisecond):
		cancel()
		t.Fatal("loaded module job did not start")
	}
	cancel()
	offCtx, offCancel := context.WithCancel(context.Background())
	go newService("job-test").Run(offCtx)
	select {
	case <-started:
		offCancel()
		t.Fatal("disabled module job started")
	case <-time.After(75 * time.Millisecond):
	}
	offCancel()
}

// The client-only modules (modapi.ClientModules) have no manifest on the server, so the manager announces their flags
// itself: on by default beside the marker, off when the modules setting names them, and never from the list when this
// build has a module of that id (its load state decides).
func TestClientOnlyModuleFlagsFollowTheSwitch(t *testing.T) {
	ids := modapi.ClientModules()
	if len(ids) == 0 {
		t.Skip("no client-only module is listed")
	}
	newService := func(mods []modapi.Module, off ...string) *Service {
		f := newFakeHub().withMachine()
		return New(Options{Hub: f, Store: func() Store { return &memStore{} }, Logf: testLogf(t), Modules: mods, ModulesOff: off})
	}
	got := newService([]modapi.Module{}).moduleFeatures()
	for _, id := range ids {
		if !slices.Contains(got, "module."+id) {
			t.Errorf("a core-only manager does not announce client-only module %s: %v", id, got)
		}
	}
	off := ids[0]
	got = newService([]modapi.Module{}, off).moduleFeatures()
	if slices.Contains(got, "module."+off) || !slices.Contains(got, "modules") {
		t.Errorf("switched off, %s is still announced or the marker is gone: %v", off, got)
	}
	// A built-in module of the same id, switched off or without a feature, is not announced from the list.
	same := testSlotModule{info: moduleTestInfo(off)}
	if got = newService([]modapi.Module{same}).moduleFeatures(); slices.Contains(got, "module."+off) {
		t.Errorf("a built-in module %s without a feature was announced from the client-only list: %v", off, got)
	}
}

// persona_settings accepts a client-only id in the modules switch: the change reaches the approval card and, approved,
// the stored row; the next start then leaves the flag out.
func TestModuleSwitchSettingAcceptsClientOnlyIDs(t *testing.T) {
	ids := modapi.ClientModules()
	if len(ids) == 0 {
		t.Skip("no client-only module is listed")
	}
	ctx := context.Background()
	st := &memStore{}
	f := newFakeHub().withMachine()
	s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t), Modules: []modapi.Module{}}))
	done := make(chan string, 1)
	go func() {
		r := s.Tool(ctx, ToolCall{Name: "persona_settings", MachineID: "server", SessionID: "main",
			Arguments: json.RawMessage(`{"action":"set","key":"modules","value":{"disabled":["` + ids[0] + `"]}}`)})
		done <- r.Content[0].Text
	}()
	card, ok := awaitCard(s)
	if !ok {
		t.Fatalf("switching client-only module %s off made no approval card; tool returned %q", ids[0], <-done)
	}
	yes := true
	if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: card.ID,
		Answer: json.RawMessage(mustMarshal(proto.Resolution{Allow: &yes}))}); err != nil {
		t.Fatal(err)
	}
	resultOf(t, done, 5*time.Second)
	off := ReadModuleSwitch(ctx, st, t.Logf)
	if !slices.Equal(off, []string{ids[0]}) {
		t.Fatalf("approved disabled list = %v", off)
	}
	next := New(Options{Hub: newFakeHub().withMachine(), Store: func() Store { return st }, Logf: testLogf(t), Modules: []modapi.Module{}, ModulesOff: off})
	if got := next.moduleFeatures(); slices.Contains(got, "module."+ids[0]) {
		t.Errorf("after the next start %s is still announced: %v", ids[0], got)
	}
}
