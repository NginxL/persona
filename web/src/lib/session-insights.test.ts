import { afterEach, describe, expect, it } from "vitest";
import { initI18n } from "../i18n";
import {
  aggregateSessionTokens,
  buildSessionInsights,
  compactActionSupport,
  formatHitRate,
  formatLatencyMs,
  latestTtftMs,
  latestTurnDurationMs,
  normalizeContextBreakdown,
  summarizeSessionPerformance,
  summarizeStepTelemetry,
} from "./session-insights";
import { clearRateHistories, emptyRateHistory, recordTokens, saveRateHistory } from "./token-rate";
import type { ContextBreakdown, Item, Session } from "./types";

const baseSession: Session = {
  machine_id: "m1",
  session_id: "s1",
  title: "Test",
  cwd: "/code",
  status: "idle",
  agent: "claude",
};

const item = (partial: Partial<Item> & Pick<Item, "id" | "seq">): Item => ({
  role: "assistant",
  kind: "text",
  ...partial,
});

describe("aggregateSessionTokens", () => {
  it("returns null for empty or zero-usage items", () => {
    expect(aggregateSessionTokens([])).toBeNull();
    expect(aggregateSessionTokens([
      item({ id: "1", seq: 1, text: "hi" }),
      item({ id: "2", seq: 2, usage: [{ in: 0, out: -5, cache_read: Number.NaN, cache_write: Number.POSITIVE_INFINITY }] }),
    ])).toBeNull();
  });

  it("sums direct and folded Item.Usage and computes cache hit rate", () => {
    const items: Item[] = [
      item({
        id: "1",
        seq: 1,
        usage: [{ model: "claude-sonnet-4-5-20250929", in: 1000, out: 500, cache_read: 8000, cache_write: 1000 }],
      }),
      item({
        id: "2",
        seq: 2,
        kind: "fold",
        fold: {
          items: 2,
          tools: 1,
          thoughts: 1,
          through_seq: 3,
          usage: [{ model: "anthropic/claude-sonnet-4-5", in: 500, out: 300, cache_read: 9000, cache_write: 500 }],
        },
      }),
    ];

    const summary = aggregateSessionTokens(items);
    expect(summary).not.toBeNull();
    expect(summary!.input).toBe(1500);
    expect(summary!.output).toBe(800);
    expect(summary!.cacheRead).toBe(17000);
    expect(summary!.cacheWrite).toBe(1500);
    expect(summary!.total).toBe(20800);
    expect(summary!.hasCache).toBe(true);
    // 17000 / (1500 + 17000 + 1500) = 17000 / 20000 = 0.85
    expect(summary!.cacheHitRate).toBeCloseTo(0.85, 6);
    expect(summary!.models).toHaveLength(1);
    expect(summary!.models[0]).toMatchObject({
      model: "claude-sonnet-4-5",
      label: "Sonnet 4.5",
      total: 20800,
      hasCache: true,
    });
  });

  it("omits cache metrics honestly when no cache tokens exist", () => {
    const summary = aggregateSessionTokens([
      item({ id: "1", seq: 1, usage: [{ model: "gpt-5.4", in: 2000, out: 600 }] }),
    ]);
    expect(summary).not.toBeNull();
    expect(summary!.hasCache).toBe(false);
    expect(summary!.cacheHitRate).toBeNull();
    expect(summary!.models[0].hasCache).toBe(false);
    expect(summary!.models[0].cacheHitRate).toBeNull();
  });

  it("reports 0% cache hit rate when cache_write occurred with zero cache_read", () => {
    const summary = aggregateSessionTokens([
      item({ id: "1", seq: 1, usage: [{ model: "claude-opus-4-5", in: 1000, out: 200, cache_write: 4000 }] }),
    ]);
    expect(summary).not.toBeNull();
    expect(summary!.hasCache).toBe(true);
    expect(summary!.cacheHitRate).toBe(0);
  });

  it("deduplicates subagent reports by (id, model, fast) and sorts multi-model breakdown by total desc", () => {
    const items: Item[] = [
      item({
        id: "1",
        seq: 1,
        model: "claude-haiku-4-5-20251001",
        // direct entry without u.model inherits item.model
        usage: [{ in: 400, out: 100 }],
      }),
      item({
        id: "2",
        seq: 2,
        usage: [
          { id: "sub-1", model: "claude-opus-4-5", in: 1000, out: 500, cache_read: 3000 },
          { id: "sub-1", model: "claude-opus-4-5", in: 1200, out: 400, cache_read: 5000 },
        ],
      }),
      item({
        id: "3",
        seq: 3,
        usage: [
          // duplicate report on a later item takes max per field, not sum
          { id: "sub-1", model: "claude-opus-4-5", in: 900, out: 700, cache_read: 5000 },
          { model: "claude-sonnet-4-5", in: 2000, out: 800, cache_read: 1000 },
        ],
      }),
    ];

    const summary = aggregateSessionTokens(items)!;
    // sub-1 deduplicated: in=1200, out=700, cache_read=5000 => 6900
    // sonnet: 3800; haiku: 500
    expect(summary.models.map((m) => [m.model, m.total])).toEqual([
      ["claude-opus-4-5", 6900],
      ["claude-sonnet-4-5", 3800],
      ["claude-haiku-4-5", 500],
    ]);
    expect(summary.total).toBe(6900 + 3800 + 500);
  });

  it("keeps normal-speed and fast-speed usage on the same model in separate buckets (matching turn-cost.modelTokens)", () => {
    const items: Item[] = [
      item({
        id: "1",
        seq: 1,
        usage: [
          { model: "claude-opus-4-5", in: 1000, out: 500, cache_read: 4000 },
          { model: "claude-opus-4-5", fast: true, in: 600, out: 400, cache_read: 1000 },
          { id: "sub-1", model: "claude-opus-4-5", in: 500, out: 200 },
          { id: "sub-1", model: "claude-opus-4-5", fast: true, in: 300, out: 100, cache_read: 600 },
        ],
      }),
      item({
        id: "2",
        seq: 2,
        usage: [
          // duplicate sub-1 reports deduplicate within each speed bucket independently
          { id: "sub-1", model: "claude-opus-4-5", in: 700, out: 200 },
          { id: "sub-1", model: "claude-opus-4-5", fast: true, in: 300, out: 200, cache_read: 600 },
        ],
      }),
    ];

    const summary = aggregateSessionTokens(items)!;
    // normal: direct (1000, 500, 4000) + sub-1 (700, 200, 0) = in 1700, out 700, read 4000 => 6400
    // fast:   direct (600, 400, 1000)  + sub-1 (300, 200, 600) = in 900, out 600, read 1600 => 3100
    expect(summary.models).toHaveLength(2);
    expect(summary.models[0]).toMatchObject({
      model: "claude-opus-4-5",
      input: 1700,
      output: 700,
      cacheRead: 4000,
      total: 6400,
    });
    expect(summary.models[0].fast).toBeUndefined();
    expect(summary.models[1]).toMatchObject({
      model: "claude-opus-4-5",
      fast: true,
      input: 900,
      output: 600,
      cacheRead: 1600,
      total: 3100,
    });
    expect(summary.total).toBe(9500);
  });
});

