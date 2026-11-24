// @vitest-environment jsdom
// Unit tests for #13045: image-limit error recognition and safe "Compact and send again" recovery.
import { afterEach, describe, expect, it, vi } from "vitest";
import { IMAGE_LIMIT_CODE, imageLimitError, parseAPIError, retryable } from "./api-errors";
import {
  canResendFailedTurn,
  compactResendSupport,
  failedTurnToResend,
  resolveFailedTurnPayload,
  type OutgoingPayload,
} from "./compact-resend";
import {
  hasPostCommandBoundary,
  hasPostCommandError,
  snapshotTerminalCompaction,
  waitForAssistantCompaction,
  waitForTerminalCompaction,
  watchTerminalCompaction,
} from "./wait-for-compaction";
import { openPickerCommand } from "./composer-commands";
import { onModelPicker } from "./model-switch";
import { onPermissionPicker } from "./permissions-command";
import { dropSession, upsertSession } from "./store";
import { chatLines } from "./turns";
import type { Item, Session } from "./types";

const baseSession = (extra: Partial<Session> = {}): Session => ({
  machine_id: "box",
  session_id: "s1",
  title: "Test",
  cwd: "/code",
  status: "idle",
  ...extra,
});

afterEach(() => {
  dropSession("box", "s1");
  vi.useRealTimers();
});

describe("image-limit turn failure parsing (#13045)", () => {
  const samples = [
    "Request too large (max 32MB). Accumulated images and attachments in the conversation pushed the request over the limit.",
    "API Error: 413 request_too_large: request exceeds maximum size",
    "API Error: an image in the conversation could not be processed and was removed. Please try again.",
    "API Error: 400 At least one of the image dimensions exceed max allowed size for many-image requests: 2000 pixels",
  ];

  it("classifies all three image-limit failure patterns as images_too_large and non-retryable", () => {
    for (const raw of samples) {
      expect(imageLimitError(raw)).toBe(true);
      const info = parseAPIError(raw);
      expect(info, raw).not.toBeNull();
      expect(info!.code).toBe(IMAGE_LIMIT_CODE);
      expect(info!.transient).toBe(false);
      expect(retryable(info!)).toBe(false);
      expect(info!.raw).toBe(raw);
    }
  });

  it("does not misclassify ordinary prose mentioning request too large", () => {
    expect(parseAPIError("I fixed the Request too large error by resizing images.")).toBeNull();
    expect(parseAPIError("Done: all checks passed.")).toBeNull();
  });
});

describe("compactResendSupport", () => {
  it("supports Persona assistant conversations only when the hub announces assistant_compact", () => {
    const s = baseSession({ assistant: true });
    expect(compactResendSupport(s, { assistant: true, hubSupportsAssistantCompact: true })).toEqual({ supported: true, mode: "assistant" });
    expect(compactResendSupport(s, { assistant: true, hubSupportsAssistantCompact: false })).toEqual({ supported: false, mode: null });
    expect(compactResendSupport(s, { assistant: true, hubSupportsAssistantCompact: true, blocked: true })).toEqual({ supported: false, mode: null });
    expect(compactResendSupport(s, { assistant: true, hubSupportsAssistantCompact: true, offline: true })).toEqual({ supported: false, mode: null });
    expect(compactResendSupport(s, { assistant: true, hubSupportsAssistantCompact: true, routed: true })).toEqual({ supported: false, mode: null });
    expect(compactResendSupport(baseSession({ assistant: true, capabilities: { send: false } }), { assistant: true, hubSupportsAssistantCompact: true })).toEqual({ supported: false, mode: null });
  });

  it("supports live terminal Claude Code sessions and withholds unsupported agent sessions", () => {
    expect(compactResendSupport(baseSession({ status: "idle" }), {})).toEqual({ supported: true, mode: "claude-terminal" });
    expect(compactResendSupport(baseSession({ status: "done", agent: "claude" }), {})).toEqual({ supported: true, mode: "claude-terminal" });
    expect(compactResendSupport(baseSession({ status: "idle" }), { offline: true })).toEqual({ supported: false, mode: null });
    // Unsupported runtimes, agents, hosts, and states must not offer a broken action.
    expect(compactResendSupport(baseSession({ agent: "codex" }), {})).toEqual({ supported: false, mode: null });
    expect(compactResendSupport(baseSession({ agent: "agy" }), {})).toEqual({ supported: false, mode: null });
    expect(compactResendSupport(baseSession({ source_kind: "runtime" }), {})).toEqual({ supported: false, mode: null });
    expect(compactResendSupport(baseSession({ host: "botmux" }), {})).toEqual({ supported: false, mode: null });
    expect(compactResendSupport(baseSession({ status: "offline" }), {})).toEqual({ supported: false, mode: null });
    expect(compactResendSupport(baseSession({ status: "blocked", state: "needs_you" }), {})).toEqual({ supported: false, mode: null });
    expect(compactResendSupport(baseSession({ status: "idle" }), { composerDisabled: true })).toEqual({ supported: false, mode: null });
    expect(compactResendSupport(baseSession({ status: "idle" }), { working: true })).toEqual({ supported: false, mode: null });
    expect(compactResendSupport(baseSession({ status: "idle" }), { stopped: true })).toEqual({ supported: false, mode: null });
  });
});

