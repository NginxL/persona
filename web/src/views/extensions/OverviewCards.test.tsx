// @vitest-environment jsdom
// Overview's extension cards as the user arranged them (#9745, ExtensionSlots' OverviewCards, OverviewArrange): as they
// always were without an arrangement; a hidden card neither drawn nor asked for; a full-row card across the row and a
// lone half one still half (web audit item 76); and arranging them: earlier / later, drag and drop, width, hide and
// show, Reset with Undo, Done, without asking a drawn card again; the section's heading counts the cards shown, and
// arranging opens it when folded.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { UIPlace } from "../../lib/extension-ui";

const hub = vi.hoisted(() => ({ cards: [] as UIPlace[], synced: true as boolean | null, mounts: [] as string[] }));
vi.mock("../../lib/extension-slots", () => ({ useExtensionSlot: () => hub.cards, useExtensionPlace: () => undefined }));
vi.mock("../../lib/start-page", () => ({ useStartPageSynced: () => hub.synced }));
// what a card draws, and so whether its view is asked for: its render request goes out when ExtensionUI mounts
vi.mock("../settings/ExtensionView", async () => {
  const { useEffect } = await import("react");
  return {
    ExtensionUI: ({ extension, ui }: { extension: string; ui: string }) => {
      useEffect(() => void hub.mounts.push(`${extension}/${ui}`), [extension, ui]);
      return <div data-view={`${extension}/${ui}`} />;
    },
  };
});

import { t } from "../../i18n";
import { feedCacheScope, scopedKey } from "../../lib/feed-cache";
import { loadPages } from "../../lib/lazy-page";
import { adoptOverview, getOverviewPrefs, noteOverviewKept, onOverviewSaved, overviewRevision, type OverviewEntry } from "../../lib/overview-cards";
import { Toasts } from "../../ui/atoms";
import { OverviewArrangeButton, OverviewCards } from "./ExtensionSlots";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const place = (extension: string, id: string, title: string): UIPlace => ({ extension, id, title, placement: "overview_card", extension_name: extension, size: "compact" }) as UIPlace;
// the hub's order: by extension, then the manifest's
const CARDS = [place("example-gateway", "today", "Usage today"), place("example-quota", "left", "Quota left"), place("example-today", "needs", "Needs me")];

let host: HTMLDivElement;
let root: Root;
let saved: OverviewEntry[][];

beforeAll(() => loadPages());

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem("persona.token", "test-token"); // the copy is kept per hub and sign-in
  window.dispatchEvent(new StorageEvent("storage", { key: null }));
  hub.cards = CARDS;
  hub.synced = true;
  hub.mounts = [];
  saved = [];
  onOverviewSaved((p) => saved.push(p));
  noteOverviewKept(true);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const draw = (editing = false) => act(() => root.render(<><OverviewCards editing={editing} onDone={() => draw(false)} /><Toasts /></>));
// this browser's copy, as another tab would leave it (the store reads it again)
const arrange = (list: OverviewEntry[]) => {
  const key = scopedKey(feedCacheScope(), "overview.cards")!;
  const oldValue = localStorage.getItem(key);
  localStorage.setItem(key, JSON.stringify(list));
  window.dispatchEvent(new StorageEvent("storage", { key, oldValue, newValue: JSON.stringify(list) }));
};
const cards = () => [...host.querySelectorAll<HTMLElement>("[data-extension-card]")];
const order = () => cards().map((c) => c.dataset.extensionCard);
// the grid of cards, inside the section under its heading (#11148)
const grid = () => host.querySelector("[data-overview-cards] > div[id]")!;
const card = (key: string) => host.querySelector<HTMLElement>(`[data-extension-card="${key}"]`)!;
const press = (el: Element | null | undefined) => act(() => (el as HTMLElement).click());
const byLabel = (label: string) => host.querySelector<HTMLElement>(`[aria-label="${label}"]`);
const byText = (text: string) => [...document.querySelectorAll<HTMLElement>("button")].find((b) => b.textContent === text);

