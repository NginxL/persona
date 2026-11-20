// @vitest-environment jsdom
// Overview's extension cards are a section with the other sections' heading, and it folds (#11148): kept per browser,
// and folded no card is mounted, so none is asked for.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { t } from "../../i18n";

type Place = { extension: string; id: string; title: string; placement: string };
const data = vi.hoisted(() => ({ cards: [] as Place[], drawn: [] as string[] }));
vi.mock("../../lib/extension-slots", () => ({ useExtensionPlace: () => undefined, useExtensionSlot: (slot: string) => (slot === "overview_card" ? data.cards : []) }));
vi.mock("../settings/ExtensionView", () => ({
  ExtensionUI: ({ extension }: { extension: string }) => {
    data.drawn.push(extension);
    return <p data-drawn={extension} />;
  },
}));

import { loadPages } from "../../lib/lazy-page";
// Overview draws its rows of cards (#10905), and holds their one fold
import { OverviewBody } from "../OverviewBody";

const NONE = { needs_you: null, waiting_on_computer: null, in_progress: null, working: null, unread: null, computers: null };

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const mount = async () => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<OverviewBody sections={NONE} editing={false} onDone={() => {}} />));
  for (let i = 0; i < 4; i++) await act(async () => { await Promise.resolve(); });
};
const unmount = () => {
  act(() => root.unmount());
  host.remove();
};
const button = () => host.querySelector<HTMLButtonElement>("[data-overview-cards] button[aria-expanded]")!;
const cards = () => host.querySelectorAll("[data-extension-card]").length;

beforeEach(async () => {
  await loadPages();
  localStorage.clear();
  data.cards = ["a", "b", "c"].map((extension) => ({ extension, id: "card", title: extension.toUpperCase(), placement: "overview_card" }));
  data.drawn = [];
});
afterEach(unmount);

it("heads the cards like the page's other sections: the name and how many", async () => {
  await mount();
  const head = host.querySelector("[data-overview-cards] h2")!;
  expect(head.textContent).toBe(t("extensions-extensionslots.extensions"));
  expect(head.nextElementSibling?.textContent).toBe("3");
  // open until the owner folds it
  expect(cards()).toBe(3);
  expect(button().getAttribute("aria-expanded")).toBe("true");
  expect(button().textContent).toBe(t("extensions-extensionslots.cards_collapse"));
  expect(document.getElementById(button().getAttribute("aria-controls")!)?.querySelectorAll("[data-extension-card]").length).toBe(3);
});

it("folds to its heading, mounts no card while folded, and opens again", async () => {
  await mount();
  await act(async () => button().click());
  expect(cards()).toBe(0);
  expect(host.querySelector("[data-overview-cards] h2")).not.toBeNull();
  expect(host.querySelector("[data-overview-cards] h2")!.nextElementSibling?.textContent).toBe("3");
  expect(button().getAttribute("aria-expanded")).toBe("false");
  expect(button().hasAttribute("aria-controls")).toBe(false);
  expect(button().textContent).toBe(t("extensions-extensionslots.cards_expand"));
  await act(async () => button().click());
  expect(cards()).toBe(3);
});

it("keeps the fold for the next visit, when no card is asked for", async () => {
  await mount();
  await act(async () => button().click());
  expect(localStorage.getItem("fold:overview-cards")).toBe("1");
  unmount();
  data.drawn = [];
  await mount();
  expect(cards()).toBe(0);
  expect(data.drawn).toEqual([]);
  expect(button().getAttribute("aria-expanded")).toBe("false");
});

it("draws nothing, heading included, without cards", async () => {
  data.cards = [];
  await mount();
  expect(host.querySelector("[data-overview-cards]")).toBeNull();
});
