// @vitest-environment jsdom
// The model panel's three columns on a wide screen (#12315): launchers on the left with their names, the chosen one's
// models in the middle, the thinking levels of the model picked (else the one that runs) on the right, a launcher that
// can't take a pick said once, a footer with the speed only where that model has one, and a pick that keeps it open.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PanelModel, PanelProvider, PanelState } from "../../lib/model-panel";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  // a wide screen with a precise pointer
  g.matchMedia = (q: string) => ({
    matches: q === "(any-pointer: fine)" || q === "(min-width: 768px)",
    media: q,
    addEventListener() {},
    removeEventListener() {},
  });
  Element.prototype.scrollIntoView ??= () => {};
});

const { ModelPanel } = await import("./ModelPanel");
const { panelStoreKey } = await import("./store");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const flush = () =>
  act(async () => {
    for (let i = 0; i < 6; i++) await Promise.resolve();
  });

const LEVELS = ["low", "medium", "high", "xhigh"];
const level = (base: string, name: string, effort: string): PanelModel => ({ id: `${base}-${effort}`, name: `${name} (${effort})`, efforts: LEVELS, defaultEffort: effort });
const agy: PanelProvider = { id: "agy", name: "agy", engine: "agy", models: ["high", "xhigh", "medium", "low"].map((e) => level("gemini-4-neon", "Gemini 4 Neon", e)) };
const claude: PanelProvider = {
  id: "claude",
  name: "claude",
  engine: "claude",
  models: [
    { id: "opus", name: "Opus 5.5", efforts: ["low", "medium", "high"], defaultEffort: "high" },
    { id: "haiku", name: "Haiku 5.5", efforts: [] },
  ],
  unavailable: "Switching from Antigravity CLI to Claude Code needs a new conversation",
};
const codex: PanelProvider = { id: "codex", name: "codex", engine: "codex", models: [{ id: "gpt-5.5", name: "GPT 5.5", efforts: ["low", "medium", "high"], defaultEffort: "medium", fast: true }] };
const state: PanelState = { current: { provider: "agy", model: "gemini-4-neon-xhigh" }, recommended: ["agy"] };

let host: HTMLDivElement;
let root: Root;
let anchor: HTMLButtonElement;

beforeEach(() => {
  localStorage.clear();
  anchor = document.createElement("button");
  document.body.append(anchor);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  anchor.remove();
});

async function open(o: { state?: PanelState; onPick?: (c: unknown) => Promise<boolean>; onApplyCurrent?: (p: { effort?: string; fast?: boolean }) => Promise<boolean> } = {}) {
  const onPick = vi.fn(o.onPick ?? (async () => true));
  const onApplyCurrent = o.onApplyCurrent && vi.fn(o.onApplyCurrent);
  const onClose = vi.fn();
  act(() =>
    root.render(<ModelPanel anchor={anchor} onClose={onClose} providers={[agy, claude, codex]} state={o.state ?? state} scope="assistant" onPick={onPick} onApplyCurrent={onApplyCurrent} />),
  );
  await flush();
  return { onPick, onApplyCurrent, onClose };
}

const stored = (kind: "recent" | "overrides") => JSON.parse(localStorage.getItem(panelStoreKey("assistant", kind)) ?? "null");

const sides = () => [...document.body.querySelectorAll<HTMLButtonElement>("[data-panel-side]")];
const side = (id: string) => document.body.querySelector<HTMLButtonElement>(`[data-panel-side="${id}"]`)!;
const rows = () => [...document.body.querySelectorAll<HTMLElement>("[data-panel-row]")];
const levels = () => [...document.body.querySelectorAll<HTMLButtonElement>("[data-panel-level]")];
const pressed = () => levels().find((b) => b.getAttribute("aria-pressed") === "true")?.dataset.panelLevel;
const hover = (el: Element) => act(() => el.dispatchEvent(new PointerEvent("pointermove", { bubbles: true })));

