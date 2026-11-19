// @vitest-environment jsdom
// Terminal settings › Appearance: three choices a screen reader names, Match page until another is picked, and the
// line under the label saying the one chosen. Renderer: what draws this terminal, with the way to Settings › Labs (no
// switch of its own).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { t } from "../../../i18n";
import { AppearanceRow, RendererRow } from "./TerminalOptions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  localStorage.clear();
});

const radios = () => [...host.querySelectorAll<HTMLButtonElement>('[role="radio"]')];
const checked = () => radios().find((r) => r.getAttribute("aria-checked") === "true")?.getAttribute("aria-label");

describe("Appearance in Terminal settings", () => {
  it("offers Match page, Light and Dark, Match page chosen", () => {
    act(() => root.render(<AppearanceRow />));
    expect(host.querySelector('[role="radiogroup"]')?.getAttribute("aria-label")).toBe(t("session-terminal-terminaloptions.appearance"));
    expect(radios().map((r) => r.getAttribute("aria-label"))).toEqual([
      t("session-terminal-terminaloptions.appearance_page"),
      t("session-terminal-terminaloptions.appearance_light"),
      t("session-terminal-terminaloptions.appearance_dark"),
    ]);
    expect(checked()).toBe(t("session-terminal-terminaloptions.appearance_page"));
    expect(host.textContent).toContain(t("session-terminal-terminaloptions.appearance_hint_page"));
  });

  it("keeps a choice in this browser and can go back to Match page", () => {
    act(() => root.render(<AppearanceRow />));
    act(() => radios()[2].click());
    expect(localStorage.getItem("persona.terminal.appearance")).toBe("dark");
    expect(checked()).toBe(t("session-terminal-terminaloptions.appearance_dark"));
    expect(host.textContent).toContain(t("session-terminal-terminaloptions.appearance_hint_dark"));
    act(() => radios()[0].click());
    expect(localStorage.getItem("persona.terminal.appearance")).toBeNull();
    expect(checked()).toBe(t("session-terminal-terminaloptions.appearance_page"));
  });
});

describe("Renderer in Terminal settings", () => {
  it.each([
    [{ renderer: "standard", rendererFellBack: false }, "session-terminal-terminaloptions.renderer_standard"],
    [{ renderer: "webgl", rendererFellBack: false }, "session-terminal-terminaloptions.renderer_webgl"],
    [{ renderer: "standard", rendererFellBack: true }, "session-terminal-terminaloptions.renderer_fell_back"],
  ] as const)("says what draws this terminal (%o) and has no switch", (v, line) => {
    act(() => root.render(<RendererRow v={v} onOpenLabs={() => {}} />));
    expect(host.textContent).toContain(t("session-terminal-terminaloptions.renderer"));
    expect(host.textContent).toContain(t(line));
    expect(host.querySelector('[role="switch"]')).toBeNull();
  });

  it("opens Labs over the page, without leaving it", () => {
    const at = location.pathname + location.search;
    let opened = 0;
    act(() => root.render(<RendererRow v={{ renderer: "standard", rendererFellBack: false }} onOpenLabs={() => opened++} />));
    const open = [...host.querySelectorAll("button")].find((b) => b.textContent === t("session-terminal-terminaloptions.renderer_open_labs"))!;
    expect(open.getAttribute("aria-haspopup")).toBe("dialog");
    act(() => open.click());
    expect(opened).toBe(1);
    expect(location.pathname + location.search).toBe(at);
  });
});
