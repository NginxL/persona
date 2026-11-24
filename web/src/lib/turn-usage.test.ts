// What a finished exchange used and what it cost (#5426): every item of the exchange adds its tokens (results, folds,
// Codex's turn duration, a subagent's notice drawn on its card), a subagent's report counts once, models are summed
// apart (the fast speed too, which has no price), and the cost is cindy's: only when every model has a price for every
// kind it used. The card's lines are cindy's buildTurnUsageTooltipLines, in order.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../i18n";
import { chatLines, segments, type Segment } from "./turns";
import type { Item, TokenUsage } from "./types";
import { usageKey } from "./turn-usage";
import type { ModelTokens, UsageSummary } from "./turn-cost";

const ask = vi.hoisted(() => vi.fn());
vi.mock("./api", async (original) => ({ ...(await original<typeof import("./api")>()), api: ask }));

const { formatCost, formatPercent, forgetPrices, lowCache, modelCost, modelTokens, parseUsage, personaLines, priceOf, requestPrices, summarize, turnCost, usageLines, usageTotals } = await import("./turn-cost");

const OPUS = "claude-opus-5-5";
const SONNET = "claude-sonnet-5-5";
const u = (seq: number, text: string): Item => ({ id: `u${seq}#0`, seq, role: "user", kind: "text", text });
const a = (seq: number, text: string, usage?: TokenUsage[]): Item => ({ id: `a${seq}#0`, seq, role: "assistant", kind: "text", text, ...(usage ? { usage } : {}) });
const think = (seq: number, usage?: TokenUsage[]): Item => ({ id: `k${seq}#0`, seq, role: "assistant", kind: "thinking", text: "", ...(usage ? { usage } : {}) });
const call = (seq: number, id: string, name = "Bash", usage?: TokenUsage[]): Item => ({ id: `t${seq}#0`, seq, role: "assistant", kind: "tool_use", tool_use_id: id, tool_name: name, tool_input: { command: "ls" }, ...(usage ? { usage } : {}) });
const result = (seq: number, id: string, usage?: TokenUsage[]): Item => ({ id: `r${seq}#0`, seq, role: "user", kind: "tool_result", tool_use_id: id, text: "ok", ...(usage ? { usage } : {}) });
const opus = (n: Partial<TokenUsage>): TokenUsage => ({ model: OPUS, ...n });
const tokens = (m: Partial<ModelTokens>): ModelTokens => ({ model: OPUS, in: 0, out: 0, cache_read: 0, cache_write: 0, cache_write_1h: 0, ...m });

const segOf = (lines: ReturnType<typeof chatLines>): Segment[] => lines.flatMap((l) => (l.kind === "segment" ? [l.seg] : []));
const models = (s: Segment | undefined) => summarize(s?.usage)?.models;

