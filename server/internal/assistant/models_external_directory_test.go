package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestExternalDirectoryIsMainChatOnlyAndMachineLocal(t *testing.T) {
	s, f, l := externalPickFixture(t)
	other := externalTestLauncher()
	other.External.Name, other.External.Revision = "Other computer connection", "other-recipe"
	other.External.Models = []proto.AgentModel{{ID: "vendor/other-model", Default: true}}
	f.mu.Lock()
	f.devices = []Device{{ID: "server", Name: "First computer", Online: true, ExternalInventory: true}, {ID: "other", Name: "Second computer", Online: true, ExternalInventory: true}}
	f.zshrc = map[string][]proto.Launcher{"server": {{Name: "claude"}, l}, "other": {{Name: "claude"}, other}}
	f.sessions["other"] = nil
	// Hub-configured wrappers cannot overwrite a local adapter's identity or recipe.
	f.hello.Models.Launchers = map[string][]ConfiguredLauncher{
		"server": {{Launcher: proto.Launcher{Name: l.Name, Model: "forged-model", BaseURL: "https://example.invalid"}, AgentType: "codex"}},
	}
	f.mu.Unlock()
	for _, tc := range []struct {
		machine string
		want    proto.Launcher
	}{{"server", l}, {"other", other}} {
		r := request(s, "GET", "/api/v1/machines/"+tc.machine+"/launchers?purpose=assistant", "")
		var out struct {
			Launchers []launcherWithHealth `json:"launchers"`
		}
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &out) != nil {
			t.Fatalf("%s directory: %d %s", tc.machine, r.Code, r.Body.String())
		}
		i := slices.IndexFunc(out.Launchers, func(x launcherWithHealth) bool { return x.Name == l.Name })
		if i < 0 {
			t.Fatalf("external connection missing from %s: %s", tc.machine, r.Body.String())
		}
		got := out.Launchers[i]
		if got.External == nil || got.External.Revision != tc.want.External.Revision || got.DisplayName != tc.want.External.Name ||
			got.AgentType != "claude" || got.Model == "forged-model" || got.BaseURL != "" || got.External.Models[0].ID != tc.want.External.Models[0].ID {
			t.Fatalf("machine-local metadata was overwritten or leaked: %+v", got)
		}
		for _, purpose := range []string{"", "agent.new", "schedule", "kb", "other"} {
			r = request(s, "GET", "/api/v1/machines/"+tc.machine+"/launchers?purpose="+purpose, "")
			if r.Code != 200 || strings.Contains(r.Body.String(), l.Name) {
				t.Fatalf("external connection entered %s's %q directory: %d %s", tc.machine, purpose, r.Code, r.Body.String())
			}
		}
	}
	for _, purpose := range modelPurposes {
		p, err := s.modelPurposeDef(t.Context(), purpose.ID)
		if err != nil {
			t.Fatal(err)
		}
		if s.modelProviderCompatible(p, ModelProvider{ID: launcherProvider(l.Name), Kind: "launcher", Agent: "claude"}, nil) {
			t.Fatalf("external connection was offered as a configurable provider for %s", p.ID)
		}
	}
	// The shared provider overview has no machine's external execution metadata to mix.
	for _, v := range s.launcherViews(t.Context()) {
		if v.Name == l.Name {
			t.Fatalf("a machine-local connection entered the global provider view: %+v", v)
		}
	}
}

func TestExternalUnavailableUsesExistingProjection(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*proto.Launcher)
	}{
		{"credentials missing", func(l *proto.Launcher) { l.External.CredentialState = "missing" }},
		{"adapter unavailable", func(l *proto.Launcher) { l.External.UnavailableCode = "adapter_not_configured" }},
		{"SDK only", func(l *proto.Launcher) { l.External.Capabilities.Runtimes = []string{"agentsdk"} }},
		{"not an assistant engine", func(l *proto.Launcher) { l.External.Engine = "another-engine" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, l := externalPickFixture(t)
			tc.edit(&l)
			setExternalAdvertisement(f, l)
			r := appRequest(t, s, "GET", "/api/v1/machines/server/launchers?purpose=assistant", "", "en")
			var out struct {
				Launchers   []ConfiguredLauncher  `json:"launchers"`
				Unavailable []unavailableLauncher `json:"unavailable"`
			}
			if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &out) != nil {
				t.Fatalf("directory: %d %s", r.Code, r.Body.String())
			}
			if slices.ContainsFunc(out.Launchers, func(x ConfiguredLauncher) bool { return x.Name == l.Name }) {
				t.Fatal("unavailable external connection stayed selectable")
			}
			i := slices.IndexFunc(out.Unavailable, func(x unavailableLauncher) bool { return x.Name == l.Name })
			if i < 0 || out.Unavailable[i].DisplayName != l.External.Name || out.Unavailable[i].Reason == "" || out.Unavailable[i].Code == "" {
				t.Fatalf("unavailable source lost its reason/name: %s", r.Body.String())
			}
		})
	}
}

