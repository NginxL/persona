package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// scopeService has three computers: "server" (the assistant's), "mac", and the work computer "work".
func scopeService(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	s, f, _ := modelService(t)
	f.sessions["mac"], f.sessions["work"] = nil, nil
	f.hello.Assistant.WorkMachines = []string{"work"}
	f.catalogNodes = true
	return s, f
}

func addProvider(t *testing.T, s *Service, body string) LauncherView {
	t.Helper()
	r := request(s, "POST", "/api/v1/launchers", body)
	if r.Code != http.StatusOK {
		t.Fatalf("add: %d %s", r.Code, r.Body)
	}
	var out struct {
		Launcher LauncherView `json:"launcher"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Launcher
}

func pull(t *testing.T, s *Service, machine, name string, own *proto.OwnServiceState) (proto.ManagedLauncher, error) {
	t.Helper()
	params := map[string]any{"name": name}
	if own != nil {
		params["own_service"] = own
	}
	raw, _ := json.Marshal(params)
	out, handled, err := s.NodeAnswer(context.Background(), machine, NodeLaunchersPull, raw)
	if !handled {
		t.Fatal("launchers.pull not answered by the assistant")
	}
	if err != nil {
		return proto.ManagedLauncher{}, err
	}
	return out.(map[string]any)["launcher"].(proto.ManagedLauncher), nil
}

func codeOf(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

func names(list []proto.ManagedLauncher) []string {
	var out []string
	for _, l := range list {
		out = append(out, l.Name)
	}
	return out
}

// A new provider takes the new defaults; one saved before scopes reads as it worked then.
func TestProviderScopeDefaults(t *testing.T) {
	s, _ := scopeService(t)
	v := addProvider(t, s, `{"name":"fresh","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-fresh-0000"}`)
	if v.Share != shareAll || v.Delivery != proto.DeliveryOnDemand || v.OwnService != proto.OwnServiceSkip || v.LegacyScope {
		t.Fatalf("new provider's scope: %+v", v)
	}
	v = addProvider(t, s, `{"name":"assigned","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-assigned-00","assign":["agent.new"]}`)
	if v.OwnService != proto.OwnServiceUse {
		t.Fatalf("a provider assigned on adding skips computers with their own service: %+v", v)
	}
	stored := s.storedLaunchers(context.Background())
	stored = append(stored, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "old", BaseURL: "https://gw.example.com", Model: "m1"}})
	putLaunchers(t, s, stored...)
	r := request(s, "GET", "/api/v1/launchers/old", "")
	var got struct {
		Launcher LauncherView `json:"launcher"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &got)
	if !got.Launcher.LegacyScope || got.Launcher.Share != shareAll || got.Launcher.Delivery != proto.DeliveryPush || got.Launcher.OwnService != proto.OwnServiceUse {
		t.Fatalf("legacy provider's scope: %+v", got.Launcher)
	}
	// An edit without scope fields keeps it legacy; one with them makes it explicit as it read.
	if r := request(s, "PUT", "/api/v1/launchers/old", `{"base_url":"https://gw.example.com","model":"m2"}`); r.Code != 200 {
		t.Fatalf("edit: %d %s", r.Code, r.Body)
	}
	if l, _ := s.managedNamed(context.Background(), "old"); l.Share != "" {
		t.Fatalf("an edit without scope fields changed the scope: %+v", l)
	}
	if r := request(s, "PUT", "/api/v1/launchers/old", `{"base_url":"https://gw.example.com","model":"m2","delivery":"on_demand"}`); r.Code != 200 {
		t.Fatalf("edit: %d %s", r.Code, r.Body)
	}
	if l, _ := s.managedNamed(context.Background(), "old"); l.Share != shareAll || l.Delivery != proto.DeliveryOnDemand || l.OwnService != proto.OwnServiceUse {
		t.Fatalf("explicit scope: %+v", l)
	}
}

