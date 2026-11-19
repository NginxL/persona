// The full herdr surface's feed (docs/integrations/terminal.md "Full herdr UI"): herdr's own client, run in a PTY on the computer,
// drawn by a plain xterm. Not TerminalFeed's job — that one mirrors a pane's grid frame by frame — so this is a feed of
// its own with the same face for what the page shares (the key bar, the find bar, the right-click menu): the hub relays
// bytes both ways over the app WebSocket (herdr_open / herdr_data / herdr_input / herdr_resize / herdr_ack / herdr_close,
// server/internal/hub/herdrclient.go) and this keeps xterm in step, acknowledging what it has parsed so the computer
// never runs more than a window ahead.
import type { Terminal as XTerm } from "@xterm/xterm";
import type { FitAddon as XFit } from "@xterm/addon-fit";
import type { SearchAddon as XSearch } from "@xterm/addon-search";
import { t as tr } from "../../../i18n";
import { openLink } from "../../../lib/links";
import { followRoute, focusAddress, focusGuard, decideFocus, parseFocus, type HerdrFocus } from "../../../lib/herdr-focus";
import { navigate } from "../../../lib/router";
import { getSession, getState, isOpen, onHubEvent, send } from "../../../lib/store";
import { currentPlatform, passesToApp } from "../../../lib/shortcuts";
import { herdrOwnsKey, isCtrlV, linkOpens, rightClickRoute, type RightClick } from "../../../lib/terminal-conflicts";
import {
  NO_MODIFIERS,
  encodeKey,
  keyDecision,
  keyNameForBytes,
  modifierHeld,
  modifierReleased,
  modifierTapped,
  modifiersActive,
  modifiersPressed,
  typedWithModifiers,
  withModifiers,
  type ModifierKey,
  type Modifiers,
  type TerminalPalette,
} from "../../../lib/terminal";
import { copyNotice, copyWith, osc52Text, transferFiles } from "../../../lib/terminal-clipboard";
import { lineEditBytes, parseCursorStyle, parseFlag, webLink, type CursorStyle } from "../../../lib/terminal-details";
import { subscribeAppearance, terminalAppearance, terminalDark } from "../../../lib/terminal-appearance";
import { FIND_LIMIT } from "../../../lib/terminal-search";
import {
  AckTracker,
  FULL_SCROLLBACK,
  HINT_KEY,
  colourReports,
  dimLine,
  endStatus,
  endText,
  fullBytes,
  fullGrid,
  schemeModeReport,
  schemeReport,
  type FullStatus,
} from "../../../lib/terminal-mode";
import { toast } from "../../../ui/atoms";
import { cjkFontStack, terminalFontReady } from "./cjk";
import { browserClipboard, clipboardFiles } from "./clipboard";
import { findDecorations, pageDark, terminalColors } from "./colors";
import { FileTransfer } from "./transfer";

export interface FullView {
  /** xterm is loaded and open. */
  ready: boolean;
  status: FullStatus;
  /** Why the view is not live (the reason the computer or the hub gave, in words). */
  note: string;
  cols: number;
  rows: number;
  /** "active" while keys can go in (the key bar's switch for being enabled). */
  control: "active" | "off";
  mods: Modifiers;
  selection: boolean;
  find: { index: number; count: number } | null;
  palette: TerminalPalette | null;
  /** The view ended before it was ever live and the reason is the computer's or the hub's, not a lost connection: this machine cannot show it. */
  failedToStart: boolean;
  /** Files on their way to the computer (pasted or dropped), in words; null when none. */
  uploading: string | null;
  /** A drag of files is over the surface. */
  dropping: boolean;
  /** herdr's focus moved to this pane without this view doing it (the computer's window, another browser); the page stayed (lib/herdr-focus.ts). */
  elsewhere: HerdrFocus | null;
}

const RESIZE_MS = 120;
/** How often (ms) an open view checks that the hub link is still up: the hub stops the client when the socket drops, and nothing else says so. */
const LINK_MS = 1000;
/** A surface that gave the feed up is waited for this long (ms) before the client is let go: the page that takes over (another session of the machine) claims it within the same moment. */
const YIELD_MS = 1200;
/** One herdr_input carries at most this many UTF-16 units: a big paste goes in several (the hub caps one at 1 MiB). */
const INPUT_CHUNK = 32 << 10;
const FONT_KEY = "persona.terminal.fontSize";
const CURSOR_KEY = "persona.terminal.cursorStyle";
const BLINK_KEY = "persona.terminal.cursorBlink";
const FONT_RANGE = { min: 9, max: 20, default: 13 };

