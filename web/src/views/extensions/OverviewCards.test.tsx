// @vitest-environment jsdom
// Overview as the user arranged it (#9745, #10905; views/OverviewBody.tsx, ExtensionSlots' OverviewCards,
// OverviewArrange): as it always was without an arrangement; a hidden card neither drawn nor asked for; a full-row card
// across the row and a lone half one still half (web audit item 76); and arranging it: earlier / later, drag and drop,
// width, hide and show, Reset with Undo, Done, without asking a drawn card again; Overview's own sections among the
// cards, moved, hidden where that is safe, kept where they are by default unless moved; a row's heading counts the
// cards shown, and arranging opens the rows when folded.
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
import { stateName } from "../../lib/state-labels";
import { adoptOverview, getOverviewPrefs, noteOverviewKept, noteSectionsLocal, onOverviewSaved, overviewRevision, SECTIONS, type OverviewEntry, type SectionId } from "../../lib/overview-cards";
import { Toasts } from "../../ui/atoms";
import { OverviewBody, type OverviewSections } from "../OverviewBody";
import { OverviewArrangeButton } from "./ExtensionSlots";

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
  empty = [];
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

// Overview's own sections as Home hands them over, each drawn as a mark of its own while it has rows (`empty`: the
// ones that have none now); their names while arranging are the editor's, Home's headings' words
const TITLES = { needs_you: stateName("needs_you"), working: stateName("working"), computers: t("home.computers") };
let empty: SectionId[] = [];
const sections = () => Object.fromEntries(SECTIONS.map((id) => [id, !empty.includes(id) && <div data-home-section={id} />])) as OverviewSections;
const body = (editing: boolean, extra: { start?: React.ReactNode; after?: Partial<Record<SectionId, React.ReactNode>> } = {}) => (
  <OverviewBody editing={editing} onDone={() => draw(false)} sections={sections()} {...extra} />
);
const draw = (editing = false) => act(() => root.render(<>{body(editing)}<Toasts /></>));
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
    act(() => root.render(<><OverviewArrangeButton on={false} onClick={() => {}} />{body(false)}</>));
    expect(host.querySelector("section")).toBeNull();
    expect(byLabel(t("home.arrange"))?.getAttribute("aria-pressed")).toBe("false");
    expect(hub.mounts).toEqual([]);
  });

  it("no card: the button is there all the same, for Overview's own sections (#10905)", () => {
    hub.cards = [];
    act(() => root.render(<OverviewArrangeButton on={false} onClick={() => {}} />));
    expect(byLabel(t("home.arrange"))).not.toBeNull();
  });
});

describe("arranging them", () => {
  it("moves a card earlier and later with its arrows, the ends disabled", () => {
    draw(true);
    // the first card goes on past the sections above it (#10905; the cards last by default, #11147): the first of those
    // is the end, and the last card
    expect(byLabel(t("extensions-overviewarrange.earlier", { name: "Usage today" }))).toHaveProperty("disabled", false);
    expect(byLabel(t("extensions-overviewarrange.earlier", { name: TITLES.needs_you }))).toHaveProperty("disabled", true);
    expect(byLabel(t("extensions-overviewarrange.later", { name: "Needs me" }))).toHaveProperty("disabled", true);
    press(byLabel(t("extensions-overviewarrange.later", { name: "Usage today" })));
    expect(saved.at(-1)?.map((e) => ("id" in e ? e.id : e.section))).toEqual(["left", "today", "needs"]);
    expect(order()).toEqual(["example-quota/left", "example-gateway/today", "example-today/needs"]);
    press(byLabel(t("extensions-overviewarrange.earlier", { name: "Needs me" })));
    expect(order()).toEqual(["example-quota/left", "example-today/needs", "example-gateway/today"]);
    expect(getOverviewPrefs().map((e) => ("id" in e ? e.id : e.section))).toEqual(["left", "needs", "today"]);
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
    act(() => noteSectionsLocal(true)); // a default manager of #9745: the cards follow, the sections stay here
    expect(head()).toContain(t("extensions-overviewarrange.sections_local"));
    act(() => noteSectionsLocal(false));
    expect(head()).toContain(t("settings.start_page_footer_synced"));
    hub.synced = false; // a hub without the route
    draw(true);
    expect(head()).toContain(t("settings.start_page_footer_local"));
  });
});