func TestProviderScopeValidation(t *testing.T) {
	s, _ := scopeService(t)
	for _, body := range []string{
		`{"name":"a","base_url":"https://gw.example.com","model":"m1","api_key":"k","share":"selected"}`,
		`{"name":"a","base_url":"https://gw.example.com","model":"m1","api_key":"k","share":"everyone"}`,
		`{"name":"a","base_url":"https://gw.example.com","model":"m1","api_key":"k","delivery":"later"}`,
		`{"name":"a","base_url":"https://gw.example.com","model":"m1","api_key":"k","own_service":"maybe"}`,
		`{"name":"a","base_url":"https://gw.example.com","model":"m1","api_key":"k","share":"selected","scope_machines":["nowhere"]}`,
		`{"name":"a","base_url":"https://gw.example.com","model":"m1","api_key":"k","machine_keys":{"nowhere":"k2"}}`,
	} {
		if r := request(s, "POST", "/api/v1/launchers", body); r.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", body, r.Code, r.Body)
		}
	}
	if len(s.storedLaunchers(context.Background())) != 0 {
		t.Fatal("a refused provider was saved")
	}
}

// Each computer is named only the providers in its scope, without their secrets; an older node gets the same with
// them; nothing reaches a computer out of scope.
func TestProviderCatalogFollowsScope(t *testing.T) {
	s, f := scopeService(t)
	addProvider(t, s, `{"name":"box","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-box-000000","share":"selected","scope_machines":["server"]}`)
	addProvider(t, s, `{"name":"shared","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-shared-0000"}`)
	addProvider(t, s, `{"name":"hubonly","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-hub-000000","share":"none"}`)
	f.mu.Lock()
	server, mac, work := f.catalog["server"], f.catalog["mac"], f.catalog["work"]
	f.mu.Unlock()
	if !slices.Equal(names(server), []string{"box", "shared"}) || !slices.Equal(names(mac), []string{"shared"}) || len(work) != 0 {
		t.Fatalf("catalogs: server %v mac %v work %v", names(server), names(mac), names(work))
	}
	for _, l := range append(server, mac...) {
		if l.APIKey != "" || !l.Withheld || l.Rev == "" {
			t.Fatalf("a catalog entry carried its secrets or no rev: %+v", l)
		}
	}
	// A node from before the catalog: launchers.managed, scoped, keys included.
	f.mu.Lock()
	f.catalogNodes = false
	f.managed = map[string][]proto.ManagedLauncher{}
	f.mu.Unlock()
	s.pushManaged(context.Background(), "")
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(names(f.managed["server"]), []string{"box", "shared"}) || !slices.Equal(names(f.managed["mac"]), []string{"shared"}) {
		t.Fatalf("older nodes: server %v mac %v", names(f.managed["server"]), names(f.managed["mac"]))
	}
	if _, ok := f.managed["work"]; !ok || len(f.managed["work"]) != 0 {
		t.Fatalf("work computer got %v", names(f.managed["work"]))
	}
	if f.managed["server"][0].APIKey != "sk-test-box-000000" {
		t.Fatal("an older node in scope did not get the key")
	}
	// The connection's configuration: the catalog with launchers.managed to fall back to.
	f.mu.Unlock()
	calls := s.managedNodeCalls(context.Background(), "mac")
	f.mu.Lock()
	if len(calls) != 1 || calls[0].Method != DeviceLaunchersCatalog || calls[0].Fallback == nil || calls[0].Fallback.Method != DeviceLaunchersManaged {
		t.Fatalf("node calls: %+v", calls)
	}
	if strings.Contains(string(calls[0].Params), "sk-test") || strings.Contains(string(calls[0].Params), "box") {
		t.Fatalf("mac's catalog: %s", calls[0].Params)
	}
}

