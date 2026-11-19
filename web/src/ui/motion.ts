// How things on the page move when their values change (#2757, #3434): a number that changed counts to its new value,
// a chart's marks go to their new place, a new row fades in, a bar's fill moves. Nothing moves the first time something
// is drawn, and nothing at all with reduced motion; at rest what is shown is exactly the value. Every duration here is
// the one place it is set (the CSS that goes with one names it).
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { EASE } from "./atoms";
import { prefersReducedMotion } from "./stream/motion";

/** How long a number takes to count to its new value, and a chart's marks to move. */
export const TWEEN_MS = 600;
/** How long a row takes to come in or go (and a text that changed to fade in). */
export const ENTER_MS = 240;
/** A fill, a highlight or a color going to its new place or value (a workflow's progress bar, its current phase, a
 *  status chip turning green). */
export const MOVE_MS = 300;
/** The running bar's token count easing up to the new one (cindy's useAnimatedNumber). */
export const COUNT_MS = 400;
/** The running bar after its turn: how long it stays as it was, then how long it fades out. */
export const LINGER_MS = 1000;
export const FADE_OUT_MS = 400;
/** The bar under a turn's last reply (#5314, cindy's MessageActionBar): how long it stays after the pointer leaves (a
 *  quick way back in keeps it steady), then how long it takes to fade out. */
export const REPLY_BAR_LINGER_MS = 250;
export const REPLY_BAR_FADE_MS = 250;

/** `cubic-bezier(x1, y1, x2, y2)` as a function of the progress (0…1), so JS moves with the same curve as CSS. */
export function bezier(css: string): (p: number) => number {
  const m = /cubic-bezier\(([^)]*)\)/.exec(css);
  const [x1, y1, x2, y2] = m ? m[1].split(",").map(Number) : [0, 0, 1, 1];
  const cx = 3 * x1, bx = 3 * (x2 - x1) - cx, ax = 1 - cx - bx;
  const cy = 3 * y1, by = 3 * (y2 - y1) - cy, ay = 1 - cy - by;
  const x = (t: number) => ((ax * t + bx) * t + cx) * t;
  const dx = (t: number) => (3 * ax * t + 2 * bx) * t + cx;
  return (p) => {
    if (p <= 0) return 0;
    if (p >= 1) return 1;
    let t = p;
    for (let i = 0; i < 8; i++) {
      const e = x(t) - p;
      if (Math.abs(e) < 1e-6) break;
      const d = dx(t);
      if (Math.abs(d) < 1e-6) break;
      t -= e / d;
    }
    if (!(t >= 0 && t <= 1) || Math.abs(x(t) - p) > 1e-4) {
      // Newton wandered off: bisect
      let lo = 0, hi = 1;
      t = p;
      for (let i = 0; i < 30 && Math.abs(x(t) - p) > 1e-6; i++) {
        if (x(t) < p) lo = t;
        else hi = t;
        t = (lo + hi) / 2;
      }
    }
    return ((ay * t + by) * t + cy) * t;
  };
}

export const ease = bezier(EASE);

/** Whether the system asks for reduced motion now. */
export const reducedMotion = () => prefersReducedMotion();

const same = (a: number[], b: number[]) => a.length === b.length && a.every((v, i) => v === b[i]);

/** The running bar's curve: fast, then settling (1 − (1 − p)³). */
export const easeOutCubic = (p: number) => 1 - (1 - p) ** 3;

export interface TweenOptions {
  /** How long a move takes (TWEEN_MS). */
  ms?: number;
  /** The curve, progress (0…1) to how far along (the design system's EASE). */
  curve?: (p: number) => number;
  /** Jump instead of moving from what is shown to the new values (the running bar's count going down: a new turn). */
  jump?: (from: number[], to: number[]) => boolean;
}

/**
 * Numbers on their way to `to`: null at rest (draw `to` itself), otherwise the values for this frame (as many as `to`
 * has). They move only when `to` changes with the same `shape` (the same kind of thing with new values); a new shape,
 * the first draw and reduced motion jump. A change while moving goes on from where it is.
 */
