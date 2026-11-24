package larkcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerProfileResolutionGatesEveryProcess(t *testing.T) {
	cfg := fakeConfig(t, "success")
	log := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("LARKCLI_FAKE_LOG", log)
	unread := errors.New("synthetic identity unavailable")
	cfg.ResolveProfile = func(context.Context) (string, error) { return "", unread }
	r := New(cfg)
	ctx := context.Background()
	for _, call := range []func() error{
		func() error { _, e := r.Version(ctx); return e },
		func() error { _, e := r.AuthStatus(ctx); return e },
		func() error { _, e := r.ListChats(ctx, 10); return e },
		func() error { _, e := r.ListMessages(ctx, "oc_example", "", "", 10); return e },
		func() error { _, e := r.Search(ctx, "messages", "example"); return e },
		func() error { _, e := r.StartLogin(ctx); return e },
	} {
		if err := call(); !errors.Is(err, unread) {
			t.Errorf("identity error was lost: %v", err)
		}
	}
	if raw, _ := os.ReadFile(log); len(raw) > 0 {
		t.Fatalf("blocked identity launched CLI: %s", raw)
	}
}

func TestRunnerRetainedInstanceResolvesCurrentProfile(t *testing.T) {
	cfg := fakeConfig(t, "success")
	log := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("LARKCLI_FAKE_LOG", log)
	profile := "other"
	cfg.ResolveProfile = func(context.Context) (string, error) { return profile, nil }
	r := New(cfg)
	for _, next := range []string{"other", "fixed", ""} {
		profile = next
		if _, err := r.ListChats(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "--profile other ") || !strings.HasPrefix(lines[1], "--profile fixed ") || strings.HasPrefix(lines[2], "--profile ") {
		t.Fatalf("retained runner selected stale profiles: %s", raw)
	}
}
