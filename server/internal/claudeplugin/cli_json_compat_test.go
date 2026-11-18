package claudeplugin

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestCLIDoFallsBackWhenJSONFlagIsUnsupported(t *testing.T) {
	var calls [][]string
	run := func(_ context.Context, _, _ string, args ...string) ([]byte, error) {
		calls = append(calls, slices.Clone(args))
		if slices.Contains(args, "--json") {
			return nil, errors.New("claude plugin marketplace add: unknown option '--json'")
		}
		return []byte("Adding marketplace…✔ Successfully added marketplace"), nil
	}
	if err := (cli{run: run}).do(context.Background(), "plugin", "marketplace", "add", "/tmp/market"); err != nil {
		t.Fatalf("legacy no-JSON retry failed: %v", err)
	}
	if len(calls) != 2 || !slices.Contains(calls[0], "--json") || slices.Contains(calls[1], "--json") {
		t.Fatalf("calls = %#v, want one JSON attempt and one no-JSON retry", calls)
	}
	for i, arg := range []string{"plugin", "marketplace", "add", "/tmp/market"} {
		if calls[1][i] != arg {
			t.Fatalf("retry changed argument %d: %#v", i, calls[1])
		}
	}
}

func TestCLIDoReturnsLegacyRetryFailure(t *testing.T) {
	calls := 0
	run := func(_ context.Context, _, _ string, args ...string) ([]byte, error) {
		calls++
		if slices.Contains(args, "--json") {
			return nil, errors.New("unknown option '--json'")
		}
		return nil, errors.New("not permitted")
	}
	err := (cli{run: run}).do(context.Background(), "plugin", "marketplace", "add", "/tmp/market")
	if err == nil || !strings.Contains(err.Error(), "not permitted") || calls != 2 {
		t.Fatalf("error = %v, calls = %d, want the legacy failure after one retry", err, calls)
	}
}

func TestCLIDoDoesNotRetryUnrelatedErrors(t *testing.T) {
	calls := 0
	run := func(_ context.Context, _, _ string, args ...string) ([]byte, error) {
		calls++
		return nil, errors.New("not permitted")
	}
	err := (cli{run: run}).do(context.Background(), "plugin", "marketplace", "add", "/tmp/market")
	if err == nil || !strings.Contains(err.Error(), "not permitted") || calls != 1 {
		t.Fatalf("error = %v, calls = %d, want no retry", err, calls)
	}
}