describe("latestTurnDurationMs", () => {
  it("computes duration of the latest turn including fold.until", () => {
    const items: Item[] = [
      { id: "u1", seq: 1, role: "user", kind: "text", ts: "2026-11-20T10:00:00Z", text: "first" },
      { id: "a1", seq: 2, role: "assistant", kind: "text", ts: "2026-11-20T10:00:05Z", text: "ok" },
      { id: "u2", seq: 3, role: "user", kind: "text", ts: "2026-11-20T10:01:00Z", text: "second" },
      {
        id: "f1",
        seq: 4,
        role: "assistant",
        kind: "fold",
        ts: "2026-11-20T10:01:02Z",
        fold: { items: 2, tools: 1, thoughts: 0, through_seq: 5, until: "2026-11-20T10:01:18Z" },
      },
    ];
    expect(latestTurnDurationMs(items)).toBe(18_000);
  });

  it("falls back to previous completed turn if latest user message has no reply yet", () => {
    const items: Item[] = [
      { id: "u1", seq: 1, role: "user", kind: "text", ts: "2026-11-20T10:00:00Z", text: "first" },
      { id: "a1", seq: 2, role: "assistant", kind: "text", ts: "2026-11-20T10:00:12Z", text: "done" },
      { id: "u2", seq: 3, role: "user", kind: "text", ts: "2026-11-20T10:02:00Z", text: "waiting" },
    ];
    expect(latestTurnDurationMs(items)).toBe(12_000);
  });

  it("returns null when timestamps are absent or non-increasing", () => {
    expect(latestTurnDurationMs([])).toBeNull();
    expect(latestTurnDurationMs([
      { id: "u1", seq: 1, role: "user", kind: "text", ts: "2026-11-20T10:00:00Z" },
      { id: "a1", seq: 2, role: "assistant", kind: "text", ts: "2026-11-20T10:00:00Z" },
    ])).toBeNull();
  });
});

