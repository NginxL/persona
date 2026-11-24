package node

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

const (
	indexIDs    = "- 开放待办索引（仅列 ID）："
	indexTitles = "- 开放待办索引（列出 ID 和标题）："
	indexCount  = "- 开放待办：共 %d 件（完整列表用 todo_list）"
)

// openTodoIndex renders an open to-do index of n to-dos (todo_1 … todo_n, the newest first) with or without titles, and the
// count section above it, as prompts/assistant/snapshot.md does.
func openTodoIndex(n int, titles bool, extra ...string) string {
	head := indexIDs
	if titles {
		head = indexTitles
	}
	items := append([]string(nil), extra...)
	for i := n; i >= 1; i-- {
		item := fmt.Sprintf("todo_%d", i)
		if titles {
			item += fmt.Sprintf("「巡检任务 %d」", i)
		}
		items = append(items, item)
	}
	return snapshotOf("- 现在：2026-11-23 周一 10:00（UTC）", fmt.Sprintf(indexCount, n+len(extra)), head+strings.Join(items, "；"))
}

// The open to-do index is told again only by the to-dos that came or went: before, every message that changed any of
// it carried all 60 ids again.
func TestContextDeltaOpenTodoIndexTellsOnlyAddedAndRemoved(t *testing.T) {
	first := openTodoIndex(60, false)
	block, seen := contextDelta(nil, first)
	if !strings.Contains(block, "todo_60；todo_59") {
		t.Fatalf("a new conversation gets the whole index:\n%s", block)
	}

	// Nothing changed: the index is not told.
	block, seen = contextDelta(seen, first)
	if strings.Contains(block, "开放待办索引") {
		t.Fatalf("unchanged index told:\n%s", block)
	}

	// One came, one went: only those, and the count line.
	next := strings.Replace(openTodoIndex(60, false, "todo_61"), "；todo_7；", "；", 1)
	next = strings.Replace(next, fmt.Sprintf(indexCount, 61), fmt.Sprintf(indexCount, 60), 1)
	block, seen = contextDelta(seen, next)
	if !strings.Contains(block, "- 开放待办索引"+rowsMark+"：\n  - todo_61\n"+rowsRemoved+"todo_7）") ||
		strings.Contains(block, "todo_59") || strings.Contains(block, rowsOrder) {
		t.Fatalf("one in, one out:\n%s", block)
	}
	if n := utf8.RuneCountInString(block); n > 250 {
		t.Fatalf("the block is %d characters:\n%s", n, block)
	}

	// The snapshot switches to titles (more room this time): the same to-dos, nothing to tell.
	titled := strings.Replace(openTodoIndex(60, true, "todo_61"), "；todo_7「巡检任务 7」；", "；", 1)
	titled = strings.Replace(titled, fmt.Sprintf(indexCount, 61), fmt.Sprintf(indexCount, 60), 1)
	block, seen = contextDelta(seen, titled)
	if strings.Contains(block, "开放待办索引") {
		t.Fatalf("a change of rendering alone was told:\n%s", block)
	}

	// With titles on both sides, a flag that changed is told by its item.
	flagged := strings.Replace(titled, "todo_5「巡检任务 5」", "todo_5「巡检任务 5」（没开起来）", 1)
	block, _ = contextDelta(seen, flagged)
	if !strings.Contains(block, "\n  - todo_5「巡检任务 5」（没开起来）") || strings.Contains(block, "todo_6") {
		t.Fatalf("changed flag:\n%s", block)
	}
}

// An index a title with "；" splits cannot be read by its items: it is told whole, as before.
func TestContextDeltaOpenTodoIndexUnreadableIsWhole(t *testing.T) {
	old := snapshotOf("- 现在：2026-11-23 周一 10:00（UTC）", indexTitles+"todo_2「甲；乙」；todo_1「丙」")
	_, seen := contextDelta(nil, old)
	next := snapshotOf("- 现在：2026-11-23 周一 10:01（UTC）", indexTitles+"todo_3「丁」；todo_2「甲；乙」；todo_1「丙」")
	block, _ := contextDelta(seen, next)
	if !strings.Contains(block, "\n"+indexTitles+"todo_3「丁」；todo_2「甲；乙」；todo_1「丙」\n") {
		t.Fatalf("not whole:\n%s", block)
	}
}

// The trailer after the listed items ("其余用 todo_list 查") is not an item; the items before it still are.
func TestInlineItemsTrailer(t *testing.T) {
	head, items, ids, ok := inlineItems(indexIDs+"todo_2；todo_1（没开起来）；其余用 todo_list 查", inlineTodo)
	if !ok || head != "- 开放待办索引（仅列 ID）" || len(items) != 2 || strings.Join(ids, ",") != "todo_2,todo_1" || items["todo_1"] != "todo_1（没开起来）" {
		t.Fatalf("head %q items %v ids %v ok %v", head, items, ids, ok)
	}
	if _, _, _, ok := inlineItems(indexIDs+"todo_2；todo_2", inlineTodo); ok {
		t.Fatal("a repeated id read as items")
	}
}