func TestProviderPullFollowsScope(t *testing.T) {
	s, _ := scopeService(t)
	addProvider(t, s, `{"name":"box","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-box-000000","share":"selected","scope_machines":["server"]}`)
	addProvider(t, s, `{"name":"skipper","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-skip-00000"}`)
	if l, err := pull(t, s, "server", "box", nil); err != nil || l.APIKey != "sk-test-box-000000" || l.Withheld {
		t.Fatalf("in scope: %+v %v", l, err)
	}
	if _, err := pull(t, s, "mac", "box", nil); codeOf(err) != proto.CodeLauncherOutOfScope {
		t.Fatalf("out of scope: %v", err)
	}
	if _, err := pull(t, s, "work", "skipper", nil); codeOf(err) != proto.CodeLauncherOutOfScope {
		t.Fatalf("a work computer not chosen: %v", err)
	}
	if _, err := pull(t, s, "mac", "nothing", nil); codeOf(err) != proto.CodeLauncherOutOfScope {
		t.Fatalf("no such provider: %v", err)
	}
	own := &proto.OwnServiceState{Agent: "claude", Known: true, Own: true, Via: "login"}
	if _, err := pull(t, s, "mac", "skipper", own); codeOf(err) != proto.CodeLauncherOwnService {
		t.Fatalf("own service: %v", err)
	}
	if l, err := pull(t, s, "mac", "skipper", &proto.OwnServiceState{Agent: "claude", Known: true}); err != nil || l.APIKey == "" {
		t.Fatalf("no own service: %+v %v", l, err)
	}
	// A pending computer gets nothing (the devices' Answer refuses before NodeAnswer; scopeReason itself never runs there).
}

func TestProviderKeyForOneComputer(t *testing.T) {
	s, _ := scopeService(t)
	// the first provider with a key is the API purposes' automatic pick (it cannot be deleted): not this one
	addProvider(t, s, `{"name":"anchor","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-anchor-000"}`)
	addProvider(t, s, `{"name":"gw","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-main-00000","own_service":"use"}`)
	if r := request(s, "PUT", "/api/v1/launchers/gw", `{"base_url":"https://gw.example.com","model":"m1","machine_keys":{"mac":"sk-test-mac-000000"}}`); r.Code != 200 {
		t.Fatalf("set: %d %s", r.Code, r.Body)
	} else if strings.Contains(r.Body.String(), "sk-test") || !strings.Contains(r.Body.String(), `"key_machines":["mac"]`) {
		t.Fatalf("view: %s", r.Body)
	}
	if l, _ := pull(t, s, "mac", "gw", nil); l.APIKey != "sk-test-mac-000000" {
		t.Fatalf("mac's key: %q", l.APIKey)
	}
	if l, _ := pull(t, s, "server", "gw", nil); l.APIKey != "sk-test-main-00000" {
		t.Fatalf("server's key: %q", l.APIKey)
	}
	if r := request(s, "GET", "/api/v1/launchers", ""); strings.Contains(r.Body.String(), "sk-test") {
		t.Fatal("the list returned a key")
	}
	if r := request(s, "PUT", "/api/v1/launchers/gw", `{"base_url":"https://gw.example.com","model":"m1","machine_keys":{"mac":""}}`); r.Code != 200 {
		t.Fatalf("remove: %d %s", r.Code, r.Body)
	}
	if l, _ := pull(t, s, "mac", "gw", nil); l.APIKey != "sk-test-main-00000" {
		t.Fatalf("mac after removal: %q", l.APIKey)
	}
	request(s, "PUT", "/api/v1/launchers/gw", `{"base_url":"https://gw.example.com","model":"m1","machine_keys":{"mac":"sk-test-mac-000000"}}`)
	if r := request(s, "DELETE", "/api/v1/launchers/gw", ""); r.Code != 200 {
		t.Fatalf("delete: %d %s", r.Code, r.Body)
	}
	if keys, err := s.readMachineKeys(context.Background()); err != nil || len(keys) != 0 {
		t.Fatalf("a deleted provider's keys stayed: %+v %v", keys, err)
	}
}

