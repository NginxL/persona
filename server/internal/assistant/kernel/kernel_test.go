package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/modapi"
)

type fake struct {
	info modapi.Info
	reg  func(h modapi.Host) error
}

func (f fake) Info() modapi.Info { return f.info }
func (f fake) Register(h modapi.Host) error {
	if f.reg == nil {
		return nil
	}
	return f.reg(h)
}

type rules struct{ modapi.WaitRules }

func setting(key string) modapi.Setting {
	return modapi.Setting{Key: key, Value: func(json.RawMessage) json.RawMessage { return nil },
		Check: func(json.RawMessage) (json.RawMessage, error) { return nil, nil }, Describe: func(json.RawMessage) string { return "" }}
}

func info(id string, settings ...string) modapi.Info {
	return modapi.Info{ID: id, API: modapi.API, Origin: modapi.OriginBuiltin, Settings: settings, Schedules: []string{modapi.ScheduleFactsName}}
}

func full(id string) fake {
	return fake{info: info(id, "x."+id), reg: func(h modapi.Host) error {
		if err := h.AddSetting(setting("x." + id)); err != nil {
			return err
		}
		return h.AddScheduleFacts(modapi.ScheduleFacts{Setting: "x." + id, Waits: func(json.RawMessage) modapi.WaitRules { return rules{} }})
	}}
}

func TestLoadsAndServes(t *testing.T) {
	k := New([]modapi.Module{full("a")}, nil, t.Logf)
	if !k.Loaded("a") || len(k.Modules()) != 1 {
		t.Fatalf("not loaded: %+v", k.Modules())
	}
	if _, ok := k.Setting("x.a"); !ok {
		t.Fatal("setting not served")
	}
	if f, ok := k.ScheduleFacts(); !ok || f.Setting != "x.a" {
		t.Fatalf("facts not served: %+v", f)
	}
}

func TestFeatureSlotLoads(t *testing.T) {
	m := full("briefing")
	m.info.Feature = "module.briefing"
	k := New([]modapi.Module{m}, nil, t.Logf)
	if !k.Loaded("briefing") {
		t.Fatalf("a module with a feature flag was not loaded: %q", k.Skipped("briefing"))
	}
	if got := k.Modules()[0].Feature; got != "module.briefing" {
		t.Fatalf("loaded feature = %q, want module.briefing", got)
	}
}

func TestFeatureMustNameItsOwnModule(t *testing.T) {
	m := plainModule("briefing")
	m.info.Feature = "module.other"
	k := New([]modapi.Module{m}, nil, t.Logf)
	if k.Loaded("briefing") || !strings.Contains(k.Skipped("briefing"), "module.briefing") {
		t.Fatalf("mismatched feature loaded %v, skipped %q", k.Loaded("briefing"), k.Skipped("briefing"))
	}
}

// A module switched off, or one that cannot load, adds nothing: what it added before failing is dropped too.
func TestAbsentModulesAddNothing(t *testing.T) {
	failing := full("b")
	inner := failing.reg
	failing.reg = func(h modapi.Host) error {
		_ = inner(h)
		return errors.New("boom")
	}
	undeclared := fake{info: info("c"), reg: func(h modapi.Host) error { return h.AddSetting(setting("x.other")) }}
	unserved := full("d")
	unserved.info.PromptSections = []string{"d"}
	oldAPI := full("e")
	oldAPI.info.API = modapi.API + 1
	needy := full("f")
	needy.info.Needs = []string{"a"}
	ext := full("g")
	ext.info.Origin = modapi.OriginExtension
	k := New([]modapi.Module{full("a"), failing, undeclared, unserved, oldAPI, needy, ext}, func(id string) bool { return id == "a" }, t.Logf)
	if len(k.Modules()) != 0 {
		t.Fatalf("loaded: %+v", k.Modules())
	}
	if _, ok := k.Setting("x.a"); ok {
		t.Fatal("a switched-off module's setting is served")
	}
	if _, ok := k.Setting("x.b"); ok {
		t.Fatal("a failed module's setting is served")
	}
	if _, ok := k.ScheduleFacts(); ok {
		t.Fatal("schedule facts served with no module loaded")
	}
	for id, want := range map[string]string{"a": "switched off", "b": "register: boom", "c": "not in the manifest", "d": "prompt_sections",
		"e": "module API", "f": "needs a", "g": "only built-in"} {
		if got := k.Skipped(id); !strings.Contains(got, want) {
			t.Errorf("skipped %s = %q, want %q", id, got, want)
		}
	}
}

