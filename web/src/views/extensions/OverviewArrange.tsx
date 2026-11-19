// Arranging Overview's extension cards (#9745, docs/web/web.md "Overview's extension cards"), loaded the first time the
// user presses Arrange cards: the heading over the cards (what to do, where it is kept, Reset with Undo, Done) and each
// card's controls in place of its head (the handle it is dragged by, its width, earlier / later, its switch), which
// also make the card itself draggable onto another. The patterns are the sidebar's pages' (Settings › General ›
// Sidebar pages, views/settings/SidebarPages.tsx): a drag with ↑ / ↓ beside it for the keyboard and touch, a switch to
// show or hide, a native select, the same footer about the hub.
import { useEffect, useRef } from "react";
import { t } from "../../i18n";
import type { UIPlace } from "../../lib/extension-ui";
import { cardKey, overviewRevision, saveOverview, useOverviewKept, useOverviewPrefs, type ArrangedCard, type CardSpan, type OverviewEntry } from "../../lib/overview-cards";
import { changeEntry, dropEntry, entriesOf, moveEntry } from "../../lib/overview-edit";
import { useStartPageSynced } from "../../lib/start-page";
import { Button, cx, IconButton, Switch, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

const FIELD = "h-8 min-w-0 rounded-control bg-field px-2.5 text-meta text-ink shadow-inset-field outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)]";

type Cards = readonly ArrangedCard<UIPlace>[];

/** The heading over the cards being arranged (`onDone`), or one card's controls (`card`). */
export function OverviewArrange({ cards, card, onDone }: { cards: Cards; card?: UIPlace; onDone?: () => void }) {
  return card ? <EditBar cards={cards} place={card} /> : <EditHead onDone={() => onDone?.()} />;
}

/** Over the cards while they are arranged: what to do, whether every device follows, Reset (with Undo) and Done. */
function EditHead({ onDone }: { onDone: () => void }) {
  const prefs = useOverviewPrefs();
  const route = useStartPageSynced();
  const field = useOverviewKept();
  const synced = route !== false && field !== false;
  // Undo puts back the arrangement from before only while the cards are still as Reset left them: one changed since
  // (here, in another tab, or on another device), even one reset again, is newer and stays
  const reset = () => {
    saveOverview([]);
    const at = overviewRevision();
    const undo = () => (overviewRevision() !== at ? toast(t("extensions-overviewarrange.undo_stale"), "info") : saveOverview(prefs));
    toast(t("extensions-overviewarrange.reset_done"), "info", { label: t("extensions-overviewarrange.undo"), run: undo });
  };
  return (
    <div data-overview-arrange className="col-span-full flex flex-wrap items-start gap-x-3 gap-y-2">
      <div className="min-w-48 flex-1">
        <h2 className="text-meta font-medium text-ink">{t("extensions-overviewarrange.title")}</h2>
        <p className="mt-0.5 text-label leading-[1.5] text-ink-2">
          {t("extensions-overviewarrange.line", { hint: t("extensions-overviewarrange.hint"), where: synced ? t("settings.start_page_footer_synced") : t("settings.start_page_footer_local") })}
        </p>
      </div>
      <div className="ml-auto flex items-center gap-2">
        {prefs.length > 0 && (
          <Button tone="quiet" size="sm" onClick={reset}>
            {t("extensions-overviewarrange.reset")}
          </Button>
        )}
        <Button tone="primary" size="sm" onClick={onDone}>
          {t("extensions-overviewarrange.done")}
        </Button>
      </div>
    </div>
  );
}

// the card being dragged, between its dragstart and its dragend (one drag at a time in a page)
let dragging: string | null = null;

/** A card's controls while the cards are arranged, in place of its head: its handle and name, its width, earlier /
 *  later, and its switch (off: hidden, so Overview draws nothing of it and does not ask for it). It makes its card
 *  draggable onto another, which it then takes the place of, and marks the card dragged (`data-drag="from"`) and the
 *  one it would land on (`"over"`), which the card's own classes draw. */
function EditBar({ cards, place }: { cards: Cards; place: UIPlace }) {
  const key = cardKey(place);
  const at = cards.findIndex((c) => c.key === key);
  const { hidden, span } = cards[at] ?? { hidden: false, span: "half" as CardSpan };
  const change = (f: (list: OverviewEntry[]) => OverviewEntry[]) => saveOverview(f(entriesOf(cards)));
  const ref = useRef<HTMLDivElement>(null);
  const latest = useRef(cards);
  useEffect(() => {
    latest.current = cards;
  });
  useEffect(() => {
    const el = ref.current?.closest<HTMLElement>("[data-extension-card]");
    if (!el) return;
    const mark = (to?: "from" | "over") => {
      if (to) el.dataset.drag = to;
      else delete el.dataset.drag;
    };
    const on: Record<string, (e: DragEvent) => void> = {
      dragstart: (e) => {
        dragging = key;
        mark("from");
        e.dataTransfer?.setData("text/plain", key);
        if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
      },
      dragover: (e) => {
        if (!dragging || dragging === key) return;
        e.preventDefault();
        mark("over");
      },
      dragleave: () => {
        if (el.dataset.drag === "over") mark();
      },
      drop: (e) => {
        e.preventDefault();
        if (el.dataset.drag === "over") mark();
        if (dragging && dragging !== key) saveOverview(dropEntry(entriesOf(latest.current), dragging, key));
      },
      dragend: () => {
        dragging = null;
        document.querySelectorAll<HTMLElement>("[data-extension-card][data-drag]").forEach((c) => delete c.dataset.drag);
      },
    };
    el.draggable = true;
    for (const [type, f] of Object.entries(on)) el.addEventListener(type, f as EventListener);
    return () => {
      el.draggable = false;
      mark();
      for (const [type, f] of Object.entries(on)) el.removeEventListener(type, f as EventListener);
    };
  }, [key]);
  const name = place.title;
  return (
    // the name wraps beside the controls on a wide card and the controls go under it on a narrow one, rather than
    // cutting it short
    <div ref={ref} className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1.5 px-3 pt-2.5 pb-1">
      <span aria-hidden className="cursor-grab text-ink-3 active:cursor-grabbing" title={t("extensions-overviewarrange.drag")}>
        <Icon name="grip" size={14} />
      </span>
      <div className="min-w-24 flex-1">
        <div className={cx("truncate text-meta font-medium", hidden ? "text-ink-3" : "text-ink")}>{name}</div>
        {(hidden || place.extension_name) && <div className="truncate text-submeta text-ink-3">{hidden ? t("extensions-overviewarrange.hidden") : place.extension_name}</div>}
      </div>
      <div className="ml-auto flex items-center gap-1.5">
        <select value={span} onChange={(e) => change((l) => changeEntry(l, key, { span: e.target.value === "full" ? "full" : "half" }))} aria-label={t("extensions-overviewarrange.width", { name })} className={cx(FIELD, "max-w-32")}>
          <option value="half">{t("extensions-overviewarrange.half")}</option>
          <option value="full">{t("extensions-overviewarrange.full")}</option>
        </select>
        <IconButton icon="up" label={t("extensions-overviewarrange.earlier", { name })} disabled={at <= 0} onClick={() => change((l) => moveEntry(l, key, -1))} size={26} iconSize={13} />
        <IconButton icon="arrowDown" label={t("extensions-overviewarrange.later", { name })} disabled={at === cards.length - 1} onClick={() => change((l) => moveEntry(l, key, 1))} size={26} iconSize={13} />
        <Switch on={!hidden} onChange={(v) => change((l) => changeEntry(l, key, { hidden: !v }))} label={t("extensions-overviewarrange.show", { name })} />
      </div>
    </div>
  );
}
