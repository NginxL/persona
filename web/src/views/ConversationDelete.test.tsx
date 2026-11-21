// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";
import { CONVERSATION_DELETE_FEATURE } from "../lib/conversation-delete";
import { getState, markConversationDeleted, reset, upsertSession } from "../lib/store";
import type { Session } from "../lib/types";
import { ConversationRowMenu } from "./ConversationRowMenu";
import { History } from "./assistant/History";
import { ChatView } from "./chat/ChatView";
vi.hoisted(() => { Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }) }); });
const chat = (patch: Partial<Session> = {}): Session => ({ machine_id: "hub", session_id: "trip", status: "idle", title: "Trip planning", cwd: "", assistant: true, updated_at: "2026-11-01T09:00:00Z", ...patch });
let host: HTMLDivElement, root: Root, anchor: HTMLButtonElement;
let fetcher: ReturnType<typeof vi.fn>, hold: Promise<Response> | undefined, refusal: boolean;
const deletedCalls = () => fetcher.mock.calls.filter(([, init]) => init?.method === "DELETE");
const flush = async () => { for (let n = 0; n < 8; n++) await act(async () => { await Promise.resolve(); }); };
const click = async (selector: string, text: string) => {
  let button: HTMLElement | undefined;
  await vi.waitFor(async () => {
    await flush();
    button = [...document.querySelectorAll(selector)].find((e) => e.textContent?.trim() === text) as HTMLElement | undefined;
    expect(button, `missing ${text}`).toBeDefined();
  });
  await act(async () => button!.click()); await flush();
};
const menu = async (s = chat(), main = false, builtIn = false) => {
  act(() => root.render(<ConversationRowMenu s={s} main={main} builtIn={builtIn} anchor={anchor} onClose={() => root.render(null)} />)); await flush();
};
beforeEach(() => {
  reset(); getState().features = [CONVERSATION_DELETE_FEATURE];
  getState().assistant = { machine_id: "hub", main: "main", conversations: [chat()] } as NonNullable<ReturnType<typeof getState>["assistant"]>;
  upsertSession(chat()); hold = undefined; refusal = false;
  fetcher = vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === "DELETE") return hold ?? (refusal ? new Response(JSON.stringify({ code: "conversation_busy", error: "This chat is busy. Try again when it finishes." }), { status: 409 }) : new Response('{"ok":true,"deleted":true}'));
    if (url.includes("/assistant?archived=1")) return new Response(JSON.stringify({ conversations: [chat({ archived: true })] }));
    return new Response("{}");
  });
  vi.stubGlobal("fetch", fetcher);
  host = document.createElement("div"); anchor = document.createElement("button"); document.body.append(host, anchor); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); anchor.remove(); reset(); vi.unstubAllGlobals(); });