describe("failedTurnToResend & resolveFailedTurnPayload", () => {
  const err = "Request too large (max 32MB). Accumulated images and attachments pushed the request over the limit.";
  const msg = (id: string, seq: number, role: "user" | "assistant", text: string, extra: Partial<Item> = {}): Item => ({
    id, seq, role, kind: "text", text, ts: "2026-11-22T12:00:00Z", ...extra,
  });

  it("returns the failed turn's user message, skipping folded bare retry-only turns", async () => {
    const lines = chatLines([
      msg("u1", 1, "user", "Compare these two screenshots"),
      msg("e1", 2, "assistant", err),
      msg("u2", 3, "user", "Continue"),
      msg("e2", 4, "assistant", err),
    ]);
    const tail = lines[lines.length - 1];
    expect(tail.kind).toBe("apiError");
    const candidate = failedTurnToResend(lines, tail.id);
    expect(canResendFailedTurn(candidate)).toBe(true);
    expect(await resolveFailedTurnPayload(candidate, "box", "s1")).toEqual({
      ok: true,
      payload: { text: "Compare these two screenshots", images: [] },
    });
  });

  it("preserves rich user content (contexts, files, attached_images, pasted_content) and inline/lazy images", async () => {
    const rawWithRich = [
      "<persona-context>\nrealtime hub snapshot\n</persona-context>",
      '<persona-context kind="edit" title="Spec" url="https://example.com/spec">quoted line</persona-context>',
      '<persona-file kind="file" path="/tmp/report.pdf" name="report.pdf"/>',
      "<attached_images>\n/tmp/photo1.png\n</attached_images>",
      "[Image #1] Check this diff",
    ].join("\n");
    const lines = chatLines([
      msg("u1#0", 1, "user", rawWithRich),
      { id: "u1#1", seq: 1, role: "user", kind: "image", image: { mime: "image/png", data: "QUJD" }, ts: "2026-11-22T12:00:00Z" } as Item,
      { id: "u1#2", seq: 1, role: "user", kind: "image", image: { mime: "image/jpeg", lazy: true }, ts: "2026-11-22T12:00:00Z" } as Item,
      msg("e1", 2, "assistant", err),
    ]);
    const tail = lines[lines.length - 1];
    const candidate = failedTurnToResend(lines, tail.id);
    expect(canResendFailedTurn(candidate)).toBe(true);
    const fetchImage = vi.fn().mockResolvedValue({ mime: "image/jpeg", data: "WFlZ" });
    const resolved = await resolveFailedTurnPayload(candidate, "box", "s1", { fetchImage });
    expect(fetchImage).toHaveBeenCalledTimes(1);
    expect(resolved.ok).toBe(true);
    if (resolved.ok) {
      expect(resolved.payload.images).toEqual([
        { mime: "image/png", data: "QUJD" },
        { mime: "image/jpeg", data: "WFlZ" },
      ]);
      expect(resolved.payload.text).toContain('<persona-context kind="edit" title="Spec" url="https://example.com/spec">quoted line</persona-context>');
      expect(resolved.payload.text).toContain('<persona-file kind="file" path="/tmp/report.pdf" name="report.pdf"/>');
      expect(resolved.payload.text).toContain("<attached_images>\n/tmp/photo1.png\n</attached_images>");
      expect(resolved.payload.text).toContain("Check this diff");
      expect(resolved.payload.text).not.toContain("realtime hub snapshot");
      expect(resolved.payload.text).not.toContain("[Image #1]");
    }
  });

  it("reuses captured outgoing payload verbatim (including images) without refetching", async () => {
    const captured = new Map<string, OutgoingPayload>([
      ["cid-1", { text: '<persona-file kind="video" path="/tmp/a.mov"/>Look here', images: [{ mime: "image/png", data: "U0hPVA==" }] }],
    ]);
    const lines = chatLines([
      msg("u1#0", 1, "user", "Look here", { client_msg_id: "cid-1" }),
      { id: "u1#1", seq: 1, role: "user", kind: "image", client_msg_id: "cid-1", image: { mime: "image/png", lazy: true }, ts: "2026-11-22T12:00:00Z" } as Item,
      msg("e1", 2, "assistant", err),
    ]);
    const tail = lines[lines.length - 1];
    const candidate = failedTurnToResend(lines, tail.id, { captured });
    expect(canResendFailedTurn(candidate)).toBe(true);
    const fetchImage = vi.fn();
    expect(await resolveFailedTurnPayload(candidate, "box", "s1", { fetchImage })).toEqual({
      ok: true,
      payload: { text: '<persona-file kind="video" path="/tmp/a.mov"/>Look here', images: [{ mime: "image/png", data: "U0hPVA==" }] },
    });
    expect(fetchImage).not.toHaveBeenCalled();
  });

  it("recovers image-only messages with inline or lazy bytes instead of degrading to Continue", async () => {
    const lines = chatLines([
      { id: "u1", seq: 1, role: "user", kind: "image", image: { mime: "image/png", data: "SU1H" }, ts: "2026-11-22T12:00:00Z" } as Item,
      msg("e1", 2, "assistant", "API Error: an image in the conversation could not be processed and was removed."),
    ]);
    const tail = lines[lines.length - 1];
    const candidate = failedTurnToResend(lines, tail.id);
    expect(canResendFailedTurn(candidate)).toBe(true);
    expect(await resolveFailedTurnPayload(candidate, "box", "s1")).toEqual({
      ok: true,
      payload: { text: "", images: [{ mime: "image/png", data: "SU1H" }] },
    });
  });

  it("withholds automatic resend when image bytes are statically unrecoverable or lazy fetch fails", async () => {
    // Missing both inline data and lazy flag: statically unrecoverable.
    const unrecoverableLines = chatLines([
      { id: "u1", seq: 1, role: "user", kind: "image", ts: "2026-11-22T12:00:00Z" } as Item,
      msg("e1", 2, "assistant", err),
    ]);
    const tail1 = unrecoverableLines[unrecoverableLines.length - 1];
    const cand1 = failedTurnToResend(unrecoverableLines, tail1.id);
    expect(canResendFailedTurn(cand1)).toBe(false);
    expect(await resolveFailedTurnPayload(cand1, "box", "s1")).toEqual({ ok: false, reason: "images_unavailable" });

    // Lazy image fetch returns null: fails cleanly rather than dropping the image.
    const lazyLines = chatLines([
      msg("u2#0", 1, "user", "Explain this screenshot"),
      { id: "u2#1", seq: 1, role: "user", kind: "image", image: { mime: "image/png", lazy: true }, ts: "2026-11-22T12:00:00Z" } as Item,
      msg("e2", 2, "assistant", err),
    ]);
    const tail2 = lazyLines[lazyLines.length - 1];
    const cand2 = failedTurnToResend(lazyLines, tail2.id);
    expect(canResendFailedTurn(cand2)).toBe(true);
    expect(await resolveFailedTurnPayload(cand2, "box", "s1", { fetchImage: async () => null })).toEqual({
      ok: false,
      reason: "images_unavailable",
    });
  });

  it("fetches full text when a user text item is truncated and aborts if full text is unavailable", async () => {
    const lines = chatLines([
      msg("u1", 1, "user", "short preview...", { full_size: 40_000 }),
      msg("e1", 2, "assistant", err),
    ]);
    const tail = lines[lines.length - 1];
    const candidate = failedTurnToResend(lines, tail.id);
    expect(canResendFailedTurn(candidate)).toBe(true);

    const ok = await resolveFailedTurnPayload(candidate, "box", "s1", {
      fetchFullItem: async () => msg("u1", 1, "user", "complete original prompt text"),
    });
    expect(ok).toEqual({ ok: true, payload: { text: "complete original prompt text", images: [] } });

    const fail = await resolveFailedTurnPayload(candidate, "box", "s1", {
      fetchFullItem: async () => { throw new Error("offline"); },
    });
    expect(fail).toEqual({ ok: false, reason: "text_unavailable" });
  });

  it("preserves user slash commands (including /clear and manual /compact) instead of walking into earlier history", async () => {
    for (const cmd of ["/clear", "/review", "/compact"]) {
      const lines = chatLines([
        msg("u0", 1, "user", "Older unrelated prompt"),
        msg("a0", 2, "assistant", "Done."),
        msg("u1", 3, "user", cmd, { client_msg_id: "user-cmd" }),
        msg("e1", 4, "assistant", err),
      ]);
      const tail = lines[lines.length - 1];
      const candidate = failedTurnToResend(lines, tail.id, { internalCompactIds: new Set(["other-internal"]) });
      expect(canResendFailedTurn(candidate)).toBe(true);
      expect(await resolveFailedTurnPayload(candidate, "box", "s1")).toEqual({
        ok: true,
        payload: { text: cmd, images: [] },
      });
    }
  });

  it("skips only internal recovery /compact commands tracked by client_msg_id", async () => {
    const lines = chatLines([
      msg("u1", 1, "user", "Compare these two screenshots"),
      msg("e1", 2, "assistant", err),
      msg("u2", 3, "user", "/compact", { client_msg_id: "internal-compact-1" }),
      msg("e2", 4, "assistant", err),
    ]);
    const tail = lines[lines.length - 1];
    const candidate = failedTurnToResend(lines, tail.id, { internalCompactIds: new Set(["internal-compact-1"]) });
    expect(await resolveFailedTurnPayload(candidate, "box", "s1")).toEqual({
      ok: true,
      payload: { text: "Compare these two screenshots", images: [] },
    });
  });

  it("does not walk past an injected message into an older unrelated user prompt", () => {
    const lines = chatLines([
      msg("u0", 1, "user", "Old user prompt"),
      msg("a0", 2, "assistant", "Done."),
      msg("u1", 3, "user", "Scheduled patrol", { injected: { kind: "followup", state: "daily", line: "Scheduled patrol" } }),
      msg("e1", 4, "assistant", err),
    ]);
    const tail = lines[lines.length - 1];
    expect(failedTurnToResend(lines, tail.id)).toBeNull();
    expect(canResendFailedTurn(failedTurnToResend(lines, tail.id))).toBe(false);
  });
});