func TestExternalDirectoryNameStaysTheSameWhenUnavailable(t *testing.T) {
	s, f, l := externalPickFixture(t)
	l.DisplayName = "Another display label"
	for _, available := range []bool{true, false} {
		if !available {
			external := *l.External
			external.CredentialState = "missing"
			l.External = &external
		}
		setExternalAdvertisement(f, l)
		r := appRequest(t, s, "GET", "/api/v1/machines/server/launchers?purpose=assistant&refresh=1", "", "en")
		var out struct {
			Launchers   []ConfiguredLauncher  `json:"launchers"`
			Unavailable []unavailableLauncher `json:"unavailable"`
		}
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &out) != nil {
			t.Fatalf("directory: %d %s", r.Code, r.Body.String())
		}
		name := ""
		if available {
			for _, item := range out.Launchers {
				if item.Name == l.Name {
					name = item.DisplayName
				}
			}
		} else {
			for _, item := range out.Unavailable {
				if item.Name == l.Name {
					name = item.DisplayName
				}
			}
		}
		if name != l.External.Name {
			t.Fatalf("available %v: display name %q, want %q", available, name, l.External.Name)
		}
	}
}

func TestExternalModelListKeepsDirectoryOrderWithoutGuessedModels(t *testing.T) {
	s, f, l := externalPickFixture(t)
	if err := s.putSetting(t.Context(), launchersCustomModelsKey, []customModel{{Machine: "server", Launcher: l.Name, Model: "vendor/typed-old"}}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.presets = []assistantwire.LauncherPreset{{ID: "sample/preset", Kind: "launcher", BaseURL: "https://example.invalid",
		Models: []extwire.ProviderModel{{ID: "vendor/guessed"}}}}
	f.mu.Unlock()
	r := request(s, "GET", fmt.Sprintf("/api/v1/machines/server/launchers/%s/models", l.Name), "")
	var res proto.LauncherModels
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &res) != nil || len(res.Models) != len(l.External.Models) {
		t.Fatalf("models: %d %s", r.Code, r.Body.String())
	}
	for i, model := range res.Models {
		if model.ID != l.External.Models[i].ID || model.Name != l.External.Models[i].Name || model.Default != l.External.Models[i].Default {
			t.Fatalf("external model order/identity changed: %+v", res.Models)
		}
	}
}

func TestExternalDirectoryDoesNotWaitForOrReplaceTheOrdinarySource(t *testing.T) {
	for _, shellFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("shellFails=%v", shellFails), func(t *testing.T) {
			s, f, l := externalPickFixture(t)
			ordinary := proto.Launcher{Name: "ordinary-pick", Source: "path"}
			f.mu.Lock()
			f.launcherList = append(f.launcherList, ordinary)
			f.mu.Unlock()
			if err := s.putSetting(t.Context(), launchersZshrcKey, map[string][]proto.Launcher{"server": {ordinary}}); err != nil {
				t.Fatal(err)
			}
			if r := putPick(t, s, "main", `{"launcher":"ordinary-pick"}`); r.Code != 200 {
				t.Fatal(r.Body.String())
			}
			s.zshrcLaunchersLastSeen(t.Context())
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			s.hub = externalInventoryContextHub{Hub: f, list: func(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
				var params map[string]any
				if err := json.Unmarshal(p.Params, &params); err != nil {
					return nil, err
				}
				if params["source"] == "external" {
					return json.Marshal(launcherListing{Source: "external", Launchers: []proto.Launcher{l}})
				}
				once.Do(func() { close(started) })
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				if shellFails {
					return nil, errors.New("example shell inventory failed")
				}
				return json.Marshal(launcherListing{Launchers: []proto.Launcher{ordinary}})
			}}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			// The normal HTTP route must answer before the ordinary shell is released.
			answer := make(chan string, 1)
			go func() {
				r := httptest.NewRequest("GET", "/api/v1/machines/server/launchers?purpose=assistant", nil).WithContext(ctx)
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				answer <- fmt.Sprintf("%d %s", w.Code, w.Body.String())
			}()
			select {
			case raw := <-answer:
				if !strings.HasPrefix(raw, "200 ") || !strings.Contains(raw, l.Name) || !strings.Contains(raw, ordinary.Name) {
					close(release)
					t.Fatalf("independent directory lost a source: %s", raw)
				}
			case <-time.After(time.Second):
				close(release)
				t.Fatal("the external directory waited for the ordinary login shell")
			}
			<-started
			close(release)
			waitFor(t, "the ordinary source refresh", func() bool {
				s.models.zshrcMu.Lock()
				defer s.models.zshrcMu.Unlock()
				return !s.models.zshrcRefreshing
			})
			seen, _ := s.zshrcLaunchersLastSeen(t.Context())
			for _, machine := range seen {
				if machine.MachineID != "server" {
					continue
				}
				if !slices.ContainsFunc(machine.Launchers, func(x proto.Launcher) bool { return x.Name == ordinary.Name }) ||
					!slices.ContainsFunc(machine.Launchers, func(x proto.Launcher) bool { return x.Name == l.Name }) || shellFails && machine.Sync.State != "error" {
					t.Fatalf("one source overwrote another or masked its failure: %+v", machine)
				}
			}
			params, err := send(t, s, "main", map[string]any{})
			if err != nil || params["launcher"] != ordinary.Name || params["external"] != nil {
				t.Fatalf("an external update invalidated the ordinary pick: %v %v", params, err)
			}
		})
	}
}

