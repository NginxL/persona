// @vitest-environment jsdom
// A project's lessons from finished tasks (#12295): listed with their kind, text, count, source task and whether they
// are still handed out; nothing at all without lessons or on a hub without the route; an edit saves the new text and
// shows the list the hub answers; a delete asks first, with a destructive button; a lesson changed on another device
// shows the hub's current list instead; the footer says whether learning is on.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => { window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia; });
const toast = vi.fn();
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: (...a: unknown[]) => toast(...a) }));
import { initI18n, t } from "../../i18n";
import { ApiError } from "../../lib/api";
const { ProjectLessons } = await import("./ProjectLessons");
import type { ProjectLesson, ProjectLessonsDeps, ProjectLessonsList } from "./ProjectLessons";

let root: Root;
let host: HTMLDivElement;
beforeEach(async () => {
  await initI18n("en");
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  toast.mockReset();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

const recent = new Date(Date.now() - 2 * 3600_000).toISOString();
const LESSONS: ProjectLesson[] = [
  { id: "lesson-a", kind: "missing_context", text: "Use the stock simulator for layout bugs.", seen: 3, last_seen_at: recent, stale: false, edited: false,
    sources: [{ todo_id: "todo_2", title: "Fix the landscape crash" }, { todo_id: "todo_1", title: "Older task" }] },
  { id: "lesson-b", kind: "wasted_step", text: "Skip the full rebuild.", seen: 1, last_seen_at: recent, stale: true, edited: true, sources: [{ todo_id: "todo_3" }] },
];
const listOf = (lessons: ProjectLesson[], enabled = true): ProjectLessonsList => ({ enabled, lessons });

const flush = async () => { for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); }); };
async function mount(deps: ProjectLessonsDeps) {
  await act(async () => root.render(<ProjectLessons deps={deps} current={() => true} />));
  await flush();
}
const buttons = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].filter((b) => b.textContent?.trim() === label);
async function click(b: HTMLElement) { await act(async () => { b.click(); }); await flush(); }
function setValue(el: HTMLTextAreaElement, value: string) {
  const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
  set.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("ProjectLessons", () => {
  it("lists each lesson with its kind, count, source and freshness", async () => {
    await mount({ list: async () => listOf(LESSONS), edit: vi.fn(), remove: vi.fn() });
    const text = host.textContent ?? "";
    expect(text).toContain(t("knowledge-projectlessons.title"));
    expect(text).toContain(t("knowledge-projectlessons.kind_missing_context"));
    expect(text).toContain("Use the stock simulator for layout bugs.");
    expect(text).toContain(t("knowledge-projectlessons.seen", { count: 3 }));
    expect(text).toContain(t("knowledge-projectlessons.from", { task: "Fix the landscape crash" }));
    expect(text).not.toContain("Older task"); // the newest source is the one named
    expect(text).toContain(t("knowledge-projectlessons.stale"));
    expect(text).toContain(t("knowledge-projectlessons.edited"));
    expect(text).toContain(t("knowledge-projectlessons.from", { task: t("knowledge-projectlessons.untitled_task") }));
    expect(text).toContain(t("knowledge-projectlessons.footer_on"));
  });

  it("draws nothing without lessons, or when the hub has no such route", async () => {
    await mount({ list: async () => listOf([]), edit: vi.fn(), remove: vi.fn() });
    expect(host.innerHTML).toBe("");
    act(() => root.unmount());
    root = createRoot(host);
    await mount({ list: async () => { throw new ApiError(404, "not found", {}); }, edit: vi.fn(), remove: vi.fn() });
    expect(host.innerHTML).toBe("");
  });

  it("says learning is off and keeps the lessons editable", async () => {
    await mount({ list: async () => listOf(LESSONS, false), edit: vi.fn(), remove: vi.fn() });
    expect(host.textContent).toContain(t("knowledge-projectlessons.footer_off"));
    expect(buttons(t("knowledge-projectlessons.edit"))).toHaveLength(2);
  });

  it("saves a correction and shows the list the hub answers", async () => {
    const edit = vi.fn(async (_id: string, text: string) => listOf([{ ...LESSONS[0], id: "lesson-a2", text, edited: true }, LESSONS[1]]));
    await mount({ list: async () => listOf(LESSONS), edit, remove: vi.fn() });
    await click(buttons(t("knowledge-projectlessons.edit"))[0]);
    const area = document.querySelector<HTMLTextAreaElement>("textarea")!;
    expect(area.value).toBe(LESSONS[0].text);
    const save = buttons(t("common.save"))[0];
    expect(save.disabled).toBe(true); // nothing changed yet
    await act(async () => setValue(area, "  Reproduce layout bugs on the stock simulator.  "));
    await click(buttons(t("common.save"))[0]);
    expect(edit).toHaveBeenCalledWith("lesson-a", "Reproduce layout bugs on the stock simulator.");
    expect(document.querySelector("textarea")).toBeNull();
    expect(host.textContent).toContain("Reproduce layout bugs on the stock simulator.");
  });

  it("refuses an edit longer than the hub keeps", async () => {
    await mount({ list: async () => listOf(LESSONS), edit: vi.fn(), remove: vi.fn() });
    await click(buttons(t("knowledge-projectlessons.edit"))[0]);
    await act(async () => setValue(document.querySelector<HTMLTextAreaElement>("textarea")!, "x".repeat(201)));
    expect(buttons(t("common.save"))[0].disabled).toBe(true);
    expect(document.body.textContent).toContain(t("knowledge-projectlessons.too_long", { max: 200 }));
  });

  it("shows the current list when the lesson changed on another device", async () => {
    const now = listOf([{ ...LESSONS[0], id: "lesson-a3", seen: 4 }]);
    const edit = vi.fn(async () => { throw new ApiError(409, "changed", { code: "lesson_changed", enabled: true, lessons: now.lessons }); });
    await mount({ list: async () => listOf(LESSONS), edit, remove: vi.fn() });
    await click(buttons(t("knowledge-projectlessons.edit"))[0]);
    await act(async () => setValue(document.querySelector<HTMLTextAreaElement>("textarea")!, "Another wording."));
    await click(buttons(t("common.save"))[0]);
    expect(toast).toHaveBeenCalledWith(t("knowledge-projectlessons.changed_elsewhere"), "info");
    expect(host.textContent).toContain(t("knowledge-projectlessons.seen", { count: 4 }));
    expect(host.textContent).not.toContain("Skip the full rebuild.");
  });

  it("asks before deleting, then shows the list without it; a failed delete says so", async () => {
    let fail = true;
    const remove = vi.fn(async () => {
      if (fail) throw new Error("Synthetic failure");
      return listOf([LESSONS[1]]);
    });
    await mount({ list: async () => listOf(LESSONS), edit: vi.fn(), remove });
    await click(buttons(t("knowledge-projectlessons.delete"))[0]);
    expect(remove).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain(t("knowledge-projectlessons.delete_body"));
    const confirm = () => buttons(t("knowledge-projectlessons.delete")).at(-1)!;
    await click(confirm());
    expect(remove).toHaveBeenCalledWith("lesson-a");
    expect(document.body.textContent).toContain(t("knowledge-projectlessons.delete_failed", { error: "Synthetic failure" }));
    fail = false;
    await click(confirm());
    expect(host.textContent).not.toContain("Use the stock simulator for layout bugs.");
    expect(host.textContent).toContain("Skip the full rebuild.");
  });
});
