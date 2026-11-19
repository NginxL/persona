// Developing Persona's recorder for the web and the desktop app (#9617, docs/operations/developing-persona.md "What
// is recorded"): its own chunk, loaded only while the switch (lib/dev-probe.ts DEV_MODE_KEY) is on. It counts in memory
// what the page does (requests by route pattern, socket frames by type, pages) and writes a `minute` line once a minute
// while the page is visible and one for each hidden stretch, a `foreground` line per visible period, and single lines
// for what changes (page, visibility, network, socket, errors, console). Lines are buffered and appended in batches to
// IndexedDB, kept 24 hours and 5 MB at most. iOS's twin is apple/Persona/DevMode/DevRecorder.swift.
import type { ErrorFrame, ErrorReport } from "./extension-wire";
import { DEV_MODE_KEY, setDevSink, watchSockets, type DevSink } from "./dev-probe";
import { devRoute, devWord, scrubText } from "./dev-scrub";
import { startFreezeWatch, type FreezeHit } from "./freeze-watch";


export const MAX_AGE_MS = 24 * 3600_000;
export const MAX_BYTES = 5 * 1024 * 1024;
export const MINUTE_MS = 60_000;
/** Lines kept in memory before they are written without waiting for the minute. */
const BUFFER_LIMIT = 200;

/** One stored line: what it is for (an event, an error report, a console line) and when. */
export interface DevLine {
  at: number;
  cat: "event" | "error" | "log";
  text: string;
}

export interface DevStore {
  append(lines: DevLine[]): Promise<void>;
  /** Every line younger than `since`, oldest first. */
  read(since: number): Promise<DevLine[]>;
  /** The characters kept. */
  size(): Promise<number>;
  /** Drops lines older than MAX_AGE_MS, then the oldest until MAX_BYTES. */
  trim(now: number): Promise<void>;
  clear(): Promise<void>;
}

/** For tests and for a browser without IndexedDB (a private window): the lines in memory. */
export class MemoryStore implements DevStore {
  lines: DevLine[] = [];
  async append(lines: DevLine[]) {
    this.lines.push(...lines);
  }
  async read(since: number) {
    return this.lines.filter((l) => l.at >= since);
  }
  async size() {
    return this.lines.reduce((n, l) => n + l.text.length, 0);
  }
  async trim(now: number) {
    this.lines = this.lines.filter((l) => l.at >= now - MAX_AGE_MS);
    let total = await this.size();
    while (total > MAX_BYTES && this.lines.length) total -= this.lines.shift()!.text.length;
  }
  async clear() {
    this.lines = [];
  }
}

const DB = "persona-dev-mode";
const STORE = "lines";

function req<T>(r: IDBRequest<T>): Promise<T> {
  return new Promise((done, fail) => {
    r.onsuccess = () => done(r.result);
    r.onerror = () => fail(r.error);
  });
}

/** The lines in IndexedDB (`persona-dev-mode`), one record each, in the order written. */
export class IndexedStore implements DevStore {
  private db: Promise<IDBDatabase>;
  constructor() {
    this.db = new Promise((done, fail) => {
      const open = indexedDB.open(DB, 1);
      open.onupgradeneeded = () => open.result.createObjectStore(STORE, { autoIncrement: true }).createIndex("at", "at");
      open.onsuccess = () => done(open.result);
      open.onerror = () => fail(open.error);
    });
  }
  private async tx(mode: IDBTransactionMode) {
    return (await this.db).transaction(STORE, mode).objectStore(STORE);
  }
  async append(lines: DevLine[]) {
    const s = await this.tx("readwrite");
    await Promise.all(lines.map((l) => req(s.add(l))));
  }
  async read(since: number) {
    const s = await this.tx("readonly");
    const all = (await req(s.getAll())) as DevLine[];
    return all.filter((l) => l.at >= since);
  }
  async size() {
    const all = await this.read(0);
    return all.reduce((n, l) => n + l.text.length, 0);
  }
  async trim(now: number) {
    const s = await this.tx("readwrite");
    const old = await req(s.index("at").getAllKeys(IDBKeyRange.upperBound(now - MAX_AGE_MS, true)));
    await Promise.all(old.map((k) => req(s.delete(k))));
    const keys = await req(s.getAllKeys());
    const values = (await req(s.getAll())) as DevLine[];
    let total = values.reduce((n, l) => n + l.text.length, 0);
    for (let i = 0; total > MAX_BYTES && i < keys.length; i++) {
      total -= values[i].text.length;
      await req(s.delete(keys[i]));
    }
  }
  async clear() {
    const s = await this.tx("readwrite");
    await req(s.clear());
  }
}

