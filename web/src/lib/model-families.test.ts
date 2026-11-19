// Model families in the model panel: which family and version a model ID names, how a provider's list is ordered and
// folded, and what never folds away (the model that runs, favorites, recent setups, a search).
import { describe, expect, it } from "vitest";
import { compareVersions, familyBlocks, modelFamily, type FamilyOptions } from "./model-families";
import { panelSections, type PanelModel, type PanelProvider, type PanelRow, type SectionsInput } from "./model-panel";

describe("modelFamily", () => {
  it("reads Claude's tier and version from the concrete ID, an alias through what it resolves to", () => {
    expect(modelFamily({ id: "claude-opus-4-8" })).toMatchObject({ key: "claude:opus", label: "Claude Opus", version: [4, 8], rank: 1 });
    expect(modelFamily({ id: "opus", resolved: "claude-opus-5-5" })).toMatchObject({ key: "claude:opus", version: [5, 5] });
    expect(modelFamily({ id: "claude-fable-5" })).toMatchObject({ key: "claude:fable", version: [5], rank: 0 });
    expect(modelFamily({ id: "claude-haiku-4-5-20251001" })).toMatchObject({ key: "claude:haiku", version: [4, 5], rank: 3 });
    expect(modelFamily({ id: "claude-fable-5-2[1m]" })).toMatchObject({ key: "claude:fable", version: [5, 2] });
    expect(modelFamily({ id: "anthropic/claude-sonnet-4-6" })).toMatchObject({ key: "claude:sonnet", version: [4, 6] });
    expect(modelFamily({ id: "claude-3-5-sonnet-20241022" })).toMatchObject({ key: "claude:sonnet", version: [3, 5] });
  });

  it("makes a GPT line of each variant and a Gemini tier of each name, without thinking levels or release channels", () => {
    expect(modelFamily({ id: "gpt-6.1-sol" })).toMatchObject({ key: "gpt:sol", label: "GPT Sol", version: [6, 1] });
    expect(modelFamily({ id: "gpt-5.5" })).toMatchObject({ key: "gpt:", label: "GPT", version: [5, 5] });
    expect(modelFamily({ id: "gpt-5.5-codex-mini" })).toMatchObject({ key: "gpt:codex-mini", label: "GPT Codex Mini" });
    expect(modelFamily({ id: "gemini-3.8-flash-medium" })).toMatchObject({ key: "gemini:flash", label: "Gemini Flash", version: [3, 8] });
    expect(modelFamily({ id: "gemini-3-pro-preview" })).toMatchObject({ key: "gemini:pro", version: [3] });
    expect(modelFamily({ id: "gemini-3.8-flash-lite" })).toMatchObject({ key: "gemini:flash-lite", label: "Gemini Flash Lite" });
    expect(modelFamily({ id: "google/gemini-4-argon" })).toMatchObject({ key: "gemini:argon", label: "Gemini Argon", version: [4] });
  });

  it("names no family for what it can't read plainly", () => {
    for (const id of ["default", "opus", "glm-5.3", "deepseek-v4", "gpt-4o", "o3", "grok-4", "vendor/model-x", "默认", "gemini-3.8"]) expect(modelFamily({ id })).toBeUndefined();
  });

  it("orders versions newest first, a missing minor counting as 0", () => {
    expect([[4, 8], [5], [5, 5], [4, 10]].sort(compareVersions)).toEqual([[5, 5], [5], [4, 10], [4, 8]]);
    expect(compareVersions([5], [5, 0])).toBe(0);
  });
});

