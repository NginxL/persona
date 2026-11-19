// The Inbox's section bar (docs/assistant/inbox.md "Section bar", #9053): one row of choices over the list, drawn as the
// Agents list's quick filters are (Sidebar's FilterBar; a pixel less padding, so all five fit the wide page's 380px list
// column in English and Chinese), with Needs you's count in its blue, In progress's and FYI's in the neutral tone, and
// the grouping in a menu at the row's end. The sections scroll sideways when they don't fit, faded at the edge with more
// and moved by a mouse's wheel too, while All, the reset, stays in sight beside the grouping, as iOS keeps its capsule's
// trailing control; the arrow keys, Home and End move along it and choose, as in a radio group.
import { useCallback, useEffect, useLayoutEffect, useRef, useState, type MouseEventHandler, type RefObject } from "react";
import { t } from "../../i18n";
import { TIERS } from "../../lib/inbox";
import { type BarChoice, type BarEntry } from "../../lib/inbox-bar";
import type { Grouping } from "../../lib/inbox-groups";
import { cx, Menu, useMenu } from "../../ui/atoms";
import { CountBadge } from "../../ui/CountBadge";
import { nextTab } from "../../ui/roving";

// How far the row fades at each edge, in px: edgeMask's widths (views/chat/edge-fade.ts), which InboxList.bar.test.tsx
// keeps the same. A copy, not an import: edge-fade is in the first screen's chunk, which then changed with the bar.
const FADE = { left: 28, right: 44 } as const;

/** A choice's name: the section's title, Done, or All. */
export function barChoiceName(id: BarChoice): string {
  if (id === "done") return t("inbox-inboxlist.done");
  if (id === "all") return t("inbox-inboxlist.bar_all");
  return TIERS.find((tier) => tier.key === id)?.title ?? id;
}

/** The fade over a row's edges with `left` / `right` px of it scrolled past them: each fades over as much, up to the usual
 *  fade (edge-fade), so a row a few px too long keeps what is in sight legible, and a pill half out reads as going on. */
export function barFade(left: number, right: number): string | undefined {
  const l = Math.min(left, FADE.left);
  const r = Math.min(right, FADE.right);
  return l > 0 || r > 0 ? `linear-gradient(90deg,transparent,#000 ${l}px,#000 calc(100% - ${r}px),transparent)` : undefined;
}

/** How many px of `ref`'s row lie past its left and right edges, measured as it scrolls, resizes and `key` changes. */
function useBeyond(ref: RefObject<HTMLElement | null>, key: string): { left: number; right: number; measure: () => void } {
  const [beyond, setBeyond] = useState({ left: 0, right: 0 });
  const measure = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    const left = Math.max(0, Math.round(el.scrollLeft));
    const right = Math.max(0, Math.round(el.scrollWidth - el.clientWidth - el.scrollLeft));
    setBeyond((b) => (b.left === left && b.right === right ? b : { left, right }));
  }, [ref]);
  useLayoutEffect(measure, [measure, key]);
  useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref, measure]);
  return { ...beyond, measure };
}

// How a list's rows are grouped (Lucide's list-tree), not which show: apart from the filter mark, as iOS keeps its
// capsule's grouping control apart from its filter (DESIGN_SYSTEM 3.4 "List controls").
const GROUPING_MARK = "M21 12h-8M21 6H8M21 18h-8M3 6v4c0 1.1.9 2 2 2h3M3 10v6c0 1.1.9 2 2 2h3";

/** The grouping menu's button: IconButton's look and size, the menu said to assistive technology, drawn here as the
 *  Agents list draws its own list menu button (Sidebar's ListMenuButton). Not IconButton with a mark in the icon set:
 *  both are on every first screen (src/startup-preload.ts FIRST_SCREEN_BUDGET), and only this page needs them. */
function GroupingButton({ open, onClick }: { open: boolean; onClick: MouseEventHandler<HTMLButtonElement> }) {
  const label = t("inbox-inboxlist.grouping");
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      aria-haspopup="menu"
      aria-expanded={open}
      onClick={onClick}
      className={cx(
        "touch-hit flex shrink-0 items-center justify-center rounded-[8px] transition-[background-color,color,transform] duration-150 enabled:active:scale-[0.94]",
        open ? "bg-hover text-ink" : "text-ink-3 enabled:hover:bg-hover enabled:hover:text-ink",
      )}
      style={{ width: 24, height: 24 }}
    >
      <svg width={13} height={13} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.9} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <path d={GROUPING_MARK} />
      </svg>
    </button>
  );
}

/** `grouping`: the list's grouping, chosen in the menu at the row's end; `groupable`: false where the choice is not grouped
 *  (Done, In progress), which hides the menu's button (out of sight, the keyboard's and screen readers' reach) but keeps
 *  its place, so All doesn't move under the pointer between choices. */
