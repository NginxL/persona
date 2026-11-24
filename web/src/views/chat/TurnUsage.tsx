// What a finished exchange cost, after its time on the reply bar (#5426, cindy's MessageActionBar usage at the owner's
// request, 2026-11-12): "Worth $0.12" in an Agent session, its card (cindy's UsageCostPopover) opening 250 ms into a
// hover, held while the pointer is on it and closing 220 ms after it leaves, pinned by a click, closed by Esc or a click
// elsewhere, opened by keyboard focus. Without a cost, the tokens ("23.4k tokens"), their lines in cindy's tooltip
// (500 ms into a hover or at keyboard focus, gone when the pointer leaves). A Persona conversation (super rules 0 and 4)
// says only "≈ $0.12" and what that is in plain words; without a cost it says nothing. On a touch screen it ends the
// reply's copy + ⋯ row (MessageActions): a tap opens the card or the tooltip, a second tap closes it. Lines and prices:
// lib/turn-cost.ts.
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type RefObject } from "react";
import { createPortal } from "react-dom";
import { t } from "../../i18n";
import { compactTokens } from "../../lib/tokens";
import { formatCost, parseUsage, personaLines, turnCost, usageLines, usageTotals, usePrices } from "../../lib/turn-cost";
import { cx, EASE, EXIT_MS, useExit } from "../../ui/atoms";
import { placeMenu, useEscapeClose } from "../../ui/overlay";

type Mode = "closed" | "hover" | "pinned";

/** cindy's UsageCostPopover: the hover that opens the card, and how long it stays once the pointer has left. */
const CARD_OPEN_MS = 250;
const CARD_CLOSE_MS = 220;
/** cindy's tooltip delay. */
const TIP_OPEN_MS = 500;
/** cindy's card: 320 wide, 12 px inside the screen's edges, 4 below the text (its popover's offset); the tooltip 6 above. */
const CARD_WIDTH = 320;
const CARD_MARGIN = 12;
const CARD_GAP = 4;
const TIP_GAP = 6;
const EDGE = 8;

/** The bar's text after the time: cindy's 12 px, the time's color, 6 px after it, half a pixel down to line up with
 *  the icons; on the touch row it follows the row's own gap. */
const TEXT = "relative top-[0.5px] inline-flex h-6 cursor-default items-center whitespace-nowrap text-[12px] text-ink-3";

export function TurnUsage({ usage: key, ms, ttftMs, persona, touch, onOpen }: {
  /** lib/turn-usage.ts usageKey. */
  usage: string;
  /** The exchange's work time, the work summary's "Worked for" (absent when it has none, or several). */
  ms?: number;
  /** First observed streaming TTFT in ms for this exchange (proto.Item.TTFTMS, #12542). */
  ttftMs?: number;
  /** A Persona conversation: the cost in plain words, or nothing. */
  persona?: boolean;
  /** The touch screen's row: a tap opens the card. */
  touch?: boolean;
  /** The card is open (the bar stays while it is). */
  onOpen?: (open: boolean) => void;
}) {
  const usage = useMemo(() => parseUsage(key), [key]);
  // a model without a name or at the fast speed has no price: then neither has the exchange, and nothing is asked
  const ids = useMemo(() => (usage?.models.every((m) => m.model && !m.fast) ? [...new Set(usage.models.map((m) => m.model))] : []), [usage]);
  const { ready, price } = usePrices(ids);
  if (!usage || !ready) return null;
  const cost = turnCost(usage.models, price);
  const priced = cost && cost.total > 0 ? cost : null;
  if (persona && !priced) return null;
  const total = usageTotals(usage.models).total;
  if (!priced && total <= 0) return null;
  const label = priced ? t(persona ? "chat-replybar.approx" : "chat-replybar.worth", { cost: formatCost(priced.total) }) : t("chat-replybar.tokens", { tokens: compactTokens(total) });
  const lines = persona && priced ? personaLines(priced) : usageLines(usage, priced, ms, ttftMs);
  const title = persona ? t("chat-replybar.persona_title") : t("chat-replybar.details_title");
  return <UsageTrigger label={label} lines={lines} title={title} tip={!priced} touch={touch} onOpen={onOpen} />;
}

