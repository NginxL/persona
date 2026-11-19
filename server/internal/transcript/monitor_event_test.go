package transcript

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestMonitorEventsKeepBackgroundTasks(t *testing.T) {
	for _, envelope := range []string{"user", "attachment", "queue-operation"} {
		t.Run(envelope, func(t *testing.T) {
			var meta Meta
			line := 0
			parse := func(record map[string]any) {
				t.Helper()
				line++
				record["timestamp"] = "2026-01-01T00:00:00Z"
				data, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				ParseLine(line, data, &meta)
			}
			launch := func(id, tool, result string) {
				parse(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "id": id, "name": tool, "input": map[string]any{
						"command": "tail -f build.log", "description": "Watch the build", "run_in_background": true,
					}},
				}}})
				parse(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": id, "content": result},
				}}})
			}
			notice := func(xml string) {
				t.Helper()
				switch envelope {
				case "user":
					parse(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": xml}})
				case "attachment":
					parse(map[string]any{"type": "attachment", "attachment": map[string]any{"type": "queued_command", "prompt": xml}})
				case "queue-operation":
					parse(map[string]any{"type": "queue-operation", "operation": "enqueue", "content": xml})
				}
			}
			launch("tm1", "Monitor", "Monitor started (task m1, expires in 30m unless the source ends first)")
			launch("tm2", "Monitor", "Monitor started (task m2, expires in 10m unless the source ends first)")
			launch("ts", "Bash", "Command running in background with ID: b1. Output is being written to: /tmp/example.output")
			launch("ta", "Agent", "Async agent launched successfully. agentId: a1")
			if len(meta.Background) != 4 || meta.Background[0].ID != "m1" || meta.Background[1].ID != "m2" ||
				meta.Background[0].Description != "Watch the build" || meta.Background[0].Command != "tail -f build.log" ||
				meta.Background[0].ExpiresAt == nil || !meta.Background[0].ExpiresAt.Equal(time.Date(2026, 1, 1, 0, 30, 0, 0, time.UTC)) {
				t.Fatalf("launch inventory: %+v", meta.Background)
			}
			want := append(meta.Background[:0:0], meta.Background...)
			for i, id := range []string{"m1", "m1", "m2", "unknown"} {
				notice(`<task-notification><task-id>` + id + `</task-id><summary>Monitor event: "Watch the build"</summary><event>build ` + strconv.Itoa(i) + ` completed; another stream ended</event></task-notification>`)
				if !reflect.DeepEqual(meta.Background, want) {
					t.Fatalf("event for %s changed background inventory:\n got %+v\nwant %+v", id, meta.Background, want)
				}
			}
			// A running agent's notice remains nonterminal, just as before.
			notice(`<task-notification><task-id>a1</task-id><status>running</status><summary>Agent "Review" is waiting</summary></task-notification>`)
			if !reflect.DeepEqual(meta.Background, want) {
				t.Fatal("a nonterminal agent notice removed background work")
			}
			// True monitor endings remove only the named monitor, leaving other kinds intact.
			notice(`<task-notification><task-id>m1</task-id><status>completed</status><summary>Monitor "Watch the build" stream ended</summary><event>final output</event></task-notification>`)
			want = want[1:]
			if !reflect.DeepEqual(meta.Background, want) {
				t.Fatalf("stream end inventory: got %+v, want %+v", meta.Background, want)
			}
			notice(`<task-notification><task-id>m2</task-id><status>stopped</status><summary>Monitor "Watch the build" stopped</summary></task-notification>`)
			want = want[1:]
			if !reflect.DeepEqual(meta.Background, want) {
				t.Fatalf("stopped inventory: got %+v, want %+v", meta.Background, want)
			}
			for _, xml := range []string{
				`<task-notification><task-id>b1</task-id><status>completed</status><summary>Background command "build" completed</summary></task-notification>`,
				`<task-notification><task-id>a1</task-id><status>completed</status><summary>Agent "Review" completed</summary></task-notification>`,
			} {
				notice(xml)
			}
			if len(meta.Background) != 0 {
				t.Fatalf("completed command/agent remain: %+v", meta.Background)
			}
		})
	}
}
