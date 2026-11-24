package machineops

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/proto"
)

type fakeProvider struct {
	mu              sync.Mutex
	hotspotsParams  proto.StorageHotspotsParams
	toolchainParams proto.MachineToolchainParams
	portsParams     proto.MachinePortsParams
	processesParams proto.MachineProcessesParams
	logsParams      proto.MachineServiceLogsParams
	storageEntered  chan struct{}
	storageBlock    chan struct{}
	storageErr      error
}

func (f *fakeProvider) Storage(ctx context.Context) (proto.MachineStorageReport, error) {
	if f.storageEntered != nil {
		close(f.storageEntered)
	}
	if f.storageBlock != nil {
		select {
		case <-f.storageBlock:
		case <-ctx.Done():
			return proto.MachineStorageReport{}, ctx.Err()
		}
	}
	if f.storageErr != nil {
		return proto.MachineStorageReport{}, f.storageErr
	}
	return proto.MachineStorageReport{
		At:        1700000000,
		OS:        "linux",
		Supported: true,
		Mounts:    []proto.MountStat{{Mount: "/", TotalBytes: 1000, UsedBytes: 400, AvailBytes: 600, UsedPercent: 40}},
	}, nil
}

func (f *fakeProvider) StorageHotspots(_ context.Context, p proto.StorageHotspotsParams) (proto.MachineStorageHotspotsReport, error) {
	f.mu.Lock()
	f.hotspotsParams = p
	f.mu.Unlock()
	return proto.MachineStorageHotspotsReport{
		At: 1700000001, OS: "linux",
		Categories: []proto.StorageHotspotCategory{{ID: proto.StorageCategoryGoBuild, Bytes: 4096, Status: proto.StorageStatusOK}},
	}, nil
}

func (f *fakeProvider) Toolchain(_ context.Context, p proto.MachineToolchainParams) (proto.MachineToolchainReport, error) {
	f.mu.Lock()
	f.toolchainParams = p
	f.mu.Unlock()
	return proto.MachineToolchainReport{
		OS: "linux", Arch: "amd64", CacheTTLSec: 60,
		Tools: []proto.ToolchainEntry{{ID: "go", Category: "core", Found: true, Version: "1.24.0"}},
	}, nil
}

func (f *fakeProvider) Network(_ context.Context) (proto.MachineNetworkReport, error) {
	return proto.MachineNetworkReport{
		CheckedAt: 1700000002,
		Proxy:     proto.MachineProxyConfig{HTTPSProxy: "http://[redacted]@proxy.example.com:8080", Active: true},
		Probes:    []proto.MachineEndpointProbe{{ID: "github_https", Reachable: true, StatusCode: 200, LatencyMS: 42}},
	}, nil
}

func (f *fakeProvider) Ports(_ context.Context, p proto.MachinePortsParams) (proto.MachinePortsResult, error) {
	f.mu.Lock()
	f.portsParams = p
	f.mu.Unlock()
	return proto.MachinePortsResult{
		At: 1700000003, Supported: true,
		Ports: []proto.ListeningPort{{Protocol: "tcp", BindAddr: "127.0.0.1", Port: 8080, Scope: proto.PortScopeLoopback}},
	}, nil
}

func (f *fakeProvider) Processes(_ context.Context, p proto.MachineProcessesParams) (proto.MachineProcessesResult, error) {
	f.mu.Lock()
	f.processesParams = p
	f.mu.Unlock()
	return proto.MachineProcessesResult{
		At: 1700000004, Supported: true, TotalCount: 1, OrphanCount: 1,
		Processes: []proto.MachineProcess{{PID: 1234, Name: "go.test", LikelyOrphan: true}},
	}, nil
}

func (f *fakeProvider) Services(_ context.Context) (proto.MachineServicesReport, error) {
	return proto.MachineServicesReport{
		At: 1700000005, OS: "linux",
		Managers: []proto.ServiceManagerStatus{{Kind: proto.ServiceKindSystemd, Available: true}},
		Services: []proto.MachineServiceItem{{Kind: proto.ServiceKindSystemd, ID: "demo.service", Name: "demo", State: proto.ServiceStateRunning}},
	}, nil
}

func (f *fakeProvider) ServiceLogs(_ context.Context, p proto.MachineServiceLogsParams) (proto.MachineServiceLogsResult, error) {
	f.mu.Lock()
	f.logsParams = p
	f.mu.Unlock()
	return proto.MachineServiceLogsResult{
		Kind: p.Kind, ID: p.ID, Lines: p.Lines, Content: "hello log\n", Source: "journalctl",
	}, nil
}

