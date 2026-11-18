// @vitest-environment jsdom
// The unified model panel (#5032) as a person uses it: the list's groups and the row that runs now, picks that wait for
// the chat (Recent records only applied ones), rows that can't be picked and why, the setup flyout (applied at once on
// the model that runs now, kept as an override on another), favorites, the keys (Tab / arrows / Enter / ← / Esc in
// order), typed IDs, a list being read or that failed, and "Configure models".
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PanelProvider, PanelState } from "../../lib/model-panel";
import type { LauncherQuotaAnswer } from "../../lib/quota";
import { effortTitle } from "../../lib/session-actions";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  // a precise pointer: the search takes the focus when the panel opens
  g.matchMedia = (q: string) => ({ matches: q === "(any-pointer: fine)", media: q, addEventListener() {}, removeEventListener() {} });
  Element.prototype.scrollIntoView ??= () => {};
});

const navigated = vi.hoisted(() => [] as string[]);
vi.mock("../../lib/router", async (orig) => ({ ...(await orig<typeof import("../../lib/router")>()), navigate: (to: string) => navigated.push(to) }));

const { ModelPanel } = await import("./ModelPanel");
const { panelStoreKey } = await import("./store");
const { railCaptions, railEdges, railFade } = await import("./Rail");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const flush = () => act(async () => { for (let i = 0; i < 6; i++) await Promise.resolve(); });
const EFFORTS = ["low", "medium", "high", "xhigh", "max"];

const claude = (over: Partial<PanelProvider> = {}): PanelProvider => ({
  id: "claude",
  name: "Claude",
  engine: "claude",
  typable: true,
  models: [
    { id: "opus", name: "Opus 5.5", resolved: "claude-opus-5-5", description: "Most capable", efforts: EFFORTS, defaultEffort: "high", context: 1_000_000 },
    { id: "sonnet", name: "Sonnet 5", description: "Everyday", efforts: EFFORTS, defaultEffort: "medium" },
    { id: "vendor/model-x", name: "vendor/model-x", efforts: [], custom: true },
  ],
  ...over,
});
const codex = (over: Partial<PanelProvider> = {}): PanelProvider => ({ id: "codex", name: "Codex", engine: "codex", models: [{ id: "gpt-5.5", name: "GPT 5.5", efforts: ["low", "medium", "high"], fast: true }], unavailable: "需要新开对话", ...over });
const baseState: PanelState = { current: { provider: "claude", model: "claude-opus-5-5", effort: "xhigh" }, recommended: ["claude"] };

let host: HTMLDivElement;
let root: Root;
let anchor: HTMLButtonElement;

