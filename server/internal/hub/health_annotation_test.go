package hub

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestAnnotateHealthSnapshot(t *testing.T) {
	snapshotJSON := `{
		"start": "2026-11-14T00:00:00+08:00",
		"end": "2026-11-14T19:30:00+08:00",
		"days": 7,
		"metrics": ["steps"],
		"data": [{"date": "2026-11-14", "steps": 1200}],
		"source": "snapshot",
		"as_of": "2026-11-14T12:00:00+08:00"
	}`

	t.Run("snapshot answer gets a plain-text note first", func(t *testing.T) {
		original := proto.TextResult(snapshotJSON, false)
		got := annotateHealthSnapshot("phone_health_summary", original)
		if got.IsError || len(got.Content) != 2 {
			t.Fatalf("expected the note plus the original JSON, got %+v", got)
		}
		if !strings.Contains(got.Content[0].Text, "on-device snapshot") ||
			!strings.Contains(got.Content[0].Text, "as_of=2026-11-14T12:00:00+08:00") {
			t.Fatalf("note missing the snapshot wording or as_of: %q", got.Content[0].Text)
		}
		if got.Content[1].Text != snapshotJSON {
			t.Fatalf("the original JSON content must be preserved unchanged")
		}
	})

	t.Run("sample and source snapshots are marked too", func(t *testing.T) {
		samples := `{"type":"heart_rate","samples":[],"source":"snapshot","as_of":"2026-11-23T08:00:00+08:00"}`
		for _, name := range []string{"phone_health_samples", "phone_health_sources"} {
			got := annotateHealthSnapshot(name, proto.TextResult(samples, false))
			if len(got.Content) != 2 || !strings.Contains(got.Content[0].Text, "as_of=2026-11-23T08:00:00+08:00") {
				t.Fatalf("%s: snapshot answer not annotated: %+v", name, got)
			}
			if got.Content[1].Text != samples {
				t.Fatalf("%s: the original JSON content must be preserved unchanged", name)
			}
		}
		if got := annotateHealthSnapshot("phone_health_types", proto.TextResult(samples, false)); len(got.Content) != 1 {
			t.Fatalf("phone_health_types never answers from a snapshot and must pass through: %+v", got)
		}
	})

	t.Run("live answer is untouched", func(t *testing.T) {
		live := proto.TextResult(`{"data":[],"source":"live"}`, false)
		got := annotateHealthSnapshot("phone_health_summary", live)
		if len(got.Content) != 1 || got.Content[0].Text != live.Content[0].Text {
			t.Fatalf("live answer changed: %+v", got)
		}
	})

	t.Run("errors and other tools pass through", func(t *testing.T) {
		locked := proto.TextResult("device_locked: unlock the phone", true)
		if got := annotateHealthSnapshot("phone_health_summary", locked); len(got.Content) != 1 {
			t.Fatalf("error result must not be annotated: %+v", got)
		}
		other := proto.TextResult(snapshotJSON, false)
		if got := annotateHealthSnapshot("phone_device_info", other); len(got.Content) != 1 {
			t.Fatalf("another tool's result must not be annotated: %+v", got)
		}
		malformed := proto.TextResult("not json", false)
		if got := annotateHealthSnapshot("phone_health_summary", malformed); len(got.Content) != 1 {
			t.Fatalf("non-JSON content must pass through: %+v", got)
		}
	})
}