describe("buildSessionInsights & performance", () => {
  afterEach(() => {
    clearRateHistories();
  });

  it("aggregates context, tokens, rate history, and reserved optional slots", () => {
    let h = emptyRateHistory();
    h = recordTokens(h, { turn: "t1", tokens: 50, at: 1000, now: 1000 });
    h = recordTokens(h, { turn: "t1", tokens: 100, at: 3000, now: 3000 });
    h = recordTokens(h, { turn: "t1", tokens: 200, at: 5000, now: 5000 });
    h = recordTokens(h, { turn: "t1", tokens: 350, at: 7000, now: 7000 }); // 250 tok / 4s = 62.5 tok/s
    saveRateHistory("m1/s1", h);

    const items: Item[] = [
      { id: "u1", seq: 1, role: "user", kind: "text", ts: "2026-11-20T10:00:00Z" },
      {
        id: "a1",
        seq: 2,
        role: "assistant",
        kind: "text",
        ts: "2026-11-20T10:00:09Z",
        usage: [{ model: "claude-opus-4-5", in: 500, out: 350, cache_read: 4500 }],
      },
    ];

    const insights = buildSessionInsights(
      {
        ...baseSession,
        context_tokens: 78_000,
        context_window: 200_000,
        context_breakdown: {
          source: "exact",
          system_and_rules: 12_000,
          tools_and_mcp: 16_000,
          conversation: 20_000,
          tool_outputs: 30_000,
          free: 122_000,
          auto_compact_threshold: 160_000,
        },
      },
      items,
      { ttftMs: 420 },
    );

    expect(insights.context).toMatchObject({ used: 39, left: 61, tokens: 78_000, window: 200_000 });
    expect(insights.tokens?.total).toBe(5350);
    expect(insights.tokens?.cacheHitRate).toBeCloseTo(0.9, 6);
    expect(insights.performance).toMatchObject({
      latestTurnDurationMs: 9000,
      latestTokPerSec: 62.5,
      avgTokPerSec: 62.5,
      peakTokPerSec: 62.5,
      ttftMs: 420,
    });
    expect(insights.breakdown).toMatchObject({
      source: "exact",
      occupiedTokens: 78_000,
      freeTokens: 122_000,
      totalTokens: 200_000,
      autoCompactThreshold: 160_000,
      autoCompactShare: 80,
      topConsumer: { key: "tool_outputs", tokens: 30_000 },
    });
    expect(insights.breakdown?.segments.map((s) => [s.key, s.tokens])).toEqual([
      ["system_and_rules", 12_000],
      ["tools_and_mcp", 16_000],
      ["conversation", 20_000],
      ["tool_outputs", 30_000],
    ]);
  });

  it("extracts latest observed ttft_ms from items automatically and preserves 0 ms", () => {
    const items: Item[] = [
      { id: "a1", seq: 1, role: "assistant", kind: "text", ttft_ms: 480 },
      { id: "a2", seq: 2, role: "assistant", kind: "text", ttft_ms: 0 },
      { id: "a3", seq: 3, role: "assistant", kind: "text", ttft_ms: -5 },
    ];
    expect(latestTtftMs(items)).toBe(0);
    const insights = buildSessionInsights(baseSession, items);
    expect(insights.performance?.ttftMs).toBe(0);
  });

  it.each([
    null,
    emptyRateHistory(),
    { ...emptyRateHistory(), genTokens: 100, genMs: 3000 },
    { ...emptyRateHistory(), latestRate: -1, samples: [{ durationMs: 1000, rate: NaN }, { durationMs: 2000, rate: 0 }] },
  ])("uses elapsed turn rates when there is no positive streaming rate (%j)", (history) => {
    const items: Item[] = [
      { id: "u1", seq: 1, role: "user", kind: "text", ts: "2026-11-20T10:00:00Z" },
      { id: "a1", seq: 2, role: "assistant", kind: "thinking", ts: "2026-11-20T10:00:02Z", usage: [{ out: 40 }] },
      { id: "q", seq: 3, role: "user", kind: "text", queued: true, ts: "2026-11-20T10:00:04Z" },
      { id: "r1", seq: 4, role: "user", kind: "tool_result", ts: "2026-11-20T10:00:06Z" },
      { id: "a2", seq: 5, role: "assistant", kind: "text", ts: "2026-11-20T10:00:10Z", usage: [{ out: 60 }] },
      { id: "u2#0", seq: 6, role: "user", kind: "text", ts: "2026-11-20T10:01:00Z" },
      { id: "u2#1", seq: 6, role: "user", kind: "image", ts: "2026-11-20T10:01:00Z" },
      {
        id: "f1", seq: 7, role: "assistant", kind: "fold", ts: "2026-11-20T10:01:03Z", ttft_ms: 2900,
        fold: { items: 3, tools: 1, thoughts: 0, through_seq: 9, until: "2026-11-20T10:01:04Z", usage: [{ out: 200 }] },
      },
      { id: "u3", seq: 10, role: "user", kind: "text", ts: "2026-11-20T10:02:00Z" },
    ];
    const performance = buildSessionInsights(baseSession, items, { rateHistory: history }).performance;
    expect(performance).toMatchObject({
      rateSource: "turn",
      latestTurnDurationMs: 4000,
      latestTokPerSec: 50,
      avgTokPerSec: 300 / 14,
      peakTokPerSec: null,
      ttftMs: 2900,
    });
    expect(latestTurnDurationMs(items)).toBe(performance?.latestTurnDurationMs);
  });

  it("keeps valid streaming samples even if a later sample is invalid", () => {
    const history = { ...emptyRateHistory(), samples: [{ durationMs: 4000, rate: 20 }, { durationMs: 5000, rate: Infinity }] };
    expect(summarizeSessionPerformance([], history)).toMatchObject({
      rateSource: "stream",
      latestTokPerSec: 20,
      avgTokPerSec: 20,
      peakTokPerSec: 20,
    });
  });

  it("uses a background wake as a turn start and leaves separately reported agent output out of its rate", () => {
    const report = { id: "agent:sample", model: "claude-opus-4-5", out: 500 };
    const items: Item[] = [
      { id: "u", seq: 1, role: "user", kind: "text", ts: "2026-11-20T10:00:00Z" },
      { id: "a", seq: 2, role: "assistant", kind: "text", ts: "2026-11-20T10:00:04Z", usage: [{ out: 40 }, report] },
      { id: "w", seq: 3, role: "system", kind: "notice", wake: true, ts: "2026-11-20T10:10:00Z", usage: [report] },
      { id: "b", seq: 4, role: "assistant", kind: "text", ts: "2026-11-20T10:10:02Z", usage: [{ out: 80 }] },
    ];
    const { tokens, performance } = buildSessionInsights(baseSession, items);
    expect(tokens?.output).toBe(620);
    expect(performance).toMatchObject({
      latestTurnDurationMs: 2000, latestTokPerSec: 40, avgTokPerSec: 20, peakTokPerSec: null,
    });
  });

  it.each([
    ["missing start", undefined, "2026-11-20T10:00:10Z", 100],
    ["missing end", "2026-11-20T10:00:00Z", undefined, 100],
    ["invalid end", "2026-11-20T10:00:00Z", "invalid", 100],
    ["zero timestamp", "0001-01-01T00:00:00Z", "2026-11-20T10:00:10Z", 100],
    ["zero duration", "2026-11-20T10:00:00Z", "2026-11-20T10:00:00Z", 100],
    ["zero output", "2026-11-20T10:00:00Z", "2026-11-20T10:00:10Z", 0],
  ] as const)("omits the turn rate with %s", (_case, start, end, out) => {
    const items: Item[] = [
      { id: "u", seq: 1, role: "user", kind: "text", ts: start },
      { id: "a", seq: 2, role: "assistant", kind: "text", ts: end, usage: [{ in: 500, out }] },
    ];
    const performance = summarizeSessionPerformance(items);
    expect(performance?.latestTokPerSec ?? null).toBeNull();
    expect(performance?.avgTokPerSec ?? null).toBeNull();
    expect(performance?.peakTokPerSec ?? null).toBeNull();
  });

  it("omits performance and breakdown when data is unavailable", () => {
    const insights = buildSessionInsights(baseSession, []);
    expect(insights.context).toBeNull();
    expect(insights.tokens).toBeNull();
    expect(insights.performance).toBeNull();
    expect(insights.breakdown).toBeUndefined();
    expect(summarizeSessionPerformance([], null)).toBeNull();
  });
});