beforeEach(() => {
  localStorage.clear();
  navigated.length = 0;
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

interface Opts {
  providers?: PanelProvider[];
  state?: PanelState;
  onPick?: (c: unknown) => Promise<boolean>;
  onApplyCurrent?: (p: { effort?: string; fast?: boolean }) => Promise<boolean>;
  onReload?: (p: string) => void;
  onForget?: (p: string, m: string) => void;
  quotas?: Readonly<Record<string, LauncherQuotaAnswer>>;
  header?: React.ReactNode;
  held?: boolean;
}

async function open(o: Opts = {}) {
  const onClose = vi.fn();
  const onPick = vi.fn(o.onPick ?? (async () => true));
  const onApplyCurrent = o.onApplyCurrent && vi.fn(o.onApplyCurrent);
  const render = (extra: Partial<Opts> = {}) =>
    root.render(
      <ModelPanel
        anchor={anchor}
        onClose={onClose}
        providers={extra.providers ?? o.providers ?? [claude(), codex()]}
        state={extra.state ?? o.state ?? baseState}
        scope="agent"
        onPick={onPick}
        onApplyCurrent={onApplyCurrent}
        onReload={o.onReload}
        onForget={o.onForget}
        quotas={o.quotas}
        header={o.header}
        held={extra.held ?? o.held}
      />,
    );
  act(() => render());
  await flush();
  return { onClose, onPick, onApplyCurrent, render };
}

const rows = () => [...document.body.querySelectorAll<HTMLElement>("[data-panel-row]")];
const row = (text: string) => rows().find((r) => r.textContent?.includes(text))!;
const groups = () => [...document.body.querySelectorAll<HTMLElement>("[data-panel-section]")].map((g) => `${g.getAttribute("aria-label")}: ${[...g.querySelectorAll("[data-panel-row]")].map((r) => r.querySelector(".truncate")?.textContent).join(", ")}`);
const button = (label: string) => [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.getAttribute("aria-label") === label || b.textContent?.trim() === label)!;
const key = (el: Element, k: string, init: KeyboardEventInit = {}) => act(() => { el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, ...init })); });
const flyout = () => document.body.querySelector<HTMLElement>("[data-model-flyout]");
const search = () => document.body.querySelector<HTMLInputElement>("[data-model-panel] input")!;
function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  act(() => {
    setter.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const stored = (kind: "favorites" | "recent" | "overrides") => JSON.parse(localStorage.getItem(panelStoreKey("agent", kind)) ?? "null");

describe("the model panel", () => {
  it("opens on All: Recommended (the model that runs now first, selected), then each provider; the search has the focus", async () => {
    await open();
    expect(groups()).toEqual(["推荐: Opus 5.5, Sonnet 5, vendor/model-x", "Codex: GPT 5.5"]);
    expect(row("Opus").getAttribute("aria-selected")).toBe("true");
    expect(row("Sonnet").getAttribute("aria-selected")).toBe("false");
    expect(document.activeElement).toBe(search());
    expect(document.body.querySelector('[role="listbox"]')).not.toBeNull();
    // the rail: Recent, Favorites, All (on), one cell per provider
    expect([...document.body.querySelectorAll("[data-rail-cell]")].map((c) => `${c.getAttribute("data-rail-cell")}${c.getAttribute("aria-pressed") === "true" ? "*" : ""}`)).toEqual(["recent", "favorites", "all*", "claude", "codex"]);
    // every cell says what it is under its mark, so a phone (no hover) tells providers apart before opening one
    expect([...document.body.querySelectorAll("[data-rail-caption]")].map((c) => c.textContent)).toEqual(["最近", "收藏", "全部", "Claude", "Codex"]);
    expect(document.body.querySelector('[data-rail-cell="claude"]')?.getAttribute("aria-label")).toBe("Claude");
  });

  it("captions providers by the words that tell them apart", () => {
    expect(railCaptions(["claude-aster", "claude-max", "claude-desktop", "codex"])).toEqual(["aster", "max", "desktop", "codex"]);
    // a first word nobody else has stays; so does a name that is only that word
    expect(railCaptions(["claude", "claude-max", "codex"])).toEqual(["claude", "max", "codex"]);
    expect(railCaptions(["Claude Max", "Claude Pro", "本机网关 aster", "本机网关 beta"])).toEqual(["Max", "Pro", "aster", "beta"]);
    // a trailing note in brackets goes while the names stay apart without it
    expect(railCaptions(["hone-claw（Claude）", "Zhipu (GLM)"])).toEqual(["hone-claw", "Zhipu"]);
    expect(railCaptions(["gw (Claude)", "gw (Codex)"])).toEqual(["gw (Claude)", "gw (Codex)"]);
    // shortening that would make two captions the same keeps those names whole
    expect(railCaptions(["claude-max", "claude-pro", "codex-max", "codex-pro"])).toEqual(["claude-max", "claude-pro", "codex-max", "codex-pro"]);
    expect(railCaptions(["claude-max", "max", "claude-pro"])).toEqual(["claude-max", "max", "pro"]);
    expect(railCaptions([])).toEqual([]);
  });

  it("distinguishes identical model names by their launcher in All", async () => {
    const shared = [{ id: "fable", name: "Fable", efforts: [] }];
    await open({ providers: [claude({ models: shared }), codex({ models: shared, unavailable: undefined })], state: { current: null, recommended: [], starting: true } });
    const duplicates = rows().filter((r) => r.textContent?.includes("Fable"));
    expect(duplicates).toHaveLength(2);
    expect(duplicates[0].textContent).toContain("Claude");
    expect(duplicates[1].textContent).toContain("Codex");
  });

  it("draws a provider's weekly quota bar and reset tooltip, and no bar when it is unknown", async () => {
    await open({ quotas: { claude: { quota: { gateway: "g", budget: [{ label: "每周预算", used_ratio: 0.62 }], pools: [{ name: "b", windows: [{ label: "每周", used_ratio: 0.77, resets_at: "2099-01-01T00:00:00Z" }] }] } } } });
    const cell = document.body.querySelector<HTMLButtonElement>('[data-rail-cell="claude"]')!;
    expect(cell.querySelector("[data-weekly-remaining]")?.getAttribute("data-weekly-remaining")).toBe("23");
    expect(cell.querySelector("[data-weekly-remaining]")?.className).toContain("bg-orange");
    expect(cell.title).toContain("每周");
    expect(cell.title).toContain("重置");
    expect(document.body.querySelector('[data-rail-cell="codex"] [data-weekly-remaining]')).toBeNull();
  });

  it("uses the accent tone for a healthy weekly quota", async () => {
    await open({ quotas: { claude: { quota: { gateway: "g", budget: [{ label: "weekly", used_ratio: 0.5 }] } } } });
    expect(document.body.querySelector('[data-rail-cell="claude"] [data-weekly-remaining]')?.className).toContain("bg-accent");
  });

  it("shows a known model's price tier and per-million flyout details, with nothing when price is unknown", async () => {
    const priced = claude({ models: [
      { id: "opus", name: "Opus 5.5", efforts: EFFORTS, price: { input: 4, output: 20, cache_read: 0.2 } },
      { id: "unknown", name: "Unknown", efforts: [] },
    ] });
    await open({ providers: [priced], state: { current: null, recommended: ["claude"] } });
    expect(row("Opus").querySelector("[data-price-tier]")?.getAttribute("data-price-tier")).toBe("3");
    expect(row("Unknown").querySelector("[data-price-tier]")).toBeNull();
    act(() => row("Opus").querySelector<HTMLButtonElement>("[data-row-customize]")!.click());
    await flush();
    expect(flyout()!.textContent).toContain("每 100 万 token");
    expect(flyout()!.textContent).toContain("输入 $4");
    expect(flyout()!.textContent).toContain("输出 $20");
    expect(flyout()!.textContent).toContain("缓存读取 $0.2");
  });

  it("a pick waits for the chat: applied, it goes into Recent and the panel closes", async () => {
    const { onPick, onClose } = await open();
    key(row("Sonnet"), "Enter");
    await flush();
    expect(onPick).toHaveBeenCalledWith({ provider: "claude", model: "sonnet", effort: "xhigh" });
    expect(onClose).toHaveBeenCalled();
    expect(stored("recent")).toEqual([{ provider: "claude", model: "sonnet", effort: "xhigh" }]);
  });

  it("a refused pick keeps the panel open and records nothing", async () => {
    const { onPick, onClose } = await open({ onPick: async () => false });
    act(() => row("Sonnet").click());
    await flush();
    expect(onPick).toHaveBeenCalledTimes(1);
    expect(onClose).not.toHaveBeenCalled();
    expect(stored("recent")).toBeNull();
  });

  it("rows that can't be picked are off and say why; the reason shows once under the provider's header", async () => {
    const { onPick } = await open();
    const gpt = row("GPT");
    expect(gpt.getAttribute("aria-disabled")).toBe("true");
    expect(gpt.title).toBe("需要新开对话");
    expect(document.body.querySelector('[data-panel-section="provider"]')?.textContent).toContain("需要新开对话");
    act(() => gpt.click());
    key(gpt, "Enter");
    await flush();
    expect(onPick).not.toHaveBeenCalled();
  });

  it("while the chat is busy every row is off and the reason is the note on top", async () => {
    const { onPick } = await open({ state: { ...baseState, busy: "Agent 正在工作" } });
    expect(document.body.querySelector('[role="note"]')?.textContent).toContain("Agent 正在工作");
    expect(rows().every((r) => r.getAttribute("aria-disabled") === "true")).toBe(true);
    key(row("Sonnet"), "Enter");
    await flush();
    expect(onPick).not.toHaveBeenCalled();
  });

  it("while busy the running model's row stays reachable for its flyout, but Enter picks nothing", async () => {
    const { onPick } = await open({ state: { ...baseState, busy: "Agent 正在工作" }, onApplyCurrent: async () => true });
    const opus = row("Opus");
    expect(opus.getAttribute("aria-disabled")).toBeNull();
    expect(rows().filter((r) => r !== opus).every((r) => r.getAttribute("aria-disabled") === "true")).toBe(true);
    key(opus, "Enter");
    await flush();
    expect(onPick).not.toHaveBeenCalled();
  });

  it("the flyout of the model that runs now applies an effort at once and stays open", async () => {
    const { onApplyCurrent } = await open({ onApplyCurrent: async () => true });
    key(row("Opus"), "ArrowLeft");
    await flush();
    const f = flyout()!;
    expect(f.getAttribute("aria-label")).toContain("Opus 5.5");
    expect(f.textContent).toContain("Claude · 1M 上下文");
    expect(f.textContent).toContain("推荐配置");
    const slider = f.querySelector<HTMLElement>('[role="slider"]')!;
    expect(document.activeElement).toBe(slider);
    expect(slider.getAttribute("aria-valuenow")).toBe("3");
    key(slider, "ArrowRight");
    await flush();
    expect(onApplyCurrent).toHaveBeenCalledWith({ effort: "max" });
    expect(flyout()).not.toBeNull();
    expect(stored("overrides")).toEqual({ "claude\nopus": { effort: "max" } });
  });

  it("the flyout of another model keeps its setup as an override, shown as customized until reset", async () => {
    const { onApplyCurrent } = await open({ onApplyCurrent: async () => true });
    act(() => row("Sonnet").querySelector<HTMLButtonElement>("[data-row-customize]")!.click());
    const slider = flyout()!.querySelector<HTMLElement>('[role="slider"]')!;
    key(slider, "Home");
    await flush();
    expect(onApplyCurrent).not.toHaveBeenCalled();
    expect(stored("overrides")).toEqual({ "claude\nsonnet": { effort: "low" } });
    expect(flyout()!.textContent).toContain("已自定义");
    act(() => button("恢复推荐").click());
    expect(stored("overrides")).toEqual({});
    expect(flyout()!.textContent).toContain("推荐配置");
  });

  it("Codex's current model takes effort and Fast in its flyout though its rows can't be picked", async () => {
    const state: PanelState = { current: { provider: "codex", model: "gpt-5.5", effort: "low" }, recommended: [] };
    const { onApplyCurrent, onPick } = await open({ providers: [codex({ unavailable: "Codex 不能换模型" })], state, onApplyCurrent: async () => true });
    const gpt = row("GPT");
    // reachable for its flyout, so not announced as disabled; its pick is still off
    expect(gpt.getAttribute("aria-disabled")).toBeNull();
    key(gpt, "Enter");
    await flush();
    expect(onPick).not.toHaveBeenCalled();
    expect(flyout()).not.toBeNull();
    const fast = flyout()!.querySelector<HTMLButtonElement>("[data-fast-toggle]")!;
    expect(fast.getAttribute("aria-pressed")).toBe("false");
    act(() => fast.click());
    await flush();
    expect(onApplyCurrent).toHaveBeenCalledWith({ fast: true });
  });

  it("the current effort-only row is not greyed out and a tap opens its working slider", async () => {
    const state: PanelState = { current: { provider: "codex", model: "gpt-5.5", effort: "high" }, recommended: [] };
    const { onApplyCurrent, onPick } = await open({ providers: [codex()], state, onApplyCurrent: async () => true });
    const gpt = row("GPT");
    expect(gpt.className).not.toContain("opacity-50");
    act(() => gpt.click());
    const slider = flyout()!.querySelector<HTMLElement>('[role="slider"]')!;
    key(slider, "Home");
    await flush();
    expect(onApplyCurrent).toHaveBeenCalledWith({ effort: "low" });
    expect(onPick).not.toHaveBeenCalled();
  });

  it("keeps actual effort and a visible reason when the session cannot change it", async () => {
    const reason = "This session has no shared control connection.";
    await open({
      providers: [codex({ models: [{ id: "gpt-5.5", name: "GPT 5.5", efforts: [], effortUnavailable: reason }] })],
      state: { current: { provider: "codex", model: "gpt-5.5", effort: "max" }, recommended: [] },
      onApplyCurrent: async () => true,
    });
    const gpt = row("GPT");
    expect(gpt.querySelector("[data-row-setup]")!.textContent).toContain(effortTitle("max"));
    expect(gpt.textContent).toContain(reason);
    act(() => gpt.click());
    expect(flyout()!.textContent).toContain(reason);
    expect(flyout()!.querySelector('[role="slider"]')).toBeNull();
  });

  it("held by a dialog about the pick (#5054): stays open under its backdrop, leaving it the clicks, focus and Escape", async () => {
    const { onClose, render } = await open({ held: true });
    const shell = document.body.querySelector<HTMLElement>("[data-model-panel]")!;
    expect(shell.className).toContain("z-30");
    expect(shell.getAttribute("data-held")).toBe("true");
    const outside = document.createElement("button");
    document.body.append(outside);
    act(() => outside.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true })));
    act(() => outside.focus());
    key(outside, "Escape");
    expect(onClose).not.toHaveBeenCalled();
    // answered: back above the page, and outside closes it again
    act(() => render({ held: false }));
    expect(shell.className).toContain("z-50");
    act(() => outside.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true })));
    expect(onClose).toHaveBeenCalledTimes(1);
    outside.remove();
  });

  it("while a switch waits, the running model's row is picked back even where its provider's rows are off", async () => {
    const fixed = codex({ unavailable: "Codex 运行中不能换模型" });
    const { onPick } = await open({ providers: [claude(), fixed], state: { current: { provider: "codex", model: "gpt-5.5" }, pending: { provider: "claude", model: "opus" }, recommended: [] } });
    const gpt = row("GPT");
    expect(gpt.getAttribute("aria-disabled")).toBeNull();
    key(gpt, "Enter");
    await flush();
    expect(onPick).toHaveBeenCalledWith(expect.objectContaining({ provider: "codex", model: "gpt-5.5" }));
  });

  it("Esc closes the flyout first (the focus back on its row), then the panel", async () => {
    const { onClose } = await open();
    const sonnet = row("Sonnet");
    sonnet.focus();
    key(sonnet, "ArrowLeft");
    expect(flyout()).not.toBeNull();
    key(document.activeElement ?? document.body, "Escape");
    expect(flyout()).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(row("Sonnet"));
    key(document.activeElement!, "Escape");
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("the star saves a copy of the row's setup once; Favorites lists it, and its star removes it", async () => {
    await open();
    const star = () => document.body.querySelector('[data-panel-section="recommended"]')!.querySelectorAll<HTMLButtonElement>('button[aria-label="存为收藏"]')[1];
    act(() => star().click());
    // the row's star is one way: a second press saves nothing new
    act(() => star().click());
    const saved = stored("favorites");
    expect(saved).toHaveLength(1);
    expect(saved[0]).toMatchObject({ provider: "claude", model: "sonnet", effort: "xhigh" });
    expect(groups()[0]).toBe("收藏: Sonnet 5");
    act(() => button("收藏").click());
    expect(groups()).toEqual(["收藏: Sonnet 5"]);
    act(() => rows()[0].querySelector<HTMLButtonElement>('button[aria-label="取消收藏"]')!.click());
    expect(stored("favorites")).toEqual([]);
    expect(document.body.textContent).toContain("还没有收藏");
  });

  it("Recent lists the setups applied, most recent first", async () => {
    localStorage.setItem(panelStoreKey("agent", "recent"), JSON.stringify([{ provider: "claude", model: "sonnet", effort: "low" }, { provider: "claude", model: "opus", effort: "xhigh" }]));
    await open();
    act(() => button("最近").click());
    expect(groups()).toEqual(["最近: Sonnet 5, Opus 5.5"]);
    expect(row("Opus").getAttribute("aria-selected")).toBe("true");
    // a recent row has no flyout
    expect(row("Sonnet").querySelector("[data-row-customize]")).toBeNull();
  });

  it("keys: Tab or ↓ from the search goes to the rows, ↑↓ move, ↑ on the first goes back", async () => {
    await open();
    key(search(), "ArrowDown");
    expect(document.activeElement).toBe(row("Opus"));
    key(row("Opus"), "ArrowDown");
    expect(document.activeElement).toBe(row("Sonnet"));
    key(row("Sonnet"), "ArrowUp");
    key(row("Opus"), "ArrowUp");
    expect(document.activeElement).toBe(search());
    key(search(), "Tab");
    expect(document.activeElement).toBe(row("Opus"));
  });

  it("a search lists every provider's matches and offers a typed ID; picking it sends the ID", async () => {
    const { onPick } = await open();
    act(() => button("Codex").click());
    type(search(), "vendor/new-1");
    expect(groups()).toEqual(["Claude: 使用「vendor/new-1」"]);
    expect(row("使用「vendor/new-1」").textContent).toContain("Claude · 列表里没有，按输入的 ID 使用");
    expect(document.body.querySelector('[data-rail-cell="all"]')?.getAttribute("aria-pressed")).toBe("true");
    key(search(), "Enter");
    await flush();
    expect(onPick).toHaveBeenCalledWith({ provider: "claude", model: "vendor/new-1" });
    type(search(), "");
    expect(document.body.querySelector('[data-rail-cell="codex"]')?.getAttribute("aria-pressed")).toBe("true");
  });

  it("an ID typed before is in monospace with a second line and can be forgotten", async () => {
    const onForget = vi.fn();
    await open({ onForget });
    const typed = row("vendor/model-x");
    expect(typed.textContent).toContain("Claude · 输入过的");
    expect(typed.querySelector(".font-mono")).not.toBeNull();
    act(() => typed.querySelector<HTMLButtonElement>('button[aria-label="忘记「vendor/model-x」"]')!.click());
    expect(onForget).toHaveBeenCalledWith("claude", "vendor/model-x");
  });

  it("a provider still being read shows a spinner; one that failed says why and offers Retry", async () => {
    const onReload = vi.fn();
    await open({ providers: [claude(), codex({ unavailable: undefined, models: null, loading: true }), { ...codex(), id: "cx2", name: "Codex 2", unavailable: undefined, models: null, error: "连不上" }], onReload });
    const sections = [...document.body.querySelectorAll('[data-panel-section="provider"]')];
    expect(sections[0].querySelector('[role="status"]')?.textContent).toContain("正在读取模型");
    expect(sections[1].textContent).toContain("连不上");
    act(() => button("重试").click());
    expect(onReload).toHaveBeenCalledWith("cx2");
  });

  it("Configure models opens Settings › Model providers and closes the panel", async () => {
    const { onClose } = await open();
    act(() => button("配置模型").click());
    expect(onClose).toHaveBeenCalled();
    expect(navigated).toEqual(["/settings?tab=models#settings-providers"]);
  });

  it("a press outside closes it; one on the pill (which toggles it itself) does not", async () => {
    const { onClose } = await open({ header: <div data-testid="strip">跟随默认</div> });
    expect(document.body.querySelector('[data-testid="strip"]')).not.toBeNull();
    act(() => { anchor.dispatchEvent(new Event("pointerdown", { bubbles: true })); });
    expect(onClose).not.toHaveBeenCalled();
    act(() => { search().dispatchEvent(new Event("pointerdown", { bubbles: true })); });
    expect(onClose).not.toHaveBeenCalled();
    act(() => { document.body.dispatchEvent(new Event("pointerdown", { bubbles: true })); });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("keeps an Agent's records apart from a Persona chat's, under this hub's address", () => {
    expect(panelStoreKey("agent", "favorites")).toBe("persona.modelPanel..agent.favorites");
    expect(panelStoreKey("assistant", "recent", "/hub")).toBe("persona.modelPanel./hub.assistant.recent");
  });
});

describe("a long list", () => {
  const many = (n: number): PanelProvider => ({ id: "gw", name: "Gateway", engine: "claude", models: Array.from({ length: n }, (_, i) => ({ id: `gw-model-${i}`, name: `Gateway ${i}`, efforts: [] })) });

  it("the All view cuts a long group to its first rows; Show all opens that provider's view", async () => {
    await open({ providers: [claude(), many(20)] });
    const more = document.body.querySelector<HTMLButtonElement>('[data-panel-more="gw"]')!;
    expect(more.textContent).toBe("显示全部 20 个");
    expect(rows().filter((r) => r.textContent?.includes("Gateway "))).toHaveLength(8);
    act(() => more.click());
    expect(button("Gateway").getAttribute("aria-pressed")).toBe("true");
    expect(rows().filter((r) => r.textContent?.includes("Gateway "))).toHaveLength(20);
    expect(document.body.querySelector("[data-panel-more]")).toBeNull();
  });

  it("the rail fades the edges it has more cells beyond", async () => {
    expect(railEdges({ scrollTop: 0, clientHeight: 400, scrollHeight: 400 })).toEqual({ up: false, down: false });
    expect(railEdges({ scrollTop: 0, clientHeight: 400, scrollHeight: 900 })).toEqual({ up: false, down: true });
    expect(railEdges({ scrollTop: 200, clientHeight: 400, scrollHeight: 900 })).toEqual({ up: true, down: true });
    expect(railEdges({ scrollTop: 500, clientHeight: 400, scrollHeight: 900 })).toEqual({ up: true, down: false });
    expect(railFade(false, false)).toBeUndefined();
    expect(railFade(false, true)).toBe("linear-gradient(180deg,#000,#000 calc(100% - 20px),transparent)");
    expect(railFade(true, false)).toBe("linear-gradient(180deg,transparent,#000 20px,#000)");

    await open({ providers: [claude(), codex(), many(3)] });
    const rail = document.body.querySelector<HTMLElement>('[role="group"][aria-label="模型视图"]')!;
    expect(rail.dataset.railFade).toBeUndefined();
    Object.defineProperties(rail, { clientHeight: { value: 200, configurable: true }, scrollHeight: { value: 600, configurable: true }, scrollTop: { value: 150, configurable: true, writable: true } });
    act(() => { rail.dispatchEvent(new Event("scroll")); });
    expect(rail.dataset.railFade).toBe("up down");
    expect(rail.style.maskImage).toContain("linear-gradient");
  });

  it("a name cut short shows the launcher and whole model ID; others add them only on a touch screen", async () => {
    const sw = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollWidth");
    const cw = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientWidth");
    // a name longer than 20 characters does not fit
    Object.defineProperty(HTMLElement.prototype, "scrollWidth", { configurable: true, get(this: HTMLElement) { return (this.textContent?.length ?? 0) * 10; } });
    Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get() { return 200; } });
    try {
      const long = "vendor/very-long-model-name-2026-11-01-preview";
      await open({ providers: [claude({ models: [{ id: long, name: long, efforts: [] }, { id: "sonnet", name: "Sonnet 5", efforts: [] }] }), codex()], state: { current: null, recommended: [] } });
      const cut = row(long).querySelector<HTMLElement>("[data-row-id]")!;
      expect(cut.dataset.rowId).toBe("always");
      expect(cut.textContent).toBe(`Claude · ${long}`);
      expect(cut.firstElementChild!.className).toContain("line-clamp-2");
      const short = row("Sonnet 5").querySelector<HTMLElement>("[data-row-id]")!;
      expect(short.dataset.rowId).toBe("touch");
      expect(short.className).toContain("hidden");
      expect(short.textContent).toBe("Claude · sonnet");
    } finally {
      if (sw) Object.defineProperty(HTMLElement.prototype, "scrollWidth", sw);
      if (cw) Object.defineProperty(HTMLElement.prototype, "clientWidth", cw);
    }
  });
});