describe("Overview's cards", () => {
  it("without an arrangement: the hub's cards in its order, half a row, two a row", () => {
    draw();
    expect(order()).toEqual(["example-gateway/today", "example-quota/left", "example-today/needs"]);
    expect(cards().some((c) => c.classList.contains("col-span-full"))).toBe(false);
    expect(grid().className).toContain("md:grid-cols-2");
    expect(hub.mounts).toEqual(["example-gateway/today", "example-quota/left", "example-today/needs"]);
  });

  it("a hidden card is not drawn, and its view is never asked for", () => {
    arrange([{ extension: "example-quota", id: "left", hidden: true }]);
    draw();
    expect(order()).toEqual(["example-gateway/today", "example-today/needs"]);
    expect(hub.mounts).not.toContain("example-quota/left");
  });

  it("the heading counts the cards shown; arranging opens a folded section, without its Collapse / Expand (#11148)", () => {
    arrange([{ extension: "example-quota", id: "left", hidden: true }]);
    localStorage.setItem("fold:overview-cards", "1");
    const count = () => host.querySelector("[data-overview-cards] h2")!.nextElementSibling?.textContent;
    const fold = () => host.querySelector("[data-overview-cards] button[aria-expanded]");
    draw();
    expect(count()).toBe("2");
    expect(cards()).toEqual([]);
    expect(fold()?.getAttribute("aria-expanded")).toBe("false");
    draw(true);
    expect(order()).toEqual(["example-quota/left", "example-gateway/today", "example-today/needs"]);
    expect(count()).toBe("2");
    expect(fold()).toBeNull();
    draw(false);
    expect(cards()).toEqual([]);
    expect(fold()?.getAttribute("aria-expanded")).toBe("false");
  });

  it("a full-row card spans the row; a lone half card stays half on a wide page (web audit item 76)", () => {
    arrange([{ extension: "example-today", id: "needs", span: "full" }, { extension: "example-quota", id: "left", hidden: true }]);
    draw();
    expect(order()).toEqual(["example-today/needs", "example-gateway/today"]);
    expect(card("example-today/needs").className).toContain("col-span-full");
    expect(card("example-gateway/today").className).not.toContain("col-span-full");
    // one half card: it fills the row until the page has room for two columns of rows, then takes one
    expect(grid().className).not.toContain("md:grid-cols-2");
    expect(grid().className).toContain("@min-[60rem]:grid-cols-2");
  });

  it("a card installed since comes after the arranged ones; one that is gone is passed over", () => {
    arrange([{ extension: "example-removed", id: "card", span: "full" }, { extension: "example-quota", id: "left" }]);
    draw();
    expect(order()).toEqual(["example-quota/left", "example-gateway/today", "example-today/needs"]);
  });

  it("all hidden: no row at all, and the button to arrange them stays", () => {
    arrange(CARDS.map((c) => ({ extension: c.extension, id: c.id, hidden: true })));
    act(() => root.render(<><OverviewArrangeButton on={false} onClick={() => {}} /><OverviewCards /></>));
    expect(host.querySelector("section")).toBeNull();
    expect(byLabel(t("home.arrange_cards"))?.getAttribute("aria-pressed")).toBe("false");
    expect(hub.mounts).toEqual([]);
  });

  it("no card: no button to arrange them", () => {
    hub.cards = [];
    act(() => root.render(<OverviewArrangeButton on={false} onClick={() => {}} />));
    expect(host.innerHTML).toBe("");
  });
});

