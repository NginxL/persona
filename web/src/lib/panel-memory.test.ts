// @vitest-environment jsdom
// A session's side panel comes back with the session (#5150): leaving keeps the open one, coming back opens it where
// it was; one closed before leaving does not come back.
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { setAgentOpenMode } from "./agent-open";
import { closePanel, getPanel, openPanel, type Panel } from "./panel";
import { KEPT, arriving, belongsTo, chatKey, leaving, notePlace, useSessionPanels, type PanelMemory, type SessionPanel } from "./panel-memory";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const A = { mid: "m1", sid: "a" };
const B = { mid: "m1", sid: "b" };
const gitA: SessionPanel = { kind: "git", mid: "m1", sid: "a", cwd: "/work/web" };
const filesA: SessionPanel = { kind: "files", mid: "m1", sid: "a", cwd: "/work/web" };
const changesA = { kind: "changes", mid: "m1", sid: "a", at: { path: "/work/web/a.ts", call: "c1" } } as SessionPanel;

describe("the memory", () => {
  it("keeps the panel a session was left with, and gives it back once", () => {
    const m = leaving({}, "m1/a", gitA);
    expect(m).toEqual({ "m1/a": gitA });
    const r = arriving(m, "m1/a", null);
    expect(r.back).toEqual(gitA);
    expect(r.memory).toEqual({});
    expect(arriving(r.memory, "m1/a", null).back).toBeNull();
  });

  it("forgets a session left with nothing of its own open (closed with ×, Esc or ⌘B first)", () => {
    expect(leaving({ "m1/a": gitA }, "m1/a", null)).toEqual({});
  });

  it("keeps the Files panel's folder and the file open in it", () => {
    const m = leaving({}, "m1/a", filesA, { path: "/work/web/src", file: "/work/web/src/main.ts" });
    expect(m["m1/a"]).toEqual({ ...filesA, path: "/work/web/src", file: "/work/web/src/main.ts" });
    // back at its root with nothing open: it comes back there, not where it was first opened
    expect(leaving({}, "m1/a", { ...filesA, path: "/work/web/src" }, {})["m1/a"]).toEqual({ ...filesA, path: undefined, file: undefined });
  });

  it("does not replace a panel that is open already (a link stays across pages), and keeps the session's for later", () => {
    const link: Panel = { kind: "link", url: "https://example.com" };
    const r = arriving({ "m1/a": gitA }, "m1/a", link);
    expect(r.back).toBeNull();
    expect(r.memory).toEqual({ "m1/a": gitA });
  });

  it("keeps the Background tasks panel at its list, without a run or overview origin", () => {
    const bg: SessionPanel = { kind: "background", mid: "m1", sid: "a", run: "w1" };
    const listed: SessionPanel = { kind: "background", mid: "m1", sid: "a" };
    expect(leaving({}, "m1/a", bg)["m1/a"]).toEqual(listed);
    expect(leaving({}, "m1/a", { ...bg, from: "overview" })["m1/a"]).toEqual(listed);
  });

  it("keeps the Changes panel at the selected file", () => {
    const m = leaving({}, "m1/a", changesA);
    expect(m["m1/a"]).toEqual(changesA);
    expect(belongsTo(changesA, A)).toBe(true);
    expect(belongsTo(changesA, B)).toBe(false);
    expect(arriving(m, "m1/a", null).back).toEqual(changesA);
  });

  it("keeps the last few sessions only", () => {
    let m: PanelMemory = {};
    for (let i = 0; i < KEPT + 5; i++) m = leaving(m, `m1/s${i}`, { ...gitA, sid: `s${i}` });
    expect(Object.keys(m)).toHaveLength(KEPT);
    expect(m["m1/s0"]).toBeUndefined();
    expect(m[`m1/s${KEPT + 4}`]).toBeDefined();
  });

  it("knows which panels are the session's", () => {
    expect(belongsTo(gitA, A)).toBe(true);
    expect(belongsTo(gitA, B)).toBe(false);
    expect(belongsTo({ kind: "file", mid: "m1", path: "/work/web/a.ts" }, A)).toBe(true);
    expect(belongsTo({ kind: "link", url: "https://example.com" }, A)).toBe(false);
    expect(belongsTo({ kind: "background", mid: "m1", sid: "a" }, A)).toBe(true);
    expect(belongsTo({ kind: "background", mid: "m1", sid: "a" }, B)).toBe(false);
    expect(belongsTo(gitA, null)).toBe(false);
  });
});

