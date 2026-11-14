// A real TypeScript module: what a .ts file usually is. Its type comes from its bytes, not its name (#7974).
import { EventEmitter } from "node:events";

export type Priority = "low" | "normal" | "high";

export interface Task<T = unknown> {
  readonly id: string;
  title: string;
  priority: Priority;
  payload?: T;
  due?: Date;
}

export class TaskQueue<T> extends EventEmitter {
  #tasks = new Map<string, Task<T>>();

  add(task: Task<T>): this {
    this.#tasks.set(task.id, task);
    this.emit("added", task);
    return this;
  }

  next(now = new Date()): Task<T> | undefined {
    const rank: Record<Priority, number> = { high: 0, normal: 1, low: 2 };
    return [...this.#tasks.values()]
      .filter((t) => !t.due || t.due <= now)
      .sort((a, b) => rank[a.priority] - rank[b.priority])[0];
  }

  get size(): number {
    return this.#tasks.size;
  }
}

export const overdue = <T,>(tasks: Iterable<Task<T>>, now: Date): Task<T>[] =>
  [...tasks].filter((t) => t.due !== undefined && t.due < now);
