// @vitest-environment jsdom
// Manual compaction belongs to every persistent Assistant conversation, not just the main chat.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Session } from "../../lib/types";
import type { MenuItem } from "../../ui/atoms";

const hub = vi.hoisted(() => ({
  calls: [] as { path: string; method?: string; json?: unknown }[],
  error: undefined as Error | undefined,
}));
const toast = vi.hoisted(() => vi.fn());
vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: (path: string, init?: { method?: string; json?: unknown }) => {
    hub.calls.push({ path, method: init?.method, json: init?.json });
    return hub.error ? Promise.reject(hub.error) : Promise.resolve({ ok: true });
  },
}));
vi.mock("../../ui/atoms", async (original) => ({
  ...(await original<typeof import("../../ui/atoms")>()),
  toast,
}));

import { getState, reset } from "../../lib/store";
import { useAssistantMenu } from "./AssistantChat";

const MAIN = "example-main";
const SESSION = { machine_id: "example", session_id: MAIN, title: "Example chat", assistant: true, status: "idle" } as Session;
const roots: Root[] = [];
let items: MenuItem[] = [];
function Probe({ session }: { session?: Session }) {
  items = useAssistantMenu(session).items;
  return null;
}
function render(session?: Session) {
  const root = createRoot(document.createElement("div"));
  roots.push(root);
  act(() => root.render(<Probe session={session} />));
}
const keys = () => items.map((item) => item.key);
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

beforeEach(() => {
  reset();
  hub.calls.length = 0;
  hub.error = undefined;
  toast.mockClear();
  Object.assign(getState(), {
    features: ["assistant_compact", "assistant_main_new", "assistant_main_clear"],
    assistant: { machine_id: "example", main: MAIN, leaders: ["example-direction"], background_supervisor: "example-supervisor", conversations: [] },
  });
});
afterEach(() => {
  for (const root of roots.splice(0)) act(() => root.unmount());
  reset();
});

describe("Compact context in Assistant menus", () => {
  it.each(["main", "direction", "side", "supervisor"])("compacts the selected %s conversation, without exposing main-only actions elsewhere", async (kind) => {
    const sid = kind === "main" ? MAIN : `example-${kind}`;
    render({ ...SESSION, session_id: sid });
    const compact = items.find((item) => item.key === "compact");
    expect(compact).toBeDefined();
    expect(compact?.disabled).toBe(false);
    expect(keys().includes("clear")).toBe(kind === "main");
    expect(keys().includes("newmain")).toBe(kind === "main");
    const requested = vi.fn();
    window.addEventListener("persona:compact-requested", requested);
    try {
      await act(async () => { compact!.onSelect?.(); });
      expect(hub.calls).toEqual([{ path: `/assistant/conversations/${sid}/compact`, method: "POST", json: { machine_id: "example" } }]);
      expect(requested).toHaveBeenCalledOnce();
      expect((requested.mock.calls[0][0] as CustomEvent).detail).toEqual({ mid: "example", sid });
    } finally {
      window.removeEventListener("persona:compact-requested", requested);
    }
  });

  it("does not require a main conversation to exist", () => {
    getState().assistant!.main = "";
    render({ ...SESSION, session_id: "example-side" });
    expect(keys()).toContain("compact");
    expect(keys()).not.toContain("clear");
    expect(keys()).not.toContain("newmain");
  });

  it("keeps an already-compacting side conversation disabled", () => {
    render({ ...SESSION, session_id: "example-direction", compaction: { state: "running", started_at: "2026-11-01T10:00:00Z" } });
    expect(items.find((item) => item.key === "compact")?.disabled).toBe(true);
    expect(hub.calls).toEqual([]);
  });

  it.each(["Answering; wait for this reply.", "Already compacting.", "This runtime cannot compact."])("keeps the existing refusal toast: %s", async (error) => {
    hub.error = new Error(error);
    render({ ...SESSION, session_id: "example-direction", status: "working" });
    const compact = items.find((item) => item.key === "compact");
    expect(compact).toBeDefined();
    await act(async () => { compact!.onSelect?.(); });
    expect(toast).toHaveBeenCalledWith(expect.stringContaining(error), "error");
  });

  it("offers no compaction on an unsupported hub or without an Assistant conversation", () => {
    getState().features = [];
    render({ ...SESSION, session_id: "example-direction" });
    expect(keys()).not.toContain("compact");
    render();
    expect(items).toEqual([]);
  });
});