describe("normalizeContextBreakdown", () => {
  it("validates source, strips negative values, derives headroom from contextWindow, and picks top consumer", () => {
    expect(normalizeContextBreakdown(undefined)).toBeNull();
    expect(normalizeContextBreakdown({ source: "bogus" as ContextBreakdown["source"], conversation: 1000 })).toBeNull();
    expect(normalizeContextBreakdown({ source: "estimated", conversation: -10 })).toBeNull();

    const est = normalizeContextBreakdown(
      {
        source: "estimated",
        system_and_rules: 10_000,
        conversation: 40_000,
        tool_outputs: 0,
        tools_and_mcp: -5,
        auto_compact_threshold: 160_000,
      },
      200_000,
    )!;
    expect(est.source).toBe("estimated");
    expect(est.segments.map((s) => [s.key, s.tokens])).toEqual([
      ["system_and_rules", 10_000],
      ["conversation", 40_000],
      ["tool_outputs", 0],
    ]);
    expect(est.occupiedTokens).toBe(50_000);
    expect(est.freeTokens).toBe(150_000);
    expect(est.freeShare).toBeCloseTo(75, 4);
    expect(est.autoCompactShare).toBeCloseTo(80, 4);
    expect(est.topConsumer).toEqual({ key: "conversation", tokens: 40_000, usedShare: 80 });
  });

  it("leaves topConsumer null when all observed categories are zero", () => {
    const zero = normalizeContextBreakdown({ source: "exact", conversation: 0, free: 100_000 }, 100_000)!;
    expect(zero.topConsumer).toBeNull();
    expect(zero.freeTokens).toBe(100_000);
  });

  it("keeps observed breakdown counts without treating their sum as the window", () => {
    const stats = normalizeContextBreakdown({ source: "exact", conversation: 5000, auto_compact_threshold: 32_000 }, 0)!;
    expect(stats.occupiedTokens).toBe(5000);
    expect(stats.totalTokens).toBe(0);
    expect(stats.freeShare).toBeNull();
    expect(stats.autoCompactShare).toBeNull();
  });
});

