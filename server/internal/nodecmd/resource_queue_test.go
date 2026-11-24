package nodecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	rq "github.com/nwverse/persona/server/internal/resourcequeue"
)

func TestGateNewJoinedCachedSubmitExactlyOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell transport fixture")
	}
	for _, status := range []string{"granted", "joined", "cached"} {
		t.Run(status, func(t *testing.T) {
			dir := t.TempDir()
			count := filepath.Join(dir, "count")
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("GATE_COUNT", count)
			if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\necho run >> \"$GATE_COUNT\"\nprintf 'RESULT=example-run\\nEXIT_CODE=0\\n'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			old := resourceCall
			t.Cleanup(func() { resourceCall = old })
			lease := &rq.Lease{ID: "example-lease", State: "granted", Token: 1, Outcome: &rq.Outcome{Exit: 0, Run: "example-run", Output: "RESULT=example-run\nEXIT_CODE=0\n"}}
			resourceCall = func(_ context.Context, name string, args, out any) error {
				var result any
				switch name {
				case "resource_status":
					result = map[string]any{"resources": []rq.Status{{Definition: rq.Definition{ID: "ci.example", Capacity: 3, Mirror: rq.Mirror{Target: "runner.example.com"}}}}}
				case "resource_acquire":
					result = rq.Reply{Status: status, Lease: lease}
				case "resource_get":
					copy := *lease
					copy.State = "released"
					result = rq.Reply{Status: "released", Lease: &copy}
				case "resource_release":
					result = rq.Reply{Status: "released", Lease: lease}
				default:
					return fmt.Errorf("unexpected %s", name)
				}
				raw, _ := json.Marshal(result)
				return json.Unmarshal(raw, out)
			}
			var stdout, stderr bytes.Buffer
			code := gateCommand(context.Background(), []string{"example", strings.Repeat("a", 40), "web"}, strings.NewReader(""), &stdout, &stderr)
			if code != 0 || stdout.String() != "RESULT=example-run\nEXIT_CODE=0\n" {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			raw, _ := os.ReadFile(count)
			want := 0
			if status == "granted" {
				want = 1
			}
			if strings.Count(string(raw), "run") != want {
				t.Fatalf("%s made %q submissions", status, raw)
			}
		})
	}
}
func TestGateManagerUnavailableFallsBackExplicitly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell transport fixture")
	}
	old := resourceCall
	t.Cleanup(func() { resourceCall = old })
	resourceCall = func(context.Context, string, any, any) error { return errors.New("unreachable") }
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PERSONA_GATE_SSH_TARGET", "runner.example.com")
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\nprintf 'RESULT=example-run\\nEXIT_CODE=7\\n'\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	code := gateCommand(context.Background(), []string{"example", strings.Repeat("a", 40), "web"}, strings.NewReader(""), &out, &errout)
	if code != 7 || !strings.Contains(errout.String(), "DEGRADED") || !strings.Contains(out.String(), "EXIT_CODE=7") {
		t.Fatalf("%d %q %q", code, out.String(), errout.String())
	}
}
func TestGateCaptureRetainsResultBeforeLargeLogs(t *testing.T) {
	var capture gateCapture
	_, _ = capture.Write([]byte("RES"))
	_, _ = capture.Write([]byte("ULT=example-run\n"))
	_, _ = capture.Write([]byte(strings.Repeat("x", 300<<10)))
	if capture.result() != "example-run" || len(capture.data) > 256<<10 {
		t.Fatalf("result=%q length=%d", capture.result(), len(capture.data))
	}
}
