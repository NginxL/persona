package db

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestCompareSettingRejectsStaleAndConcurrentWrites(t *testing.T) {
	d, ctx := open(t), context.Background()
	value := func(rev int64, model string) any {
		return map[string]any{"revision": rev, "binding": map[string]string{"model": model}}
	}
	if ok, err := d.CompareSetting(ctx, "model_config.test", 0, value(1, "first")); err != nil || !ok {
		t.Fatalf("create %v %v", ok, err)
	}
	if ok, err := d.CompareSetting(ctx, "model_config.test", 0, value(1, "stale")); err != nil || ok {
		t.Fatalf("stale %v %v", ok, err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := d.CompareSetting(ctx, "model_config.test", 1, value(2, "winner"))
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("concurrent winners: %d", wins)
	}
	if ok, err := d.CompareSetting(ctx, "model_config.other", 0, value(1, "other")); err != nil || !ok {
		t.Fatalf("independent key %v %v", ok, err)
	}
	var r struct{ Revision int64 }
	if _, err := d.Setting(ctx, "model_config.test", &r); err != nil || r.Revision != 2 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestNodeModelMetadataIsPersisted(t *testing.T) {
	d, ctx := open(t), context.Background()
	s := NodeSession{Launcher: "claude", Cwd: "/tmp/assistant", StartedAt: time.Now().UTC(), Model: "specific", ModelFingerprint: "fingerprint"}
	if err := d.PutNodeSession(ctx, "machine", "session", s); err != nil {
		t.Fatal(err)
	}
	out, err := d.NodeSessions(ctx, "machine")
	if err != nil {
		t.Fatal(err)
	}
	if got := out["session"]; got.Model != s.Model || got.ModelFingerprint != s.ModelFingerprint {
		t.Fatalf("lost model: %+v", got)
	}
	s.Model, s.ModelFingerprint = "", ""
	if err := d.PutNodeSession(ctx, "machine", "session", s); err != nil {
		t.Fatal(err)
	}
	out, err = d.NodeSessions(ctx, "machine")
	if err != nil || out["session"].Model != "" {
		t.Fatalf("failed to restore default: %+v %v", out, err)
	}
}

// The mode the app set on an assistant conversation comes back after a node restart (#2416).
func TestNodeAppModesArePersisted(t *testing.T) {
	d, ctx := open(t), context.Background()
	s := NodeSession{Launcher: "claude", Cwd: "/tmp/assistant", StartedAt: time.Now().UTC(), AppMode: "bypassPermissions", AppModeExplicit: true, AppEffort: "high"}
	if err := d.PutNodeSession(ctx, "machine", "session", s); err != nil {
		t.Fatal(err)
	}
	out, err := d.NodeSessions(ctx, "machine")
	if err != nil {
		t.Fatal(err)
	}
	if got := out["session"]; got.AppMode != s.AppMode || got.AppModeExplicit != s.AppModeExplicit || got.AppEffort != s.AppEffort {
		t.Fatalf("lost app modes: %+v", got)
	}
}

// PutSettings writes all its keys or none: a model profile's switch is never half applied.
func TestPutSettingsIsAllOrNothing(t *testing.T) {
	d, ctx := open(t), context.Background()
	if err := d.PutSettings(ctx, map[string]any{"model_config.a": map[string]int{"revision": 1}, "model_profiles": []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	var r struct{ Revision int64 }
	if _, err := d.Setting(ctx, "model_config.a", &r); err != nil || r.Revision != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	// A value that cannot be stored fails the whole write, before anything is written.
	err := d.PutSettings(ctx, map[string]any{"model_config.a": map[string]int{"revision": 2}, "model_config.b": func() {}})
	if err == nil {
		t.Fatal("an unmarshalable value was written")
	}
	if _, err := d.Setting(ctx, "model_config.a", &r); err != nil || r.Revision != 1 {
		t.Fatalf("half written: %+v %v", r, err)
	}
	// A failing statement rolls the earlier ones back: "\u0000" is valid JSON Postgres' jsonb refuses.
	err = d.PutSettings(ctx, map[string]any{"model_config.a": map[string]int{"revision": 3}, "model_config.z": "\u0000"})
	if err == nil {
		t.Fatal("jsonb took a NUL")
	}
	if _, err := d.Setting(ctx, "model_config.a", &r); err != nil || r.Revision != 1 {
		t.Fatalf("not rolled back: %+v %v", r, err)
	}
	if ok, _ := d.Setting(ctx, "model_config.z", &r); ok {
		t.Fatal("the failing key was written")
	}
}

func TestSettingsPrefix(t *testing.T) {
	d, ctx := open(t), context.Background()
	values := map[string]any{
		"model_config.unknown":               map[string]any{"revision": 3, "binding": map[string]string{"model": "sonnet"}},
		"model_config.overrides.v1.task.old": map[string]any{"revision": 4, "binding": map[string]string{"provider": "launcher.chat"}},
		"model_config_future":                "excluded",
		"modelXconfig.test":                  "excluded",
		"model_profiles":                     "excluded",
	}
	if err := d.PutSettings(ctx, values); err != nil {
		t.Fatal(err)
	}
	got, err := d.SettingsPrefix(ctx, "model_config.")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("keys = %v", got)
	}
	for _, key := range []string{"model_config.unknown", "model_config.overrides.v1.task.old"} {
		var want, value any
		raw, _ := json.Marshal(values[key])
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(got[key], &value); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(value, want) {
			t.Fatalf("%s = %s, want %s", key, got[key], raw)
		}
	}
}

// record written without one has none (#5035).
func TestNodePendingSwitchIsPersisted(t *testing.T) {
	d, ctx := open(t), context.Background()
	pending := `{"launcher":"work","model":"opus","at":"2026-11-12T10:00:00Z"}`
	s := NodeSession{Launcher: "claude", Cwd: "/tmp/project", StartedAt: time.Now().UTC(), PendingSwitch: json.RawMessage(pending)}
	if err := d.PutNodeSession(ctx, "machine", "session", s); err != nil {
		t.Fatal(err)
	}
	out, err := d.NodeSessions(ctx, "machine")
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(out["session"].PendingSwitch, &got); err != nil {
		t.Fatalf("pending switch %q: %v", out["session"].PendingSwitch, err)
	}
	_ = json.Unmarshal([]byte(pending), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pending switch = %v, want %v", got, want)
	}
	s.PendingSwitch = nil
	if err := d.PutNodeSession(ctx, "machine", "session", s); err != nil {
		t.Fatal(err)
	}
	if out, err = d.NodeSessions(ctx, "machine"); err != nil || out["session"].PendingSwitch != nil {
		t.Fatalf("cleared switch came back: %q %v", out["session"].PendingSwitch, err)
	}
}

// The conversation's earlier stretches after a switch between engines (node/engine_switch.go, #5054) come back after a
// node restart, and a record written without them has none.
func TestNodeEngineHistoryIsPersisted(t *testing.T) {
	d, ctx := open(t), context.Background()
	history := `[{"session_id":"claude-1","agent":"claude","until":"2026-11-12T10:00:00Z"}]`
	s := NodeSession{Launcher: "codex", Cwd: "/tmp/project", StartedAt: time.Now().UTC(), EngineHistory: json.RawMessage(history)}
	if err := d.PutNodeSession(ctx, "machine", "session", s); err != nil {
		t.Fatal(err)
	}
	out, err := d.NodeSessions(ctx, "machine")
	if err != nil {
		t.Fatal(err)
	}
	var got, want []any
	if err := json.Unmarshal(out["session"].EngineHistory, &got); err != nil {
		t.Fatalf("engine history %q: %v", out["session"].EngineHistory, err)
	}
	_ = json.Unmarshal([]byte(history), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("engine history = %v, want %v", got, want)
	}
	s.EngineHistory = nil
	if err := d.PutNodeSession(ctx, "machine", "session", s); err != nil {
		t.Fatal(err)
	}
	if out, err = d.NodeSessions(ctx, "machine"); err != nil || out["session"].EngineHistory != nil {
		t.Fatalf("cleared history came back: %q %v", out["session"].EngineHistory, err)
	}
}

// A switch's handoff waiting for the next message (its first one was held back by a dialog, node/engine_switch.go)
// comes back after a node restart, and a record written without one has none (#5054).
func TestNodePendingHandoffIsPersisted(t *testing.T) {
	d, ctx := open(t), context.Background()
	handoff := `{"text":"what happened","from":"codex","to":"claude","resumed":true}`
	s := NodeSession{Launcher: "claude", Cwd: "/tmp/project", StartedAt: time.Now().UTC(), PendingHandoff: json.RawMessage(handoff)}
	if err := d.PutNodeSession(ctx, "machine", "session", s); err != nil {
		t.Fatal(err)
	}
	out, err := d.NodeSessions(ctx, "machine")
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(out["session"].PendingHandoff, &got); err != nil {
		t.Fatalf("pending handoff %q: %v", out["session"].PendingHandoff, err)
	}
	_ = json.Unmarshal([]byte(handoff), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pending handoff = %v, want %v", got, want)
	}
	s.PendingHandoff = nil
	if err := d.PutNodeSession(ctx, "machine", "session", s); err != nil {
		t.Fatal(err)
	}
	if out, err = d.NodeSessions(ctx, "machine"); err != nil || out["session"].PendingHandoff != nil {
		t.Fatalf("cleared handoff came back: %q %v", out["session"].PendingHandoff, err)
	}
}