type serverSlot struct {
	name    string
	declare func(*modapi.Info, string)
	add     func(modapi.Host, string) error
	present func(*Kernel, string) bool
}

func serverSlots() []serverSlot {
	return []serverSlot{
		{"route", func(i *modapi.Info, name string) { i.HubRoutes = []string{name} }, func(h modapi.Host, name string) error {
			return h.AddHubRoute(name, func(http.ResponseWriter, *http.Request) {})
		}, func(k *Kernel, name string) bool { _, ok := k.HubRoute(name); return ok }},
		{"tool", func(i *modapi.Info, name string) { i.MCPTools = []string{name} }, func(h modapi.Host, name string) error {
			return h.AddMCPTool(name, "test", json.RawMessage(`{"type":"object"}`), func(context.Context, string, string, json.RawMessage) (string, error) { return "", nil })
		}, func(k *Kernel, name string) bool { _, ok := k.MCPTool(name); return ok }},
		{"job", func(i *modapi.Info, name string) { i.BackgroundJobs = []string{name} }, func(h modapi.Host, name string) error {
			return h.AddBackgroundJob(name, func(context.Context) error { return nil })
		}, func(k *Kernel, name string) bool {
			return slices.ContainsFunc(k.BackgroundJobs(), func(j modapi.BackgroundJob) bool { return j.Name == name })
		}},
	}
}

func plainModule(id string) fake {
	return fake{info: modapi.Info{ID: id, API: modapi.API, Origin: modapi.OriginBuiltin}}
}

func serverSlotCount(k *Kernel) int {
	return len(k.HubRoutes()) + len(k.MCPTools()) + len(k.BackgroundJobs())
}

func TestServerSlotsRejectUndeclaredContributions(t *testing.T) {
	for _, slot := range serverSlots() {
		t.Run(slot.name, func(t *testing.T) {
			m := plainModule("undeclared")
			m.reg = func(h modapi.Host) error { return slot.add(h, "contribution") }
			k := New([]modapi.Module{m}, nil, t.Logf)
			if k.Loaded(m.info.ID) || !strings.Contains(k.Skipped(m.info.ID), "not in the manifest") || serverSlotCount(k) != 0 {
				t.Fatalf("loaded %v, skipped %q, contributions %d", k.Loaded(m.info.ID), k.Skipped(m.info.ID), serverSlotCount(k))
			}
		})
	}
}

func TestServerSlotsRejectDuplicatesWithinModule(t *testing.T) {
	for _, slot := range serverSlots() {
		t.Run(slot.name, func(t *testing.T) {
			m := plainModule("duplicate")
			slot.declare(&m.info, "contribution")
			m.reg = func(h modapi.Host) error {
				if err := slot.add(h, "contribution"); err != nil {
					return err
				}
				return slot.add(h, "contribution")
			}
			k := New([]modapi.Module{m}, nil, t.Logf)
			if k.Loaded(m.info.ID) || !strings.Contains(k.Skipped(m.info.ID), "added twice") || serverSlotCount(k) != 0 {
				t.Fatalf("loaded %v, skipped %q, contributions %d", k.Loaded(m.info.ID), k.Skipped(m.info.ID), serverSlotCount(k))
			}
		})
	}
}

func TestServerSlotConflictDropsWholeModule(t *testing.T) {
	for _, conflict := range serverSlots() {
		t.Run(conflict.name, func(t *testing.T) {
			a := plainModule("a")
			conflict.declare(&a.info, "shared")
			a.reg = func(h modapi.Host) error { return conflict.add(h, "shared") }
			b := plainModule("b")
			conflict.declare(&b.info, "shared")
			var unique []struct {
				slot serverSlot
				name string
			}
			for _, slot := range serverSlots() {
				if slot.name == conflict.name {
					continue
				}
				name := "b-" + slot.name
				slot.declare(&b.info, name)
				unique = append(unique, struct {
					slot serverSlot
					name string
				}{slot, name})
			}
			b.reg = func(h modapi.Host) error {
				for _, contribution := range unique {
					if err := contribution.slot.add(h, contribution.name); err != nil {
						return err
					}
				}
				return conflict.add(h, "shared")
			}
			k := New([]modapi.Module{a, b}, nil, t.Logf)
			if !k.Loaded("a") || k.Loaded("b") || !strings.Contains(k.Skipped("b"), "another module's") {
				t.Fatalf("a %v, b %v, skipped %q", k.Loaded("a"), k.Loaded("b"), k.Skipped("b"))
			}
			if !conflict.present(k, "shared") {
				t.Error("the first module's conflicting contribution was lost")
			}
			for _, contribution := range unique {
				if contribution.slot.present(k, contribution.name) {
					t.Errorf("failed module left %s %q", contribution.slot.name, contribution.name)
				}
			}
			if got := serverSlotCount(k); got != 1 {
				t.Errorf("server slot count = %d, want only the first module's contribution", got)
			}
		})
	}
}

