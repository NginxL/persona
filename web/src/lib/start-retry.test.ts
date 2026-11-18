// A planned task whose Agent did not start (#10365): why by its code, read before the hub's text, and the other
// launcher "Use … instead" starts it on. The Retry itself is lib/feed.test.ts's, with retryTask.
import { describe, expect, it } from "vitest";
import { t } from "../i18n";
import type { TaskPlan } from "./feed";
import { startFailureRowLine } from "./start-failure";
import { planTaskOf, retryLauncher, startFailureCodeLine, TASK_RETRY_LAUNCHER_FEATURE, type StartFailureCode } from "./start-retry";
import type { Todo } from "./todos";

describe("a failed start's next steps", () => {
  it("reads the plan task's code before the text: a launcher the terminal can't run, a workspace in the way", () => {
    // the hub's long sentence (the shells, the file it could not run) stays for the details; Chinese text no longer wins
    const long = "示例电脑 上新开的终端标签页（bash）运行不了启动模型入口「work」要用的「/opt/p/personad」……"; // i18n-ignore
    const terminal = { error_code: "launcher_missing", error_args: { where: "terminal", launcher: "work", missing: "/opt/p/personad" } };
    const short = t("lib-start-retry.start_code_launcher_terminal", { launcher: "work" });
    // the Needs you row's line (views/assistant/FailedTaskRow.tsx): the code's, else the text's
    const row = (raw: string, coded?: StartFailureCode) => startFailureCodeLine(coded) ?? startFailureRowLine(raw);
    expect(startFailureCodeLine(terminal)).toBe(short);
    expect(row(long, terminal)).toBe(short);
    expect(row("checkout path exists", { error_code: "worktree_path_taken", error_args: { path: "/w/task" } })).toBe(t("lib-start-retry.start_code_worktree_path_taken"));
    // a launcher missing from the computer's list, or a code the page does not know: the text says it
    expect(startFailureCodeLine({ error_code: "launcher_missing", error_args: { where: "list", launcher: "work" } })).toBeUndefined();
    expect(row("rpc: connection closed", { error_code: "something_new" })).toBe(t("assistant-tasks.start_cause_connection_lost"));
    expect(row(long)).toBe(long);
  });

  it("suggests the plan task's first other launcher only where the hub takes it", () => {
    const task = { title: "t", prompt: "p", machine: "box", cwd: "/w", todo_id: "t1", error: "x", retry_with: ["claude", "work2"] };
    expect(retryLauncher(task, [TASK_RETRY_LAUNCHER_FEATURE])).toBe("claude");
    expect(retryLauncher(task, ["todo_seen"])).toBeUndefined();
    expect(retryLauncher({ ...task, retry_with: [] }, [TASK_RETRY_LAUNCHER_FEATURE])).toBeUndefined();
    // a to-do a plan took over through todo_id has no plan id: the plan that holds it is found all the same
    const todo: Todo = { id: "t1", title: "t", created_at: "2026-11-04T10:00:00Z", updated_at: "2026-11-04T10:00:00Z", origin: "c1", agents: [] };
    const plans: Record<string, TaskPlan> = { p1: { id: "p1", conversation: "c1", state: "started", tasks: [task] } };
    expect(planTaskOf(todo, plans)).toBe(task);
    expect(planTaskOf(todo, { p1: { ...plans.p1, state: "dismissed" } })).toBeUndefined();
  });
});