// A computer out of scope does not list the provider, and nothing starts with it there.
func TestProviderOutOfScopeIsNotOfferedOrStarted(t *testing.T) {
	s, f := scopeService(t)
	f.launcherList = []proto.Launcher{{Name: "claude"}}
	addProvider(t, s, `{"name":"box","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-box-000000","share":"selected","scope_machines":["server"]}`)
	listed := func(machine string) bool {
		r := request(s, "GET", "/api/v1/machines/"+machine+"/launchers", "")
		if r.Code != 200 {
			t.Fatalf("%s: %d %s", machine, r.Code, r.Body)
		}
		return strings.Contains(r.Body.String(), `"name":"box"`)
	}
	if !listed("server") || listed("mac") {
		t.Fatalf("pickers: server %v mac %v", listed("server"), listed("mac"))
	}
	ctx := context.Background()
	if err := s.call(ctx, "mac", DeviceSessionStart, map[string]any{"launcher": "box", "cwd": "/tmp"}, nil); codeOf(err) != proto.CodeLauncherOutOfScope {
		t.Fatalf("start on mac: %v", err)
	}
	if err := s.call(ctx, "server", DeviceSessionStart, map[string]any{"launcher": "box", "cwd": "/tmp"}, nil); err != nil {
		t.Fatalf("start on server: %v", err)
	}
	if _, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "mac", Params: map[string]any{"launcher": "box"}}); codeOf(err) != proto.CodeLauncherOutOfScope {
		t.Fatalf("an app's start on mac: %v", err)
	}
	if r := request(s, "POST", "/api/v1/launchers/box/check?machine=mac", ""); r.Code != http.StatusConflict || !strings.Contains(r.Body.String(), proto.CodeLauncherOutOfScope) {
		t.Fatalf("availability test on mac: %d %s", r.Code, r.Body)
	}
}

// A provider that skips computers with their own service is not offered where the node says it has one.
func TestProviderSkipsComputersWithTheirOwnService(t *testing.T) {
	s, f := scopeService(t)
	f.launcherList = []proto.Launcher{{Name: "claude"}}
	f.ownServices = map[string]proto.OwnServiceState{
		"mac/claude":    {Agent: "claude", Known: true, Own: true, Via: "login"},
		"server/claude": {Agent: "claude", Known: true},
	}
	addProvider(t, s, `{"name":"fill","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-fill-00000"}`)
	if p := pickerOf(t, s, "mac", ""); slices.Contains(p.names(), "fill") {
		t.Fatalf("mac lists it: %v", p.names())
	}
	if p := pickerOf(t, s, "server", ""); !slices.Contains(p.names(), "fill") {
		t.Fatalf("server does not list it: %v", p.names())
	}
	if err := s.call(context.Background(), "mac", DeviceAssistantSend, map[string]any{"launcher": "fill", "text": "hi"}, nil); codeOf(err) != proto.CodeLauncherOwnService {
		t.Fatalf("a send on mac: %v", err)
	}
	// Still named to mac (its node decides at the pull): a start there says why rather than "no such command".
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(names(f.catalog["mac"]), []string{"fill"}) {
		t.Fatalf("mac's catalog: %v", names(f.catalog["mac"]))
	}
}

type blockedOwnServiceHub struct{ Hub }

