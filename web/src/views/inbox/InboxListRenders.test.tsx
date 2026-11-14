// @vitest-environment jsdom
// The inbox list on a busy hub: a sessions or machines push that leaves the waiting agents as they were renders no row
// again (ItemRow and AgentRow are memoized). An inbox push re-reads the list as fresh JSON; items that read the same keep
// their objects (#5400), so only the item that changed renders again, and no agent row.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

// every render of an ItemRow reads its from line once (rowMeta, in lib/inbox-now), every AgentRow its agent title:
// counted per item / interaction
const renders = vi.hoisted(() => new Map<string, number>());
vi.mock("../../lib/inbox-now", async (load) => {
  const real = await load<typeof import("../../lib/inbox-now")>();
  return {
    ...real,
    rowMeta: (...a: Parameters<typeof real.rowMeta>) => {
      renders.set(a[0].id, (renders.get(a[0].id) ?? 0) + 1);
      return real.rowMeta(...a);
    },
  };
});
vi.mock("../../lib/inbox", async (load) => {
  const real = await load<typeof import("../../lib/inbox")>();
  return {
    ...real,
    interactionAgentTitle: (...a: Parameters<typeof real.interactionAgentTitle>) => {
      renders.set(a[0].id, (renders.get(a[0].id) ?? 0) + 1);
      return real.interactionAgentTitle(...a);
    },
  };
});

const item = (id: string, title: string) => ({ id, source: "mail", kind: "mail", account: "a1", account_name: "me", title, preview: `${title} 的正文`, count: 1, tier: "action", updated_at: "2026-05-01T10:00:00Z" });
const ix = (id: string, sid: string) => ({ id, machine_id: "mac", session_id: sid, session_title: `任务 ${sid}`, kind: "question", created_at: "2026-05-01T09:00:00Z" });
const hub = vi.hoisted(() => ({ items: [] as unknown[] }));
const sessionsOf = () => ["s1", "s2", "s3"].map((sid) => ({ machine_id: "mac", session_id: sid, title: `任务 ${sid}`, cwd: "/code", status: "idle", state: "idle" }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string) => {
    if (path === "/mail/accounts") return Promise.resolve({ accounts: [{ id: "a1", email: "me@example.com" }] });
    // a real push is freshly parsed JSON: no item keeps its object identity
    if (path.startsWith("/inbox")) return Promise.resolve({ items: structuredClone(hub.items) });
    if (path === "/sessions") return Promise.resolve({ sessions: sessionsOf() });
    if (path === "/interactions") return Promise.resolve({ interactions: [ix("ix1", "s1"), ix("ix2", "s2")] });
    if (path === "/machines") return Promise.resolve({ machines: [{ id: "mac", name: "MacBook", online: true }] });
    return Promise.resolve({});
  },
}));

import { reloadInbox, resetInboxForTests } from "../../lib/inbox";
import { resetMailForTests } from "../../lib/mail";
import { applyMachine, getState, refresh, reset, upsertSession } from "../../lib/store";
import { InboxList } from "./InboxList";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(async () => {
  localStorage.clear();
  resetInboxForTests();
  resetMailForTests();
  hub.items = [item("a", "周报"), item("b", "发布"), item("c", "账单")];
  await act(() => refresh());
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<InboxList />));
  await flush();
  expect(host.textContent).toContain("账单");
  expect([...renders.keys()].sort()).toEqual(["a", "b", "c", "ix1", "ix2"]);
  renders.clear();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  renders.clear();
});

describe("收件箱：长列表少渲染", () => {
  it("a sessions push for a session no row shows renders no row again", async () => {
    act(() => upsertSession({ ...getState().sessions.mac[2], title: "任务 s3（改）" }));
    await flush();
    expect([...renders.keys()]).toEqual([]);
  });

  it("a machines push that keeps the machine's name renders no row again", async () => {
    act(() => applyMachine({ ...getState().machines[0] }));
    await flush();
    expect([...renders.keys()]).toEqual([]);
  });

  it("a renamed machine renders only the agent rows that show it", async () => {
    act(() => applyMachine({ ...getState().machines[0], name: "Mac Studio" }));
    await flush();
    expect([...renders.keys()].sort()).toEqual(["ix1", "ix2"]);
    expect(host.textContent).toContain("Mac Studio");
  });

  it("an inbox push renders only the item that changed, and no agent row", async () => {
    hub.items = [hub.items[0], { ...(hub.items[1] as object), title: "发布（改）" }, hub.items[2]];
    await act(() => reloadInbox());
    await flush();
    expect([...renders.keys()].sort()).toEqual(["b"]);
    expect(host.textContent).toContain("发布（改）");
  });

  it("an inbox push that changes nothing renders no row, and a new item only itself", async () => {
    await act(() => reloadInbox());
    await flush();
    expect([...renders.keys()]).toEqual([]);
    hub.items = [item("d", "新邮件"), ...hub.items];
    await act(() => reloadInbox());
    await flush();
    expect([...renders.keys()]).toEqual(["d"]);
    expect(host.textContent).toContain("新邮件");
  });
});