// claude-max as the hub lists it today (2026-11-17)
const claudeMax: PanelModel[] = [
  { id: "default", name: "Default (recommended)", resolved: "claude-opus-5-5", efforts: [] },
  { id: "opus", name: "Opus 5.5", resolved: "claude-opus-5-5", efforts: [] },
  { id: "fable", name: "Fable 5.2", resolved: "claude-fable-5-2", efforts: [] },
  { id: "sonnet", name: "Sonnet 5.5", resolved: "claude-sonnet-5-5", efforts: [] },
  { id: "haiku", name: "Haiku 5.5", resolved: "claude-haiku-5-5", efforts: [] },
  { id: "claude-fable-5-1", name: "Fable 5.1", efforts: [] },
  { id: "claude-haiku-4-5-20251001", name: "Haiku 4.5", efforts: [] },
  { id: "claude-sonnet-5", name: "Sonnet 5", efforts: [] },
  { id: "claude-opus-5", name: "Opus 5", efforts: [] },
  { id: "claude-fable-5", name: "Fable 5", efforts: [] },
  { id: "claude-opus-4-8", name: "Opus 4.8", efforts: [] },
  { id: "claude-opus-4-7", name: "Opus 4.7", efforts: [] },
  { id: "claude-opus-4-6", name: "Opus 4.6", efforts: [] },
  { id: "claude-sonnet-4-6", name: "Sonnet 4.6", efforts: [] },
];
const max: PanelProvider = { id: "claude-max", name: "claude-max", engine: "claude", models: claudeMax };
const codex: PanelProvider = {
  id: "codex",
  name: "codex",
  engine: "codex",
  models: ["gpt-6.1-sol", "gpt-6-astra", "gpt-6.1-terra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"].map((id) => ({ id, name: id, efforts: [] })),
};
const gateway: PanelProvider = {
  id: "aster",
  name: "aster",
  engine: "claude",
  models: [
    { id: "default", name: "Default (recommended)", resolved: "claude-opus-5-5", efforts: [] },
    ...["gemini-3.8-flash", "glm-5.3", "gpt-6-sol", "gemini-3.5-flash", "claude-opus-4-8", "gemini-3.8-pro", "gpt-6.1-sol", "opus", "deepseek-v4"].map((id) => ({ id, name: id, efforts: [], ...(id === "opus" ? { resolved: "claude-opus-5-5" } : {}) })),
    { id: "vendor/model-x", name: "vendor/model-x", efforts: [], custom: true },
  ],
};

const rowsOf = (p: PanelProvider): PanelRow[] =>
  (p.models ?? []).map((m) => ({ key: `model:${p.id}\n${m.id}`, kind: "model", provider: p, model: m, setup: { provider: p.id, model: m.id }, current: false, customized: false }));
const opts = (over: Partial<FamilyOptions> = {}): FamilyOptions => ({ fold: true, keep: () => false, open: new Set(), scope: "s", ...over });
const draw = (p: PanelProvider, over: Partial<FamilyOptions> = {}) =>
  familyBlocks(rowsOf(p), opts(over))?.map((b) => `${b.family?.label ?? "-"}: ${b.rows.map((r) => `${r.model.id}${r.latest ? "^" : ""}`).join(", ")}${b.older ? ` (+${b.older}${b.open ? " open" : ""})` : ""}`);