describe("the model panel's columns", () => {
  it("lists every launcher and opens on the one that runs, its models in the middle", async () => {
    await open();
    expect(sides().map((b) => b.textContent)).toEqual(["agy", "claude", "codex"]);
    expect(side("agy").getAttribute("aria-pressed")).toBe("true");
    expect(rows().map((r) => r.dataset.panelRow)).toEqual(["model:agy\ngemini-4-neon"]);
    // no launcher summary rows and no line per launcher that can't take a pick
    expect(document.body.querySelector("[data-panel-launcher]")).toBeNull();
    expect(document.body.textContent).not.toContain("needs a new conversation");
    expect(side("claude").getAttribute("aria-description")).toContain("needs a new conversation");
  });

  it("shows the running model's levels and picks another level's own model", async () => {
    const { onPick } = await open({ onApplyCurrent: async () => true });
    expect(levels().map((b) => b.textContent)).toEqual(["Low", "Medium", "High", "XHigh"]);
    expect(pressed()).toBe("xhigh");
    act(() => levels().find((b) => b.dataset.panelLevel === "low")!.click());
    await flush();
    expect(onPick).toHaveBeenCalledWith({ provider: "agy", model: "gemini-4-neon-low" });
  });

  it("says why a launcher can't take a pick once, above its models, when it is chosen", async () => {
    await open();
    act(() => side("claude").click());
    expect(document.body.querySelectorAll("[data-panel-side-note]")).toHaveLength(1);
    expect(document.body.querySelector("[data-panel-side-note]")!.textContent).toContain("needs a new conversation");
    expect(rows().every((r) => r.getAttribute("aria-disabled") === "true")).toBe(true);
    expect(levels().every((b) => b.disabled)).toBe(true);
  });

  it("a model picked stays open: its levels, its default marked, its speed in the footer, and a level picks it again", async () => {
    const { onPick, onClose } = await open();
    act(() => side("codex").click());
    act(() => rows()[0].click());
    await flush();
    expect(onPick).toHaveBeenLastCalledWith({ provider: "codex", model: "gpt-5.5" });
    expect(onClose).not.toHaveBeenCalled();
    expect(levels().map((b) => b.dataset.panelLevel)).toEqual(["low", "medium", "high"]);
    expect(levels().find((b) => b.dataset.panelLevel === "medium")!.textContent).toContain("推荐");
    // Codex's speed is in the footer while its model is the one picked, wherever the pointer goes
    expect(document.body.querySelector('[data-panel-speed="fast"]')).not.toBeNull();
    hover(document.body.querySelector("[data-panel-launchers]")!);
    expect(document.body.querySelector('[data-panel-speed="fast"]')).not.toBeNull();
    act(() => levels().find((b) => b.dataset.panelLevel === "high")!.click());
    await flush();
    expect(onPick).toHaveBeenLastCalledWith({ provider: "codex", model: "gpt-5.5", effort: "high" });
    expect(onClose).not.toHaveBeenCalled();
  });

  it("rows say no effort of their own; the model that runs has a check", async () => {
    await open();
    expect(document.body.querySelector("[data-row-setup]")).toBeNull();
    expect(rows()[0].querySelector("svg")).not.toBeNull();
  });

  it("has no speed switch for a model without one", async () => {
    await open();
    expect(document.body.querySelector("[data-panel-speed]")).toBeNull();
  });

  it("persists a folded running model's chosen level and lets clicking the running level cancel a pending switch", async () => {
    const { onPick } = await open({
      state: { ...state, pending: { provider: "agy", model: "gemini-4-neon-low" } },
      onApplyCurrent: async () => true,
    });
    // clicking the running level while a switch is pending sends the running model ID to drop the switch
    act(() => levels().find((b) => b.dataset.panelLevel === "xhigh")!.click());
    await flush();
    expect(onPick).toHaveBeenCalledWith({ provider: "agy", model: "gemini-4-neon-xhigh" });
    expect(stored("overrides")).toEqual({ "agy\ngemini-4-neon": { effort: "xhigh" } });
  });

  it("does not store an override or Recent entry when picking a non-current level fails", async () => {
    await open({ onPick: async () => false });
    act(() => side("codex").click());
    act(() => rows()[0].click());
    await flush();
    act(() => levels().find((b) => b.dataset.panelLevel === "high")!.click());
    await flush();
    act(() => root.unmount());
    expect(stored("overrides")).toBeNull();
    expect(stored("recent")).toBeNull();
  });

  it("records Recent for live effort and Fast edits on the running model only when onApplyCurrent succeeds", async () => {
    const codexState: PanelState = { current: { provider: "codex", model: "gpt-5.5", effort: "medium" }, recommended: ["codex"] };
    // rejected live effort edit writes neither Recent nor overrides
    await open({ state: codexState, onApplyCurrent: async () => false });
    act(() => levels().find((b) => b.dataset.panelLevel === "high")!.click());
    await flush();
    act(() => root.unmount());
    expect(stored("overrides")).toBeNull();
    expect(stored("recent")).toBeNull();

    // successful Fast-only live edit records Recent on close
    root = createRoot(host);
    const { onApplyCurrent } = await open({ state: codexState, onApplyCurrent: async () => true });
    act(() => document.body.querySelector<HTMLButtonElement>('[data-panel-speed="fast"]')!.click());
    await flush();
    expect(onApplyCurrent).toHaveBeenCalledWith({ fast: true });
    act(() => root.unmount());
    expect(stored("recent")).toEqual([{ provider: "codex", model: "gpt-5.5", effort: "medium", fast: true }]);
  });
});