describe("Overview's own sections (#10905)", () => {
  const CARD_KEYS = ["example-gateway/today", "example-quota/left", "example-today/needs"];
  // what is drawn, top to bottom: the cards by key, the sections by id, a section's bar while arranging as `bar:<id>`
  const drawnOrder = () =>
    [...host.querySelectorAll<HTMLElement>("[data-extension-card],[data-home-section],[data-overview-section],[data-mark]")].map(
      (el) => el.dataset.extensionCard ?? el.dataset.homeSection ?? (el.dataset.overviewSection ? `bar:${el.dataset.overviewSection}` : `mark:${el.dataset.mark}`),
    );
  const bar = (id: SectionId) => host.querySelector<HTMLElement>(`[data-overview-section="${id}"]`)!;
  const cardsThenSections = (o: OverviewEntry[]) => o.map((e) => ("id" in e ? e.id : e.section));
  const OTHER_SECTIONS: OverviewEntry[] = SECTIONS.filter((x) => x !== "in_progress").map((x) => ({ section: x }));
  // In progress first, then the other sections and the cards as they were: the user's target arrangement
  const IN_PROGRESS_FIRST: OverviewEntry[] = [{ section: "in_progress" }, ...OTHER_SECTIONS, ...CARDS.map((c) => ({ extension: c.extension, id: c.id }))];

  it("without an arrangement: the sections in their order, then the cards (#11147); Loading where the sections begin, the quiet card after Unread", () => {
    act(() => root.render(body(false, { start: <i data-mark="loading" />, after: { unread: <i data-mark="quiet" /> } })));
    expect(drawnOrder()).toEqual(["mark:loading", ...SECTIONS.slice(0, 5), "mark:quiet", "computers", ...CARD_KEYS]);
  });

  it("In progress moved first (here, or on another device): right under the greeting, above Needs you, the rest as they were", () => {
    arrange(IN_PROGRESS_FIRST);
    act(() => root.render(body(false, { start: <i data-mark="loading" /> })));
    expect(drawnOrder()).toEqual(["mark:loading", "in_progress", "needs_you", "waiting_on_computer", "working", "unread", "computers", ...CARD_KEYS]);
    // the cards are still one row
    expect(host.querySelectorAll("section")).toHaveLength(1);
  });

  it("the cards moved above every section: Loading still comes where the sections begin, under them", () => {
    // the cards it does not name come after the one it names, so all of them are above the sections
    arrange([{ extension: "example-gateway", id: "today" }, ...SECTIONS.map((x) => ({ section: x }))]);
    act(() => root.render(body(false, { start: <i data-mark="loading" /> })));
    expect(drawnOrder()).toEqual([...CARD_KEYS, "mark:loading", ...SECTIONS]);
  });

  it("a hidden section is not drawn, the card after Unread still is; Needs you and Stuck are drawn whatever the list says", () => {
    arrange(SECTIONS.map((x) => (x === "in_progress" || x === "working" ? { section: x } : { section: x, hidden: true })));
    act(() => root.render(body(false, { after: { unread: <i data-mark="quiet" /> } })));
    expect(drawnOrder()).toEqual(["needs_you", "waiting_on_computer", "in_progress", "working", "mark:quiet", ...CARD_KEYS]);
  });

  it("a section between cards splits them into two rows, each laid out on its own, each with its heading; they fold as one", () => {
    arrange([{ extension: "example-gateway", id: "today" }, { section: "in_progress" }, { extension: "example-quota", id: "left" }, { extension: "example-today", id: "needs" }, ...OTHER_SECTIONS]);
    draw();
    expect(drawnOrder().slice(0, 4)).toEqual(["example-gateway/today", "in_progress", "example-quota/left", "example-today/needs"]);
    const rows = [...host.querySelectorAll("section")];
    expect(rows).toHaveLength(2);
    // a lone half card keeps item 76's rule; the two after the section go two a row
    expect(rows[0].querySelector("div[id]")!.className).not.toContain("md:grid-cols-2");
    expect(rows[1].querySelector("div[id]")!.className).toContain("md:grid-cols-2");
    // each row says how many cards it shows (#11148); folding one folds them all, and opens them all again
    expect(rows.map((r) => r.querySelector("h2")!.nextElementSibling?.textContent)).toEqual(["1", "2"]);
    press(rows[1].querySelector("button[aria-expanded]"));
    expect(cards()).toEqual([]);
    expect([...host.querySelectorAll("section button[aria-expanded]")].map((b) => b.getAttribute("aria-expanded"))).toEqual(["false", "false"]);
    press(host.querySelector("section button[aria-expanded]"));
    expect(order()).toEqual(["example-gateway/today", "example-quota/left", "example-today/needs"]);
  });

  it("arranging: each section is a bar in its place, even with no rows now; Needs you and Stuck have no switch and say they are always shown", () => {
    empty = ["needs_you", "working"];
    draw(true);
    expect(drawnOrder()).toEqual([...SECTIONS.map((x) => `bar:${x}`), ...CARD_KEYS]);
    expect(bar("working").textContent).toContain(TITLES.working);
    // a switch the user can reach: the fixed ones keep only an unseen stand-in, so the arrows line up
    const reachable = (id: SectionId) => [...bar(id).querySelectorAll("[role=switch]")].filter((x) => !x.closest("[aria-hidden]"));
    for (const fixed of ["needs_you", "waiting_on_computer"] as const) {
      expect(reachable(fixed)).toEqual([]);
      expect(bar(fixed).querySelector("[aria-hidden] [role=switch]")?.closest(".invisible")).not.toBeNull();
      expect(bar(fixed).textContent).toContain(t("extensions-overviewarrange.always_shown"));
    }
    for (const x of ["in_progress", "working", "unread", "computers"] as const) expect(reachable(x)).toHaveLength(1);
    // the first section's earlier and the last card's later are the ends
    expect(byLabel(t("extensions-overviewarrange.earlier", { name: TITLES.needs_you }))).toHaveProperty("disabled", true);
    expect(byLabel(t("extensions-overviewarrange.earlier", { name: "Usage today" }))).toHaveProperty("disabled", false);
    expect(byLabel(t("extensions-overviewarrange.later", { name: "Needs me" }))).toHaveProperty("disabled", true);
  });

  it("In progress dropped on Needs you takes its place, first; the list then names every section, and Done keeps it there", () => {
    draw(true);
    const drag = (type: string, el: HTMLElement) => {
      const e = new Event(type, { bubbles: true, cancelable: true });
      Object.defineProperty(e, "dataTransfer", { value: { setData() {}, effectAllowed: "", dropEffect: "" } });
      act(() => void el.dispatchEvent(e));
    };
    expect(bar("in_progress").draggable).toBe(true);
    drag("dragstart", bar("in_progress"));
    drag("dragover", bar("needs_you"));
    expect(bar("needs_you").dataset.drag).toBe("over");
    drag("drop", bar("needs_you"));
    drag("dragend", bar("in_progress"));
    expect(host.querySelector("[data-drag]")).toBeNull();
    expect(saved).toHaveLength(1);
    expect(saved[0]).toEqual(IN_PROGRESS_FIRST);
    expect(drawnOrder()[0]).toBe("bar:in_progress");
    // the cards stayed drawn: nothing of them is asked again
    expect(hub.mounts).toHaveLength(3);
    press(byText(t("extensions-overviewarrange.done")));
    expect(drawnOrder().slice(0, 2)).toEqual(["in_progress", "needs_you"]);
    expect(hub.mounts).toHaveLength(3);
  });

  it("a card moves on past the sections with its arrows, and a section past the cards", () => {
    draw(true);
    press(byLabel(t("extensions-overviewarrange.earlier", { name: "Usage today" })));
    expect(cardsThenSections(saved.at(-1)!)).toEqual(["needs_you", "waiting_on_computer", "in_progress", "working", "unread", "today", "computers", "left", "needs"]);
    press(byLabel(t("extensions-overviewarrange.later", { name: TITLES.computers })));
    expect(cardsThenSections(saved.at(-1)!).slice(-3)).toEqual(["left", "computers", "needs"]);
  });

  it("hides a section (its bar an outline) and shows it again; after Done it is not drawn", () => {
    draw(true);
    press(byLabel(t("extensions-overviewarrange.show", { name: TITLES.working })));
    expect(saved.at(-1)?.find((e) => "section" in e && e.section === "working")).toEqual({ section: "working", hidden: true });
    expect(bar("working").className).toContain("shadow-hairline");
    expect(bar("working").textContent).toContain(t("extensions-overviewarrange.section_hidden"));
    press(byText(t("extensions-overviewarrange.done")));
    expect(drawnOrder()).not.toContain("working");
    draw(true);
    press(byLabel(t("extensions-overviewarrange.show", { name: TITLES.working })));
    expect(saved.at(-1)?.find((e) => "section" in e && e.section === "working")).toEqual({ section: "working" });
  });

  it("moving only cards, with the sections where they are by default, writes only cards (a manager of #9745 keeps it)", () => {
    draw(true);
    press(byLabel(t("extensions-overviewarrange.earlier", { name: "Needs me" })));
    expect(saved.at(-1)).toEqual([{ extension: "example-gateway", id: "today" }, { extension: "example-today", id: "needs" }, { extension: "example-quota", id: "left" }]);
  });

  it("a section this web does not have (a newer web's) stays where it was when this one changes the arrangement", () => {
    arrange([{ section: "later_section" }, ...IN_PROGRESS_FIRST]);
    draw(true);
    expect(drawnOrder()).not.toContain("bar:later_section");
    press(byLabel(t("extensions-overviewarrange.later", { name: "Usage today" })));
    expect(cardsThenSections(saved.at(-1)!)).toEqual(["later_section", "in_progress", "needs_you", "waiting_on_computer", "working", "unread", "computers", "left", "today", "needs"]);
  });

  it("Reset puts the sections back too, and Undo the arrangement", () => {
    arrange(IN_PROGRESS_FIRST);
    draw(true);
    press(byText(t("extensions-overviewarrange.reset")));
    expect(saved.at(-1)).toEqual([]);
    expect(drawnOrder()).toEqual([...SECTIONS.map((x) => `bar:${x}`), ...CARD_KEYS]);
    press(byText(t("extensions-overviewarrange.undo")));
    expect(saved.at(-1)).toEqual(IN_PROGRESS_FIRST);
    expect(drawnOrder()[0]).toBe("bar:in_progress");
  });
});
