// @vitest-environment jsdom
// The To-dos page's rows say what a waiting to-do waits on (#11090): "Waiting on you" in the attention color only for
// one waiting on its owner, a neutral word for every other wait, and "Paused (nothing needed from you)" when nothing
// says. The data is made up.
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";
import { getState } from "../lib/store";
import type { Todo } from "../lib/todos";
import type { Session, WorkState } from "../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
});

vi.mock("react-dom", async (importOriginal) => ({
  ...(await importOriginal<typeof import("react-dom")>()),
  createPortal: (children: React.ReactNode) => children,
}));

const { TodosView } = await import("./Todos");

const now = new Date().toISOString();
const soon = new Date(Date.now() + 10 * 60_000).toISOString();
const blocked = (sid: string, work: Omit<WorkState, "phase">): Session =>
  ({ machine_id: "m1", session_id: sid, title: sid, cwd: "/work/app", status: "idle", state: "idle", updated_at: now, work: { phase: "blocked", ...work } }) as Session;
const sessions = [
  blocked("ci", { on: "external", line: "Wait for CI", wait: "CI", wake_at: soon }),
  blocked("other", { on: "agent", line: "Wait for the other Agent", wait: "its merge" }),
  blocked("unnamed", { on: "external", line: "", wake_at: soon }),
  { machine_id: "m1", session_id: "bg", title: "bg", cwd: "/work/app", status: "idle", state: "working", background_working: true, updated_at: now } as Session,
];
const todo = (id: string, sid: string, patch: Partial<Todo> = {}): Todo =>
  ({ id, title: `Task ${id}`, created_at: now, updated_at: now, origin: "conv", agents: [{ machine_id: "m1", session_id: sid, linked_at: now }], ...patch });

const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };
afterEach(() => seed(before));

/** Each row's status word and its class, by the row's title. */
function rows(todos: Todo[]): Record<string, { label: string; className: string }> {
  seed({ todos, todosLoaded: true, sessions: { m1: sessions }, interactions: [] });
  const html = renderToStaticMarkup(<TodosView />);
  const out: Record<string, { label: string; className: string }> = {};
  for (const row of html.split('data-todo="').slice(1)) {
    const title = /data-testid="todo-title">([^<]*)</.exec(row)?.[1];
    const status = /<span class="([^"]*)" data-testid="todo-status">([^<]*)<\/span>/.exec(row);
    if (title && status) out[title] = { label: status[2]!, className: status[1]! };
  }
  return out;
}

describe("a waiting to-do's row (#11090)", () => {
  it("says Waiting on you in the attention color only when it waits on its owner", () => {
    const got = rows([
      todo("decide", "ci", { ask: { outcome: "decide", verdict: "Pick a cover", asked_at: now } }),
      todo("held", "bg", { delivery: { state: "merged", gate: "hub update", unblock: "user" } }),
      todo("ci", "ci"),
    ]);
    for (const title of ["Task decide", "Task held"]) {
      expect(got[title]!.label, title).toBe(t("lib-state-labels.waits_owner"));
      expect(got[title]!.className, title).toContain("text-attention");
    }
    expect(got["Task ci"]!.className).not.toContain("text-attention");
  });

  it("names every other wait in the neutral color", () => {
    const got = rows([
      todo("ci", "ci"),
      todo("pr", "ci", { delivery: { state: "pr", gate: "CI", unblock: "other" } }),
      todo("pre", "ci", { prerequisite_blockers: ["a", "b"] }),
      todo("other", "other"),
      todo("bg", "bg"),
      todo("unnamed", "unnamed"),
    ]);
    const want: Record<string, string> = {
      "Task ci": "waits_external", "Task pr": "waits_delivery", "Task pre": "waits_prerequisite", "Task other": "waits_prerequisite",
      "Task bg": "waits_background", "Task unnamed": "waits_unknown",
    };
    for (const [title, key] of Object.entries(want)) {
      expect(got[title]!.label, title).toBe(t(`lib-state-labels.${key}`));
      expect(got[title]!.className, title).toContain("text-ink-3");
      expect(got[title]!.className, title).not.toContain("text-attention");
      expect(got[title]!.className, title).not.toContain("text-orange");
    }
  });

  it("its page says the same in its state pill", () => {
    seed({ todos: [todo("held", "bg", { delivery: { state: "merged", unblock: "user" } }), todo("pr", "ci", { delivery: { state: "pr", unblock: "other" } })], todosLoaded: true, sessions: { m1: sessions }, interactions: [] });
    const pill = (id: string) => {
      const html = renderToStaticMarkup(<TodosView selectedId={id} />);
      const m = /<span class="(inline-flex h-5\.5[^"]*)">(?:<[^>]*>)*?(?:<\/[^>]*>)*([^<]+)<\/span>/.exec(html);
      return m ? { className: m[1]!, label: m[2]! } : null;
    };
    expect(pill("held")).toEqual({ className: expect.stringContaining("text-attention"), label: t("lib-state-labels.waits_owner") });
    expect(pill("pr")).toEqual({ className: expect.stringContaining("text-ink-2"), label: t("lib-state-labels.waits_delivery") });
  });

  it("leaves the other statuses as they were", () => {
    const working = { machine_id: "m1", session_id: "run", title: "run", cwd: "/work/app", status: "working", state: "working", updated_at: now } as Session;
    sessions.push(working);
    try {
      const got = rows([todo("run", "run")]);
      expect(got["Task run"]).toEqual({ label: t("lib-state-labels.working"), className: expect.stringContaining("text-orange") });
    } finally {
      sessions.pop();
    }
  });
});
