// @vitest-environment jsdom
import { renderToStaticMarkup } from "react-dom/server";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { loadPages } from "../../lib/lazy-page";
import { arrangeOverview, type ArrangedCard } from "../../lib/overview-cards";
import type { UIPlace } from "../../lib/extension-ui";
import { ExtensionNavPage, MachineExtensionSections, nameBesideTitle, OverviewCards } from "./ExtensionSlots";

type Place = { extension: string; id: string; title?: string; extension_name?: string; placement?: string; opens?: string };
const data = vi.hoisted(() => ({ place: undefined as Place | undefined, slots: {} as Record<string, Place[]> }));
vi.mock("../../lib/extension-slots", () => ({ useExtensionPlace: () => data.place, useExtensionSlot: (slot: string) => data.slots[slot] ?? [] }));
const seen = vi.hoisted(() => [] as Array<Record<string, unknown>>);
vi.mock("../settings/ExtensionView", () => ({
  ExtensionUI: ({ extension, ui, compact, remember }: Record<string, unknown>) => {
    if (compact) seen.push({ extension, ui, compact, remember });
    return null;
  },
}));

// Overview's row of the hub's cards, as Overview hands it over (#9745, #10905): nothing arranged here, so the hub's order
const row = () => {
  const items = arrangeOverview((data.slots.overview_card ?? []) as UIPlace[], []);
  return <OverviewCards cards={items.filter((i): i is ArrangedCard<UIPlace> => "card" in i)} items={items} />;
};

/** The page's top bar: its title, then the extension's name unless that only says the title again (#3115). */
function header(place: typeof data.place) {
  data.place = place;
  const html = renderToStaticMarkup(<ExtensionNavPage extension="example" ui="usage" onToggleSidebar={() => {}} />);
  return html.slice(html.indexOf("<header"), html.indexOf("</header>"));
}

const count = (html: string, text: string) => html.split(text).length - 1;

// the code views and the extension page are their own chunks (lib/lazy-page.ts, #1794): in before anything is drawn
beforeAll(() => loadPages(), 60_000);

describe("extension page top bar (#3115)", () => {
  it("writes a name that is the page's title once", () => {
    expect(count(header({ extension: "example", id: "usage", title: "示例", extension_name: "示例" }), "示例")).toBe(1);
    // the same apart from case and spaces around it
    expect(count(header({ extension: "example", id: "usage", title: "Usage", extension_name: " usage " }).toLowerCase(), "usage")).toBe(1);
  });

  it("still writes a different extension name after the title", () => {
    const html = header({ extension: "example", id: "usage", title: "Token 用量", extension_name: "模型用量" });
    expect(html).toContain("Token 用量");
    expect(html).toContain("模型用量");
  });
});

/** What a card or a section writes beside its title as its extension's name. */
const names = (html: string) => [...html.matchAll(/<span data-extension-name[^>]*>(?:<svg[\s\S]*?<\/svg>)?(?:<span[^>]*>)?([^<]*)/g)].map((m) => m[1]);

describe("the extension's name beside a title (#10940)", () => {
  it("is left out when it only says the title again", () => {
    expect(nameBesideTitle("Markets", "Markets")).toBeUndefined();
    expect(nameBesideTitle(" markets ", "Markets")).toBeUndefined();
    expect(nameBesideTitle("Example usage", "Token usage")).toBe("Example usage");
    expect(nameBesideTitle(undefined, "Markets")).toBeUndefined();
    expect(nameBesideTitle("", "Markets")).toBeUndefined();
  });

  it("names an Overview card's extension only when its title does not", () => {
    data.slots = {
      overview_card: [
        { extension: "markets", id: "card", title: "行情", extension_name: "行情", placement: "overview_card", opens: "watchlist" },
        { extension: "example", id: "today", title: "Token 用量", extension_name: "模型用量", placement: "overview_card" },
        { extension: "plain", id: "card", title: "Plain", placement: "overview_card" },
      ],
    };
    // Overview hands the row its cards as arranged (#9745, #10905): nothing arranged here, so the hub's order
    const items = arrangeOverview(data.slots.overview_card as UIPlace[], []);
    const cards = items.filter((i): i is ArrangedCard<UIPlace> => "card" in i);
    const html = renderToStaticMarkup(<OverviewCards cards={cards} items={items} />);
    expect(count(html, "data-extension-card=")).toBe(3);
    expect(names(html)).toEqual(["模型用量"]);
    expect(count(html, "行情")).toBe(1);
  });

  it("keeps the cards of a row one height: each as tall as its own content left ragged rows (#11093, back in #11109)", () => {
    data.slots = { overview_card: [{ extension: "a", id: "card", title: "A", placement: "overview_card" }, { extension: "b", id: "card", title: "B", placement: "overview_card" }] };
    const grid = renderToStaticMarkup(row()).match(/<div id="[^"]*" class="([^"]*)"/)![1]!.split(" ");
    expect(grid).toContain("grid");
    expect(grid).not.toContain("items-start");
  });

  it("asks for each Overview card's last view and marks its heading while that is on screen (#11091)", () => {
    data.slots = { overview_card: [{ extension: "a", id: "card", title: "A", placement: "overview_card" }] };
    seen.length = 0;
    const html = renderToStaticMarkup(row());
    expect(seen).toEqual([{ extension: "a", ui: "card", compact: true, remember: true }]);
    // the mark is in the card's heading, drawn only while the card holds a kept view (a CSS :has on the card)
    const mark = html.match(/<span[^>]*data-card-updating[^>]*>/)![0];
    expect(mark).toContain("hidden");
    expect(mark).toContain("group-has-[[data-kept-view]]/card:inline-flex");
    expect(mark).toContain('aria-hidden="true"');
    expect(html).toMatch(/data-extension-card="a\/card"[^>]*class="group\/card /);
  });

  it("names a machine section's extension only when its title does not", () => {
    data.slots = {
      machine_section: [
        { extension: "disks", id: "section", title: "Disks", extension_name: "disks", placement: "machine_section" },
        { extension: "example", id: "gpu", title: "GPU", extension_name: "Example monitor", placement: "machine_section" },
      ],
    };
    const html = renderToStaticMarkup(<MachineExtensionSections mid="mac" heading={(title, right) => <h2>{title}{right}</h2>} />);
    expect(count(html, "data-extension-section=")).toBe(2);
    expect(names(html)).toEqual(["Example monitor"]);
  });
});
