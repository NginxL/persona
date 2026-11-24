// @vitest-environment jsdom
// One health conversation (docs/assistant/health.md) runs on the machine its space's list names: a list that can't be read says
// why with Retry instead of loading for ever, and is read again on the next hello.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({ listeners: new Map<string, (() => void)[]>(), answers: [] as (() => Promise<unknown>)[], started: 0 }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string) => (path.startsWith("/assistant?space=health") && hub.answers.length ? hub.answers.shift()!() : Promise.resolve({})),
}));
vi.mock("../../lib/store", async (orig) => ({
  ...(await orig<typeof import("../../lib/store")>()),
  onHubEvent: (type: string, l: () => void) => {
    hub.listeners.set(type, [...(hub.listeners.get(type) ?? []), l]);
    return () => hub.listeners.set(type, (hub.listeners.get(type) ?? []).filter((x) => x !== l));
  },
}));
vi.mock("./reports", async (orig) => ({
  ...(await orig<typeof import("./reports")>()),
  startHealthConversation: () => (hub.started++, new Promise(() => {})),
}));
vi.mock("../../views/chat/ChatView", () => ({
  ChatView: ({ mid, sid, headerActions, placeholder }: { mid: string; sid: string; headerActions?: React.ReactNode; placeholder?: string }) => <div><p data-chat data-placeholder={placeholder}>{`${mid}/${sid}`}</p>{headerActions}</div>,
}));

import { registerModule } from "../../core/module-host";
import { health } from "./module";
import { Health, HealthPage, healthRoute } from "./Health";
import { t } from "../../i18n";
import { parse } from "../../lib/router";

registerModule(health);

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const failing = () => Promise.reject(new Error("hub away"));
const listed = () => Promise.resolve({ machine_id: "mac", conversations: [] });