describe("an exchange's usage", () => {
  it("adds every item of the exchange: replies, thoughts, calls, a sync subagent's result, folds and Codex's turn duration", () => {
    const fold: Item = { id: "fold:f5#0", seq: 5, role: "assistant", kind: "fold", fold: { items: 4, tools: 2, thoughts: 0, through_seq: 8, usage: [opus({ in: 10, out: 20, cache_read: 300 })] } };
    const duration = { id: "d12#0", seq: 12, role: "system", kind: "turn_duration", turn_duration: { duration_ms: 9000 }, usage: [{ model: "gpt-5.5", in: 7, out: 8, cache_read: 9 }] } as unknown as Item;
    const items = [
      u(1, "fix it"),
      think(2, [opus({ in: 2, out: 30, cache_read: 1000, cache_write: 500, cache_write_1h: 100 })]),
      call(3, "c3", "Agent", [opus({ in: 1, out: 5, cache_read: 1500 })]),
      result(4, "c3", [{ id: "agent:x", model: SONNET, in: 50, out: 60, cache_read: 70, cache_write: 80 }]),
      fold,
      a(11, "done", [opus({ out: 40, cache_read: 1600 })]),
      duration,
      u(13, "next"),
      a(14, "sure"),
    ];
    const [first, second] = segOf(chatLines(items));
    expect(models(first)).toEqual([
      { model: OPUS, in: 13, out: 95, cache_read: 4400, cache_write: 500, cache_write_1h: 100 },
      { model: "gpt-5.5", in: 7, out: 8, cache_read: 9, cache_write: 0, cache_write_1h: 0 },
      { model: SONNET, in: 50, out: 60, cache_read: 70, cache_write: 80, cache_write_1h: 0 }, // its subagent's, after its own
    ]);
    expect(first.usage?.first).toBe(true);
    expect(second.usage).toBeUndefined(); // an older node's exchange: nothing to show
    // as the items carry them, summed only with the bar; the subagent's report is the chat's to place (chatLines)
    expect(segments(items)[0].usage?.entries).toHaveLength(5);
  });

  it("counts a subagent's report once, its largest count of each kind", () => {
    const report = (n: Partial<TokenUsage>): TokenUsage[] => [{ id: "agent:x", model: SONNET, ...n }];
    const items = [u(1, "go"), call(2, "c2", "Agent"), result(3, "c2", report({ in: 10, out: 5 })), { ...result(4, "c2", report({ in: 8, out: 9 })), id: "r4b#0" }, a(5, "ok")];
    expect(models(segOf(chatLines(items))[0])).toEqual([{ model: SONNET, in: 10, out: 9, cache_read: 0, cache_write: 0, cache_write_1h: 0 }]);
    // two subagents on the same model are two reports, summed; their calls not on the page, they count where they are
    const two = [u(1, "go"), result(3, "c2", report({ in: 10 })), result(4, "c4", [{ id: "agent:y", model: SONNET, in: 3 }]), a(5, "ok")];
    expect(models(segOf(chatLines(two))[0])?.[0].in).toBe(13);
  });

  it("counts a subagent once across the chat, in the exchange that launched it, its work after a resume included", () => {
    // it failed in the first exchange, was resumed (SendMessage) in the second and finished in the third
    const items = [
      u(1, "check the export"), call(2, "c2", "Agent", [opus({ out: 10 })]), result(3, "c2", [{ id: "agent:x", model: SONNET, in: 5, out: 100 }]), a(4, "it failed"),
      u(5, "try again"), call(6, "c6", "SendMessage", [opus({ out: 11 })]), result(7, "c6"), a(8, "resumed"),
      u(9, "and?"), { ...result(10, "c2", [{ id: "agent:x", model: SONNET, in: 7, out: 300 }]), id: "r10#0" }, a(11, "done", [opus({ out: 12 })]),
    ];
    const segs = segOf(chatLines(items));
    expect(segs.map((s) => models(s)?.map((m) => [m.model, m.in, m.out]))).toEqual([[[OPUS, 0, 10], [SONNET, 7, 300]], [[OPUS, 0, 11]], [[OPUS, 0, 12]]]);
  });

  it("counts a background subagent's result after your next message in the exchange that launched it", () => {
    // newer nodes turn the completion notification into a tool result, its call the notification's own
    const done: Item = { ...result(6, "notify-bg1", [{ id: "agent:bg1", model: SONNET, out: 900 }]), task: { kind: "agent", status: "completed", task_id: "bg1", tool_use_id: "c2" } };
    const items = [u(1, "look in the background"), call(2, "c2", "Agent", [opus({ out: 10 })]), result(3, "c2"), a(4, "started"), u(5, "and meanwhile?"), done, a(7, "here", [opus({ out: 20 })])];
    const segs = segOf(chatLines(items));
    expect(models(segs[0])?.map((m) => [m.model, m.out])).toEqual([[OPUS, 10], [SONNET, 900]]);
    expect(models(segs[1])?.map((m) => [m.model, m.out])).toEqual([[OPUS, 20]]);
  });

  it("counts a nested subagent once, reported under its parent's report and on its own", () => {
    const parent = result(3, "c2", [{ id: "agent:p", model: SONNET, out: 100 }, { id: "agent:child", model: "claude-haiku-5-5", out: 40 }]);
    const child: Item = { id: "n5#0", seq: 5, role: "system", kind: "notice", text: "Agent done", task: { kind: "agent", status: "completed", task_id: "child" }, usage: [{ id: "agent:child", model: "claude-haiku-5-5", out: 60 }] };
    const items = [u(1, "split it up"), call(2, "c2", "Agent"), parent, a(4, "on it"), child, a(6, "all done", [opus({ out: 1 })])];
    const [seg] = segOf(chatLines(items));
    expect(models(seg)?.map((m) => [m.model, m.out])).toEqual([[OPUS, 1], [SONNET, 100], ["claude-haiku-5-5", 60]]);
  });

  it("counts a background subagent that failed, was resumed and finished once, its nested one apart, as the node reports them", () => {
    // the node's shape: each report on a result of the launching call with its task, a nested subagent under its own
    // id on its parent's report and on its own result (whose call, inside the parent, is not on the page)
    const task = (id: string, call: string, status: string) => ({ kind: "agent", task_id: id, tool_use_id: call, status });
    const report = (seq: number, call: string, id: string, status: string, usage: TokenUsage[]): Item => ({ ...result(seq, call, usage), id: `r${seq}#0`, task: task(id, call, status) });
    const parent = (n: number): TokenUsage => ({ id: "agent:P", model: OPUS, in: 10, out: 100 + n });
    const child: TokenUsage = { id: "agent:C", model: OPUS, in: 1000, out: 5000 };
    const items = [
      u(1, "review it"), call(2, "toolu_P", "Agent"), { ...result(3, "toolu_P"), task: task("P", "toolu_P", "running") },
      report(4, "toolu_inside_P", "C", "failed", [child]),
      report(5, "toolu_P", "P", "failed", [parent(0), child]),
      a(6, "it failed"),
      u(7, "resume it"),
      report(8, "toolu_P", "P", "completed", [parent(20), child]),
      a(9, "done"),
    ];
    const segs = segOf(chatLines(items));
    expect(segs.map((s) => summarize(s.usage)?.models.map((m) => [m.in, m.out]))).toEqual([[[1010, 5120]], undefined]);
  });

  it("counts what a stopped turn's notice carries in its last exchange", () => {
    // a Codex turn you stopped: its total rides on the interruption notice, a line of its own after the exchange
    const stopped: Item = { id: "n4#0", seq: 4, role: "system", kind: "notice", text: "已中断", usage: [{ model: "gpt-5.5", in: 70, out: 8 }] };
    const lines = chatLines([u(1, "rename it"), call(2, "c2"), result(3, "c2"), a(3.5, "renaming"), stopped]);
    expect(lines.map((l) => l.kind)).toEqual(["segment", "notice"]);
    expect(segOf(lines)[0].cut).toBe(true);
    expect(models(segOf(lines)[0])).toEqual([{ model: "gpt-5.5", in: 70, out: 8, cache_read: 0, cache_write: 0, cache_write_1h: 0 }]);
  });

  it("sums a model whatever wrapper its id has, and keeps the fast speed apart", () => {
    expect(modelTokens([{ model: `${OPUS}-20261101`, in: 1 }, { model: OPUS, out: 3, fast: true }, { model: `us.anthropic.${OPUS}-v1:0`, in: 2 }, { in: 4 }])).toEqual([
      { model: OPUS, in: 3, out: 0, cache_read: 0, cache_write: 0, cache_write_1h: 0 },
      { model: OPUS, fast: true, in: 0, out: 3, cache_read: 0, cache_write: 0, cache_write_1h: 0 },
      { model: "", in: 4, out: 0, cache_read: 0, cache_write: 0, cache_write_1h: 0 },
    ]);
    expect(modelTokens([{ model: OPUS }])).toEqual([]); // all zero: nothing used
    // a report seen before the model's own requests keeps the model's place, and still counts once
    expect(modelTokens([{ id: "agent:x", model: SONNET, out: 5 }, { model: OPUS, out: 1 }, { id: "agent:x", model: SONNET, out: 4 }, { model: SONNET, out: 2 }]).map((m) => [m.model, m.out])).toEqual([[SONNET, 7], [OPUS, 1]]);
  });

  it("counts a background subagent's notice drawn on its card in the exchange that launched it", () => {
    const notice: Item = { id: "n6#0", seq: 6, role: "system", kind: "notice", text: "Agent done", task: { kind: "agent", status: "completed", task_id: "bg1", tool_use_id: "c2" }, usage: [{ id: "agent:bg1", model: SONNET, out: 900 }] };
    const items = [u(1, "look in the background"), call(2, "c2", "Agent", [opus({ out: 10 })]), result(3, "c2"), a(4, "started"), u(5, "and meanwhile?"), notice, a(7, "here", [opus({ out: 20 })])];
    const segs = segOf(chatLines(items));
    expect(models(segs[0])?.map((m) => [m.model, m.out])).toEqual([[OPUS, 10], [SONNET, 900]]);
    expect(models(segs[1])?.map((m) => [m.model, m.out])).toEqual([[OPUS, 20]]);
  });

  it("counts what woke a turn in the work it woke, and marks only the chat's first exchange with tokens as its first round", () => {
    // the page starts at a wake: its own line, then the work it woke
    const wake: Item = { id: "w1#0", seq: 1, role: "system", kind: "notice", wake: true, text: "Background command finished", task: { kind: "shell", status: "completed", task_id: "b1" }, usage: [opus({ in: 5 })] };
    const items = [wake, a(2, "built", [opus({ out: 2 })]), u(3, "hi"), a(4, "hello"), u(5, "again"), a(6, "sure", [opus({ out: 1 })])];
    const lines = chatLines(items);
    expect(lines.map((l) => l.kind)).toEqual(["notice", "segment", "segment", "segment"]);
    const segs = segOf(lines);
    expect(segs.map((s) => models(s)?.[0])).toEqual([tokens({ in: 5, out: 2 }), undefined, tokens({ out: 1 })]);
    expect(segs.map((s) => s.usage?.first)).toEqual([true, undefined, undefined]);
  });

  it("counts a wake after an answer, and the work it woke, in an exchange of its own (#9140)", () => {
    const wake: Item = { id: "w5#0", seq: 5, role: "system", kind: "notice", wake: true, text: "Background command finished", task: { kind: "shell", status: "completed", task_id: "b1" }, usage: [opus({ in: 5 })] };
    const items = [u(1, "check the trains"), a(2, "running", [opus({ out: 3 })]), u(3, "what to pack?"), a(4, "a rain jacket", [opus({ out: 4 })]), wake, a(6, "three trains run", [opus({ out: 6 })])];
    const segs = segOf(chatLines(items));
    expect(segs.map((s) => models(s)?.[0])).toEqual([tokens({ out: 3 }), tokens({ out: 4 }), tokens({ in: 5, out: 6 })]);
    expect(segs.map((s) => s.answer?.id)).toEqual(["a2#0", "a4#0", "a6#0"]);
  });

  it("is one string for the reply's props, the same for the same usage", () => {
    const one = segments([u(1, "a"), a(2, "b", [opus({ in: 1 })])])[0].usage;
    const two = segments([u(1, "a"), a(2, "b", [opus({ in: 1 })])])[0].usage;
    expect(usageKey(one)).toBe(usageKey(two));
    expect(parseUsage(usageKey(one))).toEqual({ models: [tokens({ in: 1 })] });
    expect(parseUsage(undefined)).toBeUndefined();
    expect(parseUsage("{")).toBeUndefined();
  });
});