interface RouteStat {
  n: number;
  bytes: number;
  ms: number[];
  status: Record<string, number>;
}

/** One summary being counted: a minute (or a hidden stretch) or a visible period. */
interface Tally {
  from: number;
  hidden: boolean;
  routes: Map<string, RouteStat>;
  frames: Record<string, number>;
  frameBytes: number;
  connects: number;
  drops: number;
  pages: Record<string, number>;
}

const tally = (from: number, hidden: boolean): Tally => ({ from, hidden, routes: new Map(), frames: {}, frameBytes: 0, connects: 0, drops: 0, pages: {} });

const stamp = (ms: number) => new Date(ms).toISOString();

/** One JSON line: `t`, `kind` and the fields, keys sorted. */
export function line(at: number, kind: string, fields: Record<string, unknown>): string {
  const all: Record<string, unknown> = { ...fields, t: stamp(at), kind };
  const sorted: Record<string, unknown> = {};
  for (const k of Object.keys(all).sort()) sorted[k] = all[k];
  return JSON.stringify(sorted);
}

export class DevRecorder implements DevSink {
  private minute: Tally;
  private period: Tally | null;
  private current = "launch";
  private pageSince: number;
  private buffer: DevLine[] = [];
  private noted = new Set<string>();
  private writing: Promise<void> = Promise.resolve();

  constructor(
    readonly store: DevStore,
    private readonly now: () => number = () => Date.now(),
    private readonly build = "",
    hidden = false,
    private readonly memory: () => number | undefined = () => undefined,
  ) {
    const at = now();
    this.minute = tally(at, hidden);
    this.period = hidden ? null : tally(at, false);
    this.pageSince = at;
    this.event(at, "app", { event: "recording", visible: !hidden, build });
  }

  // ── what the page does ──

  request(method: string, path: string, status: number, ms: number, bytes: number) {
    const route = devRoute(method, path);
    const code = status ? String(status) : "failed";
    for (const t of [this.minute, this.period]) {
      if (!t) continue;
      const s = t.routes.get(route) ?? { n: 0, bytes: 0, ms: [], status: {} };
      s.n += 1;
      s.bytes += Math.max(0, bytes);
      if (s.ms.length < 2000) s.ms.push(Math.round(ms));
      s.status[code] = (s.status[code] ?? 0) + 1;
      t.routes.set(route, s);
    }
    if (status && status < 400) return;
    const key = `${route} ${code}`;
    if (this.noted.has(key)) return;
    this.noted.add(key);
    this.event(this.now(), "request_error", { route, status: code, page: this.current });
  }

  frame(type: string, bytes: number) {
    const word = devWord(type || "unknown");
    for (const t of [this.minute, this.period]) {
      if (!t) continue;
      t.frames[word] = (t.frames[word] ?? 0) + 1;
      t.frameBytes += bytes;
    }
  }

  socket(event: "connected" | "dropped") {
    for (const t of [this.minute, this.period]) {
      if (!t) continue;
      if (event === "connected") t.connects += 1;
      else t.drops += 1;
    }
    this.event(this.now(), "socket", { event, page: this.current });
  }

  page(name: string) {
    const word = devWord(name);
    if (word === this.current) return;
    const at = this.now();
    this.settle(at);
    this.current = word;
    this.event(at, "page", { page: word });
  }

  report(r: ErrorReport) {
    const clean: ErrorReport = {
      ...r,
      ...(r.message ? { message: scrubText(r.message) } : {}),
      ...(r.exceptions ? { exceptions: r.exceptions.map((e) => ({ ...e, ...(e.value ? { value: scrubText(e.value) } : {}) })) } : {}),
      ...(r.breadcrumbs ? { breadcrumbs: r.breadcrumbs.map((b) => ({ ...b, ...(b.message ? { message: scrubText(b.message) } : {}) })) } : {}),
    };
    this.push({ at: this.now(), cat: "error", text: JSON.stringify(clean) });
  }

  /** A console.error / warn line, scrubbed. */
  log(level: "error" | "warn", text: string) {
    const at = this.now();
    this.push({ at, cat: "log", text: `${stamp(at)} [console] ${level}: ${scrubText(text)}` });
  }

