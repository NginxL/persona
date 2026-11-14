package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// startupOwnershipStore fails each selected setting read a fixed number of times and records when it was tried.
type startupOwnershipStore struct {
	*ownersStore
	mu       sync.Mutex
	failures map[string]int
	reads    map[string][]time.Time
}

func (s *startupOwnershipStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	s.mu.Lock()
	s.reads[key] = append(s.reads[key], time.Now())
	fail := s.failures[key] > 0
	if fail {
		s.failures[key]--
	}
	s.mu.Unlock()
	if fail {
		return false, errors.New("database restarting")
	}
	return s.ownersStore.Setting(ctx, key, out)
}

func (s *startupOwnershipStore) readTimes(key string) []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.reads[key]...)
}

func TestStartupOwnershipReadsRetryAndReconcile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		h := newTestHub(t)
		h.cfg.Machines = []MachineConfig{
			{ID: "moved", Name: "Moved", Kind: "ssh"},
			{ID: "dedicated", Name: "Dedicated", Kind: "local", OwnerRequired: true},
		}
		st := &startupOwnershipStore{
			ownersStore: &ownersStore{kept: map[string]json.RawMessage{
				machineOwnersKey: json.RawMessage(`{"moved":"m-0b0b0b","dedicated":"m-0b0b0b"}`),
				machinesKeptKey:  json.RawMessage(`{"kept":"m-0b0b0b"}`),
			}},
			failures: map[string]int{machineOwnersKey: 2, machinesKeptKey: 2},
			reads:    map[string][]time.Time{},
		}
		h.bus.idents = st
		h.store = newMeshStore()
		if err := h.store.PutSetting(ctx, machineEntriesKey, reportedMachines{
			"m-0b0b0b": {
				"kept": {
					Cfg:  json.RawMessage(`{"id":"kept","name":"Kept computer","kind":"ssh"}`),
					Info: proto.NodeInfo{Hostname: "kept-host"},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}

		h.startDevices(ctx)
		moved := h.localMachine("moved")
		if moved == nil {
			t.Fatal("the configured machine is missing after startup")
		}
		moved.mu.Lock()
		moved.online, moved.connected, moved.connectedAt = true, true, time.Now()
		moved.mu.Unlock()
		if got := moved.ownerID(); got != assistantwire.AssistantID {
			t.Fatalf("the unread moved machine initially belongs to %q, want the carrier", got)
		}
		if h.localMachine("kept") != nil {
			t.Fatal("a machine whose kept-list read failed is already listed")
		}

		loopCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.machineTrustLoop(loopCtx)
		}()
		defer func() {
			cancel()
			<-done
		}()
		synctest.Wait()
		for _, key := range []string{machineOwnersKey, machinesKeptKey} {
			if got := len(st.readTimes(key)); got != 2 {
				t.Fatalf("%s was read %d times after the first retry, want 2", key, got)
			}
		}
		if h.entryOwner("moved") != assistantwire.AssistantID || h.localMachine("kept") != nil {
			t.Fatal("the second failed read unexpectedly reconciled the machines")
		}

		synctest.Sleep(trustRetryEvery)
		synctest.Wait()
		for _, key := range []string{machineOwnersKey, machinesKeptKey} {
			reads := st.readTimes(key)
			if len(reads) != 3 {
				t.Fatalf("%s was read %d times after recovery, want 3", key, len(reads))
			}
			if gap := reads[2].Sub(reads[1]); gap != trustRetryEvery {
				t.Fatalf("%s retry gap = %v, want %v", key, gap, trustRetryEvery)
			}
		}
		if got := h.entryOwner("moved"); got != "m-0b0b0b" {
			t.Fatalf("the recovered owner = %q, want m-0b0b0b", got)
		}
		if got := moved.ownerID(); got != "m-0b0b0b" || moved.snapshot().Online {
			t.Fatalf("handover left owner %q and online %v", got, moved.snapshot().Online)
		}
		if got := h.localMachine("dedicated").ownerID(); got != "m-0b0b0b" {
			t.Fatalf("the dedicated machine's recovered owner = %q", got)
		}
		kept, ok := listed(h, "kept")
		if !ok || kept.Manager != "m-0b0b0b" || kept.Name != "Kept computer" {
			t.Fatalf("the recovered kept machine = %+v, present %v", kept, ok)
		}
		// Finish the coalesced config and manager updates before leaving the fake-time bubble.
		synctest.Sleep(200 * time.Millisecond)
		synctest.Wait()
	})
}

func TestMachineListOmitsUnavailableOwner(t *testing.T) {
	h := newTestHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "dedicated", Name: "Dedicated", Kind: "local", OwnerRequired: true}}
	st := &startupOwnershipStore{
		ownersStore: &ownersStore{kept: map[string]json.RawMessage{
			machineOwnersKey: json.RawMessage(`{"dedicated":"m-0b0b0b"}`),
		}},
		failures: map[string]int{machineOwnersKey: 1},
		reads:    map[string][]time.Time{},
	}
	h.bus.idents = st
	h.startDevices(context.Background())
	m, ok := listed(h, "dedicated")
	if !ok {
		t.Fatal("the dedicated machine is missing from the machine list")
	}
	if m.Manager != "" {
		t.Fatalf("manager = %q while ownership is unavailable, want empty", m.Manager)
	}
}

func TestLateKeptListReadReconcilesMachineList(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t)
	st := &startupOwnershipStore{
		ownersStore: &ownersStore{kept: map[string]json.RawMessage{
			machinesKeptKey: json.RawMessage(`{"kept":"m-0b0b0b"}`),
		}},
		failures: map[string]int{machinesKeptKey: 1},
		reads:    map[string][]time.Time{},
	}
	h.bus.idents = st
	h.store = newMeshStore()
	if err := h.store.PutSetting(ctx, machineEntriesKey, reportedMachines{
		"m-0b0b0b": {
			"kept": {Cfg: json.RawMessage(`{"id":"kept","name":"Kept computer","kind":"ssh"}`)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	h.startDevices(ctx)
	if h.localMachine("kept") != nil {
		t.Fatal("a machine whose kept-list read failed is already listed")
	}
	keeps, known := h.keepsAnyKnown(ctx, "m-0b0b0b")
	if !known || !keeps {
		t.Fatalf("the late kept-list read returned known %v and keeps %v", known, keeps)
	}
	m, ok := listed(h, "kept")
	if !ok || m.Manager != "m-0b0b0b" || m.Name != "Kept computer" {
		t.Fatalf("the late-read kept machine = %+v, present %v", m, ok)
	}
}
