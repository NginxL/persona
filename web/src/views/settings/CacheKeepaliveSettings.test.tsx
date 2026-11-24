// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { describe, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { CacheKeepaliveSettings, type CacheKeepaliveState } from "./CacheKeepaliveSettings";
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("cache keepalive settings", () => {
  it("shows the default policy, saves one-click off, and explains unsupported runtimes", async () => {
    await initI18n("en");
    const host = document.createElement("div"); document.body.append(host); const root = createRoot(host);
    const save = vi.fn(async (_patch: CacheKeepaliveState) => {});
    try {
      await act(async () => root.render(<CacheKeepaliveSettings value={{ cache_keepalive: true, cache_keepalive_minutes: 55, cache_keepalive_max: 24 }} save={save} />));
      expect(host.textContent).toContain("Codex and other runtimes are not supported yet");
      expect(host.textContent).toContain("Background supervision, workers and subagents are excluded");
      const toggle = host.querySelector<HTMLButtonElement>("[role=switch]")!;
      expect(toggle.getAttribute("aria-checked")).toBe("true");
      const inputs = host.querySelectorAll<HTMLInputElement>("input");
      expect([...inputs].map((i) => i.value)).toEqual(["55", "24"]);
      expect([...inputs].map((i) => i.max)).toEqual(["55", "144"]);
      await act(async () => toggle.click()); expect(save).toHaveBeenCalledWith({ cache_keepalive: false });
      await act(async () => root.render(<CacheKeepaliveSettings value={{ cache_keepalive: false }} save={save} />));
      expect([...host.querySelectorAll<HTMLInputElement>("input")].every((i) => i.disabled)).toBe(true);
    } finally { act(() => root.unmount()); host.remove(); }
  });
  it("hides the setting when the manager has no policy", async () => {
    await initI18n("en");const host = document.createElement("div"); const root = createRoot(host);
    try { await act(async () => root.render(<CacheKeepaliveSettings value={{}} save={async () => {}} />)); expect(host.innerHTML).toBe(""); }
    finally { act(() => root.unmount()); }
  });
});
