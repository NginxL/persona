// The video channel of one Mac's live screen in the browser (#6558), as the iPhone's ScreenVideoLink: when it runs,
// what it shows, and when the page falls back to VNC. RFB signs in first and shows until the first video picture
// decodes; then the picture is video and the page stops asking RFB for updates (input and the fallback stay on RFB).
// The stream ending (closed, silent for 3 s, a picture that doesn't decode) or the Mac locking (`paused`) puts RFB back
// at once and says why; a stream that ended is tried again after 30 s, doubling to 5 min, back to 30 s once video has
// stayed up a minute. A held page (in the background) stops the stream with nothing said, and opens it again when shown.
import type { VideoCodec } from "./video-probe";
import { ScreenVideoStream, type DecoderMaker, type Ending, type Picture, type VideoTransport } from "./video-stream";
import type { Hello, Timing } from "./video-wire";

/** Why VNC shows while video was chosen, in words the page turns into a line (screen-video). */
export type Fallback = "lost" | "locked" | "notAllowed";

/** The helper's `paused` reason as a fallback. */
export function pausedFallback(reason: string): Fallback {
  return reason === "locked" || reason === "login_window" ? "locked" : reason === "permission" ? "notAllowed" : "lost";
}

/** What the page wants shown as video: one display (the helper's id; null the main one) at `rect` on the framebuffer
 *  (pixels; null all of it). */
export type Want = { display: number | null; rect: [number, number, number, number] | null };
/** The signed-in RFB connection video binds to. */
export type Ready = { pair: string };
/** The video's reading for the overlay, about once a second while it shows. */
export type Reading = { fps?: number; mbps?: number; captureMs?: number; encodeMs?: number; networkMs?: number; decodeMs?: number; totalMs?: number };

export const FIRST_WAIT_MS = 30_000;
export const LONGEST_WAIT_MS = 300_000;
export const STABLE_MS = 60_000;

export type LinkOptions = {
  /** Opens the stream's transport for a signed-in RFB connection. */
  open: (ready: Ready) => VideoTransport | null;
  codecs: VideoCodec[];
  maxSize: number;
  maxKbps: number;
  fps: number;
  makeDecoder?: DecoderMaker;
  now?: () => number;
  firstWaitMs?: number;
  longestWaitMs?: number;
  stableMs?: number;
};

export class ScreenVideoLink {
  showing = false;
  fallback: Fallback | null = null;
  rect: [number, number, number, number] | null = null;
  codec: string | null = null;
  size: { width: number; height: number } | null = null;
  /** The helper said it has quality tiers (`hello.features`). */
  tiers = false;
  reading: Reading = {};
  /** When video is tried again after it stopped (ms on `now`), for tests. */
  retryAt: number | null = null;
  /** The decoder failed on the real stream before any picture showed: this browser can't play it, and video isn't
   *  tried again on this page (the stats panel says so; the line says video stopped). */
  decoderFailed = false;
  private everShown = false;

  onShowing: (on: boolean) => void = () => {};
  onFallback: (f: Fallback | null) => void = () => {};
  onPicture: (p: Picture) => void = () => {};
  onReading: (r: Reading) => void = () => {};
  onChange: () => void = () => {};

  private wanted: Want | null = null;
  private ready: Ready | null = null;
  private held = new Set<string>();
  private stream: ScreenVideoStream | null = null;
  private opened: { want: Want; ready: Ready } | null = null;
  private wait: number;
  private shownSince: number | null = null;
  private retry: ReturnType<typeof setTimeout> | null = null;
  private tally = new Tally();

  constructor(private opts: LinkOptions) {
    this.wait = opts.firstWaitMs ?? FIRST_WAIT_MS;
  }

  private now() {
    return this.opts.now?.() ?? performance.now();
  }