func TestExternalDirectoryRequiresTheEnabledNodeSignal(t *testing.T) {
	s, f, l := externalPickFixture(t)
	pickExternal(t, s, l, "vendor/model-one")
	if err := s.syncExternalDirectory(t.Context(), f.devices[0], false); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.devices[0].ExternalInventory = false
	f.mu.Unlock()
	var calls atomic.Int32
	s.hub = externalInventoryContextHub{Hub: f, list: func(_ context.Context, _ DeviceCall) (json.RawMessage, error) {
		calls.Add(1)
		return json.Marshal(launcherListing{})
	}}
	requireExternalError(t, s.sendModel(t.Context(), "server", "main", map[string]any{}, i18n.En), "disabled")
	if calls.Load() != 0 {
		t.Fatal("a stale external snapshot caused an inventory RPC on a disabled node")
	}
	if err := s.syncExternalDirectory(t.Context(), Device{ID: "server", Online: true}, true); err != nil || calls.Load() != 0 {
		t.Fatalf("disabled source refresh probed a legacy node: %v calls=%d", err, calls.Load())
	}
}

func TestExternalDirectoryFailureIsNotAHealthyEmptySnapshot(t *testing.T) {
	s, f, l := externalPickFixture(t)
	ordinary := proto.Launcher{Name: "ordinary-before", Source: "path"}
	if err := s.putSetting(t.Context(), launchersZshrcKey, map[string][]proto.Launcher{"server": {ordinary}}); err != nil {
		t.Fatal(err)
	}
	if err := s.syncExternalDirectory(t.Context(), f.devices[0], false); err != nil {
		t.Fatal(err)
	}
	f.answerNode(DeviceLaunchersList, func(_ string, _ json.RawMessage) (any, error) {
		return nil, &rpc.Error{Code: "adapter_timeout", Message: "example private adapter diagnostic"}
	})
	if err := s.syncExternalDirectory(t.Context(), f.devices[0], true); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("source error was hidden or leaked diagnostics: %v", err)
	}
	s.models.zshrcMu.Lock()
	list := slices.Clone(s.models.zshrc["server"])
	s.models.zshrcMu.Unlock()
	if !slices.ContainsFunc(list, func(x proto.Launcher) bool { return x.Name == ordinary.Name }) {
		t.Fatal("a failed external source removed the ordinary last-seen provider")
	}
	for _, item := range list {
		if item.Name == l.Name {
			if item.External == nil || item.External.UnavailableCode != "adapter_timeout" || externalLauncherError(item) == nil {
				t.Fatalf("a failed external source remained healthy: %+v", item)
			}
			return
		}
	}
	t.Fatal("the failed source lost its unavailable diagnostic row")
}

func TestExternalDirectoryRequiresASourceTaggedAnswer(t *testing.T) {
	s, f, l := externalPickFixture(t)
	pickExternal(t, s, l, "vendor/model-one")
	f.answerNode(DeviceLaunchersList, func(_ string, _ json.RawMessage) (any, error) {
		return launcherListing{Launchers: []proto.Launcher{l}}, nil
	})
	requireExternalError(t, s.sendModel(t.Context(), "server", "main", map[string]any{}, i18n.En), "protocol_unsupported")
}

