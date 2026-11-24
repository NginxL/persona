package recording

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
)

const (
	askOne   = "a0000000-0000-4000-8000-000000000001"
	askTwo   = "a0000000-0000-4000-8000-000000000002"
	askThree = "a0000000-0000-4000-8000-000000000003"
	earlier  = "55555555-5555-4555-8555-555555555555"
)

const (
	callOne = "c0000000-0000-4000-8000-000000000001"
	callTwo = "c0000000-0000-4000-8000-000000000002"
)

// mixedLines are sentences as a meeting says them: Chinese, English, and both in one sentence.
var mixedLines = []fakeLine{
	{0.5, 1.5, "我们这周先把 export cap 保持在 4 MB。"}, // i18n-ignore
	{2.0, 3.0, "Bo owns the vendor follow-up."},
	{3.5, 4.5, "下周三之前 Alice 要把 pricing 的 proposal 发出来。"}, // i18n-ignore
}

// longAnswer is a meeting in which Persona's answer (the second and third sentences) takes longer than the hold.
var longAnswer = []fakeLine{
	{0.5, 1.5, "我们先看 export cap。"}, // i18n-ignore
	{2.0, 3.0, "The cap stays at four megabytes this week."},
	{3.5, 4.5, "Bo sends the vendor notes on Friday."},
	{5.5, 6.5, "下周三之前 Alice 要把 pricing 的 proposal 发出来。"}, // i18n-ignore
}

func askPath(id string) string { return "/api/v1/recordings/" + id + "/ask" }

// meetingAskOf is the ask of a POST …/ask answer.
func meetingAskOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	a, ok := body["ask"].(map[string]any)
	if !ok {
		t.Fatalf("no ask: %v", body)
	}
	return a
}

// liveTwo are the live job's sentences of the final pass tests.
var liveTwo = []fakeLine{{0.5, 1.5, "Live one."}, {4.2, 5.0, "Live two."}}

// transcriptionProblemFixtures are the cases the hub and the iOS app share (spec/fixtures/meeting-transcription-problems).
const transcriptionProblemFixtures = "../../../../spec/fixtures/meeting-transcription-problems/cases.json"

func waitFor(t testing.TB, what string, ok func() bool) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(5 * time.Second)
	for !ok() {
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "prompts", name+".golden")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := writeGolden(path, got); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (UPDATE_GOLDEN=1 writes it)", name, err)
	}
	if got != string(want) {
		t.Errorf("%s differs from %s\n--- got ---\n%s\n--- want ---\n%s", name, path, got, want)
	}
}

// writeGolden does not let a narrowly selected partition's incomplete render erase a real fixture (#12482).
func writeGolden(path, got string) error {
	if got == "" {
		if old, err := os.ReadFile(path); err == nil && len(old) > 0 {
			return fmt.Errorf("%s: refusing to replace a nonempty golden with an empty render", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(got), 0o644)
}

// The core's names these tests use (the assistant's origins.go, health.go and titles.go say the same).
const (
	OriginChannel   = "channel"
	OriginInbox     = "inbox"
	OriginLarkAgent = "lark_agent"
	OriginExtension = "extension"
	OriginTask      = "task"
	SpaceHealth     = identity.SpaceHealth
	titleByTask     = "task"
)
