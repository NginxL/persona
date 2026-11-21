package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type fakeDirEntry struct{ name string }

func (f fakeDirEntry) Name() string               { return f.name }
func (f fakeDirEntry) IsDir() bool                { return false }
func (f fakeDirEntry) Type() os.FileMode          { return 0 }
func (f fakeDirEntry) Info() (os.FileInfo, error) { return nil, nil }

func TestMachineServicesLinuxListAndProtection(t *testing.T) {
	n := &Node{
		serviceRunner: &machineServiceRunner{
			goos: "linux",
			now:  func() time.Time { return time.Unix(1700000000, 0) },
			lookPath: func(bin string) (string, error) {
				return "/usr/bin/" + bin, nil
			},
			run: func(_ context.Context, _ int, name string, args ...string) ([]byte, bool, error) {
				switch name {
				case "systemctl":
					return []byte(strings.Join([]string{
						"redis.service loaded active running Redis server",
						"broken.service loaded failed failed Broken worker",
						"personad-node.service loaded active running Persona Node",
						"personad.socket loaded active listening Persona Hub Socket",
						"idle.service loaded inactive dead Idle batch job",
					}, "\n")), false, nil
				case "docker":
					return []byte(strings.Join([]string{
						"abcdef1234567890\t/my-web\tnginx:1.27\trunning\tUp 2 hours",
						"1122334455667788\t/persona-postgres\tpostgres:18\trunning\tUp 5 days",
						"9988776655443322\t/job-runner\talpine:3\texited\tExited (0) 3 hours ago",
						"aabbccddeeff0011\t/crashed-api\tapp:v1\texited\tExited (137) 10 minutes ago",
					}, "\n")), false, nil
				default:
					return nil, false, errors.New("unexpected command: " + name)
				}
			},
		},
	}

	raw, err := n.handle(context.Background(), proto.MethodMachineServices, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("machine.services error: %v", err)
	}
	report, ok := raw.(proto.MachineServicesReport)
	if !ok {
		t.Fatalf("unexpected type %T", raw)
	}
	if report.OS != "linux" || report.At != 1700000000 {
		t.Fatalf("unexpected header: os=%q at=%d", report.OS, report.At)
	}
	if len(report.Managers) != 2 || !report.Managers[0].Available || !report.Managers[1].Available {
		t.Fatalf("managers = %#v", report.Managers)
	}
	if len(report.Services) != 5 {
		t.Fatalf("services count = %d, want 5: %#v", len(report.Services), report.Services)
	}
	// Failed unit sorts first.
	if report.Services[0].ID != "broken.service" || report.Services[0].State != proto.ServiceStateFailed {
		t.Errorf("first service = %#v, want broken.service (failed)", report.Services[0])
	}
	// Persona units are flagged as protected.
	protectedMap := map[string]bool{}
	for _, s := range report.Services {
		protectedMap[s.ID] = s.Protected
	}
	if !protectedMap["personad-node.service"] || !protectedMap["personad.socket"] {
		t.Errorf("Persona systemd units should be protected: %#v", protectedMap)
	}
	if protectedMap["redis.service"] {
		t.Errorf("redis.service should not be protected")
	}

	// Containers parsed, short 12-char IDs, states normalized, persona-postgres protected.
	if len(report.Containers) != 4 {
		t.Fatalf("containers count = %d, want 4", len(report.Containers))
	}
	if report.Containers[0].Name != "crashed-api" || report.Containers[0].State != proto.ServiceStateFailed {
		t.Errorf("first container = %#v, want crashed-api (failed)", report.Containers[0])
	}
	byName := map[string]proto.MachineServiceItem{}
	for _, c := range report.Containers {
		byName[c.Name] = c
	}
	if !byName["persona-postgres"].Protected {
		t.Errorf("persona-postgres should be protected")
	}
	if byName["my-web"].ID != "abcdef123456" || byName["my-web"].Protected {
		t.Errorf("my-web = %#v", byName["my-web"])
	}
	if byName["job-runner"].State != proto.ServiceStateStopped {
		t.Errorf("job-runner state = %q, want stopped", byName["job-runner"].State)
	}
}

