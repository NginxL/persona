import { taskSubject, type TaskIndex } from "./tasks";
import type { Item, TaskNote } from "./types";

export interface FinishedTask {
  id: string;
  task: TaskNote;
  title: string;
  call?: string;
  at?: string;
  startedAt?: string;
}

/** Terminal notices in the loaded transcript, newest first, with repeated notices for the same task counted once. */
export function finishedBackgroundTasks(items: Item[], index: TaskIndex): FinishedTask[] {
  const taskIds = new Map<string, string>();
  for (const [call, notes] of index.notes) {
    const id = notes.findLast((note) => note.task_id)?.task_id;
    if (id) taskIds.set(call, id);
  }
  const finished = new Map<string, FinishedTask>();
  for (const item of items) {
    const task = item.task;
    if (!task || task.kind === "agent" || task.kind === "workflow") continue;
    const call = index.calls.get(item.id) ?? item.tool_use_id ?? task.tool_use_id;
    const key = task.task_id || (call && (taskIds.get(call) || call)) || item.id;
    if (task.status === "running" || task.status === "waiting") {
      finished.delete(key);
      continue;
    }
    if (!["completed", "ended", "failed", "killed", "stopped"].includes(task.status)) continue;
    finished.delete(key);
    finished.set(key, {
      id: key,
      task,
      title: taskSubject(task, call ? index.descriptions.get(call) : undefined),
      call,
      at: item.ts,
      startedAt: call ? index.startedAt.get(call) : undefined,
    });
  }
  return [...finished.values()].reverse();
}