function savedFontSize(): number {
  const n = Number(localStorage.getItem(FONT_KEY));
  return Number.isFinite(n) && n >= FONT_RANGE.min && n <= FONT_RANGE.max ? n : FONT_RANGE.default;
}

let views = 0;

export class FullFeed {
  private view: FullView;
  private listeners = new Set<() => void>();
  private term: XTerm | null = null;
  private fontFamily = "";
  private fit: XFit | null = null;
  private search: XSearch | null = null;
  private cleanup: (() => void)[] = [];
  private disposed = false;
  /** The id of the view open on the hub ("" when none): messages about other ids are an earlier view's. */
  private id = "";
  private ack: AckTracker | null = null;
  /** Let go on purpose (the surface is leaving or hidden): nothing is opened until `reconnect`. A new feed starts so: its first `claim` opens it. */
  private released = true;
  private resumeAt = 0;
  private grid: { cols: number; rows: number } | null = null;
  private sent: { cols: number; rows: number } | null = null;
  private resizeTimer = 0;
  private openTimer = 0;
  /** The TUI turned on colour-scheme notifications (DEC 2031). */
  private schemeNotify = false;
  /** What the TUI is told of the colors now (OSC 10/11 and CSI ? 997, colourReport). */
  private told = "";
  private found = { query: "", cs: false, busy: false };
  private everLive = false;
  /** The link dropped under an open view: the screen stays, and the next view starts on a clean one. */
  private linkLost = false;
  /** Files pasted or dropped (transfer.ts): sent to the computer, their paths typed in. */
  private transfer: FileTransfer;
  /** The pointer of the last press (a link opens on a tap but, with herdr holding the mouse, on ⌘ / Ctrl + click). */
  private pointer = "mouse";
  /** The session or pane address the page shows now; herdr's focus is kept on it (one feed per machine, see full-registry.ts). */
  private target: string;
  /** The address herdr was last asked to show, or opened on. */
  private focused = "";
  /** Where xterm sits now, and what listens to it there (a page of the same machine can take the feed over: `claim`). */
  private hostEl: HTMLElement | null = null;
  private areaEl: HTMLElement | null = null;
  private hostCleanup: (() => void)[] = [];
  /** The surface that has the feed now (FullTerminal's token); null when none does. */
  private owner: object | null = null;
  private yieldTimer = 0;

  constructor(
    readonly mid: string,
    sid: string,
  ) {
    this.target = sid;
    const feed = this;
    this.transfer = new FileTransfer(
      {
        mid,
        // "" = by absolute path: the pane herdr has focused is not the session's, so its folder is not known here
        cwd: "",
        get driving() {
          return feed.driving;
        },
        get disposed() {
          return feed.disposed;
        },
        setUploading: (uploading) => this.patch({ uploading }),
        setDropping: (dropping) => this.patch({ dropping }),
        paste: (text) => {
          this.term?.paste(text); // bracketed when herdr asked for it; the keys go through type()
          this.term?.focus();
        },
      },
      { notDriving: tr("terminal-full.paste_not_live"), changed: tr("terminal-full.paste_changed"), released: tr("terminal-full.paste_released") },
    );
    this.view = {
      ready: false,
      status: "connecting",
      note: "",
      cols: 0,
      rows: 0,
      control: "off",
      mods: NO_MODIFIERS,
      selection: false,
      find: null,
      palette: null,
      failedToStart: false,
      uploading: null,
      dropping: false,
      elsewhere: null,
    };
  }

  /** The address the feed shows (the page's session or pane address). */
  get sid() {
    return this.target;
  }

  // ── React ─────────────────────────────────────────────

  subscribe = (l: () => void) => {
    this.listeners.add(l);
    return () => {
      this.listeners.delete(l);
    };
  };

  snapshot = () => this.view;

  private patch(p: Partial<FullView>) {
    // The key bar's ctrl / alt / shift go with the keys: its caps are disabled once control leaves, and a disabled cap
    // gets no pointerup, so a modifier held, tapped or locked then would chord the first key typed when it comes back.
    if (p.control === "off") p = { ...p, mods: NO_MODIFIERS };
    const v = this.view as unknown as Record<string, unknown>;
    if (!Object.entries(p).some(([k, x]) => v[k] !== x)) return;
    this.view = { ...this.view, ...p };
    this.listeners.forEach((l) => l());
  }

  // ── lifecycle ─────────────────────────────────────────

