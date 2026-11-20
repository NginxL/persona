// @vitest-environment jsdom
// An Overview card that opens a page shows at most a fixed height of its view (#11149): a taller one is cut, fades out
// and gets "See all" to its page; a card without a page is never cut.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { t } from "../../i18n";

type Place = { extension: string; id: string; title: string; placement: string; opens?: string };
const data = vi.hoisted(() => ({ cards: [] as Place[], heights: {} as Record<string, number>, resize: [] as Array<() => void> }));
vi.mock("../../lib/extension-slots", () => ({ useExtensionPlace: () => undefined, useExtensionSlot: (slot: string) => (slot === "overview_card" ? data.cards : []) }));
vi.mock("../settings/ExtensionView", () => ({ ExtensionUI: ({ extension }: { extension: string }) => <p data-drawn={extension} /> }));

import { loadPages } from "../../lib/lazy-page";
// Overview draws its rows of cards (#10905)
import { OverviewBody } from "../OverviewBody";

const NONE = { needs_you: null, waiting_on_computer: null, in_progress: null, working: null, unread: null, computers: null };

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const LIMIT = 300; // what the limit comes to in the test's layout
let host: HTMLDivElement;
let root: Root;
const card = (extension: string) => host.querySelector<HTMLElement>(`[data-extension-card="${extension}/card"]`)!;
const cut = (extension: string) => card(extension).querySelector("[data-card-view]")!.hasAttribute("data-cut");
const seeAll = (extension: string) => card(extension).querySelector<HTMLAnchorElement>("[data-card-see-all]");
const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(async () => {
  await loadPages();
  localStorage.clear();
  data.resize = [];
  data.heights = { long: 520, short: 120, nopage: 520 };
  data.cards = [
    { extension: "long", id: "card", title: "Long", placement: "overview_card", opens: "page" },
    { extension: "short", id: "card", title: "Short", placement: "overview_card", opens: "page" },
    { extension: "nopage", id: "card", title: "No page", placement: "overview_card" },
  ];
  // jsdom lays nothing out: the view is as tall as the test says, and its box as tall as the limit lets it be
  const of = (el: HTMLElement) => el.closest<HTMLElement>("[data-extension-card]")?.dataset.extensionCard?.split("/")[0] ?? "";
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) { return data.heights[of(this)] ?? 0; });
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockImplementation(function (this: HTMLElement) {
    const h = data.heights[of(this)] ?? 0;
    return this.className.includes("max-h-") ? Math.min(h, LIMIT) : h;
  });
  vi.stubGlobal("ResizeObserver", class { constructor(private fn: () => void) { data.resize.push(this.fn); } observe() {} unobserve() {} disconnect() {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<OverviewBody sections={NONE} editing={false} onDone={() => {}} />));
  await settle();
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("cuts a view taller than the limit, fades its end and links to the page", () => {
  expect(cut("long")).toBe(true);
  const view = card("long").querySelector("[data-card-view]")!;
  expect(view.className).toContain("max-h-");
  expect(view.className).toContain("overflow-hidden");
  expect(view.className).toContain("mask-image");
  expect(seeAll("long")?.textContent).toBe(t("extensions-extensionslots.card_see_all"));
  expect(seeAll("long")?.getAttribute("href")).toBe("/extension/long/page");
  // at the card's foot, whatever height its row gives the card
  expect(seeAll("long")?.className).toContain("mt-auto");
});

it("leaves a view under the limit as it is", () => {
  expect(cut("short")).toBe(false);
  expect(seeAll("short")).toBeNull();
  expect(card("short").querySelector("[data-card-view]")!.className).not.toContain("mask-image");
});

it("never cuts a card with no page to read the rest on", () => {
  expect(cut("nopage")).toBe(false);
  expect(seeAll("nopage")).toBeNull();
  expect(card("nopage").querySelector("[data-card-view]")!.className).not.toContain("max-h-");
});

it("follows the view as the extension redraws it taller or shorter", async () => {
  data.heights.short = 480;
  data.heights.long = 200;
  await act(async () => data.resize.forEach((fn) => fn()));
  expect(cut("short")).toBe(true);
  expect(seeAll("short")).not.toBeNull();
  expect(cut("long")).toBe(false);
  expect(seeAll("long")).toBeNull();
});