describe("familyBlocks", () => {
  it("Claude: the default entry first, then Fable, Opus, Sonnet, Haiku, each its newest version with the older folded", () => {
    expect(draw(max)).toEqual([
      "-: default",
      "Claude Fable: fable^ (+2)",
      "Claude Opus: opus^ (+4)",
      "Claude Sonnet: sonnet^ (+2)",
      "Claude Haiku: haiku^ (+1)",
    ]);
  });

  it("unfolded, a family lists every version, newest first", () => {
    expect(draw(max, { open: new Set(["s\nclaude-max\nclaude:opus"]) })?.[2]).toBe("Claude Opus: opus^, claude-opus-5, claude-opus-4-8, claude-opus-4-7, claude-opus-4-6 (+4 open)");
  });

  it("GPT: one family per line in the order the list first names it, newest first", () => {
    expect(draw(codex)).toEqual(["GPT Sol: gpt-6.1-sol^ (+2)", "GPT Astra: gpt-6-astra", "GPT Terra: gpt-6.1-terra^ (+1)", "GPT Luna: gpt-6-luna^ (+1)"]);
  });

  it("a gateway's mixed list: vendors in first-mention order, Claude's tiers by capability, unknown models and typed IDs last as listed", () => {
    expect(draw(gateway)).toEqual([
      "-: default",
      "Gemini Flash: gemini-3.8-flash^ (+1)",
      "Gemini Pro: gemini-3.8-pro",
      "GPT Sol: gpt-6.1-sol^ (+1)",
      "Claude Opus: opus^ (+1)",
      "-: glm-5.3, deepseek-v4, vendor/model-x",
    ]);
  });

  it("an older version in use stays out of the fold, in version order, and is not counted in it", () => {
    const used = new Set(["claude-opus-4-7", "claude-haiku-4-5-20251001"]);
    const out = draw(max, { keep: (r) => used.has(r.model.id) });
    expect(out?.[2]).toBe("Claude Opus: opus^, claude-opus-4-7 (+3)");
    expect(out?.[4]).toBe("Claude Haiku: haiku^, claude-haiku-4-5-20251001");
  });

  it("not folding (a search), every row shows, families still together", () => {
    expect(draw(max, { fold: false })?.[1]).toBe("Claude Fable: fable^, claude-fable-5-1, claude-fable-5");
  });

  it("keeps every row of the newest version (its 1M-context one too) and marks none latest when nothing is older", () => {
    const p: PanelProvider = { ...max, models: [{ id: "claude-opus-5-5", name: "Opus 5.5", efforts: [] }, { id: "claude-opus-5-5[1m]", name: "Opus 5.5 (1M)", efforts: [] }, { id: "claude-opus-5", name: "Opus 5", efforts: [] }] };
    expect(draw(p)).toEqual(["Claude Opus: claude-opus-5-5^, claude-opus-5-5[1m]^ (+1)"]);
    // two aliases of one model come from the hub as one row, the other spelling among its aliases (#10365)
    const same: PanelProvider = { ...max, models: [{ id: "opus", name: "Sonnet 5.5", resolved: "claude-sonnet-5-5", aliases: ["sonnet"], efforts: [] }, { id: "claude-sonnet-5", name: "Sonnet 5", efforts: [] }] };
    expect(draw(same)).toEqual(["Claude Sonnet: opus^ (+1)"]);
  });

  it("no family with two rows: no blocks, the list stays as given", () => {
    const aliases: PanelProvider = { ...max, models: claudeMax.slice(0, 5) };
    expect(draw(aliases)).toBeUndefined();
  });
});

const input = (over: Partial<SectionsInput> = {}): SectionsInput => ({
  providers: [max, codex],
  state: { current: { provider: "codex", model: "gpt-6.1-sol" }, recommended: [] },
  view: { kind: "all" },
  query: "",
  favorites: [],
  recent: [],
  overrides: {},
  ...over,
});
const section = (o: Partial<SectionsInput>, key: string) => panelSections(input(o)).find((s) => s.key === key);
const ids = (o: Partial<SectionsInput>, key: string) => section(o, key)?.rows.map((r) => r.model.id);

describe("families in the panel's sections", () => {
  it("a provider group shows the newest of each family; its rows are the blocks' rows; no Show all for folded rows", () => {
    const s = section({}, "provider:claude-max");
    expect(s?.rows.map((r) => r.model.id)).toEqual(["default", "fable", "opus", "sonnet", "haiku"]);
    expect(s?.blocks?.flatMap((b) => b.rows)).toEqual(s?.rows);
    expect(s?.total).toBeUndefined();
  });

  it("a favorite's, a recent setup's, a customized and the running older model stay in sight", () => {
    const o: Partial<SectionsInput> = {
      favorites: [{ uid: "f", provider: "claude-max", model: "claude-opus-4-6" }],
      recent: [{ provider: "claude-max", model: "claude-sonnet-4-6" }],
      overrides: { "claude-max\nclaude-fable-5": { effort: "low" } },
      state: { current: { provider: "claude-max", model: "claude-haiku-4-5-20251001" }, recommended: [], starting: true },
    };
    expect(ids(o, "provider:claude-max")).toEqual(["default", "fable", "claude-fable-5", "opus", "claude-opus-4-6", "sonnet", "claude-sonnet-4-6", "haiku", "claude-haiku-4-5-20251001"]);
  });

  it("an older model of another provider with the same ID is not kept by this one's favorite", () => {
    expect(ids({ favorites: [{ uid: "f", provider: "codex", model: "claude-opus-4-6" }] }, "provider:claude-max")).not.toContain("claude-opus-4-6");
  });

  it("a search finds folded versions, by name or ID", () => {
    expect(ids({ query: "opus 4" }, "provider:claude-max")).toEqual(["claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6"]);
    expect(ids({ query: "5.6" }, "provider:codex")).toEqual(["gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"]);
  });

  it("unfolding opens one family of one group", () => {
    const rows = ids({ openFamilies: new Set(["provider:claude-max\nclaude-max\nclaude:haiku"]) }, "provider:claude-max");
    expect(rows?.slice(-2)).toEqual(["haiku", "claude-haiku-4-5-20251001"]);
    expect(rows).not.toContain("claude-opus-4-8");
  });

  it("Recommended: the model that runs first, then its provider's families", () => {
    const s = section({ state: { current: { provider: "claude-max", model: "claude-opus-5" }, recommended: ["claude-max"] } }, "recommended");
    expect(s?.blocks?.map((b) => b.family?.label ?? b.key)).toEqual(["recommended\nnow", "recommended\nlead", "Claude Fable", "Claude Opus", "Claude Sonnet", "Claude Haiku"]);
    expect(s?.rows.map((r) => r.model.id)).toEqual(["claude-opus-5", "default", "fable", "opus", "sonnet", "haiku"]);
  });
});

