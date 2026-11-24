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

it("shows every explicitly sourced task and its current Agent after reload, without needing the legacy link", () => {
  todos = [
    { ...todo, inbox_item: item.id },
    { ...todo, id: "todo_second", title: "Check the fix", inbox_item: item.id,
      lineage: [{ todo_id: "todo_second", current: true, machine: "worker", session_id: "verify", agent: "Verification", started: "" }] },
    { ...todo, id: "unrelated", title: "Unrelated work" },
    { ...todo, id: "other_item", inbox_item: "other:event", title: "Another item" },
    { ...todo, id: "other_manager", manager: "other", inbox_item: item.id, title: "Another manager" },
  ];
  draw({ ...item, agent: undefined });
  const links = [...host.querySelectorAll<HTMLAnchorElement>("[data-inbox-task-links] a")];
  expect(links.map((x) => x.getAttribute("href"))).toEqual([
    "/todos?id=todo_linked", "/s/worker/current", "/todos?id=todo_second", "/s/worker/verify",
  ]);
  expect(host.querySelector("[data-inbox-task-links]")?.textContent).not.toMatch(/Unrelated|Another|Previous|draft-processing/);
  act(() => root.unmount());
  root = createRoot(host);
  draw({ ...item, agent: undefined });
  expect(host.querySelectorAll("[data-inbox-task-links] a")).toHaveLength(4);
  act(() => host.querySelectorAll<HTMLAnchorElement>("[data-inbox-task-links] a")[3].click());
  expect(location.pathname).toBe("/s/worker/verify");
});

it("deduplicates the legacy task link and follows its replacement worker", () => {
  todos = [{ ...todo, inbox_item: item.id, lineage: [
    { todo_id: todo.id, machine: "worker", session_id: "current", current: false, started: "" },
    { todo_id: todo.id, machine: "worker", session_id: "replacement", agent: "Replacement", current: true, started: "" },
  ] }];
  draw();
  const links = [...host.querySelectorAll<HTMLAnchorElement>("[data-inbox-task-links] a")];
  expect(links.map((x) => x.getAttribute("href"))).toEqual(["/todos?id=todo_linked", "/s/worker/replacement"]);
});

it("keeps source-only relationships scoped to the item's manager", () => {
  todos = [{ ...todo, inbox_item: item.id, manager: "other" }];
  draw({ ...item, agent: undefined });
  expect(host.querySelector("[data-inbox-task-links]")).toBeNull();
  draw({ ...item, manager: "other", agent: undefined });
  expect(host.querySelectorAll("[data-inbox-task-links] a")).toHaveLength(2);
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

it("renders compact auto-forward badge, linked to-do fallback title, and fallback reason", () => {
  todos = [];
  draw({
    ...item,
    agent: undefined,
    todo_forward: {
      todo_id: "todo_fwd_1",
      todo_title: "Fix PR #42",
      canonical_url: "https://github.com/example/repo/pull/42",
      match_kind: "pull_request",
      status: "fallback",
      reason: "machine box is offline",
      forwarded_at: "2026-11-21T12:00:00Z",
    },
  });
  const badge = host.querySelector("[data-inbox-todo-forward='fallback']");
  expect(badge).not.toBeNull();
  expect(badge?.textContent).toContain("machine box is offline");
  const links = [...host.querySelectorAll<HTMLAnchorElement>("[data-inbox-task-links] a")];
  expect(links).toHaveLength(1);
  expect(links[0].getAttribute("href")).toBe("/todos?id=todo_fwd_1");
  expect(links[0].textContent).toContain("Fix PR #42");
});