describe("waitForAssistantCompaction", () => {
  it("resolves done after compaction transitions running -> done", async () => {
    const prev = { state: "done" as const, started_at: "2026-11-22T11:00:00Z", finished_at: "2026-11-22T11:00:10Z" };
    upsertSession(baseSession({ assistant: true, compaction: prev }));
    const ctl = new AbortController();
    const pending = waitForAssistantCompaction("box", "s1", prev, ctl.signal, 5_000);
    upsertSession(baseSession({ assistant: true, compaction: { state: "running", started_at: "2026-11-22T12:00:00Z" } }));
    upsertSession(baseSession({ assistant: true, compaction: { state: "done", started_at: "2026-11-22T12:00:00Z", finished_at: "2026-11-22T12:00:08Z" } }));
    expect(await pending).toBe("done");
  });

  it("resolves failed when compaction fails", async () => {
    upsertSession(baseSession({ assistant: true }));
    const ctl = new AbortController();
    const pending = waitForAssistantCompaction("box", "s1", undefined, ctl.signal, 5_000);
    upsertSession(baseSession({ assistant: true, compaction: { state: "running", started_at: "2026-11-22T12:00:00Z" } }));
    upsertSession(baseSession({ assistant: true, compaction: { state: "failed", started_at: "2026-11-22T12:00:00Z", finished_at: "2026-11-22T12:00:04Z", error: "too short" } }));
    expect(await pending).toBe("failed");
  });

  it("resolves cancelled when aborted", async () => {
    upsertSession(baseSession({ assistant: true }));
    const ctl = new AbortController();
    const pending = waitForAssistantCompaction("box", "s1", undefined, ctl.signal, 5_000);
    ctl.abort();
    expect(await pending).toBe("cancelled");
  });
});

