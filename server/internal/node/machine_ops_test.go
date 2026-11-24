package node

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestMachineOpsSurfaceIsStrictlyReadOnly(t *testing.T) {
	typ := reflect.TypeFor[*MachineOps]()
	got := make([]string, 0, typ.NumMethod())
	for i := range typ.NumMethod() {
		got = append(got, typ.Method(i).Name)
	}
	slices.Sort(got)
	want := []string{
		"Network",
		"Ports",
		"Processes",
		"ServiceLogs",
		"Services",
		"Storage",
		"StorageHotspots",
		"Toolchain",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("MachineOps exported methods = %v, want strictly read-only %v", got, want)
	}
}

func TestMachineOpsReadOnlyCallsAndValidation(t *testing.T) {
	ctx := context.Background()
	ops := NewMachineOps()

	storageRep, err := ops.Storage(ctx)
	if err != nil {
		t.Fatalf("Storage: %v", err)
	}
	if storageRep.OS == "" || storageRep.At <= 0 {
		t.Fatalf("Storage report missing OS/At: %+v", storageRep)
	}

	if _, err := ops.Ports(ctx, proto.MachinePortsParams{Protocol: "icmp"}); err == nil {
		t.Fatal("Ports accepted invalid protocol icmp")
	}
	if _, err := ops.Processes(ctx, proto.MachineProcessesParams{Sort: "pid"}); err == nil {
		t.Fatal("Processes accepted invalid sort pid")
	}
	if _, err := ops.ServiceLogs(ctx, proto.MachineServiceLogsParams{Kind: "systemd", ID: "../etc/passwd"}); err == nil {
		t.Fatal("ServiceLogs accepted invalid path-traversal id")
	}
}