// A gateway provider's list as the hub folds it (#10365): Claude Code listed opus, fable, sonnet and haiku there, all
// "Opus 5.5" for the one model every tier names, and the gateway's other route of it read "Opus 5.5" too (captured
// 2026-11-18); the hub sends the concrete model's row with the aliases, and the route's row says its route.
const gatewayApi: PanelProvider = {
  id: "gateway-api",
  name: "Gateway API",
  engine: "claude",
  models: [
    { id: "default", name: "Default (recommended)", resolved: "claude-opus-5-5", efforts: [] },
    { id: "claude-opus-5-5", name: "Opus 5.5", description: "Custom Opus model (claude-opus-5-5)", aliases: ["opus", "fable", "sonnet", "haiku"], efforts: [] },
    { id: "kiro/claude-opus-5-5", name: "Opus 5.5 (kiro)", description: "From gateway", efforts: [] },
  ],
};

describe("a gateway's folded list", () => {
  it("one Opus 5.5 row, the route's own beside it, Default apart; Latest once an older version is listed", () => {
    expect(draw(gatewayApi)).toEqual(["-: default", "Claude Opus: claude-opus-5-5, kiro/claude-opus-5-5"]);
    const names = familyBlocks(rowsOf(gatewayApi), opts())!.flatMap((b) => b.rows).map((r) => r.model.name);
    expect(names.filter((n) => n === "Opus 5.5")).toHaveLength(1);
    const older: PanelProvider = { ...gatewayApi, models: [...gatewayApi.models!, { id: "claude-opus-5", name: "Opus 5", efforts: [] }] };
    expect(draw(older)).toEqual(["-: default", "Claude Opus: claude-opus-5-5^, kiro/claude-opus-5-5^ (+1)"]);
  });

  it("a setup saved with an alias is that row's: it runs, never folds, and a search for the alias finds it without a typed row", () => {
    const providers = [{ ...gatewayApi, typable: true }];
    const all = (o: Partial<SectionsInput>) => panelSections({ ...input(o), providers });
    const lit = all({ state: { current: { provider: "gateway-api", model: "sonnet" }, recommended: [] } }).flatMap((s) => s.rows).filter((r) => r.current);
    expect(lit.map((r) => r.model.id)).toEqual(["claude-opus-5-5"]);
    const found = all({ query: "haiku" }).flatMap((s) => s.rows);
    expect(found.map((r) => `${r.kind}:${r.model.id}`)).toEqual(["model:claude-opus-5-5"]);
  });
});

describe("the newest version under a search", () => {
  it("is the family's newest in the whole list, not among the rows that answer", () => {
    const s = panelSections(input({ query: "opus" })).find((x) => x.key === "provider:claude-max");
    expect(s?.rows.filter((r) => r.latest).map((r) => r.model.id)).toEqual(["opus"]);
    const older = panelSections(input({ query: "opus 4" })).find((x) => x.key === "provider:claude-max");
    expect(older?.rows.map((r) => `${r.model.id}${r.latest ? "^" : ""}`)).toEqual(["claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6"]);
  });
});