describe("arranging them", () => {
  it("moves a card earlier and later with its arrows, the ends disabled", () => {
    draw(true);
    expect(byLabel(t("extensions-overviewarrange.earlier", { name: "Usage today" }))).toHaveProperty("disabled", true);
    expect(byLabel(t("extensions-overviewarrange.later", { name: "Needs me" }))).toHaveProperty("disabled", true);
    press(byLabel(t("extensions-overviewarrange.later", { name: "Usage today" })));
    expect(saved.at(-1)?.map((e) => e.id)).toEqual(["left", "today", "needs"]);
    expect(order()).toEqual(["example-quota/left", "example-gateway/today", "example-today/needs"]);
    press(byLabel(t("extensions-overviewarrange.earlier", { name: "Needs me" })));
    expect(order()).toEqual(["example-quota/left", "example-today/needs", "example-gateway/today"]);
    expect(getOverviewPrefs().map((e) => e.id)).toEqual(["left", "needs", "today"]);
  });

  it("a card dragged onto another takes its place", () => {
    draw(true);
    const drag = (type: string, el: HTMLElement) => {
      const e = new Event(type, { bubbles: true, cancelable: true });
      Object.defineProperty(e, "dataTransfer", { value: { setData() {}, effectAllowed: "", dropEffect: "" } });
      act(() => void el.dispatchEvent(e));
    };
    const from = card("example-today/needs");
    const onto = card("example-gateway/today");
    expect(from.draggable).toBe(true);
    drag("dragstart", from);
    expect(from.dataset.drag).toBe("from");
    drag("dragover", onto);
    expect(onto.dataset.drag).toBe("over");
    expect(onto.className).toContain("data-[drag=over]:shadow-[inset_0_0_0_2px_var(--accent)]");
    drag("drop", onto);
    drag("dragend", from);
    expect(host.querySelector("[data-drag]")).toBeNull();
    expect(order()).toEqual(["example-today/needs", "example-gateway/today", "example-quota/left"]);
    expect(saved).toHaveLength(1);
  });

  it("makes a card a full row and half again", () => {
    draw(true);
    const width = byLabel(t("extensions-overviewarrange.width", { name: "Quota left" })) as HTMLSelectElement;
    act(() => {
      width.value = "full";
      width.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(card("example-quota/left").className).toContain("col-span-full");
    expect(saved.at(-1)).toEqual([{ extension: "example-gateway", id: "today" }, { extension: "example-quota", id: "left", span: "full" }, { extension: "example-today", id: "needs" }]);
    act(() => {
      width.value = "half";
      width.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(card("example-quota/left").className).not.toContain("col-span-full");
  });

  it("hides a card (drawn as its outline, nothing of it asked for) and shows it again", () => {
    arrange([{ extension: "example-quota", id: "left", hidden: true }]);
    draw(true);
    // drawn in its place while arranging, without its content
    expect(order()).toEqual(["example-quota/left", "example-gateway/today", "example-today/needs"]);
    expect(card("example-quota/left").textContent).toContain(t("extensions-overviewarrange.hidden"));
    expect(card("example-quota/left").querySelector("[data-view]")).toBeNull();
    expect(hub.mounts).not.toContain("example-quota/left");
    const show = byLabel(t("extensions-overviewarrange.show", { name: "Quota left" }))!;
    expect(show.getAttribute("aria-checked")).toBe("false");
    press(show);
    expect(hub.mounts).toContain("example-quota/left");
    expect(saved.at(-1)?.[0]).toEqual({ extension: "example-quota", id: "left" });
    press(byLabel(t("extensions-overviewarrange.show", { name: "Needs me" })));
    expect(saved.at(-1)?.[2]).toEqual({ extension: "example-today", id: "needs", hidden: true });
    // Done: the hidden one is gone from Overview
    press(byText(t("extensions-overviewarrange.done")));
    expect(order()).toEqual(["example-quota/left", "example-gateway/today"]);
  });

  it("arranging and done ask no drawn card again, and its content takes no click meanwhile", () => {
    draw();
    expect(hub.mounts).toHaveLength(3);
    draw(true);
    expect(card("example-gateway/today").querySelector("[inert]")).not.toBeNull();
    draw(false);
    expect(hub.mounts).toHaveLength(3);
    expect(card("example-gateway/today").querySelector("[inert]")).toBeNull();
    expect(card("example-gateway/today").draggable).toBe(false);
  });

  it("a card's entrance plays once: moved after it, the card does not play it again", () => {
    draw(true);
    const first = card("example-gateway/today");
    expect(first.style.animation).toContain("fade-up");
    act(() => void first.dispatchEvent(new Event("animationend", { bubbles: true })));
    expect(first.style.animation).toBe("");
    press(byLabel(t("extensions-overviewarrange.later", { name: "Usage today" })));
    expect(card("example-gateway/today")).toBe(first);
    expect(first.style.animation).toBe("");
    // one inside it ending is not the card's
    const second = card("example-quota/left");
    act(() => void second.querySelector("[data-view]")!.dispatchEvent(new Event("animationend", { bubbles: true })));
    expect(second.style.animation).toContain("fade-up");
  });

  it("Reset puts the cards back as they were, with Undo", () => {
    const list = [{ extension: "example-today", id: "needs", span: "full" as const }, { extension: "example-gateway", id: "today", hidden: true }];
    arrange(list);
    draw(true);
    press(byText(t("extensions-overviewarrange.reset")));
    expect(saved.at(-1)).toEqual([]);
    expect(order()).toEqual(["example-gateway/today", "example-quota/left", "example-today/needs"]);
    expect(byText(t("extensions-overviewarrange.reset"))).toBeUndefined();
    press(byText(t("extensions-overviewarrange.undo")));
    expect(saved.at(-1)).toEqual(list);
    expect(order()).toEqual(["example-today/needs", "example-gateway/today", "example-quota/left"]);
  });

  // review of #9778, P1: Undo put the old arrangement back over a newer one
  it("Undo leaves a newer arrangement alone and says so: one from another device, or another tab", () => {
    const list = [{ extension: "example-today", id: "needs", span: "full" as const }];
    const phone = [{ extension: "example-quota", id: "left", hidden: true }];
    for (const elsewhere of [() => adoptOverview(phone), () => arrange(phone)]) {
      arrange(list);
      draw(true);
      saved = [];
      press(byText(t("extensions-overviewarrange.reset")));
      act(elsewhere);
      press(byText(t("extensions-overviewarrange.undo")));
      expect(getOverviewPrefs()).toEqual(phone);
      expect(saved).toEqual([[]]); // nothing written over it
      expect(document.body.textContent).toContain(t("extensions-overviewarrange.undo_stale"));
      expect(order()[0]).toBe("example-quota/left");
    }
  });

  // review of #9778, P2: a newer arrangement that is none too was taken for the one Reset left
  it("an older Reset's Undo leaves a newer one alone: reset, arranged again, reset again", () => {
    const list = [{ extension: "example-today", id: "needs", span: "full" as const }];
    arrange(list);
    draw(true);
    press(byText(t("extensions-overviewarrange.reset")));
    press(byLabel(t("extensions-overviewarrange.later", { name: "Usage today" }))); // arranged again
    press(byText(t("extensions-overviewarrange.reset")));
    const undos = [...document.querySelectorAll<HTMLElement>("button")].filter((b) => b.textContent === t("extensions-overviewarrange.undo"));
    expect(undos).toHaveLength(2);
    saved = [];
    press(undos[0]); // the first Reset's
    expect(getOverviewPrefs()).toEqual([]);
    expect(saved).toEqual([]);
    expect(document.body.textContent).toContain(t("extensions-overviewarrange.undo_stale"));
    press(undos[1]); // the second's still puts back what it reset
    expect(saved).toEqual([[{ extension: "example-quota", id: "left" }, { extension: "example-gateway", id: "today" }, { extension: "example-today", id: "needs" }]]);
  });

  // review of #9778, P2: another tab clearing storage, signing out or removing another sign-in's copy refused Undo
  it("Undo still puts back after storage events that leave this sign-in's copy as Reset left it", () => {
    const list = [{ extension: "example-today", id: "needs", span: "full" as const }];
    const key = scopedKey(feedCacheScope(), "overview.cards")!;
    arrange(list);
    draw(true);
    press(byText(t("extensions-overviewarrange.reset")));
    act(() => {
      window.dispatchEvent(new StorageEvent("storage", { key: "persona.feed.another:overview.cards", oldValue: JSON.stringify(list), newValue: null }));
      window.dispatchEvent(new StorageEvent("storage", { key, oldValue: null, newValue: null })); // nothing of it changed
      window.dispatchEvent(new StorageEvent("storage", { key: null })); // storage cleared: this sign-in held no copy
    });
    saved = [];
    press(byText(t("extensions-overviewarrange.undo")));
    expect(saved).toEqual([list]);
  });

  it("a clear of storage that held this sign-in's copy is newer for Undo, one another tab just wrote too", () => {
    const list = [{ extension: "example-today", id: "needs", span: "full" as const }];
    // nothing drawn reads the copy (Reset's toast outlives Overview): the store still knows what it held
    const before = overviewRevision();
    act(() => {
      arrange(list);
      window.dispatchEvent(new StorageEvent("storage", { key: null }));
    });
    expect(overviewRevision()).toBe(before + 2);
  });

  it("says where the arrangement is kept: every device, or this browser only", () => {
    draw(true);
    const head = () => host.querySelector("[data-overview-arrange]")!.textContent;
    expect(head()).toContain(t("settings.start_page_footer_synced"));
    act(() => noteOverviewKept(false));
    expect(head()).toContain(t("settings.start_page_footer_local"));
    act(() => noteOverviewKept(true));
    hub.synced = false; // a hub without the route
    draw(true);
    expect(head()).toContain(t("settings.start_page_footer_local"));
  });
});
