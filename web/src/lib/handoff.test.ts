// @vitest-environment jsdom
// Leaving a conversation for an Agent it handed work to: the side panel by default (#5424), or, set to Full page, the
// Agent's page, the way back, and the chat's place (#5096).
import { afterEach, describe, expect, it } from "vitest";
import { agentOpenMode, setAgentOpenMode } from "./agent-open";
import { chatPlaceIn, chatPlaceOf, dropChatPlace, handoffFrom, leaveChat, openAgent, openTask, panelIn, taskRoute } from "./handoff";
import { closePanel, getPanel, openPanel } from "./panel";
import { getState, reset, type State } from "./store";
import type { Todo } from "./todos";

afterEach(() => {
  setAgentOpenMode("panel");
  closePanel();
  reset();
  history.replaceState(null, "", "/");
  document.body.innerHTML = "";
});

const rect = (top: number, bottom: number) => ({ top, bottom, left: 0, right: 0, width: 0, height: bottom - top, x: 0, y: top, toJSON() {} }) as DOMRect;

/** A chat scroller showing lines `a` (above the view), `b` (cut by the view's top) and `c`. */
function chat(scroll: { top: number; height: number; client: number }) {
  const sc = document.createElement("div");
  sc.setAttribute("data-chat-scroll", "");
  Object.defineProperties(sc, { scrollTop: { value: scroll.top, writable: true }, scrollHeight: { value: scroll.height }, clientHeight: { value: scroll.client } });
  sc.getBoundingClientRect = () => rect(100, 100 + scroll.client);
  for (const [id, top] of [["a", -200], ["b", 60], ["c", 300]] as const) {
    const line = document.createElement("div");
    line.dataset.line = id;
    const row = document.createElement("div");
    row.getBoundingClientRect = () => rect(top, top + 120);
    line.append(row);
    sc.append(line);
  }
  document.body.append(sc);
  return sc;
}

describe("where a chat was", () => {
  it("is its end, or the first line still in view and how far under the top it starts", () => {
    expect(chatPlaceOf(chat({ top: 900, height: 1500, client: 600 }))).toEqual({ end: true, offset: 0 });
    document.body.innerHTML = "";
    expect(chatPlaceOf(chat({ top: 300, height: 1500, client: 600 }))).toEqual({ end: false, line: "b", offset: -40 });
  });

  it("reads only what a hand-off wrote, and is dropped once used", () => {
    history.replaceState({ chatPlace: { end: false, line: "b", offset: -40 }, other: 1 }, "");
    expect(chatPlaceIn()).toEqual({ end: false, line: "b", offset: -40 });
    dropChatPlace();
    expect(chatPlaceIn()).toBeUndefined();
    expect(history.state).toEqual({ other: 1 });
    expect(chatPlaceIn({ chatPlace: { line: "x" } })).toBeUndefined();
    expect(handoffFrom({ handoff: { path: "https://example.com/" } })).toBeUndefined();
    expect(handoffFrom({ handoff: { path: "/persona/c1" } })).toEqual({ path: "/persona/c1" });
  });
});

describe("leaving a conversation", () => {
  it("keeps the chat's place on its entry and says where the Agent's page came from", () => {
    history.replaceState(null, "", "/persona/c1?machine=m1");
    chat({ top: 300, height: 1500, client: 600 });
    const before = history.length;
    leaveChat({ name: "session", mid: "m1", sid: "s1" });
    expect(location.pathname).toBe("/s/m1/s1");
    expect(history.length).toBe(before + 1);
    expect(handoffFrom()).toEqual({ path: "/persona/c1?machine=m1" });
    history.back();
  });

  it("keeps the place for Back when it leaves from an overlay's entry, which the new page takes over", () => {
    history.replaceState({ personaOverlay: "o1" }, "", "/persona/c1");
    chat({ top: 300, height: 1500, client: 600 });
    leaveChat({ name: "session", mid: "m1", sid: "s1" });
    expect(location.pathname).toBe("/s/m1/s1");
    expect(chatPlaceIn()).toBeUndefined();
    // Back lands on the conversation's own entry, under the overlay's
    history.replaceState(null, "", "/persona/c1");
    dispatchEvent(new PopStateEvent("popstate"));
    expect(chatPlaceIn()).toEqual({ end: false, line: "b", offset: -40 });
    dropChatPlace();
    expect(chatPlaceIn()).toBeUndefined();
  });

  it("opens a link's page with no way-back strip, and anything but a conversation as a plain page", () => {
    history.replaceState(null, "", "/persona");
    leaveChat({ name: "session", mid: "m1", sid: "s1" }, false);
    expect(handoffFrom()).toBeUndefined();
    history.replaceState(null, "", "/settings");
    leaveChat({ name: "session", mid: "m1", sid: "s2" });
    expect(location.pathname).toBe("/s/m1/s2");
    expect(handoffFrom()).toBeUndefined();
  });
});

