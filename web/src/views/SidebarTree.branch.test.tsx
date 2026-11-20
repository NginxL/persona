// @vitest-environment jsdom
// A By to-do branch row on a touch screen (#11511): it was 26 px tall and only its chevron and title (a tenth of its width)
// folded it; a finger on its count, its state or the room between did nothing. Now it is 44 px tall and the chevron's
// touch-hit square takes the whole row: the chevron's button is unpositioned there, so the square's box is the row, which
// is positioned. The controls with taps of their own stay above it, being positioned and later in the row: the to-do's
// title link (its own touch-hit, the words truncating inside it, which would clip a square on it) and the ⓘ. jsdom does
// not hit-test; scripts/test/web_mobile_shell_e2e.py (agents_list_phone) taps the rows in Chrome.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { buildTodoTree } from "../lib/todo-tree";
import type { Todo } from "../lib/todos";
import type { Machine, Session } from "../lib/types";
import { GroupBranch, TodoTreeView } from "./SidebarTree";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const at = new Date().toISOString();
const machines: Machine[] = [{ id: "m1", name: "Mac", online: true }];
const s = (sid: string, state: Session["state"]): Session => ({ machine_id: "m1", session_id: sid, title: `agent ${sid}`, cwd: "/code/weather", status: state === "working" ? "working" : "idle", state, updated_at: at });
const todos: Todo[] = [{ id: "ship", title: "Ship 1.3", created_at: at, updated_at: at, agents: [{ machine_id: "m1", session_id: "a", linked_at: at }] }];
const stable = { reg: () => () => {}, hover: () => {}, go: () => () => {}, onRowMenu: () => {} };

let host: HTMLDivElement;
let root: Root;
const row = (key: string) => host.querySelector<HTMLElement>(`[data-tree-branch="${key}"] [data-tree-branch-row]`)!;
const chevron = (key: string) => row(key).querySelector<HTMLButtonElement>("button[aria-expanded]")!;
const classes = (el: Element | null | undefined) => el?.className.split(/\s+/) ?? [];
const COARSE_STATIC = "[@media(pointer:coarse)]:static";

beforeEach(() => {
  localStorage.clear();
  history.replaceState(null, "", "/agents");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("touch-hit's square", () => {
  it("is the box it sits in once that box is 44 px or more each way: so an unpositioned button's is the row", () => {
    const css = readFileSync(join(__dirname, "../styles.css"), "utf8");
    const rule = css.match(/@utility touch-hit \{([\s\S]*?)\n\}/)?.[1] ?? "";
    for (const side of ["top", "bottom", "left", "right"]) expect(rule).toContain(`${side}: min(0px, calc(50% - 22px));`);
  });
});

describe("a group's row", () => {
  const onToggle = vi.fn();
  const render = () =>
    act(() => root.render(<GroupBranch branch="todo:none" name="没挂待办的" hint="Agents on no to-do." icon="layers" n={3} state="working" folded onToggle={onToggle} />));

  it("is 44 px on a touch screen, the chevron's square taking all of it, under the ⓘ", () => {
    render();
    expect(classes(row("todo:none"))).toEqual(expect.arrayContaining(["relative", "[@media(pointer:coarse)]:min-h-11"]));
    expect(classes(chevron("todo:none"))).toEqual(expect.arrayContaining(["touch-hit", COARSE_STATIC]));
    // nothing positioned between the row and the chevron, which would take the square's box instead
    expect(chevron("todo:none").parentElement).toBe(row("todo:none"));
    const info = row("todo:none").querySelector<HTMLButtonElement>('button[aria-label="关于「没挂待办的」"]')!;
    expect(classes(info)).toEqual(expect.arrayContaining(["relative", "z-10"]));
    // the title folds as the chevron does, whichever of them a finger lands on
    const title = [...row("todo:none").querySelectorAll("button")].find((el) => el.textContent === "没挂待办的")!;
    onToggle.mockClear();
    act(() => title.click());
    act(() => chevron("todo:none").click());
    expect(onToggle).toHaveBeenCalledTimes(2);
  });
});

describe("a to-do's row", () => {
  const show = () => {
    const tree = buildTodoTree([s("a", "working")], machines, todos, { spaces: [], sessions: [] }, Date.now(), { keepEmpty: true });
    act(() => root.render(<TodoTreeView tree={tree} machines={machines} activeKey="" {...stable} />));
  };

  it("keeps its title link's own taps above the chevron's square, in the touch row's height", () => {
    show();
    expect(classes(chevron("todo:ship"))).toContain(COARSE_STATIC);
    const link = row("todo:ship").querySelector<HTMLAnchorElement>("a[href]")!;
    // positioned (touch-hit), after the chevron: painted above its square; its words truncate inside, not the link
    expect(classes(link)).toContain("touch-hit");
    expect(classes(link)).not.toContain("truncate");
    expect(classes(link.firstElementChild)).toContain("truncate");
    expect(link.textContent).toBe("Ship 1.3");
    expect(chevron("todo:ship").compareDocumentPosition(link) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("opens the to-do from its title, without folding", () => {
    show();
    const open = chevron("todo:ship").getAttribute("aria-expanded");
    act(() => row("todo:ship").querySelector<HTMLAnchorElement>("a[href]")!.click());
    expect(location.pathname + location.search).toBe("/todos?id=ship");
    expect(chevron("todo:ship").getAttribute("aria-expanded")).toBe(open);
  });
});
