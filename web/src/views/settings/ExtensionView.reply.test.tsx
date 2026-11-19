// @vitest-environment jsdom
// What is typed into an extension page's code view boxes (a discussion's reply, a diff thread's reply, a comment on lines,
// the pending review's summary) stays until the extension has taken it: a send that fails or a confirmation cancelled
// leaves the box open with the text, one that succeeds closes it. The hub is faked: every draw and action waits until
// the test answers it.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { loadPages } from "../../lib/lazy-page";
import { t } from "../../i18n";

type Asked = { kind: "draw" | "act"; action?: string; values?: Record<string, string>; answer: (r: unknown) => void };
const hub = vi.hoisted(() => ({ asked: [] as Asked[], toasts: [] as [string, string][] }));

vi.mock("../../lib/extension-ui", async (original) => {
  const real = await original<typeof import("../../lib/extension-ui")>();
  return {
    ...real,
    renderUI: () => new Promise((resolve) => hub.asked.push({ kind: "draw", answer: (v) => resolve(real.readView(v)) })),
    actUI: (_e: string, _u: string, action: string, opts: { values?: Record<string, string> } = {}) =>
      new Promise((resolve, reject) =>
        hub.asked.push({ kind: "act", action, values: opts.values, answer: (r) => (r instanceof Error ? reject(r) : resolve(r ?? {})) }),
      ),
  };
});
vi.mock("../../lib/store", async (original) => ({ ...(await original<typeof import("../../lib/store")>()), onHubEvent: () => () => {} }));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: (text: string, kind: string) => hub.toasts.push([kind, text]) }));
import { ExtensionUI } from "./ExtensionView";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = (q: string) => ({ matches: q.includes("min-width"), addEventListener() {}, removeEventListener() {} });
});

const discussion = (confirm?: string) => ({ type: "comment_thread", title: "Discussion", comments: [{ author: "Ann", body: "Ship it?" }], reply: { id: "reply", ...(confirm ? { confirm } : {}) } });
const diff = {
  type: "diff",
  path: "src/a.go",
  hunks: [{ header: "@@ -1,2 +1,2 @@", old_start: 1, new_start: 1, lines: [" package a", "-var x = 1", "+var x = 2"] }],
  threads: [{ id: "t1", side: "new", line: 2, comments: [{ author: "Ann", body: "Why 2?" }], reply: { id: "reply_line" } }],
  comment: { id: "inline_comment" },
};
const review = { count: 1, name: "body", buttons: [{ title: "Approve", action: { id: "submit_review" } }] };

let host: HTMLDivElement;
let root: Root;
const settle = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const answer = async (a: Asked | undefined, r: unknown) => {
  expect(a).toBeTruthy();
  await act(async () => a!.answer(r));
  await settle();
};
const acts = () => hub.asked.filter((a) => a.kind === "act");
const open = async (view: unknown) => {
  await act(async () => root.render(<ExtensionUI extension="demo" ui="home" history={false} />));
  await answer(hub.asked.find((a) => a.kind === "draw"), view);
};
const click = async (el: Element | null | undefined) => {
  if (!el) throw new Error("not found");
  await act(async () => (el as HTMLElement).click());
  await settle();
};
const button = (within: ParentNode, text: string) => [...within.querySelectorAll("button")].filter((b) => b.textContent?.trim() === text).at(-1);
const type = async (el: HTMLTextAreaElement | null | undefined, text: string) => {
  if (!el) throw new Error("no box");
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const boxes = (within: ParentNode = host) => [...within.querySelectorAll("textarea")].map((b) => b.value);

// the code views are their own chunk (lib/lazy-page.ts): in before anything is drawn
beforeAll(() => loadPages(), 60_000);
beforeEach(() => {
  hub.asked.length = 0;
  hub.toasts.length = 0;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("a code view's box keeps what was typed until the extension takes it", () => {
  it("a discussion's reply that fails stays open with its text; sent again and taken, it closes", async () => {
    await open({ sections: [{ items: [discussion()] }] });
    await click(button(host, t("extensions-codeviews.reply")));
    await type(host.querySelector("textarea"), "Looks good");
    await click(button(host, t("extensions-codeviews.comment")));
    expect(acts()).toHaveLength(1);
    expect(acts()[0]).toMatchObject({ action: "reply", values: { body: "Looks good" } });
    await answer(acts()[0], new Error("rate limited"));
    expect(hub.toasts.some(([kind]) => kind === "error")).toBe(true);
    expect(boxes()).toEqual(["Looks good"]);

    await click(button(host, t("extensions-codeviews.comment")));
    expect(acts()).toHaveLength(2);
    await answer(acts()[1], {});
    expect(boxes()).toEqual([]);
  });

  it("a discussion's reply whose confirmation is cancelled stays open with its text", async () => {
    await open({ sections: [{ items: [discussion("Post this reply?")] }] });
    await click(button(host, t("extensions-codeviews.reply")));
    await type(host.querySelector("textarea"), "Looks good");
    await click(button(host, t("extensions-codeviews.comment")));
    await click(button(document.body, t("common.cancel")));
    expect(acts()).toHaveLength(0);
    expect(boxes()).toEqual(["Looks good"]);
  });

  it("a diff thread's reply that fails stays open with its text", async () => {
    await open({ sections: [{ items: [diff] }] });
    const thread = host.querySelector("[data-thread=t1]")!;
    await click(button(thread, t("extensions-codeviews.reply")));
    await type(thread.querySelector("textarea"), "Because of the limit");
    await click(button(thread, t("extensions-codeviews.comment")));
    expect(acts()[0]).toMatchObject({ action: "reply_line", values: { thread: "t1", body: "Because of the limit" } });
    await answer(acts()[0], new Error("rate limited"));
    expect(boxes(host.querySelector("[data-thread=t1]")!)).toEqual(["Because of the limit"]);
  });

  it("a comment on lines that fails stays open with its text", async () => {
    await open({ sections: [{ items: [diff] }] });
    await click(host.querySelector('[data-diff-path="src/a.go"] [data-ln="new"][data-n="2"]'));
    const d = host.querySelector('[data-diff-path="src/a.go"]')!;
    const box = [...d.querySelectorAll("textarea")].find((b) => !b.closest("[data-thread]"));
    await type(box, "Name it");
    await click(button(d, t("extensions-codeviews.comment")));
    expect(acts()[0]).toMatchObject({ action: "inline_comment", values: { path: "src/a.go", line: "2", body: "Name it" } });
    await answer(acts()[0], new Error("rate limited"));
    expect(boxes(host.querySelector('[data-diff-path="src/a.go"]')!)).toContain("Name it");
  });

  it("the review's summary that fails to send stays open with its text", async () => {
    await open({ review, sections: [{ items: [diff] }] });
    await click(button(host, t("extensions-review.finish")));
    await type([...host.querySelectorAll("textarea")].at(-1), "LGTM");
    await click(button(host, "Approve"));
    expect(acts()[0]).toMatchObject({ action: "submit_review", values: { body: "LGTM" } });
    await answer(acts()[0], new Error("rate limited"));
    expect(boxes()).toContain("LGTM");
  });
});