// The page as App drives it: the session it shows changes, the hook closes and brings back.
describe("moving between sessions", () => {
  let host: HTMLDivElement;
  let root: Root;
  const Page = ({ pane }: { pane: { mid: string; sid: string } | null }) => {
    useSessionPanels(pane ? `${pane.mid}/${pane.sid}` : "home", pane);
    return null;
  };
  const show = (pane: { mid: string; sid: string } | null) => act(() => root.render(createElement(Page, { pane })));

  beforeEach(() => {
    sessionStorage.clear();
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });
  afterEach(() => {
    act(() => closePanel());
    act(() => root.unmount());
    host.remove();
  });

  it("brings Git back on coming back to A, after B closed it", () => {
    show(A);
    act(() => openPanel(gitA));
    show(B);
    expect(getPanel()).toBeNull();
    show(A); // the sidebar, or the browser's Back
    expect(getPanel()).toEqual(gitA);
  });

  it("brings Changes back at the same file", () => {
    show(A);
    act(() => openPanel(changesA));
    show(B);
    expect(getPanel()).toBeNull();
    show(A);
    expect(getPanel()).toEqual(changesA);
  });

  it("brings the Files panel back in its folder, with its file", () => {
    show(A);
    act(() => openPanel(filesA));
    notePlace(filesA, { path: "/work/web/src", file: "/work/web/src/main.ts" }); // what FilesPanel reports as it goes
    show({ mid: "m1", sid: "b" });
    show(A);
    expect(getPanel()).toEqual({ ...filesA, path: "/work/web/src", file: "/work/web/src/main.ts" });
  });

  it("does not bring back a panel closed before leaving", () => {
    show(A);
    act(() => openPanel(gitA));
    act(() => closePanel()); // ×, Esc, ⌘B
    show(B);
    show(A);
    expect(getPanel()).toBeNull();
  });

  it("keeps a session's own panel open while it stays, and leaves a link alone", () => {
    show(A);
    act(() => openPanel(gitA));
    show(A);
    expect(getPanel()).toEqual(gitA);
    const link: Panel = { kind: "link", url: "https://example.com" };
    act(() => openPanel(link));
    show(B);
    expect(getPanel()).toEqual(link);
  });

  it("closes A's Background tasks panel on going to B and brings it back at its list on A", () => {
    show(A);
    act(() => openPanel({ kind: "background", mid: "m1", sid: "a", run: "w1" }));
    show(B);
    expect(getPanel()).toBeNull();
    show(A);
    expect(getPanel()).toEqual({ kind: "background", mid: "m1", sid: "a" });
  });

  it("brings it back through a page that is not a session", () => {
    show(A);
    act(() => openPanel(gitA));
    show(null); // the inbox, settings…
    expect(getPanel()).toBeNull();
    show(A);
    expect(getPanel()).toEqual(gitA);
  });
});

// Persona chats (#6199): a task's / agent's chat opened next to one goes and comes back with that chat.
describe("moving between Persona chats", () => {
  let host: HTMLDivElement;
  let root: Root;
  const Page = ({ chat }: { chat?: string }) => {
    useSessionPanels(chat !== undefined ? `persona/${chat}` : "home", null, chat);
    return null;
  };
  const show = (chat?: string) => act(() => root.render(createElement(Page, { chat })));
  const agent: Panel = { kind: "agent", mid: "m1", sid: "s1" };
  const task: Panel = { kind: "task", todo: "t1" };

  beforeEach(() => {
    sessionStorage.clear();
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });
  afterEach(() => {
    act(() => closePanel());
    act(() => root.unmount());
    host.remove();
    setAgentOpenMode("panel");
  });

  it("closes chat A's agent on switching to B, and brings it back on A", () => {
    show("A");
    act(() => openPanel(agent));
    show("B");
    expect(getPanel()).toBeNull();
    show("A");
    expect(getPanel()).toEqual(agent);
  });

  it("shows each chat its own panel", () => {
    show("A");
    act(() => openPanel(agent));
    show("B");
    act(() => openPanel(task));
    show("A");
    expect(getPanel()).toEqual(agent);
    show("B");
    expect(getPanel()).toEqual(task);
  });

  it("keeps the panel while the chat stays, and leaves a link alone", () => {
    show("A");
    act(() => openPanel(task));
    show("A");
    expect(getPanel()).toEqual(task);
    const link: Panel = { kind: "link", url: "https://example.com" };
    act(() => openPanel(link));
    show("B");
    expect(getPanel()).toEqual(link);
  });

  it("brings back no panel of a kind the page no longer has (the plan's, #5156)", () => {
    const file: Panel = { kind: "file", mid: "m1", path: "/notes/today.md" };
    sessionStorage.setItem("persona.panel.back", JSON.stringify({ [chatKey("B")]: file, [chatKey("C")]: { kind: "plan", mid: "m1", sid: "s1" } }));
    show("B");
    expect(getPanel()).toEqual(file);
    show("C");
    expect(getPanel()).toBeNull();
  });

  it("brings back a task's or an Agent's panel only while Agents open in the side panel (#5424)", () => {
    const kept = JSON.stringify({ [chatKey("A")]: task, [chatKey("B")]: agent });
    sessionStorage.setItem("persona.panel.back", kept);
    show("A");
    expect(getPanel()).toEqual(task);
    act(() => closePanel());
    show("B");
    expect(getPanel()).toEqual(agent);
    act(() => closePanel());
    // set to "Full page": what the chat kept in the panel is not reopened beside it
    setAgentOpenMode("page");
    sessionStorage.setItem("persona.panel.back", kept);
    show("A");
    expect(getPanel()).toBeNull();
    show("B");
    expect(getPanel()).toBeNull();
  });

  it("brings it back through a page that is not a chat", () => {
    show("A");
    act(() => openPanel(task));
    show(); // settings, the inbox…
    expect(getPanel()).toBeNull();
    show("A");
    expect(getPanel()).toEqual(task);
  });
});