describe("confirmed conversation deletion", () => {
  it("confirms from the sidebar, explains retained work, and cancels without a request", async () => {
    await menu(); await click('[role="menuitem"]', t("conversation-menu.delete"));
    await vi.waitFor(async () => { await flush(); expect(document.querySelector('[role="dialog"]')?.textContent).toContain(t("conversation-delete.delete_body")); });
    expect(deletedCalls()).toHaveLength(0);
    await click('[role="dialog"] button', t("common.cancel"));
    expect(deletedCalls()).toHaveLength(0); expect(document.querySelector('[role="dialog"]')).toBeNull();
  });
  it("sends once, locks pending controls, and removes only after success", async () => {
    let release!: (r: Response) => void; hold = new Promise((r) => { release = r; });
    await menu(); await click('[role="menuitem"]', t("conversation-menu.delete")); await click('[role="dialog"] button', t("conversation-delete.delete_confirm"));
    expect(deletedCalls()).toHaveLength(1); expect(JSON.parse(deletedCalls()[0][1].body as string)).toEqual({ machine_id: "hub" });
    expect(getState().assistant?.conversations).toHaveLength(1);
    expect([...document.querySelectorAll('[role="dialog"] button')].find((b) => b.textContent === t("common.cancel"))).toHaveProperty("disabled", true);
    await act(async () => (document.querySelector(`[role="dialog"] button[aria-label="${t("ui-atoms.close")}"]`) as HTMLButtonElement).click());
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
    await act(async () => release(new Response('{"ok":true,"deleted":true}'))); await flush();
    expect(getState().assistant?.conversations).toEqual([]); expect(document.querySelector('[role="dialog"]')).toBeNull();
  });
  it("keeps the dialog and chat after 409, allowing retry", async () => {
    refusal = true; await menu(); await click('[role="menuitem"]', t("conversation-menu.delete")); await click('[role="dialog"] button', t("conversation-delete.delete_confirm"));
    expect(document.querySelector('[role="alert"]')?.textContent).toContain("This chat is busy"); expect(getState().assistant?.conversations).toHaveLength(1);
    refusal = false; await click('[role="dialog"] button', t("conversation-delete.delete_confirm"));
    expect(deletedCalls()).toHaveLength(2); expect(getState().assistant?.conversations).toEqual([]);
  });
  it("closes the confirmation when another device deletes the chat", async () => {
    await menu(); await click('[role="menuitem"]', t("conversation-menu.delete"));
    await vi.waitFor(async () => { await flush(); expect(document.querySelector('[role="dialog"]')?.textContent).toContain(t("conversation-delete.delete_body")); });
    await act(async () => markConversationDeleted("hub", "trip")); await flush();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(deletedCalls()).toHaveLength(0);
  });
  it("rechecks activity after confirmation opens", async () => {
    await menu(); await click('[role="menuitem"]', t("conversation-menu.delete"));
    await act(async () => upsertSession(chat({ status: "working" })));
    await click('[role="dialog"] button', t("conversation-delete.delete_confirm"));
    expect(deletedCalls()).toHaveLength(0);
    expect(document.querySelector('[role="alert"]')?.textContent).toBe(t("conversation-delete.delete_unavailable"));
  });
  it.each(["old", "metadata", "main", "builtIn", "working", "pending", "queue", "compacting"])("omits Delete for %s", async (kind) => {
    if (kind === "old") getState().features = [];
    if (kind === "metadata") getState().assistant = null;
    const s = chat(kind === "working" ? { status: "working" } : kind === "queue" ? { queued: ["later"] } : kind === "compacting" ? { compaction: { state: "running" } as Session["compaction"] } : {});
    if (kind === "pending") getState().interactions = [{ machine_id: "hub", session_id: "trip" } as never];
    upsertSession(s); await menu(s, kind === "main", kind === "builtIn");
    expect([...document.querySelectorAll('[role="menuitem"]')].some((e) => e.textContent?.trim() === t("conversation-menu.delete"))).toBe(false);
  });
  it("deletes an archived History row and fences the stale archived read", async () => {
    getState().assistant!.conversations = []; upsertSession(chat({ archived: true }));
    act(() => root.render(<History onToggleSidebar={() => {}} />)); await flush();
    await act(async () => (host.querySelector('button[aria-expanded]') as HTMLButtonElement).click());
    await act(async () => (host.querySelector(`button[aria-label="${t("assistant-history.more")}"]`) as HTMLButtonElement).click()); await flush();
    await click('[role="menuitem"]', t("conversation-menu.delete")); await click('[role="dialog"] button', t("conversation-delete.delete_confirm"));
    expect(deletedCalls()).toHaveLength(1); expect(host.textContent).not.toContain("Trip planning");
    await act(async () => (host.querySelector(`button[aria-label="${t("assistant-history.refresh")}"]`) as HTMLButtonElement).click()); await flush();
    expect(host.textContent).not.toContain("Trip planning");
  });
  it("shows a tombstone without a composer for a deleted old link", async () => {
    markConversationDeleted("hub", "trip"); act(() => root.render(<ChatView mid="hub" sid="trip" assistant />)); await flush();
    await vi.waitFor(async () => { await flush(); expect(host.textContent).toContain(t("conversation-delete.deleted_title")); }); expect(host.querySelector('textarea,[contenteditable="true"]')).toBeNull(); expect(host.textContent).not.toContain("Trip planning");
  });
});