describe("the totals", () => {
  it("adds input, output, cache reads and writes; the hit rate is the cache reads' share of all input", () => {
    const s = usageTotals([tokens({ in: 100, out: 50, cache_read: 300, cache_write: 100 }), tokens({ model: SONNET, out: 10 })]);
    expect([s.total, s.in, s.out, s.cache_read, s.cache_write]).toEqual([560, 100, 60, 300, 100]);
    expect(s.hit).toBe(0.6);
    expect(usageTotals([tokens({ out: 5 })]).hit).toBeNull();
  });

  it("writes amounts and percentages as cindy does", () => {
    expect([formatCost(0.123), formatCost(0.004), formatCost(0), formatCost(12.5)]).toEqual(["$0.12", "<$0.01", "$0.00", "$12.50"]);
    expect([formatPercent(0.6), formatPercent(0.1234), formatPercent(0.9999), formatPercent(null)]).toEqual(["60%", "12.3%", "100%", null]);
  });
});

describe("the cost", () => {
  const price = { input: 4, output: 20, cache_read: 0.2, cache_write: 5, cache_write_1h: 8 };

  it("prices each kind per million tokens, a 1-hour cache write at its own price or else the cache write's", () => {
    const m = tokens({ in: 1e6, out: 1e6, cache_read: 1e6, cache_write: 3e6, cache_write_1h: 1e6 });
    expect(modelCost(m, price)).toBeCloseTo(4 + 20 + 0.2 + 2 * 5 + 8);
    expect(modelCost(m, { ...price, cache_write_1h: undefined })).toBeCloseTo(4 + 20 + 0.2 + 3 * 5);
  });

  it("has none when a kind with tokens has no price, for the fast speed, or without a price at all", () => {
    expect(modelCost(tokens({ in: 10, cache_write: 5 }), { input: 4, output: 20, cache_read: 0.2 })).toBeNull();
    expect(modelCost(tokens({ in: 10 }), { input: 4, output: 20 })).not.toBeNull(); // no cache used: no cache price needed
    expect(modelCost(tokens({ in: 10, fast: true }), price)).toBeNull();
    expect(modelCost(tokens({ in: 10 }), null)).toBeNull();
  });

  it("is the exchange's only when every model has one, else the bar shows tokens", () => {
    const prices = (id: string) => (id === OPUS ? price : id === SONNET ? { input: 2, output: 10, cache_read: 0.1, cache_write: 2.5 } : null);
    const both = turnCost([tokens({ out: 5000 }), tokens({ model: SONNET, out: 2000 })], prices);
    expect(both?.models).toEqual([{ model: OPUS, cost: 0.1 }, { model: SONNET, cost: 0.02 }]);
    expect(both?.total).toBeCloseTo(0.12);
    expect(turnCost([tokens({ out: 5000 }), tokens({ model: "claude-haiku-5-5", out: 1 })], prices)).toBeNull();
    expect(turnCost([tokens({ out: 5000 }), tokens({ model: "", out: 1 })], prices)).toBeNull();
    expect(turnCost([tokens({ out: 5000 }), tokens({ out: 1, fast: true })], prices)).toBeNull();
  });
});