  /** What the page wants shown as video; null: VNC (chosen, or several displays shown at once). */
  want(w: Want | null): void {
    if (sameWant(w, this.wanted)) return;
    this.wanted = w;
    if (!w) {
      this.say(null);
      this.stopRetry();
      this.wait = this.opts.firstWaitMs ?? FIRST_WAIT_MS;
    }
    this.reconsider();
  }

  /** The signed-in RFB connection (its pair), or null: a new connection tries video at once. */
  rfb(r: Ready | null): void {
    if (r?.pair === this.ready?.pair) return;
    this.ready = r;
    this.stopRetry();
    this.reconsider();
  }

  /** Video stops while any reason holds it (the page in the background) and comes back with nothing said. */
  hold(reason: string, on: boolean): void {
    const was = this.held.size === 0;
    if (on) this.held.add(reason);
    else this.held.delete(reason);
    if (was !== (this.held.size === 0)) this.reconsider(); // the line said stays until a picture shows
  }

  /** A new cap (the network or the data choice changed, ./data-use): the open stream gets an `update`, as the
   *  iPhone's does; a new one starts with it. */
  setBudget(b: { maxKbps: number; fps: number }): void {
    if (b.maxKbps === this.opts.maxKbps && b.fps === this.opts.fps) return;
    this.opts = { ...this.opts, maxKbps: b.maxKbps, fps: b.fps };
    this.stream?.send({ fps: b.fps, max_kbps: b.maxKbps });
  }

  /** Ends everything (the page went away). */
  close(): void {
    this.stopRetry();
    this.stop();
  }

  private reconsider() {
    const should = this.wanted && this.ready && this.held.size === 0 && this.retry === null && !this.decoderFailed ? { want: this.wanted, ready: this.ready } : null;
    if (this.opened && should && sameWant(this.opened.want, should.want) && this.opened.ready.pair === should.ready.pair) return;
    if (this.stream) this.stop();
    if (should) this.open(should);
  }

  private open(target: { want: Want; ready: Ready }) {
    const t = this.opts.open(target.ready);
    if (!t) return this.ended("closed");
    this.opened = target;
    this.rect = target.want.rect;
    this.tally = new Tally();
    this.stream = new ScreenVideoStream(
      t,
      { type: "start", ...(target.want.display === null ? {} : { display: target.want.display }), max_size: this.opts.maxSize, fps: this.opts.fps, max_kbps: this.opts.maxKbps, codecs: this.opts.codecs },
      (e) => {
        switch (e.type) {
          case "picture":
            return this.picture(e.picture, e.decodeMs);
          case "hello":
            return this.hello(e.hello);
          case "paused":
            return this.paused(e.reason);
          case "timing":
            return this.helper(e.timing);
          case "ended":
            return this.ended(e.why);
          default:
            return; // resumed: the next picture shows video again; encoding: for the tiers (PR 4)
        }
      },
      { makeDecoder: this.opts.makeDecoder, now: this.opts.now },
    );
  }

  /** Closes the stream from this side, with no fallback said. */
  private stop() {
    this.stream?.close();
    this.stream = null;
    this.opened = null;
    this.show(false);
  }

  private hello(h: Hello) {
    this.codec = h.codec === "h264" ? "H.264" : "HEVC";
    this.size = { width: h.width, height: h.height };
    if (h.features?.includes("tiers")) this.tiers = true;
    if (h.rect && h.rect.length === 4) this.rect = [h.rect[0], h.rect[1], h.rect[2], h.rect[3]];
    this.onChange();
  }

  private picture(p: Picture, decodeMs: number) {
    if (!this.stream) return p.close();
    this.tally.picture(decodeMs, this.now());
    this.publish();
    if (!this.showing) {
      this.say(null); // video is back: the line goes
      this.show(true);
    }
    this.onPicture(p);
  }

  private helper(t: Timing) {
    if (!this.stream) return;
    this.tally.helper(t);
    this.publish();
  }

  private publish() {
    const r = this.stream && this.tally.reading(this.now(), this.stream.received);
    if (!r) return;
    this.reading = r;
    this.onReading(r);
  }