func TestFailedModuleDropsEveryServerSlot(t *testing.T) {
	m := plainModule("failed")
	slots := serverSlots()
	for i, slot := range slots {
		slot.declare(&m.info, string(rune('a'+i)))
	}
	m.reg = func(h modapi.Host) error {
		for i, slot := range slots {
			if err := slot.add(h, string(rune('a'+i))); err != nil {
				return err
			}
		}
		return errors.New("boom")
	}
	k := New([]modapi.Module{m}, nil, t.Logf)
	if k.Loaded("failed") || serverSlotCount(k) != 0 {
		t.Fatalf("loaded %v with %d contributions after register failed", k.Loaded("failed"), serverSlotCount(k))
	}
}

func TestTwoModulesCannotShareASlot(t *testing.T) {
	second := full("b")
	second.info.Settings = []string{"x.a"}
	second.reg = full("a").reg
	k := New([]modapi.Module{full("a"), second}, nil, t.Logf)
	if !k.Loaded("a") || k.Loaded("b") {
		t.Fatalf("loaded %+v", k.Modules())
	}
}

func TestNilKernelHasNothing(t *testing.T) {
	var k *Kernel
	if k.Loaded("a") || k.Modules() != nil || k.Skipped("a") != "" {
		t.Fatal("a nil kernel answered")
	}
	if _, ok := k.Setting("x"); ok {
		t.Fatal("a nil kernel served a setting")
	}
	if _, ok := k.ScheduleFacts(); ok {
		t.Fatal("a nil kernel served facts")
	}
}

// A module listed before what it needs still loads, after it; a cycle of needs loads neither; a module that wants
// another registers after it when it loads, and loads anyway when it does not.
func TestNeedsAndWantsIgnoreListOrder(t *testing.T) {
	var order []string
	track := func(f fake) fake {
		inner := f.reg
		f.reg = func(h modapi.Host) error {
			order = append(order, f.info.ID)
			if inner == nil {
				return nil
			}
			return inner(h)
		}
		return f
	}
	plain := func(id string) fake {
		return fake{info: modapi.Info{ID: id, API: modapi.API, Origin: modapi.OriginBuiltin}}
	}
	a, b, c, d, e, w := plain("a"), plain("b"), plain("c"), plain("d"), plain("e"), plain("w")
	a.info.Needs = []string{"b"}
	c.info.Needs = []string{"d"}
	d.info.Needs = []string{"c"}
	w.info.Wants = []string{"e", "missing"}
	k := New([]modapi.Module{track(a), track(w), track(b), track(c), track(d), track(e)}, nil, t.Logf)
	if want := []string{"b", "a", "e", "w"}; !slices.Equal(order, want) {
		t.Fatalf("registered %v, want %v", order, want)
	}
	for _, id := range []string{"c", "d"} {
		if k.Loaded(id) || !strings.Contains(k.Skipped(id), "needs") {
			t.Errorf("%s in a cycle of needs: loaded %v, skipped %q", id, k.Loaded(id), k.Skipped(id))
		}
	}
}

// A second entry with a loaded module's id is ignored: Loaded and Skipped agree.
func TestDuplicateIDKeepsTheFirst(t *testing.T) {
	k := New([]modapi.Module{full("a"), full("a")}, nil, t.Logf)
	if !k.Loaded("a") || k.Skipped("a") != "" || len(k.Modules()) != 1 {
		t.Fatalf("loaded %+v, skipped %q", k.Modules(), k.Skipped("a"))
	}
}
