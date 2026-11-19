// The page's first voice call loads the call's code (call-stack.ts) when it starts, not with the first screen (#3461):
// the click still makes the audio, nothing talks to the extension before the code is in, and a start that is left, tapped
// twice or whose code would not load does what it did when the code came with the page.
import { readFileSync } from "node:fs";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { VoiceAudio, VoiceDeps, VoiceSocket } from "./conversation";

const reported = vi.hoisted(() => ({ list: [] as { boundary: string; chunk: boolean }[] }));
vi.mock("../error-reports", async (load) => ({
  ...(await load<typeof import("../error-reports")>()),
  reportBoundary: (_e: unknown, boundary: string, chunk: boolean) => void reported.list.push({ boundary, chunk }),
}));

/** AudioContexts made, in order: what a click made and what a call closed. */
const contexts: { rate?: number; closed: boolean; made: string[] }[] = [];
class FakeContext {
  closed = false;
  made: string[] = [];
  destination = {};
  constructor(o?: { sampleRate?: number }) {
    contexts.push(Object.assign(this, { rate: o?.sampleRate }));
  }
  resume = () => Promise.resolve();
  close() {
    this.closed = true;
    return Promise.resolve();
  }
  createConstantSource() {
    this.made.push("constant");
    return { offset: { value: 0 }, connect: () => {}, disconnect: () => {}, start: () => {}, stop: () => {} };
  }
}

const owner = { mid: "m", sid: "s", owner: "page", extensionID: "voice", recent: [] };

/** A call's socket and audio the test drives: what was sent, and the microphone's frames. */
function fakeDeps() {
  const sockets: (VoiceSocket & { sent: unknown[] })[] = [];
  let frame: ((f: Int16Array, rate: number) => void) | undefined;
  const audio: VoiceAudio = {
    start: async (o) => void (frame = o.onFrame),
    setInputRate: () => {}, setMuted: () => {}, play: () => {}, clear: () => {}, cue: () => {}, stop: () => {},
    levels: () => ({ input: 0, output: 0 }), cancelsEcho: true,
  };
  const deps: VoiceDeps = {
    socketURL: () => "ws://127.0.0.1/api/v1/extensions/voice/ws?token=t",
    openSocket: () => {
      const s = { binaryType: "", readyState: 1, onopen: null, onmessage: null, onclose: null, sent: [] as unknown[], send: (d: unknown) => void s.sent.push(d), close: () => {} };
      sockets.push(s);
      return s;
    },
    audio: () => audio,
    probe: async () => null,
    resolveModel: async () => ({ ok: true, model: "rt/m1" }),
    voice: () => undefined,
    session: () => undefined,
    now: () => 0,
    setTimer: () => 0,
    clearTimer: () => {},
  };
  return { deps, sockets, speak: () => frame?.(new Int16Array(320), 16000) };
}

const flush = () => new Promise((r) => setTimeout(r, 0));

// The first import transforms the call's whole module graph, which can take seconds on a busy machine: do it once,
// outside the tests' own time (resetModules keeps the transformed code).
beforeAll(() => import("./calls").then(() => import("./call-stack")), 30_000);
beforeEach(() => {
  vi.resetModules();
  contexts.length = 0;
  reported.list = [];
  vi.stubGlobal("AudioContext", FakeContext);
  // the microphone's permission is still being asked: the audio stops there
  vi.stubGlobal("navigator", { mediaDevices: { getUserMedia: () => new Promise(() => {}) } });
});
afterEach(() => {
  vi.doUnmock("./call-stack");
  vi.unstubAllGlobals();
});

