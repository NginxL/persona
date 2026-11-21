// @vitest-environment jsdom
// Settings › About Persona › Developing Persona (#9617): off by default; on, it records and offers Export Diagnostics;
// off again with something recorded, it asks first and deletes only on Turn Off and Delete.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { DEV_MODE_KEY } from "../../lib/dev-probe";

const recorded = { size: 0 };
const setDevMode = vi.fn(async (on: boolean) => {
  if (on) localStorage.setItem(DEV_MODE_KEY, "2027-01-15T08:00:00.000Z");
  else localStorage.removeItem(DEV_MODE_KEY);
});
vi.mock("../../lib/dev-mode", () => ({
  setDevMode,
  startDevMode: () => ({ flush: async () => {}, store: { size: async () => recorded.size } }),
}));

const { DevModeRows } = await import("./DevModeSettings");
await import("../../lib/dev-mode"); // loaded once here, so the rows' own import of it resolves within a test

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;

const flush = () => act(async () => {
  for (let i = 0; i < 5; i++) await new Promise((done) => setTimeout(done, 0));
});
const toggle = () => host.querySelector<HTMLButtonElement>('[role="switch"]')!;
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);

beforeEach(() => {
  localStorage.clear();
  recorded.size = 0;
  setDevMode.mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("Developing Persona", () => {
  it("is off on a new browser, with its plain label and what it does", () => {
    act(() => root.render(<DevModeRows />));
    expect(host.textContent).toContain(t("settings-devmode.title"));
    expect(host.textContent).toContain(t("settings-devmode.hint"));
    expect(toggle().getAttribute("aria-checked")).toBe("false");
    expect(host.querySelector('[data-testid="dev-mode-export"]')).toBeNull();
  });

  it("turns on, shows what is recorded and Export Diagnostics", async () => {
    act(() => root.render(<DevModeRows />));
    await act(async () => toggle().click());
    await vi.waitFor(async () => {
      await flush();
      expect(setDevMode).toHaveBeenCalledWith(true);
    });
    expect(toggle().getAttribute("aria-checked")).toBe("true");
    expect(host.textContent).toContain(t("settings-devmode.recorded"));
    expect(host.querySelector('[data-testid="dev-mode-export"]')).not.toBeNull();
  });

  it("asks before deleting what is recorded, and deletes only on Turn Off and Delete", async () => {
    localStorage.setItem(DEV_MODE_KEY, "2027-01-15T08:00:00.000Z");
    recorded.size = 2048;
    act(() => root.render(<DevModeRows />));
    // the size is read off the recorder after the rows draw: Turn Off asks only once it is known
    await vi.waitFor(async () => {
      await flush();
      expect(host.textContent).toContain(" · ");
    });
    await act(async () => toggle().click());
    await flush();
    expect(setDevMode).not.toHaveBeenCalled();
    expect(button(t("settings-devmode.export_first"))).toBeTruthy();
    await act(async () => button(t("settings-devmode.cancel"))!.click());
    expect(toggle().getAttribute("aria-checked")).toBe("true");

    await act(async () => toggle().click());
    await flush();
    await act(async () => button(t("settings-devmode.turn_off"))!.click());
    await vi.waitFor(async () => {
      await flush();
      expect(setDevMode).toHaveBeenCalledWith(false);
    });
    expect(toggle().getAttribute("aria-checked")).toBe("false");
  });

  it("fills in its words' values", () => {
    for (const [key, values] of [["settings-devmode.note", { since: "Nov 17, 10:32" }], ["settings-devmode.since_size", { since: "Nov 17, 10:32", size: "2 KB" }], ["settings-devmode.confirm_title", { size: "2 KB" }], ["settings-devmode.details", { size: "2 KB", system: "Chrome 142" }]] as const) {
      const text = t(key, values);
      for (const v of Object.values(values)) expect(text).toContain(v);
      expect(text).not.toMatch(/[{}]/);
    }
  });

  it("turns off at once when nothing is recorded", async () => {
    localStorage.setItem(DEV_MODE_KEY, "2027-01-15T08:00:00.000Z");
    act(() => root.render(<DevModeRows />));
    // Even an empty recorder must finish reporting its size before Turn Off chooses the immediate or confirm path.
    await vi.waitFor(async () => {
      await flush();
      expect(host.textContent).toContain(" · ");
    });
    await act(async () => toggle().click());
    await vi.waitFor(async () => {
      await flush();
      expect(setDevMode).toHaveBeenCalledWith(false);
    });
    expect(button(t("settings-devmode.turn_off"))).toBeUndefined();
  });
});
