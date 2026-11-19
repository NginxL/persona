// @vitest-environment jsdom
// A failed optional kept-message chunk must leave the real chat composer and its draft mounted.
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import type { Item, Session } from "../../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
});

const captured = vi.hoisted(() => ({
  session: { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "idle", state: "idle" } as Session,
}));
const transcript = vi.hoisted(() => ({ items: [] as Item[], loading: false, error: "", hasMore: false, streaming: "" }));
vi.mock("./useTranscript", () => ({ useTranscript: () => transcript }));
vi.mock("../../lib/store", async (importOriginal) => {
  const store = await importOriginal<typeof import("../../lib/store")>();
  return {
    ...store,
    getSession: () => captured.session,
    useStore: <T,>(select: (s: ReturnType<typeof store.getState>) => T) =>
      select({ ...store.getState(), sessions: { hub: [captured.session] } }),
  };
});
vi.mock("../../lib/panel", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/panel")>(), usePanel: () => null }));
vi.mock("../../lib/router", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/router")>(), useRoute: () => ({ name: "session", mid: "hub", id: "c1" }) }));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));


import { Suspense } from "react";
import { initI18n, t } from "../../i18n";
import { PageBoundary } from "../PageBoundary";
import { ChatView as Chat } from "./ChatView";
const chunk = vi.hoisted(() => {
  let reject!: (error: Error) => void;
  const pending = new Promise<never>((_, no) => { reject = no; });
  return { pending, reject };
});
vi.mock("./UndeliveredBubbles", async () => { await chunk.pending; return {}; });
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

it("keeps the composer and its draft when the kept-message module fails", async () => {
  await initI18n("en");
  // The existing one-time reload already happened; exercise the persistent local error.
  sessionStorage.setItem("persona.chunkReload", String(Date.now()));
  captured.session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "idle", state: "idle", assistant: true,
    undelivered: [{ id: "kept-example", text: "Synthetic kept message", at: "2026-11-17T10:00:00Z", state: "waiting", attempts: 1 }] } as Session;
  vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } })));
  vi.spyOn(console, "error").mockImplementation(() => {});
  const host = document.createElement("div"); document.body.append(host);
  const root = createRoot(host);
  const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); }); };
  try {
    await act(async () => root.render(<PageBoundary page="chat-test"><Suspense fallback={null}><Chat mid="hub" sid="c1" assistant /></Suspense></PageBoundary>));
    await vi.waitFor(async () => { await flush(); expect(host.querySelector("textarea")).not.toBeNull(); }, { timeout: 3000 });
    const editor = host.querySelector<HTMLTextAreaElement>("textarea")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(editor, "Unsubmitted chat draft");
      editor.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await flush(); expect(editor.value).toBe("Unsubmitted chat draft");
    expect(host.textContent).toContain("Synthetic kept message");
    await act(async () => chunk.reject(new TypeError("Failed to fetch dynamically imported module: UndeliveredBubbles")));
    await vi.waitFor(async () => { await flush(); expect(host.querySelector('[role="alert"]')?.textContent).toContain(t("pageboundary.reload")); });
    expect(host.querySelector("textarea")).toBe(editor);
    expect(editor.value).toBe("Unsubmitted chat draft");
    expect(editor.disabled).toBe(false);
  } finally {
    await act(async () => root.unmount()); host.remove();
    sessionStorage.removeItem("persona.chunkReload"); vi.restoreAllMocks(); vi.unstubAllGlobals();
  }
});
