package assistant

import (
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// The lineage fields must survive the real snapshot template, not only the API projection: a continuation's nested
// with block changes dot to the linked id, so its link belongs to the task captured as $t (#5732).
func TestSnapshotRendersTakeoverLinks(t *testing.T) {
	text := prompts.RenderSnapshot(prompts.Snapshot{Now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC), Zone: "UTC",
		Tasks: []prompts.SnapshotTask{
			{ID: "todo_old", Title: "Old card", State: "已取消", CancelledReason: "方案调整", SupersededBy: "todo_new", SupersededByLink: "https://hub.example.com/todos?id=todo_new"},
			{ID: "todo_new", Title: "New card", State: "已交付·待看", Continues: "todo_old", ContinuesLink: "https://hub.example.com/todos?id=todo_old"},
		}})
	for _, want := range []string{"已由新卡 todo_new 接手（链接 https://hub.example.com/todos?id=todo_new）",
		"接手自 todo_old（链接 https://hub.example.com/todos?id=todo_old）", "已取消：方案调整"} {
		if !strings.Contains(text, want) {
			t.Errorf("snapshot lacks %q:\n%s", want, text)
		}
	}
}