  private paused(reason: string) {
    if (!this.stream) return;
    this.show(false);
    this.say(pausedFallback(reason));
  }

  private ended(why: Ending) {
    this.stream = null;
    this.opened = null;
    this.show(false);
    if (!this.wanted || !this.ready) return;
    this.say("lost");
    if (why === "undecodable" && !this.everShown) {
      this.decoderFailed = true; // no picture ever decoded here: not tried again on this page
      this.onChange();
      return;
    }
    const wait = this.wait;
    this.wait = Math.min(this.wait * 2, this.opts.longestWaitMs ?? LONGEST_WAIT_MS);
    this.retryAt = this.now() + wait;
    this.retry = setTimeout(() => {
      this.retry = null;
      this.retryAt = null;
      this.reconsider();
    }, wait);
  }

  private stopRetry() {
    if (this.retry) clearTimeout(this.retry);
    this.retry = null;
    this.retryAt = null;
  }

  private say(f: Fallback | null) {
    if (f === this.fallback) return;
    this.fallback = f;
    this.onFallback(f);
    this.onChange();
  }

  private show(on: boolean) {
    if (this.showing === on) return;
    this.showing = on;
    if (on) {
      this.shownSince = this.now();
      this.everShown = true;
    }
    else {
      // Video that stayed up a while starts the waits over; one that dropped soon after coming back keeps doubling.
      if (this.shownSince !== null && this.now() - this.shownSince >= (this.opts.stableMs ?? STABLE_MS)) this.wait = this.opts.firstWaitMs ?? FIRST_WAIT_MS;
      this.shownSince = null;
      this.reading = {};
    }
    this.onShowing(on);
    this.onChange();
  }
}

const sameWant = (a: Want | null, b: Want | null) =>
  a === b || (!!a && !!b && a.display === b.display && JSON.stringify(a.rect) === JSON.stringify(b.rect));

/** The overlay's reading: pictures shown and bytes that arrived over each second, the helper's segments from `timing`,
 *  and the page's decode time (median), as the iPhone's ScreenVideoTally. */
class Tally {
  private frames = 0;
  private decode: number[] = [];
  private since: number | null = null;
  private bytesAt = 0;
  private timing: Timing | null = null;

  picture(decodeMs: number, now: number) {
    if (this.since === null) this.since = now;
    this.frames += 1;
    this.decode.push(decodeMs);
  }

  helper(t: Timing) {
    this.timing = t;
  }

  /** The last second's reading once a second has passed since the last one (or since the first picture). */
  reading(now: number, received: number): Reading | null {
    if (this.since === null || now - this.since < 1000) return null;
    const seconds = (now - this.since) / 1000;
    const r: Reading = {
      fps: Math.round(this.frames / seconds),
      mbps: Math.round(((Math.max(0, received - this.bytesAt) * 8) / 1e6 / seconds) * 10) / 10,
      decodeMs: median(this.decode),
    };
    const t = this.timing;
    if (t?.capture_ms !== undefined) r.captureMs = t.capture_ms;
    if (t?.encode_ms !== undefined) r.encodeMs = t.encode_ms;
    if (t?.network_ms !== undefined) r.networkMs = t.network_ms;
    if (r.decodeMs === undefined) delete r.decodeMs;
    if (r.encodeMs !== undefined && r.networkMs !== undefined && r.decodeMs !== undefined) r.totalMs = (r.captureMs ?? 0) + r.encodeMs + r.networkMs + r.decodeMs;
    this.frames = 0;
    this.decode = [];
    this.since = now;
    this.bytesAt = received;
    return r;
  }
}

function median(values: number[]): number | undefined {
  if (!values.length) return undefined;
  const s = [...values].sort((a, b) => a - b), mid = s.length >> 1;
  const m = s.length % 2 ? s[mid] : (s[mid - 1] + s[mid]) / 2;
  return Math.round(m * 10) / 10;
}