export function InboxBar({ entries, chosen, onPick, grouping, groupable, onGrouping }: { entries: BarEntry[]; chosen: BarChoice; onPick: (choice: BarChoice) => void; grouping: Grouping; groupable: boolean; onGrouping: (g: Grouping) => void }) {
  const pills = useRef<(HTMLButtonElement | null)[]>([]);
  const strip = useRef<HTMLDivElement>(null);
  const menu = useMenu();
  const beyond = useBeyond(strip, `${entries.map((e) => `${e.id}:${e.count}`).join()}|${chosen}`);
  const mask = barFade(beyond.left, beyond.right);
  // the choice made (a tap, an arrow key, the list moving to an opened item) in sight in the row, clear of its fades
  useLayoutEffect(() => {
    const box = strip.current;
    const pill = box?.querySelector<HTMLElement>(`[data-bar-choice="${chosen}"]`);
    if (!box || !pill) return;
    const b = box.getBoundingClientRect();
    const r = pill.getBoundingClientRect();
    if (r.left < b.left + FADE.left) box.scrollLeft -= b.left + FADE.left - r.left;
    else if (r.right > b.right - FADE.right) box.scrollLeft += r.right - (b.right - FADE.right);
  }, [chosen]);
  const pill = (e: BarEntry) => {
    const i = entries.indexOf(e);
    const on = e.id === chosen;
    const name = barChoiceName(e.id);
    const n = e.count ?? 0;
    return (
      <button
        key={e.id}
        ref={(el) => {
          pills.current[i] = el;
        }}
        type="button"
        role="radio"
        aria-checked={on}
        // one stop for the keyboard: the chosen one; the arrows move along the row
        tabIndex={on ? 0 : -1}
        // the count is part of the name ("Needs you, 12"); the badge itself is not read twice
        aria-label={n > 0 ? t("inbox-inboxlist.bar_count", { name, count: n }) : undefined}
        data-bar-choice={e.id}
        onClick={() => onPick(e.id)}
        onKeyDown={(ev) => {
          const to = nextTab(i, ev.key, entries.length);
          if (to === undefined) return;
          ev.preventDefault();
          pills.current[to]?.focus();
          if (to !== i) onPick(entries[to]!.id);
        }}
        className={cx(
          "touch-hit relative z-10 flex h-6 shrink-0 items-center gap-1 whitespace-nowrap rounded-full px-1.5 text-submeta transition-[background-color,color,box-shadow] duration-150",
          on ? "bg-surface font-medium text-ink shadow-btn" : "text-ink-2 hover:bg-hover hover:text-ink",
        )}
      >
        {name}
        {e.tone && <CountBadge count={n} tone={e.tone} label={false} />}
      </button>
    );
  };
  const all = entries.find((e) => e.id === "all");
  return (
    // 4 px into the list's side padding at the end: the grouping button's mark, not its box, ends where the search field does
    <div className="-mr-1 flex items-center gap-0.5" data-inbox-bar>
      <div role="radiogroup" aria-label={t("inbox-inboxlist.sections")} className="flex min-w-0 flex-1 items-center gap-0.5">
        {/* room around the pills for their touch targets (44 px around 24) and the keyboard's focus ring, which a sideways
            scroller would clip, given back by the negative margin so the row sits where it would (12 px above and below:
            classes the first screen's CSS already has) */}
        <div
          ref={strip}
          onScroll={beyond.measure}
          // a mouse's wheel turns up and down: the row goes sideways with it (a trackpad's sideways swipe already does)
          onWheel={(ev) => {
            const box = strip.current;
            if (box && box.scrollWidth > box.clientWidth && Math.abs(ev.deltaY) > Math.abs(ev.deltaX)) box.scrollLeft += ev.deltaY * (ev.deltaMode === 1 ? 16 : 1);
          }}
          style={mask ? { maskImage: mask, WebkitMaskImage: mask } : undefined}
          className="-mx-1 -mt-3 -mb-3 flex min-w-0 items-center gap-0.5 overflow-x-auto px-1 py-3 scrollbar-none"
          data-bar-strip
        >
          {entries.filter((e) => e.id !== "all").map(pill)}
        </div>
        {all && pill(all)}
      </div>
      <div inert={!groupable} className={cx("flex shrink-0 items-center gap-0.5", !groupable && "invisible")} data-bar-grouping>
        <span aria-hidden className="h-4 w-px shrink-0 bg-line-strong" />
        <GroupingButton open={Boolean(menu.anchor)} onClick={menu.open} />
      </div>
      {menu.anchor && groupable && (
        <Menu anchor={menu.anchor} onClose={menu.close} align="right" width={180} header={t("inbox-inboxlist.grouping")}
          items={(["source", "time"] as const).map((g) => ({
            key: g,
            label: g === "source" ? t("inbox-inboxlist.by_source") : t("inbox-inboxlist.by_time"),
            radio: true,
            checked: grouping === g,
            onSelect: () => onGrouping(g),
          }))} />
      )}
    </div>
  );
}
