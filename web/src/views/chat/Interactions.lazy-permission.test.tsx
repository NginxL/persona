// @vitest-environment jsdom
// A permission card's own chunk can be slow or unavailable after a deploy: its slot keeps saying what is waiting, and
// a local failure offers a retry without taking down the surrounding conversation.
import { act, type ComponentType } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const reported = vi.hoisted(() => vi.fn());
vi.mock("../../lib/error-reports", () => ({ reportBoundary: reported }));
import { PermissionCardFrame } from "./Interactions";
import { lazyPage, type LazyPageComponent } from "../../lib/lazy-page";
import { cardToolName } from "../../lib/turns";
import { t } from "../../i18n";
import type { Interaction } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ix = { id: "p_lazy", machine_id: "m", session_id: "s", kind: "permission", tool_name: "Bash", tool_input: { command: "npm test" } } as Interaction;
type CardComponent = ComponentType<{ ix: Interaction }>;
let host: HTMLDivElement;
let root: Root;

async function render(Lazy: LazyPageComponent<{ ix: Interaction }>, interaction = ix) {
  await act(async () => {
    root.render(
      <PermissionCardFrame ix={interaction}><Lazy ix={interaction} /></PermissionCardFrame>,
    );
    await Promise.resolve();
  });
}

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reported.mockClear();
  vi.unstubAllGlobals();
});

const titleCases = [
  { ...ix, id: "p_settings", tool_name: "persona_settings", tool_title: "Persona settings" },
  { ...ix, id: "p_mail", tool_name: "mcp__persona__mail_send", tool_title: undefined },
] as Interaction[];

describe("a lazy permission card", () => {
  it.each(titleCases)("uses the display name while loading: $tool_name", async (toolIx) => {
    const Lazy = lazyPage(() => new Promise<CardComponent>(() => {}));
    await render(Lazy, toolIx);
    await vi.waitFor(() => expect(host.querySelector('[role="status"]')).not.toBeNull());
    expect(host.querySelector("[data-pending-card]")?.getAttribute("aria-label"))
      .toBe(t("chat-interactions.permission_title", { tool: cardToolName(toolIx) }));
  });

  it.each(titleCases)("uses the display name after a load failure: $tool_name", async (toolIx) => {
    const failure = new TypeError("Failed to fetch dynamically imported module");
    const Lazy = lazyPage(() => Promise.reject<CardComponent>(failure));
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      await render(Lazy, toolIx);
      await vi.waitFor(() => expect(host.querySelector('[role="alert"]')).not.toBeNull());
      expect(host.querySelector("[data-pending-card]")?.getAttribute("aria-label"))
        .toBe(t("chat-interactions.permission_title", { tool: cardToolName(toolIx) }));
    } finally {
      consoleError.mockRestore();
    }
  });
  it("keeps an identifiable loading slot until its controls arrive", async () => {
    let arrive!: (card: CardComponent) => void;
    const Lazy = lazyPage(() => new Promise<CardComponent>((resolve) => { arrive = resolve; }));
    await render(Lazy);
    await vi.waitFor(() => expect(host.querySelector('[role="status"]')?.textContent).toBe(t("common.loading")));
    expect(host.querySelector("[data-pending-card]")?.getAttribute("aria-label")).toBe(t("chat-interactions.permission_title", { tool: "Bash" }));
    const Loaded = () => <button type="button">Loaded approval</button>;
    await act(async () => arrive(Loaded));
    await vi.waitFor(() => expect(host.textContent).toContain("Loaded approval"));
    expect(host.querySelector('[role="status"]')).toBeNull();
  });

  it("contains a failed import and retries it locally", async () => {
    const failure = new TypeError("Failed to fetch dynamically imported module");
    const Loaded = () => <button type="button">Recovered approval</button>;
    const load = vi.fn<() => Promise<CardComponent>>().mockRejectedValueOnce(failure).mockResolvedValue(Loaded);
    const Lazy = lazyPage(load);
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      await render(Lazy);
      await vi.waitFor(() => expect(host.querySelector('[role="alert"]')?.textContent).toContain(t("chat-interactions.permission_load_failed")), { timeout: 20_000 });
      expect(host.textContent).toContain(t("chat-interactions.permission_load_hint"));
      expect(host.textContent).not.toContain("Recovered approval");
      expect(reported).toHaveBeenCalledWith(failure, "permission_card", true, expect.anything());
      const retry = [...host.querySelectorAll("button")].find((b) => b.textContent === t("app.retry"))!;
      act(() => retry.click());
      await vi.waitFor(() => expect(host.textContent).toContain("Recovered approval"));
      expect(load).toHaveBeenCalledTimes(2);
      expect(host.querySelector('[role="alert"]')).toBeNull();
    } finally {
      consoleError.mockRestore();
    }
  });

  it("reloads when the browser returns the same failed chunk after Retry", async () => {
    const failure = new TypeError("Failed to fetch dynamically imported module");
    const load = vi.fn<() => Promise<CardComponent>>().mockRejectedValue(failure);
    const Lazy = lazyPage(load);
    const reload = vi.fn();
    vi.stubGlobal("location", { reload });
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      await render(Lazy);
      await vi.waitFor(() => expect(host.querySelector('[role="alert"]')).not.toBeNull());
      const retry = [...host.querySelectorAll("button")].find((b) => b.textContent === t("app.retry"))!;
      act(() => retry.click());
      await vi.waitFor(() => expect(reload).toHaveBeenCalledTimes(1));
      expect(load).toHaveBeenCalledTimes(2);
      expect(host.querySelector('[role="alert"]')).not.toBeNull();
    } finally {
      consoleError.mockRestore();
    }
  });

  it("does not loop full reloads when the same card keeps failing", async () => {
    const loopIx = { ...ix, id: "p_reload_loop" } as Interaction;
    const failure = new TypeError("Failed to fetch dynamically imported module");
    const load = vi.fn<() => Promise<CardComponent>>().mockRejectedValue(failure);
    const reload = vi.fn();
    vi.stubGlobal("location", { reload });
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    let attempt = 0;
    const showFailure = async () => {
      const Lazy = lazyPage(load);
      attempt += 1;
      await act(async () => {
        root.render(<PermissionCardFrame key={attempt} ix={loopIx}><Lazy ix={loopIx} /></PermissionCardFrame>);
        await Promise.resolve();
      });
      await vi.waitFor(() => expect(host.querySelector('[role="alert"]')).not.toBeNull());
    };
    try {
      await showFailure();
      const firstRetry = [...host.querySelectorAll("button")].find((b) => b.textContent === t("app.retry"))!;
      act(() => firstRetry.click());
      await vi.waitFor(() => expect(reload).toHaveBeenCalledTimes(1));

      // A real reload creates a fresh boundary but preserves sessionStorage.
      await showFailure();
      const secondRetry = [...host.querySelectorAll("button")].find((b) => b.textContent === t("app.retry"))!;
      act(() => secondRetry.click());
      await vi.waitFor(() => expect(reported).toHaveBeenCalledTimes(4));
      expect(load).toHaveBeenCalledTimes(4);
      expect(reload).toHaveBeenCalledTimes(1);
      expect(host.querySelector('[role="alert"]')).not.toBeNull();
    } finally {
      consoleError.mockRestore();
    }
  });
});