func TestCLIAllReadOnlyDomains(t *testing.T) {
	fp := &fakeProvider{}
	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := MainWithProvider(context.Background(), "test-v1", fp, args, strings.NewReader(""), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}

	if code, out, errOut := run("--help"); code != 0 || !strings.Contains(out, "ops storage") || errOut != "" {
		t.Fatalf("--help: code=%d out=%q err=%q", code, out, errOut)
	}
	if code, out, errOut := run("storage"); code != 0 || !strings.Contains(out, `"mount":"/"`) || errOut != "" {
		t.Fatalf("storage: code=%d out=%q err=%q", code, out, errOut)
	}
	if code, out, errOut := run("hotspots", "--timeout-ms", "3000", "--max-depth", "5"); code != 0 || !strings.Contains(out, `"go-build"`) || errOut != "" {
		t.Fatalf("hotspots: code=%d out=%q err=%q", code, out, errOut)
	}
	if fp.hotspotsParams.TimeoutMS != 3000 || fp.hotspotsParams.MaxDepth != 5 {
		t.Fatalf("hotspots params = %+v", fp.hotspotsParams)
	}
	if code, out, errOut := run("toolchain", "--refresh"); code != 0 || !strings.Contains(out, `"1.24.0"`) || errOut != "" {
		t.Fatalf("toolchain: code=%d out=%q err=%q", code, out, errOut)
	}
	if !fp.toolchainParams.Refresh {
		t.Fatal("toolchain --refresh not forwarded")
	}
	if code, out, errOut := run("network"); code != 0 || !strings.Contains(out, `[redacted]`) || errOut != "" {
		t.Fatalf("network: code=%d out=%q err=%q", code, out, errOut)
	}
	if code, out, errOut := run("ports", "--protocol", "TCP"); code != 0 || !strings.Contains(out, `"port":8080`) || errOut != "" {
		t.Fatalf("ports: code=%d out=%q err=%q", code, out, errOut)
	}
	if fp.portsParams.Protocol != "tcp" {
		t.Fatalf("ports params = %+v", fp.portsParams)
	}
	if code, out, errOut := run("processes", "--sort", "memory", "--query", "go", "--orphan-only", "--limit", "25"); code != 0 || !strings.Contains(out, `"likely_orphan":true`) || errOut != "" {
		t.Fatalf("processes: code=%d out=%q err=%q", code, out, errOut)
	}
	if fp.processesParams.Sort != "memory" || fp.processesParams.Query != "go" || !fp.processesParams.OrphanOnly || fp.processesParams.Limit != 25 {
		t.Fatalf("processes params = %+v", fp.processesParams)
	}
	if code, out, errOut := run("services"); code != 0 || !strings.Contains(out, `"demo.service"`) || errOut != "" {
		t.Fatalf("services: code=%d out=%q err=%q", code, out, errOut)
	}
	if code, out, errOut := run("service-logs", "--kind", "systemd", "--id", "demo.service", "--lines", "999"); code != 0 || !strings.Contains(out, `"lines":500`) || errOut != "" {
		t.Fatalf("service-logs: code=%d out=%q err=%q", code, out, errOut)
	}
	if fp.logsParams.Lines != proto.ServiceLogsMaxLines {
		t.Fatalf("service-logs lines not clamped: %+v", fp.logsParams)
	}
}

func TestCLIRejectsMutatingFileAndInvalidCommands(t *testing.T) {
	fp := &fakeProvider{}
	cases := [][]string{
		{},
		{"cleanup"},
		{"storage-cleanup"},
		{"kill"},
		{"process-kill"},
		{"service-action"},
		{"files"},
		{"file-read"},
		{"file-write"},
		{"storage", "extra"},
		{"ports", "--protocol", "icmp"},
		{"processes", "--sort", "pid"},
		{"processes", "--limit", "-1"},
		{"hotspots", "--timeout-ms", "-5"},
		{"service-logs", "--kind", "systemd", "--id", "../bad"},
		{"service-logs", "--kind", "docker", "--id", "c1", "--lines", "0"},
		{"mcp", "extra"},
		{"skill", "bogus"},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		code := MainWithProvider(context.Background(), "test", fp, args, strings.NewReader(""), &stdout, &stderr)
		if code != exitUsage {
			t.Errorf("%v: exit=%d, want %d (stdout=%q stderr=%q)", args, code, exitUsage, stdout.String(), stderr.String())
		}
	}
	// Subcommand -h exits 0 with no stderr.
	for _, sub := range []string{"storage", "hotspots", "toolchain", "network", "ports", "processes", "services", "service-logs", "mcp", "skill"} {
		var stdout, stderr bytes.Buffer
		code := MainWithProvider(context.Background(), "test", fp, []string{sub, "-h"}, strings.NewReader(""), &stdout, &stderr)
		if code != exitOK || stderr.Len() != 0 || stdout.Len() == 0 {
			t.Errorf("%s -h: code=%d stdout=%q stderr=%q", sub, code, stdout.String(), stderr.String())
		}
	}
}

