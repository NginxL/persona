package claudeplugin

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCLIDoFormatsSubcommandAndPreservesCallerSlice(t *testing.T) {
	backing := make([]string, 0, 8)
	args := append(backing, "plugin", "marketplace", "add", "/tmp/market")
	backing[:5][4] = "sentinel"

	runFailed := func(_ context.Context, _, _ string, got ...string) ([]byte, error) {
		return []byte(`{"outcome":"failed","failureCode":"boom","message":"disk full"}`), errors.New("exit status 1")
	}
	err := (cli{run: runFailed}).do(context.Background(), args...)
	if err == nil {
		t.Fatal("expected error from failed JSON outcome")
	}
	if backing[:5][4] != "sentinel" {
		t.Fatalf("cli.do overwrote caller slice backing array: got %q, want sentinel", backing[:5][4])
	}
	if strings.Contains(err.Error(), "claude plugin plugin:") || !strings.Contains(err.Error(), "claude plugin marketplace add: disk full") {
		t.Fatalf("cli.do error = %q, want 'claude plugin marketplace add: disk full'", err.Error())
	}

	runEmpty := func(_ context.Context, _, _ string, _ ...string) ([]byte, error) {
		return nil, nil
	}
	err = (cli{run: runEmpty}).do(context.Background(), "plugin", "install", "persona@persona")
	if err == nil || strings.Contains(err.Error(), "claude plugin plugin:") || !strings.Contains(err.Error(), "claude plugin install: no result") {
		t.Fatalf("cli.do no-result error = %v, want 'claude plugin install: no result'", err)
	}
}

func TestExecRunnerPropagatesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ExecRunner(t.TempDir())(ctx, os.Args[0], t.TempDir(), "-test.run=^$")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecRunner with canceled caller ctx = %v, want errors.Is(context.Canceled)", err)
	}
	if strings.Contains(err.Error(), "timed out after") {
		t.Fatalf("ExecRunner misreported caller cancellation as timeout: %v", err)
	}
}

func TestNewerHandlesLargeNumericSegments(t *testing.T) {
	if !Newer("2.1.20261124032650123456", "2.1.0") {
		t.Error("Newer(2.1.20261124032650123456, 2.1.0) = false, want true")
	}
	if !Newer("2.1.20261124032650123457", "2.1.20261124032650123456") {
		t.Error("Newer(2.1.20261124032650123457, 2.1.20261124032650123456) = false, want true")
	}
	if Newer("2.1.0020261124032650123456", "2.1.20261124032650123456") {
		t.Error("Newer with leading zeros on equal segment = true, want false")
	}
}