describe("formatLatencyMs & summarizeStepTelemetry", () => {
  it("formats sub-second latencies in ms and >= 1s in seconds", async () => {
    await initI18n("en");
    expect(formatLatencyMs(0)).toBe("0 ms");
    expect(formatLatencyMs(342)).toBe("342 ms");
    expect(formatLatencyMs(999)).toBe("999 ms");
    expect(formatLatencyMs(1000)).toBe("1 s");
    expect(formatLatencyMs(1450)).toBe("1.45 s");
    expect(formatLatencyMs(12_340)).toBe("12.3 s");
  });

  it("aggregates per-step tokens, duration, and TTFT and returns null when empty", () => {
    expect(summarizeStepTelemetry([], null)).toBeNull();
    expect(summarizeStepTelemetry([item({ id: "t1", seq: 1 })], 0)).toBeNull();

    const summary = summarizeStepTelemetry(
      [
        item({ id: "t1", seq: 1, ttft_ms: 210, usage: [{ model: "claude-opus-4-5", in: 800, out: 200, cache_read: 5000 }] }),
        item({ id: "r1", seq: 2, usage: [{ id: "sub-1", model: "claude-sonnet-4-5", in: 400, out: 100 }] }),
      ],
      1850,
    )!;
    expect(summary.tokens).toEqual({
      input: 1200,
      output: 300,
      cacheRead: 5000,
      cacheWrite: 0,
      total: 6500,
      hasCache: true,
    });
    expect(summary.durationMs).toBe(1850);
    expect(summary.ttftMs).toBe(210);
  });
});