func TestCLISkillOptInInstallStatusUninstallAndConflict(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	fp := &fakeProvider{}
	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := MainWithProvider(context.Background(), "test", fp, args, strings.NewReader(""), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}

	code, out, _ := run("skill", "status", "--dir", dir)
	if code != 0 || !strings.Contains(out, `"installed":false`) {
		t.Fatalf("initial status: code=%d out=%q", code, out)
	}
	code, out, errOut := run("skill", "install", "--dir", dir)
	if code != 0 || !strings.Contains(out, `"installed":true`) || !strings.Contains(out, `"managed":true`) || errOut != "" {
		t.Fatalf("install: code=%d out=%q err=%q", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, assistantskills.MachineOpsSkillName, "SKILL.md")); err != nil {
		t.Fatalf("installed SKILL.md missing: %v", err)
	}
	// Modifying the installed skill makes uninstall refuse to delete user edits.
	skillFile := filepath.Join(dir, assistantskills.MachineOpsSkillName, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("custom user edits"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = run("skill", "uninstall", "--dir", dir)
	if code != exitFailed || !strings.Contains(errOut, "not managed by Persona") {
		t.Fatalf("expected conflict on uninstall: code=%d err=%q", code, errOut)
	}
	// Install into a clean dir and uninstall cleanly.
	dir2 := filepath.Join(t.TempDir(), "skills2")
	if code, _, errOut := run("skill", "install", "--dir", dir2); code != 0 {
		t.Fatalf("install dir2: code=%d err=%q", code, errOut)
	}
	if code, out, errOut := run("skill", "uninstall", "--dir", dir2); code != 0 || !strings.Contains(out, `"installed":false`) || errOut != "" {
		t.Fatalf("uninstall dir2: code=%d out=%q err=%q", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir2, assistantskills.MachineOpsSkillName)); !os.IsNotExist(err) {
		t.Fatalf("skill dir survived uninstall: %v", err)
	}
}

func TestMCPServerReadOnlyToolsAndSafetyBoundaries(t *testing.T) {
	fp := &fakeProvider{}
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"machine_ops_storage","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"machine_ops_storage_hotspots","arguments":{"timeout_ms":2000,"max_depth":3}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"machine_ops_toolchain","arguments":{"refresh":true}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"machine_ops_network","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"machine_ops_ports","arguments":{"protocol":"tcp"}}}`,
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"machine_ops_processes","arguments":{"sort":"uptime","orphan_only":true,"limit":10}}}`,
		`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"machine_ops_services","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"machine_ops_service_logs","arguments":{"kind":"docker","id":"web-1","lines":50}}}`,
		// Strict unknown field rejection (prevents caller-supplied SSRF probe targets on network).
		`{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"machine_ops_network","arguments":{"target":"https://metadata.example.invalid"}}}`,
		// Mutating & human-only file tools are never exposed and always refused.
		`{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"machine_ops_storage_cleanup","arguments":{"targets":["go-build"]}}}`,
		`{"jsonrpc":"2.0","id":14,"method":"tools/call","params":{"name":"machine_ops_process_kill","arguments":{"pid":123}}}`,
		`{"jsonrpc":"2.0","id":15,"method":"tools/call","params":{"name":"machine_ops_service_action","arguments":{"kind":"systemd","id":"x","action":"restart"}}}`,
		`{"jsonrpc":"2.0","id":16,"method":"tools/call","params":{"name":"machine_files_read","arguments":{"path":"/etc/passwd"}}}`,
		// JSON-RPC errors.
		`{not json`,
		`{"jsonrpc":"2.0","id":17,"method":"unknown/method"}`,
		`{"jsonrpc":"2.0","id":18,"method":"tools/call","params":{"name":"machine_ops_storage","arguments":["bad"]}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	if err := ServeMCP(context.Background(), "1.2.3", fp, strings.NewReader(in), &out); err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}

	byID := map[string]mcpResponse{}
	var parseErr *mcpRPCError
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		var resp mcpResponse
		if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
			t.Fatalf("invalid response JSON %q: %v", sc.Text(), err)
		}
		if string(resp.ID) == "null" {
			parseErr = resp.Error
			continue
		}
		byID[mcpIDKey(resp.ID)] = resp
	}
	if parseErr == nil || parseErr.Code != -32700 {
		t.Fatalf("parse error = %+v, want -32700", parseErr)
	}

	// Verify initialize
	initRes, _ := byID["1"].Result.(map[string]any)
	info, _ := initRes["serverInfo"].(map[string]any)
	if info["name"] != "persona-machine-ops" || info["version"] != "1.2.3" {
		t.Fatalf("initialize serverInfo = %#v", info)
	}

	// Verify tools/list exposes ONLY the 8 read-only tools and marks every one readOnlyHint=true, destructiveHint=false.
	listRes, _ := byID["3"].Result.(map[string]any)
	rawTools, _ := listRes["tools"].([]any)
	var listedNames []string
	for _, rt := range rawTools {
		tool, _ := rt.(map[string]any)
		name, _ := tool["name"].(string)
		listedNames = append(listedNames, name)
		ann, _ := tool["annotations"].(map[string]any)
		if ann["readOnlyHint"] != true || ann["destructiveHint"] != false {
			t.Errorf("tool %s missing read-only annotations: %#v", name, ann)
		}
		lower := strings.ToLower(name)
		for _, forbidden := range []string{"cleanup", "kill", "action", "file", "write", "delete", "upload", "rename"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("forbidden mutating/file tool exposed: %s", name)
			}
		}
	}
	if !slices.Equal(listedNames, ReadOnlyToolNames) {
		t.Fatalf("tools/list = %v, want %v", listedNames, ReadOnlyToolNames)
	}

	// Verify all 8 read-only calls succeeded.
	for _, id := range []string{"4", "5", "6", "7", "8", "9", "10", "11"} {
		res, _ := byID[id].Result.(map[string]any)
		if res["isError"] != false {
			t.Errorf("id=%s expected isError=false, got %#v", id, res)
		}
	}

	// Verify unknown-field injection and mutating/file tool calls returned isError=true.
	for _, id := range []string{"12", "13", "14", "15", "16"} {
		res, _ := byID[id].Result.(map[string]any)
		if res["isError"] != true {
			t.Errorf("id=%s expected isError=true, got %#v", id, res)
		}
	}

	if byID["17"].Error == nil || byID["17"].Error.Code != -32601 {
		t.Fatalf("id=17 error = %+v, want -32601", byID["17"].Error)
	}
	if byID["18"].Error == nil || byID["18"].Error.Code != -32602 {
		t.Fatalf("id=18 error = %+v, want -32602", byID["18"].Error)
	}
}

func TestMCPCancellationDropsResponse(t *testing.T) {
	fp := &fakeProvider{
		storageEntered: make(chan struct{}),
		storageBlock:   make(chan struct{}),
	}
	pr, pw := io.Pipe()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- ServeMCP(context.Background(), "dev", fp, pr, &out)
	}()

	_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","id":100,"method":"tools/call","params":{"name":"machine_ops_storage","arguments":{}}}`+"\n")
	<-fp.storageEntered
	_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":100}}`+"\n")
	_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","id":101,"method":"ping"}`+"\n")
	_ = pw.Close()

	if err := <-done; err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	text := out.String()
	if strings.Contains(text, `"id":100`) {
		t.Fatalf("cancelled request 100 produced a response: %s", text)
	}
	if !strings.Contains(text, `"id":101`) {
		t.Fatalf("ping 101 missing from output: %s", text)
	}
}

func TestLocalProviderSmoke(t *testing.T) {
	p := NewLocalProvider()
	var stdout, stderr bytes.Buffer
	code := MainWithProvider(context.Background(), "dev", p, []string{"storage"}, strings.NewReader(""), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("local storage failed: code=%d stderr=%q", code, stderr.String())
	}
	var rep proto.MachineStorageReport
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("decode local storage report: %v (%s)", err, stdout.String())
	}
	if rep.OS == "" || rep.At <= 0 {
		t.Fatalf("unexpected local storage report: %+v", rep)
	}
}

func TestCLIProviderErrorReturnsExitFailed(t *testing.T) {
	fp := &fakeProvider{storageErr: errors.New("boom")}
	var stdout, stderr bytes.Buffer
	code := MainWithProvider(context.Background(), "dev", fp, []string{"storage"}, strings.NewReader(""), &stdout, &stderr)
	if code != exitFailed || !strings.Contains(stderr.String(), "boom") {
		t.Fatalf("expected exit 1 with boom, got code=%d stderr=%q", code, stderr.String())
	}
}
