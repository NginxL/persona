package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// machines.pair_link and machines.status (#12798):
//   - machines.pair_link requires the named machines.pair scope (legacy full-hub grant does NOT include it).
//   - machines.status accepts machines.read or machines.pair (or legacy full-hub grant).
func TestExtensionMachinesPairAndStatusGrants(t *testing.T) {
	h := newTestManager(t)
	pairExt := kbExtensionSpec(t, h, "example-pair", extension.Permissions{Hub: []string{extension.ScopeMachinesPair}})
	readExt := kbExtensionSpec(t, h, "example-read", extension.Permissions{Hub: []string{extension.ScopeMachinesRead}})
	kbOnly := kbExtensionSpec(t, h, "example-kb", extension.Permissions{Hub: []string{extension.ScopeKBRead}})
	full := kbExtensionSpec(t, h, "example-full", extension.Permissions{HubAPI: true})

	var mu sync.Mutex
	var gotMethods []string
	th := hubOf(h)
	th.mu.Lock()
	th.app = func(method string, _ json.RawMessage) (any, error) {
		mu.Lock()
		gotMethods = append(gotMethods, method)
		mu.Unlock()
		switch method {
		case extwire.HubMachinesPairLink:
			return json.RawMessage(`{"machine_id":"muse","name":"Muse","link":"persona://link?token=t&machine=muse","expires_at":"2026-11-22T00:00:00Z","pending":true}`), nil
		case extwire.HubMachinesStatus:
			return json.RawMessage(`{"machine_id":"muse","name":"Muse","exists":true,"online":false,"node_online":false,"pending":true}`), nil
		}
		return map[string]any{}, nil
	}
	th.mu.Unlock()

	pairParams := json.RawMessage(`{"name":"Muse","os":"linux"}`)
	statusParams := json.RawMessage(`{"machine_id":"muse"}`)

	// machines.pair_link denied for kb.read, machines.read, and legacy full-hub.
	for name, fn := range map[string]func(context.Context, string, json.RawMessage) (any, error){
		"kb.read":       kbOnly.Handle,
		"machines.read": readExt.Handle,
		"full-hub":      full.Handle,
	} {
		_, err := fn(context.Background(), "machines.pair_link", pairParams)
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != extension.CodePermissionDenied || !strings.Contains(string(re.Data), `"machines.pair"`) {
			t.Fatalf("%s machines.pair_link: got %v, want permission_denied(machines.pair)", name, err)
		}
	}

	// machines.status denied for kb.read.
	if _, err := kbOnly.Handle(context.Background(), "machines.status", statusParams); err == nil {
		t.Fatal("kb.read machines.status unexpectedly succeeded")
	}
	if len(gotMethods) != 0 {
		t.Fatalf("denied calls reached hub: %v", gotMethods)
	}

	// machines.pair_link allowed for machines.pair.
	if _, err := pairExt.Handle(context.Background(), "machines.pair_link", pairParams); err != nil {
		t.Fatalf("machines.pair machines.pair_link: %v", err)
	}
	// machines.status allowed for machines.read, machines.pair, and full-hub.
	for name, fn := range map[string]func(context.Context, string, json.RawMessage) (any, error){
		"machines.read": readExt.Handle,
		"machines.pair": pairExt.Handle,
		"full-hub":      full.Handle,
	} {
		if _, err := fn(context.Background(), "machines.status", statusParams); err != nil {
			t.Fatalf("%s machines.status: %v", name, err)
		}
	}
	if len(gotMethods) != 4 || gotMethods[0] != extwire.HubMachinesPairLink {
		t.Fatalf("hub methods = %v", gotMethods)
	}
}