describe("leaving a conversation from its side panel (Open Agent page)", () => {
  const back = () => new Promise<void>((done) => {
    addEventListener("popstate", () => setTimeout(done, 0), { once: true });
    history.back();
  });

  it("opens the task's panel again and puts the main chat back where it was, on Back (the strip's return too)", async () => {
    history.replaceState(null, "", "/persona/c1");
    chat({ top: 300, height: 1500, client: 600 });
    // the panel's own chat scroller comes after the main one's and is not the conversation's place
    const aside = document.createElement("aside");
    const inPanel = document.createElement("div");
    inPanel.setAttribute("data-chat-scroll", "");
    aside.append(inPanel);
    document.body.prepend(aside);
    openPanel({ kind: "task", todo: "t1" });
    leaveChat({ name: "session", mid: "m1", sid: "s1" }, true, getPanel()!);
    closePanel();
    expect(location.pathname).toBe("/s/m1/s1");
    expect(handoffFrom()).toEqual({ path: "/persona/c1" });
    expect(panelIn()).toBeUndefined();
    await back();
    expect(location.pathname).toBe("/persona/c1");
    expect(getPanel()).toEqual({ kind: "task", todo: "t1" });
    expect(chatPlaceIn()).toEqual({ end: false, line: "b", offset: -40 });
  });

  it("keeps an Agent's panel too, and nothing for other panels or a plain hand-off", async () => {
    history.replaceState(null, "", "/persona");
    leaveChat({ name: "session", mid: "m1", sid: "s1" }, true, { kind: "agent", mid: "m1", sid: "s1" });
    await back();
    expect(getPanel()).toEqual({ kind: "agent", mid: "m1", sid: "s1" });
    closePanel();
    leaveChat({ name: "session", mid: "m1", sid: "s1" }, true, { kind: "files", mid: "m1", sid: "s1", cwd: "/example" });
    await back();
    expect(getPanel()).toBeNull();
    // a hand-off with no panel clears what an earlier one kept on this entry
    leaveChat({ name: "session", mid: "m1", sid: "s1" }, true, { kind: "task", todo: "t1" });
    await back();
    closePanel();
    chat({ top: 900, height: 1500, client: 600 });
    leaveChat({ name: "session", mid: "m1", sid: "s1" });
    await back();
    expect(getPanel()).toBeNull();
    expect(panelIn({ sidePanel: { kind: "link", url: "https://example.com/" } })).toBeUndefined();
  });

  it("keeps the panel for Back when it leaves from an overlay's entry", async () => {
    history.replaceState({ personaOverlay: "o1" }, "", "/persona/c1");
    leaveChat({ name: "session", mid: "m1", sid: "s1" }, true, { kind: "task", todo: "t1" });
    expect(panelIn()).toBeUndefined();
    history.replaceState(null, "", "/persona/c1");
    dispatchEvent(new PopStateEvent("popstate"));
    expect(getPanel()).toEqual({ kind: "task", todo: "t1" });
  });
});

describe("where a task opens", () => {
  const todo = (agents: Todo["agents"]): Todo => ({ id: "t1", title: "Task", created_at: "", updated_at: "", agents });
  const at = "2026-11-10T08:00:00Z";
  const with_ = (t: Todo): State => ({ ...getState(), todos: [t] });

  it("is its most pressing Agent's page, else the to-do's page", () => {
    expect(taskRoute("t1", with_(todo([{ machine_id: "m1", session_id: "s1", linked_at: at }])))).toEqual({ name: "session", mid: "m1", sid: "s1" });
    expect(taskRoute("t1", with_(todo([{ machine_id: "m1", session_id: "s1", linked_at: at, released_at: at }])))).toEqual({ name: "todos", id: "t1" });
    expect(taskRoute("t1", with_(todo([])))).toEqual({ name: "todos", id: "t1" });
    expect(taskRoute("t1", with_(todo([{ machine_id: "m1", session_id: "s1", linked_at: at, start_error: "launcher missing" }])))).toEqual({ name: "todos", id: "t1" });
    expect(taskRoute("unknown")).toEqual({ name: "todos", id: "unknown" });
  });

  it("opens there, set to Full page", () => {
    setAgentOpenMode("page");
    openTask("unknown");
    expect(location.pathname + location.search).toBe("/todos?id=unknown");
    expect(getPanel()).toBeNull();
  });
});

describe("where it opens (#5424)", () => {
  it("is the side panel by default: the task's or the Agent's, the page staying where it is", () => {
    history.replaceState(null, "", "/persona");
    expect(agentOpenMode()).toBe("panel");
    openTask("t1");
    expect(getPanel()).toEqual({ kind: "task", todo: "t1" });
    openAgent("m1", "s1");
    expect(getPanel()).toEqual({ kind: "agent", mid: "m1", sid: "s1" });
    expect(location.pathname).toBe("/persona");
    expect(handoffFrom()).toBeUndefined();
  });

  it("is the Agent's page with the way back, set to Full page, and the panel again once set back", () => {
    history.replaceState(null, "", "/persona");
    setAgentOpenMode("page");
    expect(localStorage.getItem("agent.open")).toBe("page");
    openAgent("m1", "s1");
    expect(getPanel()).toBeNull();
    expect(location.pathname).toBe("/s/m1/s1");
    expect(handoffFrom()).toEqual({ path: "/persona" });
    setAgentOpenMode("panel");
    // the default is not written: a browser left on it follows the default
    expect(localStorage.getItem("agent.open")).toBeNull();
    openAgent("m1", "s2");
    expect(getPanel()).toEqual({ kind: "agent", mid: "m1", sid: "s2" });
    expect(location.pathname).toBe("/s/m1/s1");
  });
});