  /** A change of the browser's state: online / offline. */
  note(kind: string, fields: Record<string, unknown>) {
    this.event(this.now(), kind, { ...fields, page: this.current });
  }

  // ── time ──

  /** Once a minute while visible: the minute's line, and a fresh minute. */
  roll() {
    const at = this.now();
    this.settle(at);
    this.push({ at, cat: "event", text: this.close(this.minute, at) });
    this.minute = tally(at, this.minute.hidden);
    this.noted.clear();
    this.flush();
  }

  /** The page was shown or hidden: the open summary closes, and a visible period starts or ends. */
  visibility(visible: boolean) {
    if (this.minute.hidden === !visible) return;
    const at = this.now();
    this.settle(at);
    this.push({ at, cat: "event", text: this.close(this.minute, at) });
    if (visible) {
      this.period = tally(at, false);
    } else if (this.period) {
      this.push({ at, cat: "event", text: this.closePeriod(this.period, at) });
      this.period = null;
    }
    this.minute = tally(at, !visible);
    this.noted.clear();
    this.event(at, "app", { event: visible ? "visible" : "hidden" });
    this.flush();
  }

  /** Writes what is buffered; resolves once written (an export or a size waits for it). */
  flush(): Promise<void> {
    const batch = this.buffer;
    this.buffer = [];
    if (batch.length) this.writing = this.writing.then(() => this.store.append(batch)).then(() => this.store.trim(this.now())).catch(() => {});
    return this.writing;
  }

  // ── lines ──

  private event(at: number, kind: string, fields: Record<string, unknown>) {
    this.push({ at, cat: "event", text: line(at, kind, fields) });
  }

  private push(l: DevLine) {
    this.buffer.push(l);
    if (this.buffer.length >= BUFFER_LIMIT) void this.flush();
  }

  private settle(at: number) {
    const spent = Math.max(0, (at - this.pageSince) / 1000);
    if (spent > 0) {
      for (const t of [this.minute, this.period]) if (t) t.pages[this.current] = (t.pages[this.current] ?? 0) + spent;
    }
    this.pageSince = at;
  }

  private close(t: Tally, at: number): string {
    const http = [...t.routes.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([route, s]) => {
      const sorted = [...s.ms].sort((a, b) => a - b);
      return { route, n: s.n, in: s.bytes, p50_ms: sorted[Math.floor(sorted.length / 2)] ?? 0, max_ms: sorted[sorted.length - 1] ?? 0, status: s.status };
    });
    const memory = this.memory();
    return line(at, "minute", {
      from: stamp(t.from),
      to: stamp(at),
      background: t.hidden,
      build: this.build,
      page: this.current,
      pages: seconds(t.pages),
      requests: http.reduce((n, h) => n + h.n, 0),
      http,
      ws: { frames: t.frames, bytes: t.frameBytes, connects: t.connects, drops: t.drops },
      online: typeof navigator === "undefined" ? true : navigator.onLine,
      ...(memory !== undefined ? { memory_mb: memory } : {}),
    });
  }

  private closePeriod(t: Tally, at: number): string {
    let requests = 0;
    let bytes = 0;
    for (const s of t.routes.values()) {
      requests += s.n;
      bytes += s.bytes;
    }
    return line(at, "foreground", {
      from: stamp(t.from),
      to: stamp(at),
      seconds: Math.round((at - t.from) / 1000),
      build: this.build,
      requests,
      bytes_in: bytes,
      ws_frames: Object.values(t.frames).reduce((n, x) => n + x, 0),
      ws_connects: t.connects,
      ws_drops: t.drops,
      pages: seconds(t.pages),
    });
  }
}

function seconds(pages: Record<string, number>): Record<string, number> {
  const out: Record<string, number> = {};
  for (const [k, v] of Object.entries(pages)) if (Math.round(v) > 0) out[k] = Math.round(v);
  return out;
}

// ── the page's recorder ──

let running: { recorder: DevRecorder; stop: () => void } | null = null;

/** The build this page runs: its entry script's hashed name. */
export function webBuild(): string {
  const src = document.querySelector<HTMLScriptElement>('script[type="module"][src]')?.getAttribute("src") ?? "";
  return src.split("/").pop()?.replace(/\.js$/, "") ?? "";
}

function usedMemory(): number | undefined {
  const m = (performance as Performance & { memory?: { usedJSHeapSize?: number } }).memory;
  return m?.usedJSHeapSize ? Math.round(m.usedJSHeapSize / 1048576) : undefined;
}

