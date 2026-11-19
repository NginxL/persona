// @vitest-environment jsdom
// Settings › Labs (#9877) through the generic configuration form: the terminal renderer module's choice kept in this
// browser (Standard as no key), the old WebGL switch shown as WebGL, a value from another version named and left until a
// choice replaces it, a browser that won't keep the choice saying the save is unconfirmed, and another tab's change.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { LabsSettings } from "./Labs";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const KEY = "persona.terminal.webgl";
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
  vi.restoreAllMocks();
  localStorage.clear();
});

const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => {});
};
const field = () => host.querySelector<HTMLElement>('[data-configuration-field="renderer"]')!;
const radios = () => [...field().querySelectorAll<HTMLButtonElement>('[role="radio"]')];
const chosen = () => radios().find((r) => r.getAttribute("aria-checked") === "true")?.textContent;
const render = async () => {
  act(() => root.render(<LabsSettings />));
  await settle();
};
const click = async (el: HTMLElement) => {
  act(() => el.click());
  await settle();
};

describe("Settings › Labs", () => {
  it("explains Labs and that each item states its own scope, and shows the terminal renderer under Agents", async () => {
    await render();
    expect(host.textContent).toContain(t("settings-labs.intro"));
    expect(host.textContent).toContain(t("settings-labs.scope_note"));
    expect(host.querySelector('[data-configuration="terminal"] h2')?.textContent).toBe(t("settings-labs.area_agents"));
    expect(field().textContent).toContain(t("settings-labs.terminal_renderer"));
    expect(field().textContent).toContain(t("settings-labs.terminal_renderer_hint"));
  });

  it("shows the module's default in the footer and its scope, activation and rollback as details", async () => {
    await render();
    expect(field().textContent).toContain(`${t("settings-configuration.default")} ${t("settings-labs.terminal_renderer_standard")}`);
    expect([...field().querySelectorAll("dt")].map((d) => d.textContent)).toEqual([t("settings-labs.scope"), t("settings-labs.activation"), t("settings-labs.rollback")]);
    expect([...field().querySelectorAll("dd")].map((d) => d.textContent)).toEqual([
      t("settings-labs.scope_browser"),
      t("settings-labs.terminal_renderer_activation"),
      t("settings-labs.terminal_renderer_rollback"),
    ]);
  });

  it("offers Standard and WebGL as one named choice, Standard chosen with nothing saved", async () => {
    await render();
    expect(field().querySelector('[role="radiogroup"]')?.getAttribute("aria-label")).toBe(t("settings-labs.terminal_renderer"));
    expect(radios().map((r) => r.textContent)).toEqual([t("settings-labs.terminal_renderer_standard"), t("settings-labs.terminal_renderer_webgl")]);
    expect(chosen()).toBe(t("settings-labs.terminal_renderer_standard"));
  });

  it("keeps WebGL as the old switch's value, says it is saved, and Reset removes it again", async () => {
    await render();
    await click(radios()[1]);
    expect(localStorage.getItem(KEY)).toBe("1");
    expect(chosen()).toBe(t("settings-labs.terminal_renderer_webgl"));
    expect(field().textContent).toContain(t("settings-configuration.saved"));
    const reset = [...field().querySelectorAll("button")].find((b) => b.textContent === t("settings-configuration.reset"))!;
    await click(reset);
    expect(localStorage.getItem(KEY)).toBeNull();
    expect(chosen()).toBe(t("settings-labs.terminal_renderer_standard"));
  });

  it("shows WebGL for the WebGL switch saved before Labs", async () => {
    localStorage.setItem(KEY, "1");
    await render();
    expect(chosen()).toBe(t("settings-labs.terminal_renderer_webgl"));
    expect(field().textContent).not.toContain(t("settings-labs.invalid"));
  });

  it("names a saved value it doesn't recognize, leaves it, and replaces it once a choice is made", async () => {
    localStorage.setItem(KEY, "webgl2");
    await render();
    expect(chosen()).toBe(t("settings-labs.terminal_renderer_standard"));
    expect(field().textContent).toContain(t("settings-labs.invalid"));
    expect(localStorage.getItem(KEY)).toBe("webgl2");
    await click(radios()[1]);
    expect(localStorage.getItem(KEY)).toBe("1");
    expect(field().textContent).not.toContain(t("settings-labs.invalid"));
  });

  it("says when the browser won't keep the choice, and shows what is still in effect", async () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("full", "QuotaExceededError");
    });
    await render();
    await click(radios()[1]);
    expect(field().querySelector('[role="alert"]')?.textContent).toBe(t("settings-labs.save_failed"));
    expect(chosen()).toBe(t("settings-labs.terminal_renderer_standard"));
    expect(field().textContent).not.toContain(t("settings-configuration.saved"));
  });

  it("says when the browser won't let it read what is saved", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    await render();
    expect(field().textContent).toContain(t("settings-labs.unreadable"));
    expect(chosen()).toBe(t("settings-labs.terminal_renderer_standard"));
  });

  it("follows a choice made in another tab", async () => {
    await render();
    localStorage.setItem(KEY, "1");
    act(() => void window.dispatchEvent(new StorageEvent("storage", { key: KEY })));
    await settle();
    expect(chosen()).toBe(t("settings-labs.terminal_renderer_webgl"));
  });
});
