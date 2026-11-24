package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
)

type fakeCDPBackend struct {
	gotExtID string
	gotReq   wire.BrowserRunParams
}

func (f *fakeCDPBackend) Name() string { return "cdp" }

func (f *fakeCDPBackend) Machines(context.Context) ([]wire.BrowserMachine, error) {
	return []wire.BrowserMachine{{MachineID: "cdp-box", Name: "CDP Box", Backend: "cdp", Online: true, Ready: true}}, nil
}

func (f *fakeCDPBackend) Run(_ context.Context, extID string, p wire.BrowserRunParams) (wire.BrowserRunResult, error) {
	f.gotExtID = extID
	f.gotReq = p
	return wire.BrowserRunResult{Backend: "cdp", Output: `{"ok":true}`}, nil
}

// TestExtensionBrowserGrantsAndPluggableBackends verifies:
//   - browser.machines / browser.run require the named browser.run scope (excluded from legacy full-hub grant).
//   - Built-in egoBrowserBackend lists enabled ego Macs and isolates calls to ext-<extID>-<slot>.
//   - Additional BrowserBackend implementations (such as CDP) plug in via RegisterBrowserBackend.
func TestExtensionBrowserGrantsAndPluggableBackends(t *testing.T) {
	h, _ := extensionTestHub(t)
	granted := kbExtensionSpec(t, h, "club-ext", extension.Permissions{Hub: []string{extension.ScopeBrowserRun}})
	readOnly := kbExtensionSpec(t, h, "read-ext", extension.Permissions{Hub: []string{extension.ScopeMachinesRead}})
	full := kbExtensionSpec(t, h, "full-ext", extension.Permissions{HubAPI: true})

	mac, seen := egoMac(t, h, "mac")
	if err := h.extensions.reg.SetEnabled(context.Background(), egoExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.SetConfig(context.Background(), egoExtensionID, json.RawMessage(`{"machines":["mac"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	h.refreshBrowser(context.Background(), mac)

	cdp := &fakeCDPBackend{}
	h.RegisterBrowserBackend(cdp)

	// Denied for machines.read and legacy full-hub.
	for name, fn := range map[string]func(context.Context, string, json.RawMessage) (any, error){
		"machines.read": readOnly.Handle,
		"full-hub":      full.Handle,
	} {
		_, err := fn(context.Background(), "browser.machines", json.RawMessage(`{}`))
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != extension.CodePermissionDenied || !strings.Contains(string(re.Data), `"browser.run"`) {
			t.Fatalf("%s browser.machines: got %v, want permission_denied(browser.run)", name, err)
		}
	}

	// browser.machines lists both ego and cdp backends.
	out, err := granted.Handle(context.Background(), "browser.machines", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("browser.machines: %v", err)
	}
	list := out.(wire.BrowserMachinesResult)
	if len(list.Machines) != 2 {
		t.Fatalf("machines = %+v, want 2 (ego + cdp)", list.Machines)
	}

	// browser.run on default ego backend uses unambiguous isolated session per (extID, slot).
	runOut, err := granted.Handle(context.Background(), "browser.run", json.RawMessage(`{"machine_id":"mac","slot":"pac-muse","script":"return 1"}`))
	if err != nil {
		t.Fatalf("browser.run ego: %v", err)
	}
	if runOut.(wire.BrowserRunResult).Backend != DefaultBrowserBackend {
		t.Fatalf("ego result = %+v", runOut)
	}
	wantSession := browserSessionName("club-ext", "pac-muse")
	calls := seen()
	foundIsolated := false
	for _, c := range calls {
		if c.Method == "browser.call" && c.Params["session"] == wantSession {
			foundIsolated = true
		}
	}
	if !foundIsolated {
		t.Fatalf("expected isolated %s session in node calls: %+v", wantSession, calls)
	}

	// Colliding hyphenated (extID, slot) pairs must map to distinct session names.
	collideExt := kbExtensionSpec(t, h, "club", extension.Permissions{Hub: []string{extension.ScopeBrowserRun}})
	if _, err := collideExt.Handle(context.Background(), "browser.run", json.RawMessage(`{"machine_id":"mac","slot":"ext-pac-muse","script":"return 1"}`)); err != nil {
		t.Fatalf("collide browser.run: %v", err)
	}
	otherSession := browserSessionName("club", "ext-pac-muse")
	if otherSession == wantSession {
		t.Fatalf("colliding session names: %q == %q", otherSession, wantSession)
	}

	// timeout_ms bounds and empty/null params return invalid_params consistently.
	for _, raw := range []json.RawMessage{
		nil,
		json.RawMessage(``),
		json.RawMessage(`null`),
		json.RawMessage(`{"machine_id":"mac","slot":"pac-muse","script":"return 1","timeout_ms":-1}`),
		json.RawMessage(`{"machine_id":"mac","slot":"pac-muse","script":"return 1","timeout_ms":120001}`),
	} {
		_, err := granted.Handle(context.Background(), "browser.run", raw)
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != "invalid_params" {
			t.Fatalf("browser.run(%s): got %v, want invalid_params", raw, err)
		}
	}

	// Concurrent browser.machines + fleet refresh must stay race-free under -race.
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				h.refreshBrowser(context.Background(), mac)
			}
			_, err := granted.Handle(context.Background(), "browser.machines", json.RawMessage(`{}`))
			if err != nil {
				t.Errorf("concurrent browser.machines: %v", err)
			}
		}(i)
	}
	wg.Wait()

	// browser.run on custom cdp backend dispatches to fakeCDPBackend.
	cdpOut, err := granted.Handle(context.Background(), "browser.run", json.RawMessage(`{"machine_id":"cdp-box","backend":"cdp","slot":"pac-duet","script":"return 2"}`))
	if err != nil {
		t.Fatalf("browser.run cdp: %v", err)
	}
	if cdpOut.(wire.BrowserRunResult).Backend != "cdp" || cdp.gotExtID != "club-ext" || cdp.gotReq.Slot != "pac-duet" {
		t.Fatalf("cdp dispatch = %+v, gotExt=%q req=%+v", cdpOut, cdp.gotExtID, cdp.gotReq)
	}
}
