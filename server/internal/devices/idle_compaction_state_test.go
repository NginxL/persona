package devices

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/db/dbtest"
)

func TestIdleCompactionStateSurvivesManagerRestart(t *testing.T) {
	dsn := os.Getenv(dbtest.Env)
	if dsn == "" {
		t.Skip("test database is required")
	}
	ctx := t.Context()
	store, err := db.Open(ctx, dsn, db.AllSets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := MachineConfig{ID: "example-idle-machine"}
	since := time.Now().Add(-31 * time.Minute).UTC().Format(time.RFC3339Nano)
	for _, attempted := range []bool{true, false} {
		idle := map[string]any{
			"policy":    map[string]any{"enabled": true, "idle_minutes": 30, "context_tokens": 300000},
			"since":     since,
			"tokens":    450000,
			"attempted": attempted,
			"future":    map[string]any{"keep": true},
		}
		if !attempted {
			idle["since"] = time.Now().UTC().Format(time.RFC3339Nano)
			idle["tokens"] = 90000
			idle["policy"] = map[string]any{"enabled": false, "idle_minutes": 45, "context_tokens": 600000}
		}
		wire, err := json.Marshal(map[string]any{
			"session_id": "example-idle-main",
			"state": map[string]any{
				"launcher": "claude", "cwd": "/example/main", "is_main": true,
				"started_at": time.Now().UTC(), "idle_compaction": idle,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		m := &Machine{d: New(Options{Store: store}), cfg: cfg}
		m.onEvent("state.put", wire)

		// Cross the real manager decoder, SQL write and a fresh manager/DB
		// connection. A node-only JSON round trip cannot catch a dropped field.
		store.Close()
		reopened, err := db.Open(ctx, dsn, db.AllSets)
		if err != nil {
			t.Fatal(err)
		}
		store = reopened
		m = &Machine{d: New(Options{Store: store}), cfg: cfg}
		out, err := m.onRequest(context.Background(), "state.load", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		var loaded struct {
			Sessions map[string]struct {
				IdleCompaction map[string]any `json:"idle_compaction"`
			} `json:"sessions"`
		}
		if err := json.Unmarshal(raw, &loaded); err != nil {
			t.Fatal(err)
		}
		// Decode the expected wire JSON too, so JSON numbers have matching types.
		wantRaw, _ := json.Marshal(idle)
		var want map[string]any
		if err := json.Unmarshal(wantRaw, &want); err != nil {
			t.Fatal(err)
		}
		if got := loaded.Sessions["example-idle-main"].IdleCompaction; !reflect.DeepEqual(got, want) {
			t.Fatalf("state.load lost idle compaction after restart: got %v, want %v", got, want)
		}
	}
}