function UsageTrigger({ label, lines, title, tip, touch, onOpen }: { label: string; lines: string[]; title: string; tip: boolean; touch?: boolean; onOpen?: (open: boolean) => void }) {
  const button = useRef<HTMLButtonElement>(null);
  const card = useRef<HTMLDivElement>(null);
  const [mode, setMode] = useState<Mode>("closed");
  const timer = useRef<number | undefined>(undefined);
  const pressed = useRef(false);
  const id = useId();
  const open = mode !== "closed";
  const clear = () => clearTimeout(timer.current);
  const close = () => {
    clear();
    setMode("closed");
  };
  // the card's pointer has left: it goes after a moment unless pinned (the tooltip goes at once)
  const leave = () => {
    clear();
    if (tip) setMode((m) => (m === "pinned" ? m : "closed"));
    else timer.current = window.setTimeout(() => setMode((m) => (m === "pinned" ? m : "closed")), CARD_CLOSE_MS);
  };
  useEffect(() => () => clearTimeout(timer.current), []);
  useEffect(() => {
    if (!open) return;
    onOpen?.(true);
    return () => onOpen?.(false); // closed, or gone while open
  }, [open, onOpen]);
  useEscapeClose(open, close);
  useEffect(() => {
    if (!open) return;
    // a press anywhere else closes it, pinned or not (cindy's popover); so does leaving the window while only hovered
    const down = (e: PointerEvent) => {
      const at = e.target as Node;
      if (!button.current?.contains(at) && !card.current?.contains(at)) close();
    };
    const away = () => setMode((m) => (m === "hover" ? "closed" : m));
    document.addEventListener("pointerdown", down, true);
    addEventListener("blur", away);
    return () => {
      document.removeEventListener("pointerdown", down, true);
      removeEventListener("blur", away);
    };
  }, [open]);

  return (
    <>
      <button
        ref={button}
        type="button"
        className={cx(TEXT, touch ? "touch-hit" : "ml-1.5")}
        data-turn-usage={tip ? "tokens" : "cost"}
        aria-haspopup={tip ? undefined : "dialog"}
        aria-expanded={tip ? undefined : open}
        aria-controls={!tip && open ? id : undefined}
        aria-describedby={tip && open ? id : undefined}
        onPointerEnter={(e) => {
          if (e.pointerType !== "mouse") return;
          clear();
          timer.current = window.setTimeout(() => setMode((m) => (m === "closed" ? "hover" : m)), tip ? TIP_OPEN_MS : CARD_OPEN_MS);
        }}
        onPointerLeave={(e) => {
          if (e.pointerType === "mouse") leave();
        }}
        onPointerDown={() => {
          pressed.current = true;
          addEventListener("pointerup", () => (pressed.current = false), { once: true });
        }}
        // the keyboard opens it; a press's focus opens the card at once (cindy's popover), never the tooltip
        onFocus={() => {
          if (!tip || !pressed.current) setMode((m) => (m === "closed" ? "hover" : m));
        }}
        onBlur={leave}
        onClick={(e) => {
          e.stopPropagation();
          clear();
          // a click pins the card; the tooltip closes, as a tooltip does. A finger has no hover: a tap holds either, and
          // the next tap on the text lets it go
          if (touch) setMode((m) => (m === "pinned" ? "closed" : "pinned"));
          else setMode(tip ? "closed" : "pinned");
        }}
      >
        {label}
      </button>
      <UsageCard
        cardRef={card}
        id={id}
        anchor={button}
        open={open}
        pinned={mode === "pinned"}
        tip={tip}
        title={title}
        lines={lines}
        onPointerEnter={tip ? undefined : clear}
        onPointerLeave={tip ? undefined : leave}
      />
    </>
  );
}

interface Place {
  left: number;
  top: number;
  width?: number;
  below: boolean;
}

/** Where the card goes: under the text from its start, above it when there is no room below it in the chat (the last
 *  reply sits on the composer, which the card should not cover); the tooltip above the text's middle, under it when
 *  there is no room above. Placed again when the window or anything that scrolls moves. */