func (h blockedOwnServiceHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method == DeviceLaunchersOwnService {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return h.Hub.DeviceCall(ctx, p)
}

func TestPickerOwnServiceKeepsTheLastAnswerWhenTimeRunsOut(t *testing.T) {
	s, f := scopeService(t)
	s.hub = blockedOwnServiceHub{Hub: f}
	previous := proto.OwnServiceState{Agent: "claude", Known: true, Own: true, Via: "login"}
	s.models.ownMu.Lock()
	s.models.own = map[string]ownSeen{"mac/claude": {st: previous, at: time.Now().Add(-2 * ownServiceFresh)}}
	s.models.ownMu.Unlock()
	for _, tc := range []struct {
		name   string
		budget time.Duration
	}{
		{name: "deadline already expired", budget: -time.Second},
		{name: "shortened probe times out", budget: 50 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(t.Context(), pickerDiscoveryDeadline{}, time.Now().Add(tc.budget))
			if got := s.ownService(ctx, "mac", "claude"); got != previous {
				t.Fatalf("own service = %+v, want last answer %+v", got, previous)
			}
			if got, fresh := s.ownServiceCached("mac", "claude"); got != previous || fresh {
				t.Fatalf("cached own service = %+v, fresh=%v, want the original stale answer", got, fresh)
			}
		})
	}
	current := proto.OwnServiceState{Agent: "claude", Known: true}
	f.mu.Lock()
	f.ownServices = map[string]proto.OwnServiceState{"mac/claude": current}
	f.mu.Unlock()
	s.hub = f
	if got := s.ownService(t.Context(), "mac", "claude"); got != current {
		t.Fatalf("own service after a successful probe = %+v, want %+v", got, current)
	}
	if got, fresh := s.ownServiceCached("mac", "claude"); got != current || !fresh {
		t.Fatalf("updated own service = %+v, fresh=%v, want the new answer", got, fresh)
	}
}

// pickerOf is GET /api/v1/machines/{machine}/launchers?purpose=purpose: what New Agent's picker lists, and what it
// shows off with why.
func pickerOf(t *testing.T, s *Service, machine, purpose string) picker {
	t.Helper()
	path := "/api/v1/machines/" + machine + "/launchers"
	if purpose != "" {
		path += "?purpose=" + purpose
	}
	r := request(s, "GET", path, "")
	if r.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, r.Code, r.Body)
	}
	var out picker
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

type picker struct {
	Launchers   []proto.Launcher      `json:"launchers"`
	Unavailable []unavailableLauncher `json:"unavailable"`
}

func (p picker) names() []string {
	var out []string
	for _, l := range p.Launchers {
		out = append(out, l.Name)
	}
	return out
}

