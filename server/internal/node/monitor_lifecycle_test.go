package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestBackgroundMonitorEventsAndLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.jsonl")
	n := &Node{bgScans: map[string]*bgScan{}}
	started := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	appendRecord := func(record map[string]any) {
		t.Helper()
		record["timestamp"] = started.Format(time.RFC3339)
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.Write(append(data, '\n'))
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("append: %v, close: %v", err, closeErr)
		}
	}
	launch := func(id, tool, result string) {
		appendRecord(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "tool-" + id, "name": tool, "input": map[string]any{
				"command": "tail -f build.log", "description": "Watch the build", "run_in_background": true,
			}},
		}}})
		appendRecord(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tool-" + id, "content": result},
		}}})
	}
	read := func() []proto.BackgroundTask {
		t.Helper()
		tasks, _, _, pending := n.backgroundTasks(path)
		if pending {
			t.Fatal("small synthetic transcript unexpectedly scanned asynchronously")
		}
		return tasks
	}
	notice := func(xml string) {
		appendRecord(map[string]any{"type": "attachment", "attachment": map[string]any{"type": "queued_command", "prompt": xml}})
	}
	launch("m1", "Monitor", "Monitor started (task m1, expires in 30m unless the source ends first)")
	launch("m2", "Monitor", "Monitor started (task m2, expires in 30m unless the source ends first)")
	launch("expired", "Monitor", "Monitor started (task expired, expires in 1ms unless the source ends first)")
	launch("b1", "Bash", "Command running in background with ID: b1. Output is being written to: /tmp/example.output")
	want := read()
	if len(want) != 3 || want[0].ID != "m1" || want[1].ID != "m2" || want[2].ID != "b1" ||
		want[0].Description != "Watch the build" || want[0].Command != "tail -f build.log" || want[0].ExpiresAt == nil ||
		!want[0].ExpiresAt.Equal(started.Add(30*time.Minute)) {
		t.Fatalf("initial inventory (expired monitor must be absent): %+v", want)
	}
	for i, id := range []string{"m1", "m1", "m2", "unknown"} {
		notice(`<task-notification><task-id>` + id + `</task-id><summary>Monitor event: "Watch the build"</summary><event>build ` + strconv.Itoa(i) + ` passed</event></task-notification>`)
		if got := read(); !reflect.DeepEqual(got, want) {
			t.Fatalf("event for %s changed scanned inventory: got %+v, want %+v", id, got, want)
		}
	}
	notice(`<task-notification><task-id>m1</task-id><status>completed</status><summary>Monitor "Watch the build" stream ended</summary></task-notification>`)
	want = want[1:]
	if got := read(); !reflect.DeepEqual(got, want) {
		t.Fatalf("stream end: got %+v, want %+v", got, want)
	}
	appendRecord(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{
		map[string]any{"type": "tool_use", "id": "stop-m2", "name": "TaskStop", "input": map[string]any{"task_id": "m2"}},
	}}})
	appendRecord(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "stop-m2", "content": "Stopped background task m2"},
	}}})
	want = want[1:]
	if got := read(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TaskStop: got %+v, want %+v", got, want)
	}
}