export function useTween(to: number[], shape: string, { ms = TWEEN_MS, curve = ease, jump }: TweenOptions = {}): number[] | null {
  // the frame is for one `id` (shape and target): on the render where `to` has just changed it is the old one's frame
  // (possibly another length), so it is not returned until the effect below has started the new move
  const [now, setNow] = useState<{ id: string; v: number[] } | null>(null);
  const last = useRef({ to, shape });
  const shown = useRef(to); // what is on the screen
  const id = `${shape}\n${to.join(",")}`;
  useLayoutEffect(() => {
    const prev = last.current;
    last.current = { to, shape };
    const from = shown.current.length === to.length ? shown.current : prev.to;
    if (prev.shape !== shape || same(prev.to, to) || reducedMotion() || typeof requestAnimationFrame !== "function" || jump?.(from, to)) {
      shown.current = to;
      setNow(null);
      return;
    }
    let start: number | undefined;
    let frame = 0;
    const step = (t: number) => {
      start ??= t;
      const p = Math.min(1, (t - start) / ms);
      if (p >= 1) {
        shown.current = to;
        setNow(null);
        return;
      }
      const e = curve(p);
      const v = from.map((f, i) => f + (to[i] - f) * e);
      shown.current = v;
      setNow({ id, v });
      frame = requestAnimationFrame(step);
    };
    // set before the browser paints: the first frame of the change still shows the old values
    shown.current = from;
    setNow({ id, v: from });
    frame = requestAnimationFrame(step);
    return () => cancelAnimationFrame(frame);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);
  return now && now.id === id && now.v.length === to.length ? now.v : null;
}

const goesDown = (from: number[], to: number[]) => to[0] < from[0];

/** One number easing up to `target` over `ms` (the running bar's tokens, cindy's useAnimatedNumber): a drop (a new
 *  turn) snaps, and so does everything with reduced motion. */
export function useCountUp(target: number, ms = COUNT_MS): number {
  return useTween([target], "count", { ms, curve: easeOutCubic, jump: goesDown })?.[0] ?? target;
}

/**
 * One breath (`.status-breath` in ui/stream/stream.css, 1.5 s) each time `value` moves while `on`: the next cycle replays the
 * one-shot animation. A change during a breath queues one more, so steady output breathes continuously and silence (a
 * long think, a tool) stays still.
 */
export function useBreath(value: number | undefined, on: boolean): { cycle: number; ended: () => void } {
  const [cycle, setCycle] = useState(0);
  const playing = useRef(false);
  const pending = useRef(false);
  const seen = useRef(value);
  useEffect(() => {
    if (!on) {
      playing.current = pending.current = false;
      seen.current = value;
      return;
    }
    if (value === seen.current) return;
    seen.current = value;
    if (value === undefined || reducedMotion()) return; // reduced: the animation never ends
    if (playing.current) {
      pending.current = true;
      return;
    }
    playing.current = true;
    setCycle((n) => n + 1);
  }, [value, on]);
  const ended = () => {
    playing.current = false;
    if (!pending.current) return;
    pending.current = false;
    playing.current = true;
    setCycle((n) => n + 1);
  };
  return { cycle, ended };
}

// A number in a value: 1,234.5 or 12 or 0.75 (a sign, a currency, a unit and the rest stay as they are)
const NUMBER = /\d{1,3}(?:,\d{3})+(?:\.\d+)?|\d+(?:\.\d+)?/g;

export interface NumberParts {
  /** The text with each number as "#": two values with the same shape count from one to the other. */
  shape: string;
  /** The text around the numbers (one more than the numbers). */
  text: string[];
  nums: number[];
  /** How each number is written: its decimals, grouped by thousands, the digits before the point when it starts with 0. */
  format: { decimals: number; grouped: boolean; pad: number }[];
}

/** The numbers in a value and the text around them. */
export function numberParts(value: string): NumberParts {
  const text: string[] = [];
  const nums: number[] = [];
  const format: NumberParts["format"] = [];
  let at = 0;
  for (const m of value.matchAll(NUMBER)) {
    const s = m[0];
    text.push(value.slice(at, m.index));
    at = m.index + s.length;
    const plain = s.replace(/,/g, "");
    const [int, frac = ""] = plain.split(".");
    nums.push(Number(plain));
    format.push({ decimals: frac.length, grouped: s.includes(","), pad: /^0\d/.test(int) ? int.length : 0 });
  }
  text.push(value.slice(at));
  return { shape: text.join("#"), text, nums, format };
}

/**
 * The value with its numbers at `nums` (a frame of the count), each written with the decimals, grouping and padding of
 * the number at its place in `parts` (the value being counted to), so a frame of "1.5" → "2.25" reads "1.83".
 */
export function formatParts(parts: NumberParts, nums: number[]): string {
  let out = parts.text[0];
  nums.forEach((n, i) => {
    const f = parts.format[i];
    let s = Math.abs(n).toFixed(f.decimals);
    if (f.pad) s = s.padStart(f.pad + (f.decimals ? f.decimals + 1 : 0), "0");
    if (f.grouped) {
      const [int, frac] = s.split(".");
      s = int.replace(/\B(?=(\d{3})+(?!\d))/g, ",") + (frac !== undefined ? `.${frac}` : "");
    }
    out += s + parts.text[i + 1];
  });
  return out;
}

/**
 * A number that changes each time `id` changes after the first draw (and back to 0 once `ms` have passed): the key of
 * something that fades in for a while. 0 at rest, on the first draw and with reduced motion.
 */
export function useFlash(id: string, ms = ENTER_MS): number {
  const [n, setN] = useState(0);
  const last = useRef(id);
  useLayoutEffect(() => {
    if (last.current === id) return;
    last.current = id;
    if (reducedMotion()) {
      setN(0);
      return;
    }
    setN((k) => k + 1);
    const t = setTimeout(() => setN(0), ms + 40);
    return () => clearTimeout(t);
  }, [id, ms]);
  return n;
}

/**
 * The keys that joined `keys` since the last draw, each for `ms` (the rows that fade in): none on the first draw, none
 * with reduced motion. Keys that leave are simply gone (nothing slides or is kept in their place).
 */
export function useArrivals(keys: string[], ms = ENTER_MS): ReadonlySet<string> {
  const [fresh, setFresh] = useState<ReadonlySet<string>>(NONE);
  const prev = useRef<Set<string> | null>(null);
  const timers = useRef(new Set<ReturnType<typeof setTimeout>>());
  const joined = keys.join("\n");
  useLayoutEffect(() => {
    const before = prev.current;
    const now = new Set(keys);
    prev.current = now;
    if (!before || reducedMotion()) return;
    const came = keys.filter((k) => !before.has(k));
    if (!came.length) return;
    setFresh((f) => new Set([...[...f].filter((k) => now.has(k)), ...came]));
    const t = setTimeout(() => {
      timers.current.delete(t);
      setFresh((f) => {
        const next = new Set(f);
        came.forEach((k) => next.delete(k));
        return next.size ? next : NONE;
      });
    }, ms + 40);
    timers.current.add(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [joined]);
  useEffect(() => {
    const ts = timers.current;
    return () => ts.forEach(clearTimeout);
  }, []);
  return fresh;
}

const NONE: ReadonlySet<string> = new Set();

/** A thing that just came in or changed: it fades in (opacity only, nothing moves). */
export const fadeInStyle = (ms = ENTER_MS): React.CSSProperties => ({ animation: `fade-in ${ms}ms ${EASE} both` });

/** One thing in a list, by its key; `draw` draws it with the style that brings it in or takes it out. */
export interface Present {
  key: string;
  draw: (style?: React.CSSProperties) => React.ReactNode;
}

export type Phase = "in" | "out" | "rest";

/**
 * The list as it should be drawn: what has just come into it (by key, after the first draw) marked "in" for `ms`, what
 * has just gone from it kept in its old place marked "out" for `ms` so it can fade out. With reduced motion nothing is
 * marked or kept.
 */
export function usePresence(list: Present[], ms = ENTER_MS): { entry: Present; phase: Phase }[] {
  const [moving, setMoving] = useState<Map<string, { phase: "in" | "out"; entry: Present; at: number }>>(() => new Map());
  const prev = useRef<Present[] | null>(null);
  const timers = useRef(new Set<ReturnType<typeof setTimeout>>());
  const keys = list.map((e) => e.key).join("\n");
  useLayoutEffect(() => {
    const before = prev.current;
    prev.current = list;
    if (!before || reducedMotion()) return;
    const was = new Set(before.map((e) => e.key));
    const now = new Set(list.map((e) => e.key));
    const changed = new Map<string, { phase: "in" | "out"; entry: Present; at: number }>();
    list.forEach((e, at) => !was.has(e.key) && changed.set(e.key, { phase: "in", entry: e, at }));
    before.forEach((e, at) => !now.has(e.key) && changed.set(e.key, { phase: "out", entry: e, at }));
    if (!changed.size) return;
    setMoving((m) => {
      const next = new Map([...m].filter(([k, v]) => !changed.has(k) && (v.phase === "in" ? now.has(k) : !now.has(k))));
      changed.forEach((v, k) => next.set(k, v));
      return next;
    });
    const t = setTimeout(() => {
      timers.current.delete(t);
      setMoving((m) => {
        const next = new Map(m);
        changed.forEach((v, k) => next.get(k) === v && next.delete(k));
        return next;
      });
    }, ms + 40);
    timers.current.add(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [keys]);
  // a key that stays is drawn as it is now
  useLayoutEffect(() => {
    prev.current = list;
  });
  useEffect(() => {
    const ts = timers.current;
    return () => ts.forEach(clearTimeout);
  }, []);
  const out: { entry: Present; phase: Phase }[] = list.map((entry) => ({ entry, phase: moving.get(entry.key)?.phase === "in" ? "in" : "rest" }));
  if (!moving.size) return out;
  const here = new Set(list.map((e) => e.key));
  const gone = [...moving.values()].filter((v) => v.phase === "out" && !here.has(v.entry.key)).sort((a, b) => a.at - b.at);
  for (const g of gone) out.splice(Math.min(g.at, out.length), 0, { entry: g.entry, phase: "out" });
  return out;
}

/** The style that brings a thing in or takes it out (nothing at rest). */
export function presenceStyle(phase: Phase): React.CSSProperties | undefined {
  if (phase === "in") return { animation: `fade-up ${ENTER_MS}ms ${EASE} both` };
  if (phase === "out") return { animation: `fade-up ${ENTER_MS}ms ${EASE} reverse both`, pointerEvents: "none" };
  return undefined;
}
