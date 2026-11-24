// @vitest-environment jsdom
// Opening the review panel again while it is up (a changed-files card's Review or file, #5157) brings its scope and its
// file with it; a card's file the review does not list, or a review that cannot be read, opens the file instead.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  (globalThis as { CSS?: unknown }).CSS ??= { escape: (s: string) => s.replace(/["\\]/g, "\\$&") };
});

const calls = vi.hoisted(() => ({ targets: [] as { scope: string }[], fail: false }));
vi.mock("../../lib/session-review", async (importOriginal) => {
  const real = await importOriginal<typeof import("../../lib/session-review")>();
  return {
    ...real,
    loadReview: (_mid: string, _cwd: string, target: { scope: string }) => {
      calls.targets.push({ scope: target.scope });
      if (calls.fail) return Promise.reject(new Error("not a Git repository"));
      return Promise.resolve({
        root: "/w",
        scope: target.scope,
        base: { hash: "a1b2c3d4", short: "a1b2c3d", ref: "origin/main" },
        head: { hash: "e5f6a7b8", short: "e5f6a7b", branch: "work" },
        files: [{ path: "src/a.ts", status: "modified", added: 1, deleted: 1, hunks: [{ old_start: 1, new_start: 1, lines: ["-a", "+b"] }] }],
      });
    },
  };
});
vi.mock("../../lib/use-fs-live", () => ({ useFsLive: () => {} }));

import { closePanel, getPanel, getPanelHistory, openPanel, type ReviewSpot } from "../../lib/panel";
import type { ReviewScope } from "../../lib/session-review";
import { ReviewPanel } from "./ReviewPanel";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  calls.targets = [];
  calls.fail = false;
  closePanel();
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  Element.prototype.scrollIntoView = vi.fn();
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const settle = () => act(async () => { for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0)); });
const draw = (o: { scope?: ReviewScope; at?: ReviewSpot; opened: object }) =>
  act(() => root.render(<ReviewPanel mid="m" sid="s" cwd="/w" scope={o.scope} at={o.at} opened={o.opened} onClose={() => {}} />));

describe("the review panel opened again", () => {
  it("takes the new opening's scope while it is up, and goes to a file the review lists", async () => {
    draw({ opened: {} });
    await settle();
    expect(calls.targets.at(-1)).toEqual({ scope: "uncommitted" });
    draw({ scope: "branch", at: { path: "/w/src/a.ts", file: true }, opened: {} });
    await settle();
    expect(calls.targets.at(-1)).toEqual({ scope: "branch" });
    expect(getPanel()).toBeNull();
    await act(async () => { await new Promise((r) => requestAnimationFrame(() => r(null))); });
    const went = (Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>).mock.contexts as Element[];
    expect(went.some((el) => el.getAttribute("data-diff-path") === "src/a.ts")).toBe(true);
  });

  it("opens a card's file the review does not list in the files panel instead, preserving back stack", async () => {
    openPanel({ kind: "task", todo: "todo-1" });
    openPanel({ kind: "review", mid: "m", sid: "s", cwd: "/w", scope: "branch" }, { push: true });
    draw({ opened: {} });
    await settle();
    draw({ scope: "branch", at: { path: "/w/src/other.ts", file: true }, opened: {} });
    await settle();
    expect(getPanel()).toEqual({ kind: "file", mid: "m", path: "/w/src/other.ts", cwd: "/w", sid: "s" });
    expect(getPanelHistory()).toEqual([{ kind: "task", todo: "todo-1" }]);
  });

  it("opens a card's file in the files panel when the review cannot be read (not a repository)", async () => {
    calls.fail = true;
    draw({ scope: "branch", at: { path: "/w/notes.md", file: true }, opened: {} });
    await settle();
    expect(getPanel()).toEqual({ kind: "file", mid: "m", path: "/w/notes.md", cwd: "/w", sid: "s" });
  });

  it("leaves a comment's spot (no file fallback) where it was: no other panel opens", async () => {
    draw({ at: { path: "src/missing.ts", line: 3 }, opened: {} });
    await settle();
    expect(getPanel()).toBeNull();
  });
});
