// @vitest-environment jsdom
// Settings › Labs (#9877) through the generic configuration form: the terminal renderer module's choice kept in this
// browser (Standard as no key), the old WebGL switch shown as WebGL, a value from another version named and left until a
// choice replaces it, a browser that won't keep the choice saying the save is unconfirmed, and another tab's change.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { allTypes } from "../../lib/configuration/fixtures/shared";
import type { ConfigurationUpdate } from "../../lib/configuration/contract";
import { LabsDialog, LabsSettings, type LabsModule } from "./Labs";

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

describe.each(["settings", "feature dialog"])("experiment guidance from %s", (entry) => {
  it("shows notes, asks only for the trial and reports its timing after saving", async () => {
    const update = vi.fn(async (u: ConfigurationUpdate) => ({
      ...allTypes, revision: "next", values: { ...allTypes.values, ...u.changes },
    }));
    const module: LabsModule = {
      source: { key: "example", read: async () => allTypes, update },
      guidance: {
        notes: [{ kind: "info", text: "Try different layouts." }, { kind: "info", text: " " }],
        fields: { compact: {
          notes: [{ kind: "warning", text: "Cards show less detail." }],
          rules: [{
            when: { action: "change", to: true },
            confirm: { title: "Use compact cards?", body: "Cards will show a shorter preview.", scope: "This browser", impact: "Shorter previews", activation: "New views", confirmLabel: "Save compact layout" },
            afterSave: "Saved. New views will use compact cards.",
          }],
        } },
      },
    };
    const outerClose = vi.fn();
    await act(async () => root.render(entry === "settings" ? <LabsSettings modules={[module]} /> : <LabsDialog modules={[module]} open onClose={outerClose} />));
    const form = document.querySelector<HTMLElement>('[data-configuration="example-preferences"]')!;
    const row = form.querySelector<HTMLElement>('[data-configuration-field="compact"]')!;
    expect(form.textContent?.split("Try different layouts.")).toHaveLength(2);
    expect(row.textContent).toContain("Cards show less detail.");
    const toggle = row.querySelector<HTMLButtonElement>('[role="switch"]')!;
    toggle.focus();
    const dialog = () => [...document.querySelectorAll<HTMLElement>('[role="dialog"]')].find(d => d.querySelector("h2")?.textContent === "Use compact cards?")!;
    await click(toggle);
    const modal = dialog();
    expect(document.activeElement).toBe(modal);
    expect(document.getElementById(modal.getAttribute("aria-describedby")!)?.textContent).toBe("Cards will show a shorter preview.");
    expect(update).not.toHaveBeenCalled();
    act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(dialog()).toBeUndefined();
    expect(document.activeElement).toBe(toggle);
    expect(outerClose).not.toHaveBeenCalled();
    await click(toggle);
    const submit = [...dialog().querySelectorAll("button")].find(b => b.textContent === "Save compact layout")!;
    await click(submit);
    expect(update).toHaveBeenCalledTimes(1);
    expect(row.querySelector("[aria-live]")?.textContent).toBe("Saved. New views will use compact cards.");
    await click(toggle);
    expect(dialog()).toBeUndefined();
    expect(update).toHaveBeenCalledTimes(2);
    expect(row.querySelector("[aria-live]")?.textContent).toBe(t("settings-configuration.saved"));
  });
});

describe("Settings › Labs › Features (#13434)", () => {
  it("switches a feature module off and on in this browser, through the generic form", async () => {
    const { registerModule, resetModulesForTest, enabledNavItems } = await import("../../core/module-host");
    const { forgetModuleSwitches } = await import("../../core/module-switch");
    const { modulesLabsSource } = await import("../../core/module-labs");
    forgetModuleSwitches();
    resetModulesForTest();
    registerModule({ id: "garden", title: () => "Garden", description: () => "A synthetic module", navItems: [{ key: "garden", path: "/garden", icon: "heart", label: () => "Garden", after: "inbox" }], load: async () => ({}) });
    try {
      act(() => root.render(<LabsSettings modules={[{ source: modulesLabsSource()! }]} />));
      await settle();
      const row = host.querySelector<HTMLElement>('[data-configuration-field="garden"]')!;
      expect(row.textContent).toContain("Garden");
      expect(row.textContent).toContain("A synthetic module");
      const toggle = row.querySelector<HTMLButtonElement>('[role="switch"]')!;
      expect(toggle.getAttribute("aria-checked")).toBe("true");
      expect(enabledNavItems().map((n) => n.key)).toEqual(["garden"]);
      await click(toggle);
      expect(localStorage.getItem("persona.modules.off")).toBe('["garden"]');
      expect(enabledNavItems()).toEqual([]);
      await click(host.querySelector<HTMLButtonElement>('[data-configuration-field="garden"] [role="switch"]')!);
      expect(localStorage.getItem("persona.modules.off")).toBeNull();
      expect(enabledNavItems().map((n) => n.key)).toEqual(["garden"]);
    } finally {
      resetModulesForTest();
      forgetModuleSwitches();
    }
  });
});
