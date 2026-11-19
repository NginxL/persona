import { describe, expect, it } from "vitest";
import { buildTaskIndex } from "./tasks";
import { finishedBackgroundTasks } from "./finished-tasks";
import type { Item, TaskNote } from "./types";

const notice = (id: string, task: TaskNote): Item => ({ id, seq: id.charCodeAt(0) - 96, role: "assistant", kind: "notice", task });

describe("finished background tasks", () => {
  it("counts the latest terminal notice per task, including call-only repeats", () => {
    const items = [
      notice("a", { kind: "shell", task_id: "build", tool_use_id: "call-build", status: "completed", title: "Build" }),
      notice("b", { kind: "shell", task_id: "check", status: "failed", title: "Check" }),
      notice("c", { kind: "shell", tool_use_id: "call-build", status: "completed", title: "Build" }),
    ];
    expect(finishedBackgroundTasks(items, buildTaskIndex(items)).map(({ id, call, title }) => ({ id, call, title }))).toEqual([
      { id: "build", call: "call-build", title: "Build" },
      { id: "check", call: undefined, title: "Check" },
    ]);
  });

  it("keeps agent and workflow notices with their cards and ignores events and resumed tasks", () => {
    const items = [
      notice("a", { kind: "monitor", task_id: "monitor", status: "event" }),
      notice("b", { kind: "agent", task_id: "agent", status: "completed" }),
      notice("c", { kind: "workflow", task_id: "workflow", status: "failed" }),
      notice("d", { kind: "shell", task_id: "shell", status: "completed" }),
      notice("e", { kind: "shell", task_id: "shell", status: "running" }),
      notice("f", { kind: "monitor", task_id: "ended", status: "stopped" }),
    ];
    expect(finishedBackgroundTasks(items, buildTaskIndex(items)).map(({ id }) => id)).toEqual(["ended"]);
  });
});