func TestMachineServicesGracefullyReportsUnavailableManagers(t *testing.T) {
	n := &Node{
		serviceRunner: &machineServiceRunner{
			goos: "linux",
			now:  time.Now,
			lookPath: func(bin string) (string, error) {
				if bin == "docker" {
					return "", errors.New("executable file not found")
				}
				return "/usr/bin/" + bin, nil
			},
			run: func(_ context.Context, _ int, name string, _ ...string) ([]byte, bool, error) {
				return []byte("Failed to connect to bus: No medium found\n"), false, errors.New("exit status 1")
			},
		},
	}

	raw, err := n.handle(context.Background(), proto.MethodMachineServices, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	report := raw.(proto.MachineServicesReport)
	if len(report.Managers) != 2 {
		t.Fatalf("managers = %#v", report.Managers)
	}
	if report.Managers[0].Available || !strings.Contains(report.Managers[0].Unavailable, "Failed to connect to bus") {
		t.Errorf("systemd manager = %#v", report.Managers[0])
	}
	if report.Managers[1].Available || report.Managers[1].Unavailable != "docker not found" {
		t.Errorf("docker manager = %#v", report.Managers[1])
	}
	if len(report.Services) != 0 || len(report.Containers) != 0 {
		t.Errorf("expected empty items, got services=%d containers=%d", len(report.Services), len(report.Containers))
	}
}

func TestMachineServicesMacOSLaunchd(t *testing.T) {
	n := &Node{
		serviceRunner: &machineServiceRunner{
			goos: "darwin",
			home: "/Users/test",
			now:  time.Now,
			lookPath: func(bin string) (string, error) {
				if bin == "launchctl" {
					return "/bin/launchctl", nil
				}
				return "", errors.New("not found")
			},
			readDir: func(string) ([]os.DirEntry, error) {
				return []os.DirEntry{
					fakeDirEntry{name: "com.persona.node.plist"},
					fakeDirEntry{name: "com.example.unloaded.plist"},
				}, nil
			},
			run: func(_ context.Context, _ int, name string, args ...string) ([]byte, bool, error) {
				if name != "launchctl" {
					return nil, false, errors.New("unexpected: " + name)
				}
				return []byte(strings.Join([]string{
					"PID\tStatus\tLabel",
					"4321\t0\tcom.persona.node",
					"-\t0\tcom.apple.Finder",
					"-\t1\thomebrew.mxcl.redis",
					"999\t0\tapplication.com.apple.Safari.123",
				}, "\n")), false, nil
			},
		},
	}

	raw, err := n.handle(context.Background(), proto.MethodMachineServices, nil)
	if err != nil {
		t.Fatalf("launchd list error: %v", err)
	}
	report := raw.(proto.MachineServicesReport)
	if len(report.Services) != 3 {
		t.Fatalf("launchd services = %#v, want 3 (filtered com.apple/application)", report.Services)
	}
	byID := map[string]proto.MachineServiceItem{}
	for _, s := range report.Services {
		byID[s.ID] = s
	}
	if !byID["com.persona.node"].Protected || byID["com.persona.node"].PID != 4321 {
		t.Errorf("com.persona.node = %#v", byID["com.persona.node"])
	}
	if byID["homebrew.mxcl.redis"].State != proto.ServiceStateFailed {
		t.Errorf("homebrew.mxcl.redis = %#v", byID["homebrew.mxcl.redis"])
	}
	if byID["com.example.unloaded"].State != proto.ServiceStateStopped {
		t.Errorf("com.example.unloaded = %#v", byID["com.example.unloaded"])
	}
}

func TestMachineServiceLogsBoundedAndValidated(t *testing.T) {
	var gotCmd []string
	bigLog := strings.Repeat("2026-11-21T10:00:00Z line of output\n", 3000) // > 64 KiB
	n := &Node{
		serviceRunner: &machineServiceRunner{
			goos:     "linux",
			lookPath: func(bin string) (string, error) { return "/usr/bin/" + bin, nil },
			run: func(_ context.Context, maxBytes int, name string, args ...string) ([]byte, bool, error) {
				gotCmd = append([]string{name}, args...)
				tb := newServiceTailBuffer(maxBytes)
				_, _ = tb.Write([]byte(bigLog))
				return tb.Bytes(), tb.Truncated(), nil
			},
		},
	}

	// Rejects injection / invalid unit names.
	_, err := n.handle(context.Background(), proto.MethodMachineServiceLogs,
		json.RawMessage(`{"kind":"systemd","id":"redis.service;whoami","lines":50}`))
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != "bad_request" {
		t.Fatalf("expected bad_request for injection id, got %v", err)
	}

	// Clamps lines above ServiceLogsMaxLines and caps bytes at ServiceLogsMaxBytes.
	raw, err := n.handle(context.Background(), proto.MethodMachineServiceLogs,
		json.RawMessage(`{"kind":"systemd","id":"redis.service","lines":9999}`))
	if err != nil {
		t.Fatalf("systemd logs error: %v", err)
	}
	res := raw.(proto.MachineServiceLogsResult)
	if res.Lines != proto.ServiceLogsMaxLines {
		t.Errorf("lines = %d, want %d", res.Lines, proto.ServiceLogsMaxLines)
	}
	if !res.Truncated {
		t.Errorf("expected Truncated=true for >64KiB output")
	}
	if len(res.Content) > proto.ServiceLogsMaxBytes {
		t.Errorf("content len = %d > %d", len(res.Content), proto.ServiceLogsMaxBytes)
	}
	if strings.Join(gotCmd, " ") != "journalctl --user -u redis.service -n 500 --no-pager --output=short-iso" {
		t.Errorf("unexpected cmd: %q", gotCmd)
	}
}

func TestMachineServiceLaunchdPlistFileLogs(t *testing.T) {
	home := t.TempDir()
	logFile := filepath.Join(home, "worker.log")
	if err := os.WriteFile(logFile, []byte("line1\nline2\nline3\nline4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<plist><dict><key>StandardErrorPath</key><string>` + logFile + `</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(plistDir, "com.example.worker.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	n := &Node{
		serviceRunner: &machineServiceRunner{
			goos:     "darwin",
			home:     home,
			readFile: os.ReadFile,
			lookPath: func(string) (string, error) { return "", errors.New("unused") },
		},
	}
	raw, err := n.handle(context.Background(), proto.MethodMachineServiceLogs,
		json.RawMessage(`{"kind":"launchd","id":"com.example.worker","lines":2}`))
	if err != nil {
		t.Fatalf("launchd logs error: %v", err)
	}
	res := raw.(proto.MachineServiceLogsResult)
	if res.Source != "file" || res.Content != "line3\nline4" || !res.Truncated {
		t.Errorf("got %#v", res)
	}
}

func TestMachineServiceActionGuardrailsAndExecution(t *testing.T) {
	var calls [][]string
	n := &Node{
		serviceRunner: &machineServiceRunner{
			goos:     "linux",
			lookPath: func(bin string) (string, error) { return "/usr/bin/" + bin, nil },
			run: func(_ context.Context, _ int, name string, args ...string) ([]byte, bool, error) {
				calls = append(calls, append([]string{name}, args...))
				if name == "docker" && len(args) > 0 && args[0] == "inspect" {
					id := args[len(args)-1]
					if id == "112233445566" {
						return []byte("/persona-postgres\n"), false, nil
					}
					return []byte("/my-web\n"), false, nil
				}
				return []byte("ok\n"), false, nil
			},
		},
	}

	// 1. Unconfirmed action is refused.
	_, err := n.handle(context.Background(), proto.MethodMachineServiceAction,
		json.RawMessage(`{"kind":"systemd","id":"redis.service","action":"restart","confirmed":false}`))
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != "bad_request" {
		t.Fatalf("unconfirmed should fail with bad_request, got %v", err)
	}

	// 2. Protected systemd unit is refused.
	_, err = n.handle(context.Background(), proto.MethodMachineServiceAction,
		json.RawMessage(`{"kind":"systemd","id":"personad-node.service","action":"stop","confirmed":true}`))
	if !errors.As(err, &re) || re.Code != proto.CodeServiceProtected {
		t.Fatalf("protected systemd unit should fail with %s, got %v", proto.CodeServiceProtected, err)
	}

	// 3. Protected Docker container passed by container ID is resolved and refused.
	_, err = n.handle(context.Background(), proto.MethodMachineServiceAction,
		json.RawMessage(`{"kind":"docker","id":"112233445566","action":"stop","confirmed":true}`))
	if !errors.As(err, &re) || re.Code != proto.CodeServiceProtected {
		t.Fatalf("protected docker container by ID should fail with %s, got %v", proto.CodeServiceProtected, err)
	}
	if len(calls) != 1 || calls[0][0] != "docker" || calls[0][1] != "inspect" {
		t.Fatalf("expected only docker inspect call before refusal, got %#v", calls)
	}

	// 4. Normal systemd restart succeeds with exact -- separator.
	calls = nil
	raw, err := n.handle(context.Background(), proto.MethodMachineServiceAction,
		json.RawMessage(`{"kind":"systemd","id":"redis.service","action":"restart","confirmed":true}`))
	if err != nil {
		t.Fatalf("redis restart error: %v", err)
	}
	res := raw.(proto.MachineServiceActionResult)
	if !res.OK || len(calls) != 1 || strings.Join(calls[0], " ") != "systemctl --user restart -- redis.service" {
		t.Fatalf("res=%#v calls=%#v", res, calls)
	}

	// 5. Normal Docker restart inspects then restarts with -- separator.
	calls = nil
	raw, err = n.handle(context.Background(), proto.MethodMachineServiceAction,
		json.RawMessage(`{"kind":"docker","id":"abcdef123456","action":"restart","confirmed":true}`))
	if err != nil {
		t.Fatalf("docker restart error: %v", err)
	}
	res = raw.(proto.MachineServiceActionResult)
	if !res.OK || len(calls) != 2 || strings.Join(calls[1], " ") != "docker restart -- abcdef123456" {
		t.Fatalf("res=%#v calls=%#v", res, calls)
	}
}

func TestMachineServiceLaunchdAction(t *testing.T) {
	var got []string
	n := &Node{
		serviceRunner: &machineServiceRunner{
			goos:     "darwin",
			uid:      func() int { return 501 },
			lookPath: func(bin string) (string, error) { return "/bin/" + bin, nil },
			run: func(_ context.Context, _ int, name string, args ...string) ([]byte, bool, error) {
				got = append([]string{name}, args...)
				return nil, false, nil
			},
		},
	}

	// Protected com.persona.* label refused.
	_, err := n.handle(context.Background(), proto.MethodMachineServiceAction,
		json.RawMessage(`{"kind":"launchd","id":"com.persona.node","action":"restart","confirmed":true}`))
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != proto.CodeServiceProtected {
		t.Fatalf("expected service_protected, got %v", err)
	}

	// Allowed launchd restart uses kickstart -k gui/<uid>/<label>.
	_, err = n.handle(context.Background(), proto.MethodMachineServiceAction,
		json.RawMessage(`{"kind":"launchd","id":"homebrew.mxcl.redis","action":"restart","confirmed":true}`))
	if err != nil {
		t.Fatalf("launchd restart error: %v", err)
	}
	if strings.Join(got, " ") != "launchctl kickstart -k gui/501/homebrew.mxcl.redis" {
		t.Errorf("got %q", got)
	}
}
