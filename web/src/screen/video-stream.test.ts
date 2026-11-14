// One video stream in the browser (#6558), against the mock's real HEVC fixture: start out, hello / format / frames in,
// a report for every frame, the newest picture of a burst handed on, and the ways it ends, as the iPhone's stream.
import { afterEach, describe, expect, it, vi } from "vitest";
import { ScreenVideoStream, type StreamEvent } from "./video-stream";
import { fakeDecoders, fixture, framed, prefix, scripted, tick } from "./video-test-helpers";

const start = { type: "start" as const, max_size: 1920, fps: 60, max_kbps: 12_000, codecs: ["hevc", "h264"] };
afterEach(() => vi.useRealTimers());

describe("ScreenVideoStream", () => {
  it("sends start, configures the decoder from hello and format, reports every frame, shows the newest of a burst", async () => {
    const t = scripted(), d = fakeDecoders(), events: StreamEvent[] = [];
    const s = new ScreenVideoStream(t, start, (e) => events.push(e), { makeDecoder: d.make, nowMicros: () => 42 });
    expect(t.sent[0]).toEqual(start);
    t.feed(prefix(6)); // hello, format, four frames in one read
    await tick();
    expect(events[0]).toMatchObject({ type: "hello", hello: { codec: "hevc", width: 960, height: 600 } });
    expect(d.made[0].configured[0]).toMatchObject({ codec: "hvc1.1.6.L93.B0", hardwareAcceleration: "prefer-hardware", optimizeForLatency: true });
    const reports = t.sent.filter((m) => m.type === "report");
    expect(reports).toHaveLength(4);
    expect(reports[0]).toMatchObject({ last_frame: 1, arrival_micros: 42, flags: 0 });
    expect((reports[0] as { bytes_since_last: number }).bytes_since_last).toBe(prefix(6).length); // the burst's first frame takes the read
    expect((reports[1] as { bytes_since_last: number }).bytes_since_last).toBe(0);
    const pictures = events.filter((e) => e.type === "picture");
    expect(pictures).toHaveLength(1); // only the newest of the burst
    expect((pictures[0] as { picture: { timestamp: number } }).picture.timestamp).toBe(4);
    expect(d.made[0].pictures.slice(0, 3).every((p) => (p as unknown as { closed: boolean }).closed)).toBe(true); // the replaced ones closed
    expect(s.received).toBe(prefix(6).length);
    s.close();
    expect(t.closedHere()).toBe(true);
    expect(events.some((e) => e.type === "ended")).toBe(false); // closed from this side: nothing more said
  });

  it("decodes the whole fixture cut anywhere", async () => {
    const t = scripted(), d = fakeDecoders(), events: StreamEvent[] = [];
    new ScreenVideoStream(t, start, (e) => events.push(e), { makeDecoder: d.make });
    for (let i = 0; i < fixture.length; i += 777) t.feed(fixture.slice(i, i + 777));
    await tick();
    expect(d.made[0].pictures).toHaveLength(90);
    expect(t.sent.filter((m) => m.type === "report")).toHaveLength(90);
  });

  it("ends silent after 3 s without a frame or heartbeat, but not while capture is paused", () => {
    vi.useFakeTimers();
    let now = 0;
    const t = scripted(), events: StreamEvent[] = [];
    new ScreenVideoStream(t, start, (e) => events.push(e), { makeDecoder: fakeDecoders().make, now: () => now });
    t.feed(prefix(2));
    t.feed(framed({ type: "paused", reason: "locked" }));
    now = 10_000;
    vi.advanceTimersByTime(500);
    expect(events.filter((e) => e.type === "ended")).toEqual([]);
    expect(events.find((e) => e.type === "paused")).toEqual({ type: "paused", reason: "locked" });
    t.feed(framed({ type: "resumed" }));
    now = 13_100;
    vi.advanceTimersByTime(500);
    expect(events.at(-1)).toEqual({ type: "ended", why: "silent" });
    expect(t.closedHere()).toBe(true);
  });

  it("waits 15 s for the first message", () => {
    vi.useFakeTimers();
    let now = 0;
    const events: StreamEvent[] = [];
    new ScreenVideoStream(scripted(), start, (e) => events.push(e), { makeDecoder: fakeDecoders().make, now: () => now });
    now = 14_000;
    vi.advanceTimersByTime(500);
    expect(events).toEqual([]);
    now = 15_500;
    vi.advanceTimersByTime(500);
    expect(events).toEqual([{ type: "ended", why: "silent" }]);
  });

  it("ends undecodable on a frame before its format, a picture that fails, or a broken message", async () => {
    for (const [feed, why] of [
      [() => framed({ type: "frame", number: 1, capture_micros: 0, flags: 1, sample: Uint8Array.of(0, 0, 0, 1, 2) }), "a frame first"],
      [() => prefix(4), "the second frame fails"],
      [() => Uint8Array.of(0, 0, 0, 0, 0), "a zero length"],
    ] as const) {
      const t = scripted(), events: StreamEvent[] = [];
      new ScreenVideoStream(t, start, (e) => events.push(e), { makeDecoder: fakeDecoders({ failOn: 2 }).make });
      t.feed(feed());
      await tick();
      expect(events.at(-1), why).toEqual({ type: "ended", why: "undecodable" });
    }
  });

  it("ends closed when the transport closes, and passes timing and encoding on", () => {
    const t = scripted(), events: StreamEvent[] = [];
    new ScreenVideoStream(t, start, (e) => events.push(e), { makeDecoder: fakeDecoders().make });
    t.feed(framed({ type: "timing", capture_ms: 0, encode_ms: 5, network_ms: 7, kbps: 900, fps: 30 }));
    t.feed(framed({ type: "encoding", width: 1600, height: 1000, fps: 30, min_kbps: 800, max_kbps: 6000 }));
    t.end();
    expect(events.map((e) => e.type)).toEqual(["timing", "encoding", "ended"]);
    expect(events.at(-1)).toEqual({ type: "ended", why: "closed" });
  });

  it("sends updates while open, nothing after", () => {
    const t = scripted();
    const s = new ScreenVideoStream(t, start, () => {}, { makeDecoder: fakeDecoders().make });
    s.send({ paused: true });
    s.close();
    s.send({ paused: false });
    expect(t.sent.filter((m) => m.type === "update")).toEqual([{ type: "update", paused: true }]);
  });
});
