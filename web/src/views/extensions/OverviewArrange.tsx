// Arranging Overview (#9745, #10905, docs/web/web.md "Overview's extension cards"), loaded the first time the user
// presses Arrange Overview: the heading over it (what to do, where it is kept, Reset with Undo, Done), each card's
// controls in place of its head (the handle it is dragged by, its width, earlier / later, its switch) and each of
// Overview's own sections as a bar with the same controls (no width: a section takes the row; no switch for the ones
// that are only moved), which also make the card or the bar itself draggable onto another. The patterns are the
// sidebar's pages' (Settings › General › Sidebar pages, views/settings/SidebarPages.tsx): a drag with ↑ / ↓ beside it
// for the keyboard and touch, a switch to show or hide, a native select, the same footer about the hub.
import { useEffect, useRef } from "react";
import { t } from "../../i18n";
import type { UIPlace } from "../../lib/extension-ui";
import { ALWAYS_SHOWN, cardKey, getOverviewPrefs, overviewRevision, saveOverview, sectionKey, useOverviewKept, useOverviewPrefs, useSectionsLocal, type ArrangedItem, type CardSpan, type OverviewEntry, type SectionId } from "../../lib/overview-cards";
import { changeEntry, dropEntry, entriesOf, moveEntry, written } from "../../lib/overview-edit";
import { useStartPageSynced } from "../../lib/start-page";
import { stateName } from "../../lib/state-labels";
import { Button, cx, IconButton, Switch, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

const FIELD = "h-8 min-w-0 rounded-control bg-field px-2.5 text-meta text-ink shadow-inset-field outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)]";
// what can be dragged: a card (its element is ExtensionSlots' OverviewCard) or a section's bar
const ITEM = "[data-extension-card],[data-overview-section]";

type Items = readonly ArrangedItem<UIPlace>[];

/** Each section's name, as its heading on Overview has it (views/Home.tsx). */
const TITLE: Record<SectionId, () => string> = {
  needs_you: () => stateName("needs_you"),
  waiting_on_computer: () => t("home.waiting_on_computer"),
  in_progress: () => t("home.tasks"),
  working: () => stateName("working"),
  unread: () => stateName("unread"),
  computers: () => t("home.computers"),
};

/** The heading over Overview being arranged (`onDone`), one card's controls (`card`) or a section's bar (`section`). */
export function OverviewArrange({ items, card, section, onDone }: { items: Items; card?: UIPlace; section?: SectionId; onDone?: () => void }) {
  if (card) return <EditBar items={items} place={card} />;
  if (section) return <SectionBar items={items} section={section} />;
  return <EditHead onDone={() => onDone?.()} />;
}

/** Over Overview while it is arranged: what to do, whether every device follows, Reset (with Undo) and Done. */
function EditHead({ onDone }: { onDone: () => void }) {
  const prefs = useOverviewPrefs();
  const route = useStartPageSynced();
  const field = useOverviewKept();
  const synced = route !== false && field !== false;
  const sectionsHere = useSectionsLocal();
  // Undo puts back the arrangement from before only while Overview is still as Reset left it: one changed since (here,
  // in another tab, or on another device), even one reset again, is newer and stays
  const reset = () => {
    saveOverview([]);
    const at = overviewRevision();
    const undo = () => (overviewRevision() !== at ? toast(t("extensions-overviewarrange.undo_stale"), "info") : saveOverview(prefs));
    toast(t("extensions-overviewarrange.reset_done"), "info", { label: t("extensions-overviewarrange.undo"), run: undo });
  };
  return (
    <div data-overview-arrange className="flex flex-wrap items-start gap-x-3 gap-y-2">
      <div className="min-w-48 flex-1">
        <h2 className="text-meta font-medium text-ink">{t("extensions-overviewarrange.title")}</h2>
        <p className="mt-0.5 text-label leading-[1.5] text-ink-2">
          {t("extensions-overviewarrange.line", { hint: t("extensions-overviewarrange.hint"), where: !synced ? t("settings.start_page_footer_local") : sectionsHere ? t("extensions-overviewarrange.sections_local") : t("settings.start_page_footer_synced") })}
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

/** A change of the arrangement as it is drawn now (`items`): written with what this web does not draw kept. */
const change = (items: Items, f: (list: OverviewEntry[]) => OverviewEntry[]) => saveOverview(written(items, f(entriesOf(items)), getOverviewPrefs()));

// the card or section being dragged, between its dragstart and its dragend (one drag at a time in a page)
let dragging: string | null = null;

/** Makes the card or section `key` (the element `ref` is in) draggable onto another, which it then takes the place of,
 *  and marks the one dragged (`data-drag="from"`) and the one it would land on (`"over"`), which their classes draw. */
function useDrag(ref: React.RefObject<HTMLElement | null>, key: string, items: Items) {
  const latest = useRef(items);
  useEffect(() => {
    latest.current = items;
  });
  useEffect(() => {
    const el = ref.current?.closest<HTMLElement>(ITEM);
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
        const from = dragging;
        if (from && from !== key) change(latest.current, (l) => dropEntry(l, from, key));
      },
      dragend: () => {
        dragging = null;
        document.querySelectorAll<HTMLElement>("[data-extension-card][data-drag],[data-overview-section][data-drag]").forEach((c) => delete c.dataset.drag);
      },
    };
    el.draggable = true;
    for (const [type, f] of Object.entries(on)) el.addEventListener(type, f as EventListener);
    return () => {
      el.draggable = false;
      mark();
      for (const [type, f] of Object.entries(on)) el.removeEventListener(type, f as EventListener);
    };
  }, [ref, key]);
}

/** The handle a card or section is dragged by. */
const Grip = () => (
  <span aria-hidden className="cursor-grab text-ink-3 active:cursor-grabbing" title={t("extensions-overviewarrange.drag")}>
    <Icon name="grip" size={14} />
  </span>
);

/** Earlier / later for the keyboard and touch, the ends disabled. */
function Arrows({ items, at, name }: { items: Items; at: number; name: string }) {
  const key = items[at]?.key ?? "";
  return (
    <>
      <IconButton icon="up" label={t("extensions-overviewarrange.earlier", { name })} disabled={at <= 0} onClick={() => change(items, (l) => moveEntry(l, key, -1))} size={26} iconSize={13} />
      <IconButton icon="arrowDown" label={t("extensions-overviewarrange.later", { name })} disabled={at === items.length - 1} onClick={() => change(items, (l) => moveEntry(l, key, 1))} size={26} iconSize={13} />
    </>
  );
}

/** A card's controls while Overview is arranged, in place of its head: its handle and name, its width, earlier /
 *  later, and its switch (off: hidden, so Overview draws nothing of it and does not ask for it). */
function EditBar({ items, place }: { items: Items; place: UIPlace }) {
  const key = cardKey(place);
  const at = items.findIndex((c) => c.key === key);
  const item = items[at];
  const { hidden, span } = item && "card" in item ? item : { hidden: false, span: "half" as CardSpan };
  const ref = useRef<HTMLDivElement>(null);
  useDrag(ref, key, items);
  const name = place.title;
  return (
    // the name wraps beside the controls on a wide card and the controls go under it on a narrow one, rather than
    // cutting it short
    <div ref={ref} className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1.5 px-3 pt-2.5 pb-1">
      <Grip />
      <div className="min-w-24 flex-1">
        <div className={cx("truncate text-meta font-medium", hidden ? "text-ink-3" : "text-ink")}>{name}</div>
        {(hidden || place.extension_name) && <div className="truncate text-submeta text-ink-3">{hidden ? t("extensions-overviewarrange.hidden") : place.extension_name}</div>}
      </div>
      <div className="ml-auto flex items-center gap-1.5">
        <select value={span} onChange={(e) => change(items, (l) => changeEntry(l, key, { span: e.target.value === "full" ? "full" : "half" }))} aria-label={t("extensions-overviewarrange.width", { name })} className={cx(FIELD, "max-w-32")}>
          <option value="half">{t("extensions-overviewarrange.half")}</option>
          <option value="full">{t("extensions-overviewarrange.full")}</option>
        </select>
        <Arrows items={items} at={at} name={name} />
        <Switch on={!hidden} onChange={(v) => change(items, (l) => changeEntry(l, key, { hidden: !v }))} label={t("extensions-overviewarrange.show", { name })} />
      </div>
    </div>
  );
}

/** One of Overview's own sections while Overview is arranged (#10905), in its place: a bar, the row's width, with its
 *  handle and name, earlier / later and its switch; a hidden one as its outline. Needs you and Stuck have no switch:
 *  they are only moved (ALWAYS_SHOWN), and the bar says so. */
function SectionBar({ items, section }: { items: Items; section: SectionId }) {
  const key = sectionKey(section);
  const title = TITLE[section]();
  const at = items.findIndex((c) => c.key === key);
  const hidden = items[at]?.hidden === true;
  const fixed = ALWAYS_SHOWN.includes(section);
  const ref = useRef<HTMLDivElement>(null);
  useDrag(ref, key, items);
  return (
    <div
      ref={ref}
      data-overview-section={section}
      className={cx(
        "flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1.5 rounded-card px-3 py-2.5",
        hidden ? "shadow-hairline" : "bg-surface shadow-card",
        "data-[drag=from]:opacity-50 data-[drag=over]:shadow-[inset_0_0_0_2px_var(--accent)]",
      )}
    >
      <Grip />
      <div className="min-w-24 flex-1">
        <h2 className={cx("truncate text-meta font-medium", hidden ? "text-ink-3" : "text-ink")}>{title}</h2>
        {(hidden || fixed) && <div className="truncate text-submeta text-ink-3">{fixed ? t("extensions-overviewarrange.always_shown") : t("extensions-overviewarrange.section_hidden")}</div>}
      </div>
      <div className="ml-auto flex items-center gap-1.5">
        <Arrows items={items} at={at} name={title} />
        {fixed ? (
          // where the others' switch is, so every bar's arrows line up: not drawn, nor read out or reached
          <span aria-hidden className="invisible flex">
            <Switch on onChange={() => {}} label="" />
          </span>
        ) : (
          <Switch on={!hidden} onChange={(v) => change(items, (l) => changeEntry(l, key, { hidden: !v }))} label={t("extensions-overviewarrange.show", { name: title })} />
        )}
      </div>
    </div>
  );
}