describe("the card's lines", () => {
  beforeEach(async () => {
    await initI18n("en");
  });
  const usage = (models: ModelTokens[], first?: boolean): UsageSummary => ({ models, ...(first ? { first } : {}) });

  it("with a cost: the title, the value, the tokens, the cache, the time and the model, in cindy's order", () => {
    const one = usage([tokens({ in: 2, out: 3400, cache_read: 1_200_000, cache_write: 9000 })]);
    expect(usageLines(one, { total: 0.1234, models: [{ model: OPUS, cost: 0.1234 }] }, 65_000)).toEqual([
      "Request details",
      "Token value this turn: $0.12",
      "Tokens: 1.2M total · input 2 · output 3.4k",
      "Cache breakdown: read 1.2M · write 9.0k · hit rate 99.3%",
      "Time 1m 5s",
      "Model: Opus 5.5",
    ]);
    expect(usageLines(one, { total: 0.1234, models: [{ model: OPUS, cost: 0.1234 }] }, 65_000, 342)).toEqual([
      "Request details",
      "Token value this turn: $0.12",
      "Tokens: 1.2M total · input 2 · output 3.4k",
      "Cache breakdown: read 1.2M · write 9.0k · hit rate 99.3%",
      "Time 1m 5s",
      "First token 342 ms",
      "Model: Opus 5.5",
    ]);
  });

  it("captures first observed non-negative ttft_ms on a Segment", () => {
    const segs = segOf(chatLines([
      u(1, "go"),
      { ...think(2), ttft_ms: -5 },
      { ...call(3, "c3"), ttft_ms: 0 },
      { ...a(4, "done"), ttft_ms: 420 },
    ]));
    expect(segs[0].ttftMs).toBe(0);
  });

  it("with two priced models: each one's cost instead of the model", () => {
    const two = usage([tokens({ out: 5000 }), tokens({ model: SONNET, out: 2000 })]);
    expect(usageLines(two, { total: 0.12, models: [{ model: OPUS, cost: 0.1 }, { model: SONNET, cost: 0.02 }] })).toEqual([
      "Request details",
      "Token value this turn: $0.12",
      "By model:",
      "· Opus 5.5 $0.10",
      "· Sonnet 5.5 $0.02",
      "Tokens: 7.0k total · input 0 · output 7.0k",
      "Cache breakdown: read 0 · write 0",
    ]);
  });

  it("without a cost: the tokens, how many models, and why there is no cost", () => {
    const two = usage([tokens({ in: 100, out: 23_300 }), tokens({ model: "claude-haiku-5-5", out: 10 })]);
    expect(usageLines(two, null)).toEqual([
      "Tokens: 23.4k total · input 100 · output 23.3k",
      "Cache breakdown: read 0 · write 0 · hit rate 0%",
      "Model: 2 models",
      "Cost unavailable for this turn; showing usage only",
    ]);
  });

  it("suggests looking at the cache when a lot of input missed it, never in the first round", () => {
    const missed = [tokens({ in: 45_000, cache_read: 5000, out: 10 })];
    expect(lowCache(usage(missed))).toBe(true);
    expect(lowCache(usage(missed, true))).toBe(false);
    expect(lowCache(usage([tokens({ in: 40_000, cache_read: 5000 })]))).toBe(false); // under 50,000 input
    expect(lowCache(usage([tokens({ in: 30_000, cache_read: 30_000 })]))).toBe(false); // half from the cache
    expect(usageLines(usage(missed), null).at(-2)).toBe("Suggestion: Cache hit rate is low; more context was billed again this turn");
  });

  it("in a Persona conversation: the cost in plain words, each model's share when there were several", () => {
    expect(personaLines({ total: 0.12, models: [{ model: OPUS, cost: 0.12 }] })).toEqual(["About $0.12 for this reply, estimated at list prices"]);
    expect(personaLines({ total: 0.12, models: [{ model: OPUS, cost: 0.1 }, { model: SONNET, cost: 0.02 }] })).toEqual([
      "About $0.12 for this reply, estimated at list prices",
      "By model:",
      "· Opus 5.5 $0.10",
      "· Sonnet 5.5 $0.02",
    ]);
  });
});

