// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { InboxItem } from "../../lib/inbox";
import type { Todo } from "../../lib/todos";
import { settleRosterDefault } from "../../lib/manager-choice";
import { InboxDetail } from "./InboxDetail";

let todos: Todo[] = [];
vi.mock("../../lib/store", async (original) => {
  const m = await original<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(select: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => select({ ...s, todos })) };
});
vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: () => undefined }));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const item: InboxItem = { id: "example:event", source: "example", kind: "dm", account: "example", account_name: "Example", title: "A question", preview: "Please investigate", count: 1,
  agent: { state: "drafted", todo_id: "todo_linked", machine_id: "draft-box", session_id: "draft-processing" } };
const todo: Todo = { id: "todo_linked", title: "Investigate the report", created_at: "", updated_at: "", agents: [
  { machine_id: "worker", session_id: "old", title: "Previous round", linked_at: "" },
  { machine_id: "worker", session_id: "current", title: "Current investigation", linked_at: "" },
], lineage: [{ todo_id: "todo_linked", machine: "worker", session_id: "current", current: true, started: "" }] };
let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  settleRosterDefault();
  todos = [todo];
  history.replaceState(null, "", "/inbox/example%3Aevent");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); });
const draw = (value = item) => act(() => root.render(<InboxDetail item={value} loaded />));

it("shows the linked task and current business Agent, and both links actually navigate", () => {
  draw();
  const links = [...host.querySelectorAll<HTMLAnchorElement>("[data-inbox-task-links] a")];
  expect(links.map((x) => x.textContent)).toEqual(["关联待办Investigate the report", "关联 AgentCurrent investigation"]);
  expect(links[0].getAttribute("href")).toBe("/todos?id=todo_linked");
  expect(links[1].getAttribute("href")).toBe("/s/worker/current");
  expect(host.querySelector("[data-inbox-task-links]")?.textContent).not.toContain("Previous round");
  expect(host.querySelector("[data-inbox-task-links]")?.textContent).not.toContain("draft-processing");
  act(() => links[0].click()); expect(location.pathname + location.search).toBe("/todos?id=todo_linked");
  act(() => links[1].click()); expect(location.pathname).toBe("/s/worker/current");
});

it("keeps the task link while its Agent is starting and never substitutes an old worker", () => {
  todos = [{ ...todo, lineage: [{ todo_id: todo.id, current: true, started: "" }] }];
  draw();
  expect(host.querySelectorAll("[data-inbox-task-links] a")).toHaveLength(1);
});

it("uses active workers on an older hub without lineage, excluding released and failed Agents", () => {
  todos = [{ ...todo, lineage: undefined, agents: [todo.agents![1], { ...todo.agents![0], released_at: "2026-01-01" }, { ...todo.agents![0], session_id: "failed", start_error: "Unavailable" }] }];
  draw();
  expect(host.querySelectorAll("[data-inbox-task-links] a")).toHaveLength(2);
  expect(host.querySelector("[data-inbox-task-links]")?.textContent).toContain("Current investigation");
});

it("never borrows another manager's same-ID task, and updates when a link is cleared", () => {
  todos = [{ ...todo, manager: "other" }];
  draw();
  expect(host.querySelector("[data-inbox-task-links]")?.textContent).toBe("关联待办todo_linked");
  draw({ ...item, agent: { state: "skipped" } });
  expect(host.querySelector("[data-inbox-task-links]")).toBeNull();
});

it("also shows task links above the event body", () => {
  draw({ ...item, status: "action", recap: "Investigating" });
  expect(host.querySelectorAll("[data-inbox-task-links] a")).toHaveLength(2);
});
