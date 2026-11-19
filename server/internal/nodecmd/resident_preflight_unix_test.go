//go:build unix

package nodecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/node"
	"github.com/nwverse/persona/server/internal/nodeupdate"
)

func residentCheckFixture(t *testing.T, handover bool) string {
	t.Helper()
	check := nodeupdate.Check{Version: "test", OS: runtime.GOOS, Arch: runtime.GOARCH, Program: "personad", ResidentHandover: handover}
	payload, err := json.Marshal(check)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "replacement")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+string(payload)+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func shortResidentHome(t *testing.T) {
	t.Helper()
	home, err := os.MkdirTemp("", "nr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("PERSONA_HOME", filepath.Join(home, ".persona"))
}

func TestResidentPreflightRejectsLegacyBeforeClosingAttach(t *testing.T) {
	shortResidentHome(t)
	transport, err := node.OpenResidentTransport()
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	before, err := os.Stat(node.AttachSocketPath())
	if err != nil {
		t.Fatal(err)
	}
	legacy := residentCheckFixture(t, false)
	if _, err := checkResidentReplacement(context.Background(), legacy, "personad"); err == nil {
		t.Fatal("legacy image accepted descriptor handover")
	}
	conn, err := net.Dial("unix", node.AttachSocketPath())
	if err != nil {
		t.Fatalf("preflight refusal closed attach: %v", err)
	}
	_ = conn.Close()
	after, err := os.Stat(node.AttachSocketPath())
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("preflight refusal replaced the socket")
	}
	if _, err := checkResidentReplacement(context.Background(), residentCheckFixture(t, true), "personad"); err != nil {
		t.Fatalf("handover-capable image refused: %v", err)
	}
}

func TestResidentReloadLogsPreflightFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		script  string
		timeout time.Duration
		reason  string
		parsed  bool
	}{
		{name: "non-zero exit", script: "exit 23", reason: "exit status 23"},
		{name: "timeout", script: "exec sleep 30", timeout: 100 * time.Millisecond, reason: "context deadline exceeded"},
		{name: "unparsable output", script: "printf broken", reason: "invalid character 'b'"},
		{name: "identity mismatch",
			script: fmt.Sprintf(`printf '%%s\n' '{"version":"candidate","os":%q,"arch":%q,"program":"persona-node","resident_handover":true}'`, runtime.GOOS, runtime.GOARCH),
			reason: "resident replacement identity does not match this node", parsed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "replacement")
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"+tc.script+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if tc.timeout != 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}
			var output bytes.Buffer
			err := preflightResidentReload(ctx, log.New(&output, "", 0), path, "personad")
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("preflight error = %v, want %q", err, tc.reason)
			}
			if tc.timeout != 0 && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("preflight error lost the deadline: %v", err)
			}
			want := "resident reload: replacement did not pass preflight; keeping the current node: " + err.Error()
			if tc.parsed {
				want += fmt.Sprintf(" (version=\"candidate\" os=%q arch=%q program=\"persona-node\" resident_handover=true)", runtime.GOOS, runtime.GOARCH)
			}
			if got := output.String(); got != want+"\n" {
				t.Fatalf("log = %q, want %q", got, want)
			}
		})
	}
}

func TestLegacyStartupRecoveryReleasesListenerAndRunLock(t *testing.T) {
	shortResidentHome(t)
	transport, err := node.OpenResidentTransport()
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	check, err := residentReplacementInfo(context.Background(), residentCheckFixture(t, false), "personad")
	if err != nil {
		t.Fatal(err)
	}
	files, err := prepareResidentRecovery(transport, check.ResidentHandover)
	if err != nil || files != nil {
		t.Fatalf("legacy recovery inherited descriptors: files=%v error=%v", files, err)
	}
	if conn, err := net.Dial("unix", node.AttachSocketPath()); err == nil {
		_ = conn.Close()
		t.Fatal("legacy recovery retained a listener")
	}
	release, err := link.LockFile(node.AttachSocketPath()+".run", false)
	if err != nil {
		t.Fatalf("legacy recovery retained the run lock: %v", err)
	}
	release()
}