describe("model families", () => {
  const max = (): PanelProvider => ({
    id: "claude-max",
    name: "claude-max",
    engine: "claude",
    models: [
      { id: "default", name: "Default (recommended)", resolved: "claude-opus-5-5", efforts: [] },
      { id: "opus", name: "Opus 5.5", resolved: "claude-opus-5-5", efforts: [] },
      { id: "fable", name: "Fable 5.2", resolved: "claude-fable-5-2", efforts: [] },
      { id: "claude-fable-5", name: "Fable 5", efforts: [] },
      { id: "claude-opus-4-8", name: "Opus 4.8", efforts: [] },
      { id: "claude-opus-4-7", name: "Opus 4.7", efforts: [] },
    ],
  });
  const names = () => rows().map((r) => r.querySelector(".truncate")?.textContent);
  const fold = (family: string) => document.body.querySelector<HTMLButtonElement>(`[data-panel-fold$="claude:${family}"]`)!;

  it("each family under its name, the newest marked Latest; Earlier versions unfolds and folds; a search finds the folded", async () => {
    await open({ providers: [max(), codex()], state: { current: null, recommended: [] } });
    expect([...document.body.querySelectorAll("[data-panel-family]")].map((f) => f.getAttribute("aria-label"))).toEqual(["Claude Fable", "Claude Opus"]);
    expect(names()).toEqual(["Default (recommended)", "Fable 5.2", "Opus 5.5", "GPT 5.5"]);
    expect(row("Opus 5.5").querySelector("[data-row-latest]")?.textContent).toBe("最新");
    expect(row("Default (recommended)").querySelector("[data-row-latest]")).toBeNull();

    const opus = fold("opus");
    expect(opus.textContent).toBe("更早版本 · 2");
    expect(opus.getAttribute("aria-label")).toBe("Claude Opus：显示 2 个更早版本");
    expect(opus.getAttribute("aria-expanded")).toBe("false");
    act(() => opus.click());
    expect(names()).toEqual(["Default (recommended)", "Fable 5.2", "Opus 5.5", "Opus 4.8", "Opus 4.7", "GPT 5.5"]);
    expect(fold("opus").getAttribute("aria-expanded")).toBe("true");
    expect(fold("opus").textContent).toBe("收起更早版本");
    act(() => fold("opus").click());
    expect(names()).not.toContain("Opus 4.8");

    type(search(), "opus 4.7");
    expect(names()).toEqual(["Opus 4.7"]);
    expect(document.body.querySelector("[data-panel-fold]")).toBeNull();
  });

  it("the older model that runs now stays in its family, and ↑↓ walk the rows shown", async () => {
    await open({ providers: [max(), codex()], state: { current: { provider: "claude-max", model: "claude-opus-4-7" }, recommended: [], starting: true } });
    expect(names()).toEqual(["Default (recommended)", "Fable 5.2", "Opus 5.5", "Opus 4.7", "GPT 5.5"]);
    expect(fold("opus").textContent).toBe("更早版本 · 1");
    const now = row("Opus 4.7");
    expect(now.hasAttribute("data-current")).toBe(true);
    act(() => now.focus());
    await key(now, "ArrowUp");
    expect(document.activeElement).toBe(row("Opus 5.5"));
  });
});