func TestExternalLaunchersChangedDoesNotWaitForConversationReads(t *testing.T) {
	st := &memStore{}
	s := &Service{store: func() Store { return st }}
	// Block Store reads as a stalled tombstone read would. A machine-level
	// directory invalidation neither reads nor forwards a conversation's data.
	st.mu.Lock()
	defer st.mu.Unlock()
	s.models.zshrcRefreshing = true // the existing refresher owns the next inventory pass
	done := make(chan bool, 1)
	go func() {
		done <- s.NodeEvent("server", "launchers.changed", json.RawMessage(`{}`))
	}()
	select {
	case forwarded := <-done:
		if forwarded {
			t.Fatal("manager-owned launcher event was forwarded")
		}
	case <-time.After(time.Second):
		t.Fatal("directory invalidation waited for an unrelated conversation read")
	}
	s.models.zshrcMu.Lock()
	again := s.models.zshrcRefreshAgain
	s.models.zshrcMu.Unlock()
	if !again {
		t.Fatal("directory invalidation did not schedule another inventory pass")
	}
}

func TestExternalDirectoryInvalidationKeepsDeletedSessionEventsBlocked(t *testing.T) {
	s, _, _ := externalPickFixture(t)
	if err := s.deleteConversation(t.Context(), "server", "side"); err != nil {
		t.Fatal(err)
	}
	s.models.zshrcMu.Lock()
	s.models.zshrcRefreshing = true
	s.models.zshrcMu.Unlock()
	if s.NodeEvent("server", "launchers.changed", json.RawMessage(`{}`)) {
		t.Fatal("manager-owned launcher event was forwarded")
	}
	for _, event := range []string{"session.items", "session.delta", "session.screen"} {
		if s.NodeEvent("server", event, json.RawMessage(`{"session_id":"side","text":"Synthetic old content"}`)) {
			t.Fatalf("deleted conversation's %s was forwarded after directory invalidation", event)
		}
	}
	if !s.NodeEvent("server", "session.delta", json.RawMessage(`{"session_id":"main","text":"Synthetic current content"}`)) {
		t.Fatal("directory invalidation blocked an unrelated current conversation")
	}
}

func TestExternalLaunchersChangedRefreshesColdDirectoryWithoutBlocking(t *testing.T) {
	s, f, l := externalPickFixture(t)
	f.mu.Lock()
	f.launcherList = []proto.Launcher{{Name: "claude"}}
	f.mu.Unlock()
	_ = s.zshrcLaunchers(t.Context(), true)
	s.models.cacheMu.Lock()
	s.models.cacheAt = map[string]time.Time{"server\x00" + l.Name: time.Now(), "other\x00" + l.Name: time.Now()}
	s.models.have = map[string]map[string]bool{"server\x00" + l.Name: {"old": true}, "other\x00" + l.Name: {"keep": true}}
	s.models.cacheMu.Unlock()
	gate := make(chan struct{})
	f.mu.Lock()
	f.launcherGate = gate
	f.launcherList = append(f.launcherList, l)
	f.mu.Unlock()
	start := time.Now()
	if forwarded := s.NodeEvent("server", "launchers.changed", json.RawMessage(`{}`)); forwarded {
		t.Fatal("manager-owned launcher event was forwarded unnecessarily")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("node read loop waited for the directory: %v", elapsed)
	}
	s.models.cacheMu.Lock()
	_, invalidated := s.models.cacheAt["server\x00"+l.Name]
	_, untouched := s.models.cacheAt["other\x00"+l.Name]
	s.models.cacheMu.Unlock()
	if invalidated || !untouched {
		t.Fatal("model-cache invalidation was not machine-scoped")
	}
	// A second event arriving while the first refresh runs must schedule another pass.
	s.NodeEvent("server", "launchers.changed", json.RawMessage(`{}`))
	s.models.zshrcMu.Lock()
	again := s.models.zshrcRefreshAgain
	s.models.zshrcMu.Unlock()
	if !again {
		t.Fatal("a directory change during a refresh was dropped")
	}
	close(gate)
	waitFor(t, "the external directory refresh", func() bool {
		s.models.zshrcMu.Lock()
		defer s.models.zshrcMu.Unlock()
		return !s.models.zshrcRefreshing
	})
	seen, _ := s.zshrcLaunchersLastSeen(context.Background())
	if !slices.ContainsFunc(seen, func(x zshrcLaunchers) bool {
		return x.MachineID == "server" && slices.ContainsFunc(x.Launchers, func(y proto.Launcher) bool { return y.Name == l.Name })
	}) {
		t.Fatalf("cold source never reached the last-seen snapshot: %+v", seen)
	}
	var saved map[string][]proto.Launcher
	if ok, err := s.store().Setting(t.Context(), launchersZshrcKey, &saved); err != nil || !ok || !slices.ContainsFunc(saved["server"], func(x proto.Launcher) bool { return x.Name == l.Name }) {
		t.Fatalf("updated directory was not persisted: %v %v", saved, err)
	}
}