function openStore(): DevStore {
  try {
    return typeof indexedDB === "undefined" ? new MemoryStore() : new IndexedStore();
  } catch {
    return new MemoryStore();
  }
}

/** An uncaught error or rejection as an error report (the hub's format), for the recorder's own copy. */
export function errorReport(error: unknown, mechanism: string, at = Date.now()): ErrorReport {
  const e = error instanceof Error ? error : null;
  const name = e?.name || "Error";
  const message = e ? e.message : typeof error === "string" ? error : safeJSON(error);
  const frames = stackFrames(e?.stack);
  return {
    id: `${at.toString(36)}-${Math.random().toString(36).slice(2, 10)}`,
    source: "web",
    kind: "error",
    level: "error",
    time: new Date(at).toISOString(),
    message: `${name}: ${message}`,
    exceptions: [{ type: name, value: message, ...(frames.length ? { frames } : {}) }],
    tags: { mechanism },
  };
}

/** A frozen page (lib/freeze-watch.ts), as the hub's reports write it. */
export function freezeReport(hit: FreezeHit, at = Date.now()): ErrorReport {
  const text = `Main thread blocked for ${Math.round(hit.durationMs)}ms`;
  return {
    id: `${at.toString(36)}-${Math.random().toString(36).slice(2, 10)}`,
    source: "web",
    kind: "anr",
    level: "warning",
    time: new Date(hit.startedAt ?? at).toISOString(),
    message: text,
    exceptions: [{ type: "ANR", value: text, ...(hit.frames.length ? { frames: hit.frames } : {}) }],
    tags: { mechanism: hit.mechanism },
  };
}

/** The page in front: its address's first word ("s" for an Agent's chat, "settings", "" for the home page), as a
 *  code word. The router's own names would cost the first screen an export. */
export function pageName(path = location.pathname): string {
  const first = path.replace(/^\/+/, "").split("/")[0] ?? "";
  return devWord(first || "home");
}

/** A stack's frames as the error reports write them (innermost last), each its function and the script's path. */
export function stackFrames(stack: string | undefined): ErrorFrame[] {
  if (!stack) return [];
  const out: ErrorFrame[] = [];
  for (const raw of stack.split("\n").slice(0, 50)) {
    const m = /^\s*at (?:(.+?) \()?(.+?):(\d+):(\d+)\)?\s*$/.exec(raw) ?? /^\s*(.*?)@(.+?):(\d+):(\d+)\s*$/.exec(raw);
    if (!m) continue;
    out.push({ ...(m[1] ? { function: m[1] } : {}), file: scrubText(m[2]), line: Number(m[3]), column: Number(m[4]) });
  }
  return out.reverse();
}

/** Starts recording (at launch with the switch on, or as it is turned on). Everything it watches it hooks itself and
 *  puts back on `setDevMode(false)`: the hub socket (lib/dev-probe.ts, wrapped before the app opens it), fetch, the
 *  history for pages, uncaught errors and rejections, freezes and console.error / warn; it also starts the To-dos view's
 *  shadow, and stops it. */
