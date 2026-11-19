// @vitest-environment jsdom
// Token usage (#10042): the totals, the calendar with only a picked day's line, the last 7 days by the hour, by
// computer (a removed one named so), by agent (marks and names; usage kept before the hub counted agents apart reads
// "not split"; none from an older hub) and by kind; the calendar card's line opens it in a dialog.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({ calls: [] as string[], agents: true }));
vi.mock("../../lib/api", async (orig) => {
  const real = await orig<typeof import("../../lib/api")>();
  return {
    ...real,
    api: (path: string) => {
      hub.calls.push(path);
      if (path.startsWith("/usage/days")) {
        const days = [
          { date: "2026-11-16", in: 1, out: 2_000_000, cache_write: 30_000_000, cache_read: 5_800_000_000 },
          { date: "2026-11-17", cache_read: 3_200_000_000 },
        ];
        return Promise.resolve({ usage: { zone: "UTC", stored: true, days } });
      }
      const hours = [
        { t: 0, date: "2026-11-16", hour: 9, cache_read: 900_000_000 },
        { t: 3600, date: "2026-11-17", hour: 0, cache_read: 100_000_000, out: 5_000 },
      ];
      return Promise.resolve({
        usage: {
          zone: "UTC",
          stored: true,
          hours,
          machines: [
            { id: "a", name: "Studio", cache_read: 700_000_000 },
            { id: "gone", name: "", cache_read: 300_000_000 },
          ],
          ...(hub.agents
            ? {
                agents: [
                  { agent: "claude", cache_read: 600_000_000 },
                  { agent: "codex", cache_read: 250_000_000, out: 5_000 },
                  { agent: "", cache_read: 150_000_000 },
                ],
              }
            : {}),
          total: { in: 10, out: 5_000, cache_write: 2_000, cache_read: 1_000_000_000 },
        },
      });
    },
  };
});

import TokenUsage from "./TokenUsagePage";
import TokenCalendar from "./TokenCalendar";

let root: Root;
let host: HTMLDivElement;

beforeEach(() => {
  hub.calls = [];
  hub.agents = true;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  globalThis.ResizeObserver ??= class {
    observe() {}
    disconnect() {}
    unobserve() {}
  } as unknown as typeof ResizeObserver;
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

async function show(node: React.ReactNode) {
  await act(async () => root.render(node));
  await act(async () => {});
}

describe("TokenUsagePage", () => {
  it("reads a year of days and the week by the hour, and shows every section", async () => {
    await show(<TokenUsage />);
    expect(hub.calls.sort()).toEqual(["/usage/days?days=371", "/usage/hours?days=7"]);
    const page = host.querySelector("[data-token-usage-page]")!;
    const text = page.textContent ?? "";
    // the tests run in Chinese
    for (const s of ["今天", "近 7 天", "近 30 天", "每天", "最近 7 天 · 每小时", "按电脑 · 最近 7 天", "按 Agent · 最近 7 天", "按类别 · 最近 7 天"]) {
      expect(text).toContain(s);
    }
    expect(text).toContain("Studio");
    expect(text).toContain("已移除的电脑");
    expect(text).toContain("Claude Code");
    expect(text).toContain("Codex");
    expect(text).toContain("未区分（更新前）");
    expect(page.querySelectorAll("[data-agent-mark]")).toHaveLength(2); // the not-split row has no mark
    // the calendar on the page has no totals line, only a picked day's
    expect(page.querySelector("[data-token-calendar-line]")).toBeNull();
    expect(text).toContain("和「机器」页的「今天」是同一个数");
  });

  it("has no By agent from an older hub", async () => {
    hub.agents = false;
    await show(<TokenUsage />);
    const text = host.textContent ?? "";
    expect(text).toContain("按电脑 · 最近 7 天");
    expect(text).not.toContain("按 Agent");
  });

  it("an hour clicked says its tokens", async () => {
    await show(<TokenUsage />);
    const busy = [...host.querySelectorAll<HTMLElement>("span[title]")].find((s) => s.title.includes("9亿"))!;
    expect(busy).toBeDefined();
    act(() => busy.click());
    expect(host.textContent).toContain("9亿 token");
  });
});

describe("the calendar card's line", () => {
  it("opens Token usage in a dialog, which reads its own days and hours", async () => {
    await show(<TokenCalendar />);
    expect(hub.calls).toEqual(["/usage/days?days=280"]);
    const line = host.querySelector<HTMLButtonElement>("[data-token-calendar-line]")!;
    expect(line.getAttribute("aria-haspopup")).toBe("dialog");
    await act(async () => line.click());
    await act(async () => {}); // the chunk, then its reads
    await act(async () => {});
    const dialog = document.querySelector<HTMLElement>("[role=dialog]")!;
    expect(dialog).not.toBeNull();
    expect(dialog.textContent).toContain("Token 用量");
    expect(dialog.querySelector("[data-token-usage-page]")).not.toBeNull();
    expect(hub.calls).toContain("/usage/hours?days=7");
  });
});