describe("formatHitRate", () => {
  it("formats whole and decimal percentages and returns null for null/NaN", () => {
    expect(formatHitRate(null)).toBeNull();
    expect(formatHitRate(Number.NaN)).toBeNull();
    expect(formatHitRate(0)).toBe("0%");
    expect(formatHitRate(0.85)).toBe("85%");
    expect(formatHitRate(0.824)).toBe("82.4%");
    expect(formatHitRate(1.2)).toBe("100%");
  });
});

describe("compactActionSupport", () => {
  it("supports assistant chats only when hub exposes assistant_compact", () => {
    const asst: Session = { ...baseSession, assistant: true };
    expect(compactActionSupport(asst, { assistant: true, hubSupportsAssistantCompact: true })).toEqual({
      supported: true,
      mode: "assistant",
      running: false,
    });
    expect(compactActionSupport(
      { ...asst, compaction: { state: "running", started_at: "2026-11-20T10:00:00Z" } },
      { assistant: true, hubSupportsAssistantCompact: true },
    )).toEqual({
      supported: true,
      mode: "assistant",
      running: true,
    });
    expect(compactActionSupport(asst, { assistant: true, hubSupportsAssistantCompact: false })).toEqual({
      supported: false,
      mode: null,
      running: false,
    });
  });

  it("supports agent chats only for terminal-command agents with send capability and callback", () => {
    expect(compactActionSupport(baseSession, { hasSendCompact: true })).toEqual({
      supported: true,
      mode: "agent",
      running: false,
    });
    expect(compactActionSupport({ ...baseSession, source_kind: "antigravity" }, { hasSendCompact: true }).supported).toBe(false);
    expect(compactActionSupport({ ...baseSession, capabilities: { send: false } }, { hasSendCompact: true }).supported).toBe(false);
    expect(compactActionSupport(baseSession, { hasSendCompact: false }).supported).toBe(false);
    expect(compactActionSupport(baseSession, { hasSendCompact: true, blocked: true }).supported).toBe(false);
    expect(compactActionSupport(baseSession, { hasSendCompact: true, routed: true }).supported).toBe(false);
    expect(compactActionSupport({ ...baseSession, assistant: true }, { assistant: true, hubSupportsAssistantCompact: true, blocked: true }).supported).toBe(false);
    expect(compactActionSupport({ ...baseSession, assistant: true }, { assistant: true, hubSupportsAssistantCompact: true, routed: true }).supported).toBe(false);
  });
});