beforeEach(() => {
  hub.listeners.clear();
  hub.answers = [];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("the health page's box", () => {
  it("takes no second message while a conversation is starting", async () => {
    hub.answers.push(listed);
    localStorage.setItem("draft:health-new", "first question");
    act(() => root.render(<Health fresh onToggleSidebar={() => {}} />));
    await flush();
    const send = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-composer.send")}"]`)!;
    await act(async () => send.click());
    await flush();
    expect(hub.started).toBe(1);
    expect(host.querySelector("textarea")!.disabled).toBe(true);
  });

  it("keeps the home page alive when a suggestion's draft write hits a full localStorage", async () => {
    hub.answers.push(listed);
    localStorage.clear();
    const quotaError = () => {
      const err = new Error("QuotaExceededError");
      err.name = "QuotaExceededError";
      throw err;
    };
    const original = Storage.prototype.setItem;
    const spy = vi.spyOn(Storage.prototype, "setItem").mockImplementation(function (this: Storage, key: string, value: string) {
      if (key === "draft:health-new") quotaError();
      return original.call(this, key, value);
    });
    const errors: string[] = [];
    const onError = (e: ErrorEvent) => {
      errors.push(e.message);
      e.preventDefault(); // keep the jsdom console clean; the failure is asserted below
    };
    window.addEventListener("error", onError);
    try {
      act(() => root.render(<Health fresh onToggleSidebar={() => {}} />));
      await flush();
      const suggestion = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes(t("health.suggestion_sleep")))!;
      expect(suggestion).toBeTruthy();
      expect(() => act(() => suggestion.click())).not.toThrow();
      await flush();
      expect(errors).toEqual([]); // a raw localStorage throw escapes React as a window error; the helper swallows it
      expect(localStorage.getItem("draft:health-new")).toBeNull(); // the quota failure was swallowed, not surfaced
      expect(host.querySelector("textarea")).not.toBeNull(); // the page and its composer stay mounted
    } finally {
      window.removeEventListener("error", onError);
      spy.mockRestore();
    }
  });
});

describe("a health conversation", () => {
  it("says why when its space's list can't be read, and opens on Retry", async () => {
    hub.answers.push(failing, listed);
    act(() => root.render(<Health sid="h1" onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector("[role=alert]")?.textContent).toContain("hub away");
    const retry = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("重试"))!;
    await act(async () => retry.click());
    await flush();
    expect(host.querySelector("[data-chat]")?.textContent).toBe("mac/h1");
  });

  it("asks health questions in its box, as the space's home does, not the main chat's words", async () => {
    hub.answers.push(listed);
    act(() => root.render(<Health sid="h1" onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector("[data-chat]")?.getAttribute("data-placeholder")).toBe(t("health.ask_placeholder"));
  });

  it("reads the list again on the next hello", async () => {
    hub.answers.push(failing, listed);
    act(() => root.render(<Health sid="h1" onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector("[data-chat]")).toBeNull();
    act(() => hub.listeners.get("hello")?.forEach((l) => l()));
    await flush();
    expect(host.querySelector("[data-chat]")?.textContent).toBe("mac/h1");
  });
});

// Entering Health (#13273): the home opens immediately and lists past chats; New returns to it.
describe("entering Health", () => {
  const conv = (session_id: string, updated_at: string) => ({ session_id, updated_at, title: session_id, space: "health" });

  beforeEach(() => history.replaceState(null, "", "/health"));

  it("stays on /health and lists the conversations newest first, excluding archived ones", async () => {
    const before = history.length;
    hub.answers.push(() => Promise.resolve({ machine_id: "mac", conversations: [conv("old", "2026-11-01T08:00:00Z"), conv("latest", "2026-11-11T08:00:00Z"), { ...conv("archived", "2026-11-12T08:00:00Z"), archived: true }] }));
    act(() => root.render(<Health onToggleSidebar={() => {}} />));
    await flush();
    expect(location.pathname).toBe("/health");
    expect(history.length).toBe(before);
    expect(host.querySelector("textarea")).not.toBeNull();
    expect(host.querySelector("[data-chat]")).toBeNull();
    expect([...host.querySelectorAll<HTMLAnchorElement>('a[href^="/health/"]')].map((a) => a.getAttribute("href"))).toEqual(["/health/latest", "/health/old"]);
    await act(async () => host.querySelector<HTMLAnchorElement>('a[href="/health/old"]')!.click());
    expect(location.pathname).toBe("/health/old");
  });

  it("opens the home before the conversation list arrives", async () => {
    hub.answers.push(() => new Promise(() => {}));
    act(() => root.render(<Health onToggleSidebar={() => {}} />));
    await flush();
    expect(location.pathname).toBe("/health");
    expect(host.querySelector("textarea")).not.toBeNull();
    expect(host.querySelector("h1")?.textContent).toBe(t("health.title"));
  });

  it("keeps the home available when the list cannot be read", async () => {
    hub.answers.push(failing);
    act(() => root.render(<Health onToggleSidebar={() => {}} />));
    await flush();
    expect(location.pathname).toBe("/health");
    expect(host.querySelector("textarea")).not.toBeNull();
    expect(host.textContent).toContain("hub away");
  });

  it("stays on the home when the space has no conversations", async () => {
    hub.answers.push(listed);
    act(() => root.render(<Health onToggleSidebar={() => {}} />));
    await flush();
    expect(location.pathname).toBe("/health");
    expect(host.textContent).toContain(t("health.empty_title"));
  });

  it("still opens the home at /health/new", async () => {
    history.replaceState(null, "", "/health/new");
    hub.answers.push(listed);
    const route = parse(location.pathname);
    expect(route).toEqual(healthRoute({ fresh: true }));
    if (route.name !== "module") throw new Error("Expected the Health module route");
    act(() => root.render(<HealthPage {...route} onToggleSidebar={() => {}} />));
    await flush();
    expect(location.pathname).toBe("/health/new");
    expect(host.querySelector("textarea")).not.toBeNull();
    expect(host.querySelector("[data-chat]")).toBeNull();
  });

  it("New in a health conversation opens the new conversation page; the clock lists the past ones", async () => {
    hub.answers.push(() => Promise.resolve({ machine_id: "mac", conversations: [conv("h1", "2026-11-11T08:00:00Z"), conv("h2", "2026-11-10T08:00:00Z")] }));
    act(() => root.render(<Health sid="h1" onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector(`button[aria-label="${t("health.history")}"]`)).not.toBeNull();
    const fresh = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("health.new_chat")}"]`)!;
    await act(async () => fresh.click());
    expect(location.pathname).toBe("/health");
  });
});
