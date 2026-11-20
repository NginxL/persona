// @vitest-environment jsdom
// Settings › General › Appearance › Sidebar icons (#10822, lib/sidebar-icons.ts): Monochrome unless chosen, Colored kept
// in this browser, and choosing Monochrome again stores nothing, as before the setting existed.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { initI18n } from "../../i18n";
import { sidebarIconMode } from "../../lib/sidebar-icons";
import { SidebarIconsRow } from "./LookSettings";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const radios = () => [...host.querySelectorAll<HTMLButtonElement>("[role=radio]")];
const checked = () => radios().find((r) => r.getAttribute("aria-checked") === "true")?.textContent;

beforeEach(() => {
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  act(() => root.unmount());
  host.remove();
  await initI18n("zh-Hans");
});

describe("Sidebar icons", () => {
  it("is Monochrome until Colored is chosen, and Monochrome again stores nothing", () => {
    act(() => root.render(<SidebarIconsRow />));
    expect(host.querySelector("[data-settings-row=sidebar-icons]")?.textContent).toContain("侧栏图标");
    expect(radios().map((r) => r.textContent)).toEqual(["单色", "彩色"]);
    expect(checked()).toBe("单色");
    act(() => radios()[1].click());
    expect(checked()).toBe("彩色");
    expect(localStorage.getItem("persona.sidebar-icons")).toBe("colored");
    expect(sidebarIconMode()).toBe("colored");
    act(() => radios()[0].click());
    expect(checked()).toBe("单色");
    expect(localStorage.getItem("persona.sidebar-icons")).toBeNull();
  });

  it.each([
    ["en", "Sidebar icons", ["Monochrome", "Colored"]],
    ["ja", "サイドバーのアイコン", ["モノクロ", "カラー"]],
  ] as const)("is named in %s", async (lang, name, choices) => {
    await initI18n(lang);
    act(() => root.render(<SidebarIconsRow />));
    expect(host.textContent).toContain(name);
    expect(radios().map((r) => r.textContent)).toEqual(choices);
  });
});