describe("waitForTerminalCompaction / watchTerminalCompaction", () => {
  it("resolves done only after working -> post-command boundary (seq > afterSeq) -> idle", async () => {
    let curItems: Item[] = [{ id: "e1", seq: 10, role: "assistant", kind: "text", text: "Request too large (max 32MB)." }];
    const listeners = new Set<() => void>();
    const notify = () => listeners.forEach((fn) => fn());
    upsertSession(baseSession({ status: "idle", state: "idle" }));

    const ctl = new AbortController();
    const snapshot = snapshotTerminalCompaction(curItems, "cmd-1");
    const watcher = watchTerminalCompaction(
      "box",
      "s1",
      snapshot,
      () => curItems,
      (fn) => {
        listeners.add(fn);
        return () => listeners.delete(fn);
      },
      ctl.signal,
      5_000,
      50,
    );

    // Even if working arrives before markDelivered (in-flight HTTP), the watcher records it cleanly.
    upsertSession(baseSession({ status: "working", state: "working" }));
    watcher.markDelivered();
    curItems = [
      ...curItems,
      { id: "c1", seq: 11, role: "assistant", kind: "notice", compact: { trigger: "manual" }, text: "上下文已压缩" },
    ];
    expect(hasPostCommandBoundary(curItems, snapshot)).toBe(true);
    notify();
    upsertSession(baseSession({ status: "idle", state: "idle" }));
    expect(await watcher.promise).toBe("done");
  });

  it("does NOT resolve done when older compaction boundary is exposed by paging or without sawWorking", async () => {
    let curItems: Item[] = [{ id: "e1", seq: 10, role: "assistant", kind: "text", text: "Request too large (max 32MB)." }];
    const listeners = new Set<() => void>();
    const notify = () => listeners.forEach((fn) => fn());
    upsertSession(baseSession({ status: "idle", state: "idle" }));

    const ctl = new AbortController();
    const snapshot = snapshotTerminalCompaction(curItems, "cmd-1");
    let outcome: string | undefined;
    const pending = waitForTerminalCompaction(
      "box",
      "s1",
      snapshot,
      () => curItems,
      (fn) => {
        listeners.add(fn);
        return () => listeners.delete(fn);
      },
      ctl.signal,
      5_000,
      30,
    ).then((v) => {
      outcome = v;
      return v;
    });

    // Older paged boundary (seq <= afterSeq): ignored.
    curItems = [
      { id: "old-c0", seq: 4, role: "assistant", kind: "notice", compact: { trigger: "auto" }, text: "上下文已压缩" },
      ...curItems,
    ];
    expect(hasPostCommandBoundary(curItems, snapshot)).toBe(false);
    notify();
    await new Promise((r) => setTimeout(r, 50));
    expect(outcome).toBeUndefined();

    // Even a new-seq boundary while still idle without ever transitioning through working must NOT resolve done.
    curItems = [
      ...curItems,
      { id: "c1", seq: 11, role: "assistant", kind: "notice", compact: { trigger: "manual" }, text: "上下文已压缩" },
    ];
    notify();
    await new Promise((r) => setTimeout(r, 50));
    expect(outcome).toBeUndefined();

    ctl.abort();
    expect(await pending).toBe("cancelled");
  });

  it("resolves failed if an API error appears after /compact, even if a boundary also appeared", async () => {
    let curItems: Item[] = [{ id: "e1", seq: 10, role: "assistant", kind: "text", text: "Request too large (max 32MB)." }];
    const listeners = new Set<() => void>();
    const notify = () => listeners.forEach((fn) => fn());
    upsertSession(baseSession({ status: "working", state: "working" }));

    const ctl = new AbortController();
    const snapshot = snapshotTerminalCompaction(curItems, "cmd-1");
    const pending = waitForTerminalCompaction(
      "box",
      "s1",
      snapshot,
      () => curItems,
      (fn) => {
        listeners.add(fn);
        return () => listeners.delete(fn);
      },
      ctl.signal,
      5_000,
      50,
    );

    // Boundary + post-command API error: must fail rather than declaring done.
    curItems = [
      ...curItems,
      { id: "c1", seq: 11, role: "assistant", kind: "notice", compact: { trigger: "manual" }, text: "上下文已压缩" },
      { id: "e2", seq: 12, role: "assistant", kind: "text", text: "API Error: 500 internal error" },
    ];
    expect(hasPostCommandError(curItems, snapshot)).toBe(true);
    notify();
    expect(await pending).toBe("failed");
  });

  it("resolves failed if the terminal returns to idle after working without a post-command boundary", async () => {
    const curItems: Item[] = [{ id: "e1", seq: 10, role: "assistant", kind: "text", text: "Request too large (max 32MB)." }];
    upsertSession(baseSession({ status: "working", state: "working" }));
    const ctl = new AbortController();
    const pending = waitForTerminalCompaction(
      "box",
      "s1",
      snapshotTerminalCompaction(curItems, "cmd-1"),
      () => curItems,
      () => () => {},
      ctl.signal,
      5_000,
      20,
    );
    upsertSession(baseSession({ status: "idle", state: "idle" }));
    expect(await pending).toBe("failed");
  });
});

describe("openPickerCommand", () => {
  it("opens registered model or permission picker for bare commands without images", () => {
    const modelSpy = vi.fn();
    const permSpy = vi.fn();
    const unsubModel = onModelPicker("box/s1", modelSpy);
    const unsubPerm = onPermissionPicker("box/s1", permSpy);
    try {
      expect(openPickerCommand("box/s1", "/model", 0)).toBe(true);
      expect(modelSpy).toHaveBeenCalledTimes(1);
      expect(openPickerCommand("box/s1", "/permissions", 0)).toBe(true);
      expect(permSpy).toHaveBeenCalledTimes(1);
      // Messages with images or other commands are not intercepted.
      expect(openPickerCommand("box/s1", "/model", 1)).toBe(false);
      expect(openPickerCommand("box/s1", "/permissions", 1)).toBe(false);
      expect(openPickerCommand("box/s1", "/clear", 0)).toBe(false);
      expect(openPickerCommand("box/other", "/model", 0)).toBe(false);
    } finally {
      unsubModel();
      unsubPerm();
    }
  });
});
