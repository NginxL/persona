// @vitest-environment jsdom
import { renderToStaticMarkup } from "react-dom/server";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { loadPages } from "../../lib/lazy-page";
import { ExtensionNavPage, MachineExtensionSections, nameBesideTitle, OverviewCards } from "./ExtensionSlots";

type Place = { extension: string; id: string; title?: string; extension_name?: string; placement?: string; opens?: string };
const data = vi.hoisted(() => ({ place: undefined as Place | undefined, slots: {} as Record<string, Place[]> }));
vi.mock("../../lib/extension-slots", () => ({ useExtensionPlace: () => data.place, useExtensionSlot: (slot: string) => data.slots[slot] ?? [] }));
vi.mock("../settings/ExtensionView", () => ({ ExtensionUI: () => null }));

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
    const html = renderToStaticMarkup(<OverviewCards />);
    expect(count(html, "data-extension-card=")).toBe(3);
    expect(names(html)).toEqual(["模型用量"]);
    expect(count(html, "行情")).toBe(1);
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