function usePlace(shown: boolean, anchor: RefObject<HTMLElement | null>, card: RefObject<HTMLDivElement | null>, tip: boolean, lines: string): Place | null {
  const [place, setPlace] = useState<Place | null>(null);
  useLayoutEffect(() => {
    const el = anchor.current;
    if (!shown || !el) {
      setPlace(null);
      return;
    }
    const measure = () => {
      const box = card.current;
      if (!box) return;
      const r = el.getBoundingClientRect();
      const screenW = document.documentElement.clientWidth || innerWidth;
      if (tip) {
        const w = box.offsetWidth, h = box.offsetHeight;
        const left = Math.max(EDGE, Math.min(r.left + r.width / 2 - w / 2, screenW - w - EDGE));
        const above = r.top - h - TIP_GAP;
        setPlace(above >= EDGE ? { left, top: above, below: false } : { left, top: r.bottom + TIP_GAP, below: true });
        return;
      }
      const width = Math.min(CARD_WIDTH, screenW - 2 * CARD_MARGIN);
      box.style.width = `${width}px`;
      const chat = el.closest("[data-chat-scroll]")?.getBoundingClientRect();
      const screenH = chat ? Math.min(innerHeight, chat.bottom) : innerHeight;
      const at = placeMenu(r, box.offsetHeight, { width, align: "left", side: "bottom", screenW, screenH, gap: CARD_GAP });
      setPlace({ left: at.left, top: at.top, width: at.width, below: at.top >= r.bottom });
    };
    measure();
    addEventListener("resize", measure);
    addEventListener("scroll", measure, { capture: true, passive: true });
    return () => {
      removeEventListener("resize", measure);
      removeEventListener("scroll", measure, { capture: true });
    };
  }, [shown, anchor, card, tip, lines]);
  return shown ? place : null;
}

function UsageCard({ cardRef: ref, id, anchor, open, pinned, tip, title, lines, onPointerEnter, onPointerLeave }: {
  cardRef: RefObject<HTMLDivElement | null>;
  id: string;
  anchor: RefObject<HTMLElement | null>;
  open: boolean;
  pinned: boolean;
  tip: boolean;
  title: string;
  lines: string[];
  onPointerEnter?: () => void;
  onPointerLeave?: () => void;
}) {
  const exit = useExit(open, EXIT_MS.card, () => [ref.current?.animate([{ opacity: 1 }, { opacity: 0 }], { duration: EXIT_MS.card, easing: "ease-in", fill: "forwards" })]);
  const text = lines.join("\n");
  const place = usePlace(exit.mounted, anchor, ref, tip, text);
  if (!exit.mounted) return null;
  // measured hidden first, then shown where it fits
  const at: CSSProperties = place ? { left: place.left, top: place.top } : { left: 0, top: 0, visibility: "hidden" };
  const state = exit.leaving ? "leaving" : pinned ? "pinned" : "hover";
  return createPortal(
    tip ? (
      <div
        ref={ref}
        id={id}
        role="tooltip"
        className="pointer-events-none fixed z-50 w-max max-w-[min(420px,calc(100vw-16px))] rounded-xl bg-tooltip-bg px-2.5 py-1.5 text-[13px] leading-snug whitespace-pre-line text-tooltip-fg shadow-overlay select-none [overflow-wrap:anywhere]"
        style={{ ...at, animation: place ? `fade-in 160ms ${EASE} both` : undefined }}
        data-usage-card={state}
      >
        {text}
      </div>
    ) : (
      <div
        ref={ref}
        id={id}
        role="dialog"
        aria-label={title}
        onPointerEnter={onPointerEnter}
        onPointerLeave={onPointerLeave}
        onFocusCapture={onPointerEnter}
        className="fixed z-50 max-h-[min(70vh,520px)] overflow-y-auto rounded-xl border border-line bg-surface p-4 text-[12px] leading-[1.5] whitespace-pre-line text-ink-2 shadow-overlay [overflow-wrap:anywhere]"
        style={{ ...at, width: place?.width ?? CARD_WIDTH, animation: place ? `pop-in 160ms ${EASE} both` : undefined, transformOrigin: place?.below === false ? "bottom left" : "top left" }}
        data-usage-card={state}
      >
        {text}
      </div>
    ),
    document.body,
  );
}