describe("the page's first voice call", () => {
  it("opens its socket once the call's code is in, and loses nothing said before ready", async () => {
    const { voiceCalls } = await import("./calls");
    const { deps, sockets, speak } = fakeDeps();
    const started = voiceCalls.start({ ...owner, deps });
    expect(voiceCalls.current).toBeNull();
    expect(sockets).toHaveLength(0);
    const call = await started;
    expect(voiceCalls.current).toBe(call);
    await flush();
    expect(sockets).toHaveLength(1);
    speak();
    const socket = sockets[0];
    socket.onopen?.({});
    socket.onmessage?.({ data: JSON.stringify({ type: "ready", protocol: 2, input_rate: 16000, output_rate: 24000, target: { machine_id: "m", session_id: "s" } }) });
    socket.onmessage?.({ data: JSON.stringify({ type: "transcript", id: "u1", role: "user", text: "hello", final: true }) });
    expect(socket.sent.filter((d) => d instanceof ArrayBuffer)).toHaveLength(1);
    expect(call!.getSnapshot().captions.map((c) => c.text)).toEqual(["hello"]);
  });

  it("makes its audio contexts inside the click and hands them to the call", async () => {
    const { voiceCalls } = await import("./calls");
    const started = voiceCalls.start(owner);
    // before anything is awaited: the capture context, then the player at the default output rate
    expect(contexts.map((c) => c.rate)).toEqual([undefined, 24000]);
    const call = await started;
    expect(call).not.toBeNull();
    expect(contexts).toHaveLength(2);
    expect(contexts[0].made).toEqual(["constant"]);
    voiceCalls.end();
    expect(contexts.every((c) => c.closed)).toBe(true);
  });

  it("is one start when tapped twice while its code loads, and none once the page left", async () => {
    const { voiceCalls } = await import("./calls");
    const first = voiceCalls.start(owner);
    expect(voiceCalls.start(owner)).toBe(first);
    expect(contexts).toHaveLength(2);
    voiceCalls.leave(owner);
    expect(contexts.every((c) => c.closed)).toBe(true);
    expect(await first).toBeNull();
    expect(voiceCalls.current).toBeNull();
  });

  it("reports code that would not load, closes the click's audio, and the next tap tries again", async () => {
    vi.doMock("./call-stack", () => {
      throw new TypeError("Failed to fetch dynamically imported module");
    });
    const { voiceCalls } = await import("./calls");
    expect(await voiceCalls.start(owner)).toBeNull();
    expect(contexts.every((c) => c.closed)).toBe(true);
    expect(reported.list).toEqual([{ boundary: "voice_call", chunk: true }]);
    vi.doUnmock("./call-stack");
    expect(await voiceCalls.start(owner)).not.toBeNull();
  });

  it("is on only once the page's own parts for it (its bar) are in too", async () => {
    const { voiceCalls } = await import("./calls");
    let arrive!: () => void;
    const ui = vi.fn(() => new Promise<void>((r) => (arrive = r)));
    const started = voiceCalls.start({ ...owner, deps: fakeDeps().deps, ui });
    expect(ui).toHaveBeenCalledTimes(1);
    await import("./call-stack"); // the call's code is in; its bar's is not
    await flush();
    expect(voiceCalls.current).toBeNull();
    arrive();
    const call = await started;
    expect(call).not.toBeNull();
    expect(voiceCalls.current).toBe(call);
  });

  it("reports parts that would not load like the call's code, and the next tap tries again", async () => {
    const { voiceCalls } = await import("./calls");
    const failing = () => Promise.reject(new TypeError("Failed to fetch dynamically imported module"));
    expect(await voiceCalls.start({ ...owner, ui: failing })).toBeNull();
    expect(contexts.every((c) => c.closed)).toBe(true);
    expect(reported.list).toEqual([{ boundary: "voice_call", chunk: true }]);
    expect(await voiceCalls.start({ ...owner, ui: () => Promise.resolve() })).not.toBeNull();
  });
});

// The first screen's voice modules that name the call's types take them as types only (an `import type` is erased).
const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");
const staticImports = (source: string) => [...source.matchAll(/^import(?!\s+type\b)[^;]*from\s+["']([^"']+)["']/gm)].map((m) => m[1]);
const CALL_ONLY = /(?:^|\/)(?:call-stack|conversation|audio|uplink|resample|capture-pace|cues|frames)$/;

describe("the first screen's voice modules", () => {
  // (the bar, notes.ts and bar-line.ts are in VoiceCall.tsx's chunk, which a call's start loads with the call's code)
  it.each(["./calls.ts", "./support.ts", "./wire.ts", "./availability.ts", "../../views/chat/voice/VoiceParts.tsx"])("%s does not bring the call's code in", (file) => {
    expect(staticImports(read(file)).filter((s) => CALL_ONLY.test(s))).toEqual([]);
  });

  it("calls.ts loads it when a call starts", () => {
    expect(read("./calls.ts")).toContain('import("./call-stack")');
  });
});