describe("prices", () => {
  beforeEach(() => {
    forgetPrices();
    ask.mockReset();
  });
  const settle = async () => {
    for (let i = 0; i < 5; i++) await Promise.resolve();
  };

  it("asks the catalog once for ids asked for together or again, and remembers which have none", async () => {
    ask.mockResolvedValue({ models: [{ id: OPUS, price: { input: 4, output: 20 } }] });
    requestPrices([OPUS, "claude-haiku-5-5"]);
    requestPrices([OPUS]);
    await settle();
    requestPrices([OPUS, "claude-haiku-5-5"]);
    await settle();
    expect(ask).toHaveBeenCalledTimes(1);
    expect(ask.mock.calls[0][0]).toBe("/launchers/catalog");
    expect(ask.mock.calls[0][1]).toMatchObject({ method: "POST", json: { ids: [OPUS, "claude-haiku-5-5"] } });
    expect(priceOf(OPUS)).toEqual({ input: 4, output: 20 });
    expect(priceOf("claude-haiku-5-5")).toBeNull();
  });

  it("has no price when the catalog cannot be asked, and does not ask again at once", async () => {
    ask.mockRejectedValue(new Error("offline"));
    requestPrices([OPUS]);
    await settle();
    expect(priceOf(OPUS)).toBeUndefined();
    requestPrices([OPUS]);
    await settle();
    expect(ask).toHaveBeenCalledTimes(1);
  });
});
