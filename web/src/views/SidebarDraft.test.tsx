// @vitest-environment jsdom
// A conversation with words left unsent in its box says so in the sidebar (#5568): "Draft:" and the words, once you are
// not in it; sending or clearing the box takes it away; whitespace alone is no draft.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
// every render of a SessionRow reads its preview once: counted per session
const previews = vi.hoisted(() => new Map<string, number>());
vi.mock("../lib/format", async (load) => {
  const real = await load<typeof import("../lib/format")>();
  return {
    ...real,
    sessionPreview: (s: Parameters<typeof real.sessionPreview>[0]) => {
      previews.set(s.session_id, (previews.get(s.session_id) ?? 0) + 1);
      return real.sessionPreview(s);
    },
  };
});
import { addQuote, setQuotes } from "../lib/drafts";
import { refresh, reset } from "../lib/store";
import type { Machine, Session } from "../lib/types";
import { Composer } from "./chat/Composer";
import { PersonaRow, Sidebar } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const machines: Machine[] = [{ id: "mac", name: "MacBook", online: true }];
const sessions: Session[] = [
  { machine_id: "mac", session_id: "s1", title: "补齐网络层的单元测试", cwd: "/code", status: "idle", state: "idle" },
  { machine_id: "mac", session_id: "s2", title: "整理发布说明", cwd: "/code", status: "idle", state: "unread", last_text: "发布说明整理好了" },
];
const answers: Record<string, unknown> = { "/api/v1/sessions": { sessions }, "/api/v1/machines": { machines } };

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  localStorage.clear();
  setQuotes("mac/s1", []);
  setQuotes("mac/s2", []);
  vi.stubGlobal("fetch", vi.fn((url: string) => Promise.resolve(new Response(JSON.stringify(answers[new URL(url, "http://x").pathname] ?? {}), { headers: { "Content-Type": "application/json" } }))));
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

const sent: string[] = [];
// the sidebar, with the conversation `sid` open beside it (its box) or none; `box`: a box on screen for another one
// than the open page's (a side panel, the pane under a lit tab row)
const show = async (sid?: string, box = sid) => {
  act(() =>
    root.render(
      <>
        <Sidebar route={sid ? { name: "session", mid: "mac", sid } : { name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />
        {box && <Composer draftKey={`mac/${box}`} placeholder="" onSend={async (m) => (sent.push(m.text), true)} />}
      </>,
    ),
  );
  await flush();
};
const typeInto = (text: string) =>
  act(() => {
    const el = host.querySelector("textarea")!;
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
// past the box's 250 ms save
const settle = () => act(() => new Promise((r) => setTimeout(r, 300)));
const row = (sid: string) => host.querySelector<HTMLAnchorElement>(`[data-sidebar-rows] a[href$="/s/mac/${sid}"]`)!;
const draftOf = (sid: string) => row(sid).querySelector("[data-draft]")?.textContent;
const lineOf = (sid: string) => row(sid).querySelector("[data-row-line]")?.textContent;

describe("a sidebar row with an unsent draft (#5568)", () => {
  it("shows nothing while you type in it, and the draft once you leave", async () => {
    await act(() => refresh());
    await show("s1");
    typeInto("先把网络层的测试\n跑一遍");
    await settle();
    expect(localStorage.getItem("draft:mac/s1")).toBe("先把网络层的测试\n跑一遍");
    expect(draftOf("s1")).toBeUndefined();

    await show("s2");
    expect(draftOf("s1")).toBe("草稿：先把网络层的测试 跑一遍");
    expect(row("s1").querySelector("[data-draft] .text-attention")?.textContent).toBe("草稿：");
    expect(draftOf("s2")).toBeUndefined();
  });

  it("takes the place of the row's last words, and is still there after a reload", async () => {
    localStorage.setItem("draft:mac/s2", "这段再改改");
    await act(() => refresh());
    await show();
    expect(lineOf("s2")).toBe("草稿：这段再改改");
    expect(row("s2").textContent).not.toContain("发布说明整理好了");
  });

  it("goes when the box is cleared", async () => {
    localStorage.setItem("draft:mac/s1", "写到一半");
    await act(() => refresh());
    await show("s2");
    expect(draftOf("s1")).toBe("草稿：写到一半");
    await show("s1");
    expect(host.querySelector("textarea")!.value).toBe("写到一半");
    typeInto("");
    await show("s2");
    expect(draftOf("s1")).toBeUndefined();
    expect(localStorage.getItem("draft:mac/s1")).toBeNull();
  });

  it("goes when the words are sent", async () => {
    localStorage.setItem("draft:mac/s1", "发出去吧");
    await act(() => refresh());
    await show("s2");
    expect(draftOf("s1")).toBe("草稿：发出去吧");
    await show("s1");
    await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
    expect(sent.at(-1)).toBe("发出去吧");
    await show("s2");
    expect(draftOf("s1")).toBeUndefined();
  });

  it("does not re-render the list while you type", async () => {
    await act(() => refresh());
    await show("s1");
    previews.clear();
    typeInto("一个字");
    await settle();
    typeInto("一个字一个字");
    await settle();
    expect(localStorage.getItem("draft:mac/s1")).toBe("一个字一个字");
    expect(previews.get("s1") ?? 0).toBe(0);
    expect(previews.get("s2") ?? 0).toBe(0);
  });

  it("is not there for whitespace alone", async () => {
    await act(() => refresh());
    await show("s1");
    typeInto("  \n\t ");
    await show("s2");
    expect(localStorage.getItem("draft:mac/s1")).toBe("  \n\t ");
    expect(draftOf("s1")).toBeUndefined();
  });

  it("is not there while the conversation's box is on screen, lit row or not", async () => {
    localStorage.setItem("draft:mac/s1", "写到一半");
    await act(() => refresh());
    await show(undefined, "s1");
    expect(row("s1").getAttribute("aria-current")).toBeNull();
    expect(draftOf("s1")).toBeUndefined();
    await show();
    expect(draftOf("s1")).toBe("草稿：写到一半");
  });

  it("says just 草稿 when only a quote waits", async () => {
    await act(() => refresh());
    await show("s2");
    act(() => void addQuote("mac/s1", { text: "引用的一段话" }));
    expect(draftOf("s1")).toBe("草稿");
    act(() => setQuotes("mac/s1", []));
    expect(draftOf("s1")).toBeUndefined();
  });
});

describe("Persona's main row with an unsent draft (#5568)", () => {
  const main: Session = { machine_id: "hub", session_id: "main", title: "Persona", cwd: "/", status: "idle", state: "idle", assistant: true };
  const props = { reg: () => () => {}, hover: () => {}, go: () => () => {}, main };
  const draft = () => host.querySelector("[data-draft]")?.textContent;

  it("shows it under Main chat when the main conversation is not open", () => {
    localStorage.setItem("draft:hub/main", "明天的日程帮我排一下");
    act(() => root.render(<PersonaRow {...props} active={false} />));
    expect(draft()).toBe("草稿：明天的日程帮我排一下");
    expect(host.textContent).toContain("主对话");
  });

  it("not while the main conversation is open", () => {
    localStorage.setItem("draft:hub/main", "明天的日程帮我排一下");
    act(() => root.render(<><PersonaRow {...props} active /><Composer draftKey="hub/main" placeholder="" onSend={async () => true} /></>));
    expect(draft()).toBeUndefined();
  });
});