  /** Builds xterm in `host` (`area` around it is where files are dropped), then opens the view once there is a grid and a connection and the feed is claimed. */
  async mount(host: HTMLElement, area: HTMLElement = host) {
    this.hostEl = host;
    this.areaEl = area;
    let mod: typeof import("./xterm");
    try {
      mod = await import("./xterm");
    } catch {
      if (!this.disposed) this.patch({ status: "error", note: tr("terminal-full.load_failed") });
      return;
    }
    if (this.disposed) return;
    const { Terminal, FitAddon, ClipboardAddon, WebLinksAddon, SearchAddon, matchHerdrWidths } = mod;
    const size = savedFontSize();
    const family = getComputedStyle(host).fontFamily;
    this.fontFamily = family;
    await terminalFontReady(size, family);
    if (this.disposed) return;
    const palette = this.theme();
    this.told = this.colourReport(palette);
    const term = new Terminal({
      fontFamily: family,
      fontSize: size,
      lineHeight: 1, // herdr draws its borders and bars with box characters: with any gap between rows they come out dashed
      scrollback: FULL_SCROLLBACK,
      cursorBlink: parseFlag(localStorage.getItem(BLINK_KEY)),
      cursorStyle: parseCursorStyle(localStorage.getItem(CURSOR_KEY)) satisfies CursorStyle,
      cursorInactiveStyle: "outline",
      allowProposedApi: true, // the search addon marks its matches with decorations
      macOptionIsMeta: true,
      rightClickSelectsWord: false, // xterm's default on a Mac: a right click would select a word of herdr's screen under herdr's own menu
      theme: palette,
      linkHandler: { activate: (e, uri) => this.openWebLink(e, uri) }, // OSC 8 links
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.loadAddon(new WebLinksAddon((e, uri) => this.openWebLink(e, uri)));
    const search = new SearchAddon({ highlightLimit: FIND_LIMIT });
    term.loadAddon(search);
    // herdr's own client copies with OSC 52 (a selection, or a pane's program): a write goes to the clipboard, a read is
    // refused — nothing running on the computer gets to read what the user copied.
    term.loadAddon(new ClipboardAddon(undefined, { readText: () => "", writeText: (_selection: string, text: string) => this.programCopied(text) }));
    matchHerdrWidths(term);
    term.open(this.hostEl ?? host); // a page that claimed the feed meanwhile has moved the place
    this.term = term;
    this.fit = fit;
    this.search = search;

    const parser = term.parser;
    const handlers = [
      // what herdr asks of the terminal it draws in: the colour scheme (CSI ? 996 n), and to be told when it changes; the
      // terminal's scheme (its Appearance), which is the page's under "Match page"
      parser.registerCsiHandler({ prefix: "?", final: "n" }, (p) => (p.length === 1 && p[0] === 996 ? (this.raw(schemeReport(this.dark())), true) : false)),
      ...(["h", "l"] as const).map((final) =>
        parser.registerCsiHandler({ prefix: "?", final }, (p) => {
          if (!p.includes(2031)) return false;
          this.schemeNotify = final === "h";
          return p.length === 1; // other modes in the same sequence go on to xterm
        }),
      ),
      parser.registerCsiHandler({ prefix: "?", intermediates: "$", final: "p" }, (p) =>
        p.length === 1 && p[0] === 2031 ? (this.raw(schemeModeReport(this.schemeNotify)), true) : false,
      ),
    ];
    const data = term.onData((d) => this.type(d));
    const selected = term.onSelectionChange(() => this.patch({ selection: term.hasSelection() }));
    const results = search.onDidChangeResults((r) => this.found.query && this.patch({ find: { index: r.resultIndex, count: r.resultCount } }));
    term.attachCustomKeyEventHandler(this.key);
    // The page's scheme changed: under "Match page" the terminal's did too, and the TUI hears it if it asked; under
    // "Light" / "Dark" the terminal's is the same as before, so it hears nothing.
    const colors = new MutationObserver(() => {
      const report = this.recolor();
      if (terminalAppearance() === "page" && this.schemeNotify) this.raw(report);
    });
    colors.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
    // Appearance changed (Terminal settings): the TUI hears the new colors if it asked and they are not what it has
    const appearance = subscribeAppearance(() => {
      const before = this.told;
      const report = this.recolor();
      if (report !== before && this.schemeNotify) this.raw(report);
    });
    const offs = [
      onHubEvent("herdr_state", this.onState),
      onHubEvent("herdr_data", this.onData),
      onHubEvent("herdr_end", this.onEnd),
      onHubEvent("herdr_focus", this.onFocus),
      onHubEvent("hello", this.onHello),
    ];
    const link = window.setInterval(this.checkLink, LINK_MS);
    this.cleanup.push(
      () => clearInterval(link),
      ...handlers.map((h) => () => h.dispose()),
      () => data.dispose(),
      () => selected.dispose(),
      () => results.dispose(),
      () => this.unbindHost(),
      () => colors.disconnect(),
      appearance,
      ...offs,
    );

    this.bindHost();
    this.patch({ ready: true, palette });
    void cjkFontStack(family).then((stack) => {
      if (stack && !this.disposed && this.term && this.fontFamily === family) this.term.options.fontFamily = stack; // the cell is the same width: nothing resizes
    });
    this.measure(true);
    this.begin();
  }

  /** Listens to the box xterm sits in: its size, a press (the pointer kind), a paste, files dragged over the area around it. */
  private bindHost() {
    this.unbindHost();
    const host = this.hostEl;
    const area = this.areaEl ?? host;
    if (!host || !area || !this.term) return;
    const track = (e: PointerEvent) => void (this.pointer = e.pointerType);
    host.addEventListener("pointerdown", track, true);
    host.addEventListener("paste", this.pasted, true);
    area.addEventListener("dragenter", this.transfer.dragEnter, true);
    area.addEventListener("dragover", this.transfer.dragOver, true);
    area.addEventListener("dragleave", this.transfer.dragLeave, true);
    area.addEventListener("drop", this.transfer.drop, true);
    const sizes = new ResizeObserver(() => this.measure());
    sizes.observe(host);
    this.hostCleanup = [
      () => host.removeEventListener("pointerdown", track, true),
      () => host.removeEventListener("paste", this.pasted, true),
      () => area.removeEventListener("dragenter", this.transfer.dragEnter, true),
      () => area.removeEventListener("dragover", this.transfer.dragOver, true),
      () => area.removeEventListener("dragleave", this.transfer.dragLeave, true),
      () => area.removeEventListener("drop", this.transfer.drop, true),
      () => sizes.disconnect(),
    ];
  }

  private unbindHost() {
    this.hostCleanup.forEach((f) => f());
    this.hostCleanup = [];
  }

  // ── one feed per machine, taken over by the page in front (full-registry.ts) ──

  /**
   * The surface `token` is now the one in front: xterm moves into its box (nothing is reopened: the screen and the
   * client stay as they are), the feed shows `sid` (herdr's focus follows it), and a feed that was let go opens again.
   * `delay`: wait this long before opening a client (the surface before this one is still on screen, driving its pane).
   */
  claim(token: object, host: HTMLElement, area: HTMLElement, sid: string, delay = 0) {
    if (this.disposed) return;
    clearTimeout(this.yieldTimer);
    this.owner = token;
    this.hostEl = host;
    this.areaEl = area;
    const term = this.term;
    if (term?.element && term.element.parentElement !== host) {
      host.appendChild(term.element);
      this.bindHost();
      this.measure(true);
      term.refresh(0, term.rows - 1);
    } else if (term && !this.hostCleanup.length) {
      this.bindHost();
    }
    // A retained machine feed can move into a newly opened view without mount() running again.
    // Read its current mono choice on attachment, keeping size and the local CJK width correction.
    const family = getComputedStyle(host).fontFamily;
    if (term && family !== this.fontFamily) {
      this.fontFamily = family;
      void terminalFontReady(term.options.fontSize ?? savedFontSize(), family).then(async () => {
        if (this.disposed || this.term !== term || this.fontFamily !== family) return;
        term.options.fontFamily = family;
        this.measure(true);
        const stack = await cjkFontStack(family);
        if (stack && !this.disposed && this.term === term && this.fontFamily === family) term.options.fontFamily = stack;
      });
    }
    this.retarget(sid);
    if (this.released) this.reconnect(delay);
    else if (this.view.status === "live") term?.focus();
  }

  /**
   * The surface `token` is no longer in front: when no other surface takes the feed soon, the client on the computer is
   * let go. `now`: at once (the surface is leaving for the single pane, which drives its own pane next).
   */
  yield(token: object, now = false) {
    if (this.owner !== token) return;
    this.owner = null;
    clearTimeout(this.yieldTimer);
    if (now) return this.release();
    this.yieldTimer = window.setTimeout(() => {
      if (!this.owner && !this.disposed) this.release();
    }, YIELD_MS);
  }

  /** Whether a surface has the feed (it is in front). */
  get claimed() {
    return this.owner !== null;
  }

  /** The page shows another session or pane: herdr's focus follows it, unless the page got there by following herdr. */
  private retarget(sid: string) {
    if (sid === this.target && this.focused) return;
    this.target = sid;
    if (this.view.elsewhere && focusAddress(this.view.elsewhere) === sid) this.patch({ elsewhere: null });
    this.syncFocus();
  }

  /** Asks herdr to show the page's address when it is not on it already (feature herdr_focus, a live view). */
  private syncFocus() {
    if (!this.id || this.view.status !== "live" || this.focused === this.target) return;
    const address = this.target;
    const followed = focusGuard.consumeFollow(address);
    this.focused = address;
    if (followed || !getState().features.includes("herdr_focus")) return;
    focusGuard.noteSent(address);
    send({ type: "herdr_focus", id: this.id, session_id: address });
  }

  /** herdr's focus moved (lib/herdr-focus.ts decides what that means for the page). */
  private onFocus = (msg: Record<string, unknown>) => {
    const ev = parseFocus(msg);
    if (!ev || !this.id || ev.id !== this.id || this.disposed) return;
    const address = focusAddress(ev);
    const action = decideFocus(ev, { shown: this.target, front: this.claimed && this.view.status === "live", sent: focusGuard.wasSent(address) });
    if (action === "follow") {
      this.patch({ elsewhere: null });
      this.followTo(ev);
    } else if (action === "elsewhere") {
      this.patch({ elsewhere: ev });
    } else if (action === "same") {
      this.patch({ elsewhere: null });
    }
  };

  /** The page goes where herdr's focus is (a change made in this view, or the "Focus moved" chip tapped). */
  followTo(ev: HerdrFocus) {
    const sessions = getState().sessions[this.mid] ?? [];
    focusGuard.noteFollow(focusAddress(ev));
    navigate(followRoute(this.mid, ev, sessions), true);
  }

  /** Leaves: the hub stops the client on the computer, xterm goes. */
  dispose() {
    if (this.disposed) return;
    this.disposed = true;
    this.close();
    clearTimeout(this.resizeTimer);
    clearTimeout(this.openTimer);
    clearTimeout(this.yieldTimer);
    this.cleanup.forEach((f) => f());
    this.cleanup = [];
    this.term?.dispose();
    this.term = null;
    this.listeners.clear();
  }

  /** Let go on purpose and not opened again yet (`reconnect` does). */
  get isReleased() {
    return this.released;
  }

  /** Lets go of the client on the computer but keeps what xterm shows (the surface fading out, a hidden terminal). */
  release() {
    this.released = true;
    clearTimeout(this.openTimer);
    this.close();
    this.patch({ status: "closed", note: "", control: "off", elsewhere: null });
  }

  /** Opens a view again after `release`, or after it ended ("Reconnect"): a clean screen, the client draws it all. `delay`: wait this long first. */
  reconnect(delay = 0) {
    if (this.disposed) return;
    clearTimeout(this.yieldTimer);
    this.released = false;
    this.close();
    this.term?.reset();
    this.patch({ status: "connecting", note: "", control: "off", failedToStart: false });
    this.resumeAt = delay > 0 ? Date.now() + delay : 0;
    this.begin();
  }

  private close() {
    if (this.id) send({ type: "herdr_close", id: this.id });
    this.id = "";
    this.ack?.dispose();
    this.ack = null;
  }

  /** The terminal's scheme: the page's, or the one its Appearance says. */
  private dark() {
    return terminalDark(terminalAppearance(), pageDark());
  }

  private theme(): TerminalPalette {
    return terminalColors();
  }

  /** What a terminal tells the TUI of its colors: the foreground and background (OSC 10/11) and the scheme (CSI ? 997). */
  private colourReport(p: TerminalPalette) {
    return colourReports(p.foreground, p.background) + schemeReport(this.dark());
  }

  /** Takes the terminal's colors again (xterm answers OSC 10/11 from them); returns what the TUI would be told of them. */
  private recolor() {
    const next = this.theme();
    this.patch({ palette: next });
    if (this.term) this.term.options.theme = next;
    return (this.told = this.colourReport(next));
  }

  // ── the view on the hub ───────────────────────────────

  private linkUp() {
    return isOpen() && getState().conn === "online";
  }

  /** Opens the view when everything is there for it: xterm sized, the hub reachable, and not let go. */
  private begin() {
    if (this.disposed || this.released || this.id || !this.term) return;
    const wait = this.resumeAt - Date.now();
    if (wait > 0) {
      clearTimeout(this.openTimer);
      this.openTimer = window.setTimeout(() => this.begin(), wait);
      return;
    }
    if (!getState().features.includes("herdr_client")) return void this.patch({ status: "unsupported", note: tr("terminal-full.hub_unsupported") });
    if (!this.grid || !this.linkUp()) return; // the first measure or the hub's hello calls again
    this.id = `v${(++views).toString(36)}${Math.random().toString(36).slice(2, 8)}`;
    const id = this.id;
    this.sent = this.grid;
    this.ack = new AckTracker((bytes) => send({ type: "herdr_ack", id, bytes }));
    this.patch({ status: "connecting", note: "" });
    // herdr opens on the pane of the address (a page that got here by following herdr's focus is on it already)
    this.focused = this.target;
    focusGuard.consumeFollow(this.target);
    if (this.linkLost) {
      this.linkLost = false;
      this.term.reset(); // the client draws it all again
    }
    send({ type: "herdr_open", id, machine_id: this.mid, session_id: this.target, cols: this.grid.cols, rows: this.grid.rows, focus: true });
  }

  private onHello = () => {
    if (this.disposed || this.released) return;
    // the connection dropped and came back: the hub let go of the view with it; open another on a clean screen
    if (this.id || this.view.status === "live") {
      this.id = "";
      this.ack?.dispose();
      this.ack = null;
      this.term?.reset();
      this.patch({ status: "connecting", note: "", control: "off" });
    }
    this.begin();
  };

  /**
   * The hub link went away under an open view: the hub has already stopped the client with it (docs/integrations/terminal.md), and
   * keys sent now would be dropped. The view stops being live; the hub's hello opens another (onHello → begin).
   */
  private checkLink = () => {
    if (this.disposed || !this.id || this.linkUp()) return;
    this.id = "";
    this.ack?.dispose();
    this.ack = null;
    this.linkLost = true;
    this.patch({ status: "connecting", note: tr("session-terminal-feed.hub_unreachable"), control: "off" });
  };

  private onState = (msg: Record<string, unknown>) => {
    if (msg.id !== this.id || !this.id) return;
    this.goLive(Number(msg.cols), Number(msg.rows));
  };

  private goLive(cols: number, rows: number) {
    if (this.view.status === "live") return;
    this.everLive = true;
    this.patch({ status: "live", note: "", control: "active", cols: cols || this.view.cols, rows: rows || this.view.rows });
    this.term?.focus();
    this.hint();
    this.syncFocus(); // the page moved on while the client started
    if (this.grid && this.sent && (this.grid.cols !== this.sent.cols || this.grid.rows !== this.sent.rows)) this.measure(true); // it changed while the client started
  }

  private onData = (msg: Record<string, unknown>) => {
    if (msg.id !== this.id || !this.id || !this.term) return;
    const bytes = fullBytes(msg.data);
    if (!bytes) return;
    if (this.view.status === "connecting") this.goLive(this.grid?.cols ?? 0, this.grid?.rows ?? 0); // data can come ahead of the state message
    const ack = this.ack;
    this.term.write(bytes, () => ack?.noteParsed(bytes.length));
  };

  private onEnd = (msg: Record<string, unknown>) => {
    if (msg.id !== this.id || !this.id) return;
    const end = { error: typeof msg.error === "string" ? msg.error : undefined, code: typeof msg.code === "string" ? msg.code : undefined, unsupported: msg.unsupported === true };
    const status = endStatus(end);
    const note = endText(end);
    this.id = "";
    this.ack?.dispose();
    this.ack = null;
    this.term?.write(dimLine(note));
    this.patch({ status, note, control: "off", failedToStart: !this.everLive && end.code !== "offline" });
  };

  /** Said once in this browser: this surface shares herdr's layout with every other client. */
  private hint() {
    if (localStorage.getItem(HINT_KEY)) return;
    try {
      localStorage.setItem(HINT_KEY, "1");
    } catch {
      // not kept: it is said again next time
    }
    toast(tr("terminal-full.hint_toast"), "info");
  }

  // ── size ──────────────────────────────────────────────

  private measure(now = false) {
    clearTimeout(this.resizeTimer);
    const run = () => {
      if (this.disposed || !this.fit || !this.term) return;
      const g = fullGrid(this.fit.proposeDimensions());
      if (!g) return;
      this.grid = g;
      if (this.term.cols !== g.cols || this.term.rows !== g.rows) this.term.resize(g.cols, g.rows);
      this.patch({ cols: g.cols, rows: g.rows });
      if (this.id && (!this.sent || this.sent.cols !== g.cols || this.sent.rows !== g.rows) && this.view.status === "live") {
        this.sent = g;
        send({ type: "herdr_resize", id: this.id, cols: g.cols, rows: g.rows });
      }
      this.begin();
    };
    if (now || !this.grid) run();
    else this.resizeTimer = window.setTimeout(run, RESIZE_MS);
  }

  // ── input ─────────────────────────────────────────────

  /** Bytes for the client, as they are (the grid it draws in is the page's; keys, mouse reports and pastes all come here). */
  private raw(text: string) {
    if (!text || !this.id || this.view.status !== "live") return;
    for (let i = 0; i < text.length; ) {
      let end = Math.min(text.length, i + INPUT_CHUNK);
      const last = text.charCodeAt(end - 1);
      if (end < text.length && last >= 0xd800 && last <= 0xdbff) end--; // not between the halves of a surrogate pair
      send({ type: "herdr_input", id: this.id, data: text.slice(i, end) });
      i = end;
    }
  }

  /** What xterm makes of the keys typed on it; with the key bar's ctrl / alt / shift down, that key. */
  private type(data: string) {
    const m = this.view.mods;
    if (!modifiersActive(m)) return this.raw(data);
    const name = keyNameForBytes(data);
    if (name) {
      this.sendKey(withModifiers(m, name));
    } else if ([...data].length === 1) {
      const t = typedWithModifiers(m, data);
      if (t.key) this.sendKey(t.key);
      else if (t.text) this.raw(t.text);
    } else {
      this.raw(data);
    }
    this.patch({ mods: modifiersPressed(m) });
  }

  private sendKey(name: string) {
    const b = encodeKey(name, this.term?.modes.applicationCursorKeysMode ?? false);
    if (b) this.raw(b);
  }

  get driving() {
    return this.view.status === "live";
  }

  pressKey(name: string) {
    if (!this.driving) return;
    const m = this.view.mods;
    this.sendKey(withModifiers(m, name));
    this.patch({ mods: modifiersPressed(m) });
  }

  pressBare(name: string) {
    if (this.driving) this.sendKey(name);
  }

  tapModifier(k: ModifierKey, quick: boolean) {
    this.patch({ mods: modifierTapped(this.view.mods, k, quick) });
  }

  holdModifier(k: ModifierKey) {
    this.patch({ mods: modifierHeld(this.view.mods, k) });
  }

  releaseModifier(k: ModifierKey) {
    this.patch({ mods: modifierReleased(this.view.mods, k) });
  }

  /** A saved command: pasted into the client (bracketed when the program asked for that), and run with return when `run`. */
  typeText(text: string, run: boolean) {
    if (!this.driving || !text) return;
    this.term?.paste(text);
    if (run) this.raw("\r");
    this.term?.focus();
  }

  /** ⌘V / Ctrl+V with files on the clipboard (a screenshot): uploaded, the paths typed in. Text is xterm's own paste. */
  private pasted = (e: ClipboardEvent) => {
    const files = transferFiles(e.clipboardData);
    if (!files.length) return;
    e.preventDefault();
    e.stopPropagation();
    void this.transfer.uploadFiles(files);
  };

  /** Ctrl+V off the Mac (lib/terminal-conflicts.ts): files on the clipboard are uploaded, otherwise the ^V xterm would have sent. */
  private async ctrlV() {
    const files = await clipboardFiles();
    if (files.length) return void this.transfer.uploadFiles(files);
    this.raw("\x16");
  }

  /** The key bar's and the menu's paste: files on the clipboard (where the browser lets a page look), else the text, as ⌘V does. */
  async pasteClipboard() {
    if (!this.driving) return;
    const files = await clipboardFiles();
    if (files.length) return void this.transfer.uploadFiles(files);
    if (!navigator.clipboard?.readText) return void toast(tr("terminal-full.clipboard_https"));
    let text = "";
    try {
      text = (await navigator.clipboard.readText()) ?? "";
    } catch {
      return void toast(tr("terminal-full.clipboard_denied"));
    }
    if (!text) return void toast(tr("terminal-full.clipboard_empty"));
    this.term?.paste(text);
  }

  focus() {
    this.term?.focus();
  }

  /** Whether the program under it takes the mouse (herdr does, as soon as it starts). */
  get mouseTracked() {
    return (this.term?.modes.mouseTrackingMode ?? "none") !== "none";
  }

  /** Who a right click is for (lib/terminal-conflicts.ts): herdr's own menus while it has the mouse, Persona's with Shift. */
  rightClick(e: { shiftKey: boolean }, pointerType?: string): RightClick {
    return rightClickRoute(e, this.mouseTracked, pointerType);
  }

  /**
   * Keys the page takes before xterm: one of the app's shortcuts (the app takes it), a copy with text selected, shift+enter
   * as a new line, Mac line editing; an IME composition is left alone (keyDecision).
   */
  private key = (e: KeyboardEvent) => {
    if (!herdrOwnsKey(e) && passesToApp(e)) return false; // herdr's prefix (Ctrl+B off the Mac) is herdr's, not a shortcut here
    if (this.driving) {
      const bytes = lineEditBytes(e);
      if (bytes) {
        e.preventDefault();
        e.stopPropagation();
        this.raw(bytes);
        return false;
      }
    }
    if (this.driving && isCtrlV(e, currentPlatform())) {
      if (e.type === "keydown" && !e.repeat) void this.ctrlV();
      e.preventDefault();
      e.stopPropagation();
      return false;
    }
    const d = keyDecision(e, this.term?.hasSelection() ?? false);
    if (d === "xterm") return true;
    if (e.type === "keydown") {
      if (d === "copy") this.copySelection();
      else if (d === "newline") this.raw("\x1b\r");
    }
    e.preventDefault();
    e.stopPropagation();
    return false;
  };

  private openWebLink(e: MouseEvent, uri: string) {
    if (!linkOpens(e, this.mouseTracked, this.pointer)) return; // the click is herdr's (lib/terminal-conflicts.ts)
    const url = webLink(uri);
    if (url) openLink(url, undefined, getSession(this.mid, this.target)?.manager);
  }

  // ── the clipboard ─────────────────────────────────────

  copySelection() {
    const text = this.term?.getSelection() ?? "";
    if (!text) return void toast(tr("terminal-full.select_first_herdr"));
    void copyWith(text, browserClipboard()).then((outcome) => {
      const n = copyNotice(outcome, window.isSecureContext);
      toast(n.text, n.tone);
      if (outcome !== "failed") this.term?.clearSelection();
    });
  }

  copyText(text: string) {
    if (!text) return void toast(tr("terminal-full.select_first"));
    void copyWith(text, browserClipboard()).then((outcome) => {
      const n = copyNotice(outcome, window.isSecureContext);
      toast(n.text, n.tone);
    });
  }

  hasSelection() {
    return this.term?.hasSelection() ?? false;
  }

  selectedText() {
    return this.term?.getSelection() ?? "";
  }

  selectAll() {
    this.term?.selectAll();
  }

  clearSelection() {
    this.term?.clearSelection();
  }

  /** herdr copied something (OSC 52): onto the clipboard at once where the browser allows it, else the user taps "Copy" (it wants a gesture). */
  private programCopied(text: string) {
    const t = osc52Text(text);
    if (!t) return;
    void copyWith(t, browserClipboard()).then((outcome) => {
      if (outcome !== "failed") return toast(tr("terminal-full.copied"), "ok");
      toast(tr("terminal-full.copy_prompt"), "info", {
        label: tr("terminal-full.copy"),
        run: () =>
          void copyWith(t, browserClipboard()).then((o) => {
            const n = copyNotice(o, window.isSecureContext);
            toast(n.text, n.tone);
          }),
      });
    });
  }

  // ── find ──────────────────────────────────────────────

  private searchOptions() {
    return { caseSensitive: this.found.cs, decorations: findDecorations() };
  }

  /** Looks for `query` in xterm's screen and scrollback. dir 0 starts afresh from the newest; 1 / -1 step, wrapping around. */
  find(query: string, caseSensitive: boolean, dir: 0 | 1 | -1) {
    const search = this.search;
    if (!query || !search || !this.term) return this.clearFind();
    const fresh = dir === 0 || query !== this.found.query || caseSensitive !== this.found.cs;
    this.found = { query, cs: caseSensitive, busy: true };
    try {
      if (fresh) {
        this.term.clearSelection();
        search.clearDecorations(); // also forgets the last term: the addon does not notice a changed "Match case" on its own
        search.findPrevious(query, this.searchOptions());
      } else if (dir === -1) search.findPrevious(query, this.searchOptions());
      else search.findNext(query, this.searchOptions());
    } finally {
      this.found.busy = false;
    }
  }

  clearFind() {
    this.found = { query: "", cs: false, busy: false };
    this.search?.clearDecorations();
    this.patch({ find: null });
  }
}