// New Agent's picker says why a provider is missing on a computer whose harness has a service of its own (#8109), as
// the main chat's model panel does: listed off with its own-service reason there, not where it runs, and not for a
// provider out of the computer's scope (its scope is the user's choice) or one that is used everywhere.
func TestNewAgentPickerSaysWhyAProviderSkipsThisComputer(t *testing.T) {
	s, f := scopeService(t)
	f.launcherList = []proto.Launcher{{Name: "claude"}}
	f.ownServices = map[string]proto.OwnServiceState{
		"mac/claude":    {Agent: "claude", Known: true, Own: true, Via: "login"},
		"server/claude": {Agent: "claude", Known: true},
	}
	addProvider(t, s, `{"name":"fill","display_name":"DeepSeek","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-fill-00000"}`)
	addProvider(t, s, `{"name":"box","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-box-000000","share":"selected","scope_machines":["server"]}`)
	addProvider(t, s, `{"name":"everywhere","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-use-000000","own_service":"use"}`)
	addProvider(t, s, `{"kind":"api","api":"messages","name":"direct","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-api-000000","own_service":"skip"}`)
	addProvider(t, s, `{"name":"dup","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-dup-000000"}`)
	// an alias by the same name in the computers' rc files since: where the provider is left out, the alias is listed
	f.launcherList = append(f.launcherList, proto.Launcher{Name: "dup"})
	for _, purpose := range []string{"", "agent.new"} {
		mac := pickerOf(t, s, "mac", purpose)
		if slices.Contains(mac.names(), "fill") || !slices.Contains(mac.names(), "everywhere") || !slices.Contains(mac.names(), "dup") {
			t.Fatalf("%q: mac lists %v", purpose, mac.names())
		}
		if len(mac.Unavailable) != 1 {
			t.Fatalf("%q: mac's unavailable: %+v", purpose, mac.Unavailable)
		}
		u := mac.Unavailable[0]
		if u.Name != "fill" || u.DisplayName != "DeepSeek" || u.Kind != "launcher" || u.Code != proto.CodeLauncherOwnService {
			t.Fatalf("%q: mac's unavailable: %+v", purpose, u)
		}
		if !strings.Contains(u.Reason, "DeepSeek") || !strings.Contains(u.Reason, "Claude Code") {
			t.Fatalf("%q: reason %q", purpose, u.Reason)
		}
		if server := pickerOf(t, s, "server", purpose); !slices.Contains(server.names(), "fill") || len(server.Unavailable) != 0 {
			t.Fatalf("%q: server lists %v, unavailable %+v", purpose, server.names(), server.Unavailable)
		}
	}
	// Turning the rule off for it, as docs/integrations/launchers.md has the PUT: the provider as read with that field
	// changed. The field alone is not an edit (base_url and model are sent on every PUT, as on POST).
	if r := request(s, "PUT", "/api/v1/launchers/fill", `{"own_service":"use"}`); r.Code != http.StatusBadRequest {
		t.Fatalf("PUT with own_service alone: %d %s", r.Code, r.Body)
	}
	r := request(s, "GET", "/api/v1/launchers/fill", "")
	var read struct {
		Launcher map[string]any `json:"launcher"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &read); err != nil || read.Launcher == nil {
		t.Fatalf("GET: %v %s", err, r.Body)
	}
	read.Launcher["own_service"] = "use"
	body, _ := json.Marshal(read.Launcher)
	if r := request(s, "PUT", "/api/v1/launchers/fill", string(body)); r.Code != http.StatusOK {
		t.Fatalf("PUT as read with own_service use: %d %s", r.Code, r.Body)
	}
	if mac := pickerOf(t, s, "mac", ""); !slices.Contains(mac.names(), "fill") || len(mac.Unavailable) != 0 {
		t.Fatalf("after turning the rule off, mac lists %v, unavailable %+v", mac.names(), mac.Unavailable)
	}
}

// A provider whose latest check failed. Where its own-service rule leaves it out anyway, that rule is the reason the
// pickers give, the main chat's as New Agent's: with the check passing it would still not be listed. Where it would
// run, the check alone hides it and no picker calls it skipped.
func TestAFailedCheckDoesNotChangeTheOwnServiceReason(t *testing.T) {
	s, f := scopeService(t)
	f.launcherList = []proto.Launcher{{Name: "claude"}}
	f.ownServices = map[string]proto.OwnServiceState{
		"mac/claude":    {Agent: "claude", Known: true, Own: true, Via: "login"},
		"server/claude": {Agent: "claude", Known: true},
	}
	addProvider(t, s, `{"name":"fill","display_name":"DeepSeek","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-fill-00000"}`)
	failed := func(machine string) *LauncherHealth {
		return &LauncherHealth{Machine: machine, Launcher: "fill", State: healthFailing, Error: "exit status 1", CheckedAt: time.Now()}
	}
	s.health.loaded = true
	s.health.recs = map[string]*LauncherHealth{healthKey("mac", "fill"): failed("mac"), healthKey("server", "fill"): failed("server")}
	fill := func(p picker) (unavailableLauncher, bool) {
		i := slices.IndexFunc(p.Unavailable, func(u unavailableLauncher) bool { return u.Name == "fill" })
		if i < 0 {
			return unavailableLauncher{}, false
		}
		return p.Unavailable[i], true
	}
	for _, purpose := range []string{"", "agent.new", "assistant"} {
		mac := pickerOf(t, s, "mac", purpose)
		if u, ok := fill(mac); slices.Contains(mac.names(), "fill") || !ok || u.Code != proto.CodeLauncherOwnService {
			t.Fatalf("%q: mac lists %v, unavailable %+v", purpose, mac.names(), mac.Unavailable)
		}
		server := pickerOf(t, s, "server", purpose)
		if _, ok := fill(server); slices.Contains(server.names(), "fill") || ok {
			t.Fatalf("%q: server lists %v, unavailable %+v", purpose, server.names(), server.Unavailable)
		}
	}
	// The check passing again: the server lists it, the mac still says why not.
	s.health.recs = map[string]*LauncherHealth{}
	if server := pickerOf(t, s, "server", ""); !slices.Contains(server.names(), "fill") {
		t.Fatalf("server after the check passed: %v", server.names())
	}
	if _, ok := fill(pickerOf(t, s, "mac", "")); !ok {
		t.Fatal("mac stopped saying why after the check passed")
	}
}

// Model configuration says why the main chat's provider does not run on its computer.
func TestModelConfigExplainsAnOutOfScopeProvider(t *testing.T) {
	s, _ := scopeService(t)
	addProvider(t, s, `{"name":"box","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-box-000000","share":"selected","scope_machines":["mac"]}`)
	// Not one to pick for the main chat on its computer; one picked before the scope changed stays and is explained.
	if r := modelRequest(s, "PUT", "assistant", `{"revision":0,"provider":"launcher.box","model":"m1"}`); r.Code != http.StatusBadRequest {
		t.Fatalf("binding the main chat to a provider out of its computer's scope: %d %s", r.Code, r.Body)
	}
	if err := s.putSetting(context.Background(), modelRecordKey("assistant"), modelRecord{Binding: ModelBinding{Provider: "launcher.box", Model: "m1"}}); err != nil {
		t.Fatal(err)
	}
	p := purposeOf(t, s, "assistant")
	if p.MachineID != "server" || !strings.Contains(p.Status, "server") || slices.Contains(p.ProviderIDs, "launcher.box") {
		t.Fatalf("assistant purpose: machine %q status %q providers %v", p.MachineID, p.Status, p.ProviderIDs)
	}
}

// launchers.agents_default tells a node the provider and model Model Settings › Agents names, for pi (#8194): only a
// provider pi can use and the computer is in scope for, names only, never the key.
func TestAgentsDefaultForPi(t *testing.T) {
	s, _ := scopeService(t)
	addProvider(t, s, `{"name":"box","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-box-000000","share":"selected","scope_machines":["server"]}`)
	addProvider(t, s, `{"name":"gem","agent":"agy","base_url":"https://gm.example.com","model":"g1","api_key":"sk-test-gem-000000"}`)
	ask := func(machine string) map[string]any {
		t.Helper()
		out, handled, err := s.NodeAnswer(context.Background(), machine, NodeAgentsDefault, []byte(`{}`))
		if !handled || err != nil {
			t.Fatalf("not answered: %v %v", handled, err)
		}
		b, _ := json.Marshal(out)
		if strings.Contains(string(b), "sk-test") {
			t.Fatalf("a key in the answer: %s", b)
		}
		return out.(map[string]any)
	}
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.box", Model: "m2", Effort: "high"})
	if got := ask("server"); got["launcher"] != "box" || got["model"] != "m2" || got["effort"] != "high" {
		t.Fatalf("in scope: %v", got)
	}
	if got := ask("mac"); len(got) != 0 {
		t.Fatalf("out of scope: %v", got)
	}
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.gem"})
	if got := ask("server"); len(got) != 0 {
		t.Fatalf("a provider pi cannot use: %v", got)
	}
	var list struct {
		Launchers []LauncherView `json:"launchers"`
	}
	_ = json.Unmarshal(request(s, "GET", "/api/v1/launchers", "").Body.Bytes(), &list)
	pi := map[string]bool{}
	for _, l := range list.Launchers {
		pi[l.Name] = l.Pi
	}
	if !pi["box"] || pi["gem"] {
		t.Fatalf("pi in the list: %v", pi)
	}
}