export function startDevMode(): DevRecorder {
  if (running) return running.recorder;
  watchSockets(); // a socket already open is counted from its next connection
  // the desktop shell records its main process too (#9675); a browser or an older shell has no such operation
  void window.personaDesktop?.setDevDiagnostics?.(true).catch(() => {});
  const recorder = new DevRecorder(openStore(), () => Date.now(), webBuild(), document.visibilityState === "hidden", usedMemory);
  setDevSink(recorder);
  recorder.page(pageName());
  let timer: ReturnType<typeof setInterval> | null = null;
  const tick = () => {
    if (timer) clearInterval(timer);
    timer = document.visibilityState === "visible" ? setInterval(() => recorder.roll(), MINUTE_MS) : null;
  };
  const onVisibility = () => {
    recorder.visibility(document.visibilityState === "visible");
    tick();
  };
  const onOnline = () => recorder.note("network", { online: navigator.onLine });
  const onHide = () => void recorder.flush();
  const onPage = () => recorder.page(pageName());
  const onError = (e: ErrorEvent) => recorder.report(errorReport(e.error ?? e.message, "onerror"));
  const onRejection = (e: PromiseRejectionEvent) => recorder.report(errorReport(e.reason, "unhandledrejection"));

  // requests to the hub: the route, status, time to the answer's headers and its declared size
  const nativeFetch = window.fetch;
  window.fetch = async function (this: unknown, input: RequestInfo | URL, init?: RequestInit) {
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url, location.href);
    const at = url.pathname.indexOf("/api/v1/");
    if (url.origin !== location.origin || at < 0) return nativeFetch.call(this, input, init);
    const method = init?.method ?? (input instanceof Request ? input.method : "GET");
    const path = url.pathname.slice(at + "/api/v1".length);
    const started = performance.now();
    try {
      const res = await nativeFetch.call(this, input, init);
      recorder.request(method, path, res.status, performance.now() - started, Number(res.headers.get("Content-Length")) || 0);
      return res;
    } catch (e) {
      if (!(e instanceof DOMException && e.name === "AbortError")) recorder.request(method, path, 0, performance.now() - started, 0);
      throw e;
    }
  } as typeof fetch;

  // pages: the router pushes and replaces history entries; Back and Forward are popstate
  const { pushState, replaceState } = history;
  history.pushState = function (this: History, ...args: Parameters<History["pushState"]>) {
    pushState.apply(this, args);
    onPage();
  };
  history.replaceState = function (this: History, ...args: Parameters<History["replaceState"]>) {
    replaceState.apply(this, args);
    onPage();
  };

  // console.error / warn go on as before; the recorder keeps a scrubbed copy
  const original = { error: console.error, warn: console.warn };
  const wrap = (level: "error" | "warn") =>
    function (this: Console, ...args: unknown[]) {
      try {
        recorder.log(level, args.map((a) => (a instanceof Error ? `${a.name}: ${a.message}` : typeof a === "string" ? a : safeJSON(a))).join(" "));
      } catch {
        // never in the way of the console
      }
      return original[level].apply(this, args);
    };
  console.error = wrap("error");
  console.warn = wrap("warn");
  const freezes = startFreezeWatch({ enabled: () => true, report: (hit) => (recorder.report(freezeReport(hit)), true) });
  // the To-dos view's shadow (#10226, features/todos/model/view-shadow.ts): a chunk of its own, comparing while the hub
  // sends `view`; its lines go to whichever recorder runs
  let shadow: (() => void) | null | undefined;
  void import("../features/todos/model/view-shadow")
    .then((m) => {
      if (shadow === undefined) shadow = m.startViewShadow({ note: (kind, fields) => devRecorder()?.note(kind, fields) });
    })
    .catch(() => {});

  document.addEventListener("visibilitychange", onVisibility);
  window.addEventListener("online", onOnline);
  window.addEventListener("offline", onOnline);
  window.addEventListener("pagehide", onHide);
  window.addEventListener("popstate", onPage);
  window.addEventListener("error", onError);
  window.addEventListener("unhandledrejection", onRejection);
  tick();
  running = {
    recorder,
    stop: () => {
      if (timer) clearInterval(timer);
      shadow?.();
      shadow = null; // stopped before its chunk loaded: it never starts
      window.fetch = nativeFetch;
      history.pushState = pushState;
      history.replaceState = replaceState;
      console.error = original.error;
      console.warn = original.warn;
      freezes();
      document.removeEventListener("visibilitychange", onVisibility);
      window.removeEventListener("online", onOnline);
      window.removeEventListener("offline", onOnline);
      window.removeEventListener("pagehide", onHide);
      window.removeEventListener("popstate", onPage);
      window.removeEventListener("error", onError);
      window.removeEventListener("unhandledrejection", onRejection);
      setDevSink(null);
    },
  };
  return recorder;
}

/** The recorder while it runs. */
export function devRecorder(): DevRecorder | null {
  return running?.recorder ?? null;
}

/** Turns the switch on (recording from now) or off (stopping, and deleting what was recorded). */
export async function setDevMode(on: boolean): Promise<void> {
  if (on) {
    try {
      localStorage.setItem(DEV_MODE_KEY, new Date().toISOString());
    } catch {
      // a private window without storage: records for this page only
    }
    startDevMode();
    return;
  }
  try {
    localStorage.removeItem(DEV_MODE_KEY);
  } catch {
    // nothing kept
  }
  const r = running;
  running?.stop();
  running = null;
  void window.personaDesktop?.setDevDiagnostics?.(false).catch(() => {});
  const store = r?.recorder.store ?? openStore();
  await r?.recorder.flush();
  await store.clear();
}

function safeJSON(v: unknown): string {
  try {
    return JSON.stringify(v) ?? String(v);
  } catch {
    return String(v);
  }
}
