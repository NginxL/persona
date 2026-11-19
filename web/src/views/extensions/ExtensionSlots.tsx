// The extensions' places among the web's own pages (docs/extensions/extensions.md "UI slots"): a nav_page's full page (/extension/<id>/<ui>,
// its entry is on the sidebar), the cards on "Overview" (overview_card) and the sections on a machine's page
// (machine_section). Each draws the extension's declarative View with ExtensionUI; the list comes from lib/extension-slots.ts.
import { Suspense, useEffect, useId, useMemo, useRef, useState } from "react";
import { useFolded } from "../../lib/use-folded";
import { uiIcon, type UIIcon, type UIPlace, type UITone } from "../../lib/extension-ui";
import { useExtensionPlace, useExtensionSlot } from "../../lib/extension-slots";
import { href, navigate, onLinkClick, type Route } from "../../lib/router";
import { useKnownDefault } from "../../lib/manager-choice";
import { useStore } from "../../lib/store";
import { t } from "../../i18n";
import { cx, EASE, IconButton, Loading, Spinner } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { lazyPage } from "../../lib/lazy-page";
import { NEW_ICON_GLYPH } from "./icon-map";
import { ExtensionOwnerScope } from "../../lib/extension-owner";

// The extension page is its own chunk (#1794): App.tsx reaches this file for the sidebar's and the nav page's icons and
// titles, and the first screen draws no extension UI. A page, a card and a section each wait under their own Suspense
// (the page keeps its top bar) with the Loading that ExtensionUI shows while it asks for its view.
const ExtensionUI = lazyPage(() => import("../settings/ExtensionView").then((m) => m.ExtensionUI));

// Whose extension is being drawn (ExtensionView knows it, the items it draws do not): the graph's per-key store is
// scoped by it through a context (lib/extension-owner.tsx), so two extensions using the same key never share what it
// keeps — a context, not a module global set in a render, stays right on a lone re-render of the graph under it.
const OwnerScope = ({ extension, children }: { extension: string; children: React.ReactNode }) => (
  <ExtensionOwnerScope owner={extension}>{children}</ExtensionOwnerScope>
);

/** A nav_page's icon from the fixed vocabulary, as one of the web's icons (unknown: the extension's). */
const NAV_ICON: Record<UIIcon, IconName> = { extension: "extension", plugin: "extension", chart: "chart", gauge: "gauge", list: "list", bell: "bell", doc: "read", calendar: "calendar", money: "money",
  branch: "branch", code: "code", pull_request: "pullRequest", merge: "merge", ...NEW_ICON_GLYPH };
export const navIcon = (icon?: string): IconName => NAV_ICON[uiIcon(icon)];

/** A badge's tone as the sidebar's badge colors. */
export const BADGE_TONE: Record<UITone, "red" | "orange" | "green" | "accent"> = { danger: "red", caution: "orange", positive: "green", neutral: "accent" };

/** Where an extension's page opens: its own route (a nav_page, or a plugin_page a card opens). */
export const pageRoute = (extension: string, ui: string): Route => ({ name: "extension", extension, ui });

/** The extension's name where it is written beside a place's title (a page's top bar, an Overview card's and a
 *  machine section's heading): nothing when it only says the title again ("Example Example", #3115, #10940), compared
 *  without case and the spaces around it. */
export function nameBesideTitle(name: string | undefined, title: string): string | undefined {
  return name && name.trim().toLowerCase() !== title.trim().toLowerCase() ? name : undefined;
}

/** An extension's page, full size (a nav_page opened from the sidebar, or what a card opens). */
export function ExtensionNavPage({ extension, ui, onToggleSidebar }: { extension: string; ui: string; onToggleSidebar: () => void }) {
  const place = useExtensionPlace(extension, ui);
  const [n, setN] = useState(0);
  // a page a card or a section pushed opens here with the state it was pushed with (?state=)
  const state = useMemo(() => new URLSearchParams(location.search).get("state") ?? undefined, [extension, ui]);
  const title = place?.title || ui;
  // the extension's name after the title, unless it only says the title again
  const name = nameBesideTitle(place?.extension_name, title);
  // a code review or a file browser (UI version 4) and a dashboard of columns (8) take the page's whole width; everything
  // else stays a column
  const [wide, setWide] = useState(false);
  return (
    <div className="flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("extensions-extensionslots.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        <span className="flex size-7 shrink-0 items-center justify-center rounded-[8px] bg-field text-ink-2">
          <Icon name={navIcon(place?.icon)} size={14} />
        </span>
        <h1 className="min-w-0 truncate text-[14px] font-semibold text-ink">{title}</h1>
        {name && <span className="hidden min-w-0 truncate text-[12px] text-ink-3 sm:inline">{name}</span>}
        <span className="flex-1" />
        <IconButton icon="retry" label={t("extensions-extensionslots.redraw")} onClick={() => setN((x) => x + 1)} />
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className={cx("mx-auto flex flex-col px-4 py-6 sm:px-8", wide ? "max-w-[1680px]" : "max-w-[880px]")}>
          <Suspense fallback={<Loading />}>
            <OwnerScope extension={extension}><ExtensionUI key={`${extension}/${ui}/${n}`} extension={extension} ui={ui} title={title} bare state={state} onWide={setWide} remember /></OwnerScope>
          </Suspense>
        </div>
      </div>
    </div>
  );
}

/** The cards on "Overview" (overview_card), the page's last block, under the agents' sections and the computers
 *  (#11147): one card full width, more two a row;
 *  on a page wide enough for two columns of rows, a lone card takes one of them. */
export function OverviewCards() {
  const cards = useExtensionSlot("overview_card");
  // A section like the page's others, with their heading (its name and how many), and it folds (#11148): kept per
  // browser with the sidebar's folds (`fold:overview-cards`), open until the owner folds it. Folded, no card is mounted,
  // so none is asked for either.
  const [folded, toggle] = useFolded("overview-cards", false, { all: false });
  const grid = useId();
  if (!cards.length) return null;
  return (
    <section aria-label={t("extensions-extensionslots.extensions")} data-overview-cards>
      <div className={cx("flex items-baseline gap-2", !folded && "mb-2.5")}>
        <h2 className="text-meta font-medium text-ink">{t("extensions-extensionslots.extensions")}</h2>
        <span className="text-label text-ink-3 tabular-nums">{cards.length}</span>
        <span className="flex-1" />
        <button type="button" aria-expanded={!folded} aria-controls={folded ? undefined : grid} onClick={toggle} className="shrink-0 whitespace-nowrap rounded-control px-1.5 text-label text-ink-3 transition-colors hover:bg-hover hover:text-ink-2">
          {folded ? t("extensions-extensionslots.cards_expand") : t("extensions-extensionslots.cards_collapse")}
        </button>
      </div>
      {!folded && <CardGrid id={grid} cards={cards} />}
    </section>
  );
}

function CardGrid({ id, cards }: { id: string; cards: UIPlace[] }) {
  return (
    // a card is never wider than one of the overview's two columns of rows below it (Home's GRID.rows, from 60rem of
    // the page): alone at 1920 px it stretched its chart to 1540 px of sparse bars, and to 1300 px at 1680
    // the cards of a row are one height (the grid's default stretch): each as tall as its own content left a gap under
    // the shorter one in every row, and the rows read as ragged (#11093 tried it, #11109 took it back)
    <div id={id} className={cx("grid grid-cols-1 gap-3 @min-[60rem]:grid-cols-2", cards.length > 1 && "md:grid-cols-2")}>
      {cards.map((c, i) => (
        <OverviewCard key={`${c.extension}/${c.id}`} place={c} i={i} />
      ))}
    </div>
  );
}

const INTERACTIVE = "a,button,input,select,textarea,label,[role=radio],[role=switch],[tabindex]";

// The most of its view a card that opens a page shows (#11149), in rem so it follows the text size: a stat grid with
// its chart, or a stat grid and three rows. A card is a glance at its extension; one long list made its whole row,
// and the card beside it, twice as tall. What is under the limit fades out over CARD_FADE, and "See all" opens the page.
const CARD_MAX = "max-h-[18.75rem]";
const CARD_FADE = "[mask-image:linear-gradient(to_bottom,black_calc(100%-2.75rem),transparent)]";

function OverviewCard({ place, i }: { place: UIPlace; i: number }) {
  const [gone, setGone] = useState(false);
  const context = useMemo(() => ({ size: "compact" as const }), []);
  const to = place.opens ? pageRoute(place.extension, place.opens) : null;
  // whose card it is, unless its title already is that name
  const name = nameBesideTitle(place.extension_name, place.title);
  // Cut only when there is a page to read the rest on; whether the view is taller than the limit is measured (its
  // height is the extension's, and changes as it redraws).
  const view = useRef<HTMLDivElement>(null);
  const [cut, setCut] = useState(false);
  const page = to !== null;
  useEffect(() => {
    const el = view.current;
    if (!el || !page || typeof ResizeObserver === "undefined") return;
    const check = () => setCut(el.offsetHeight > (el.parentElement?.clientHeight ?? Infinity) + 1);
    const ro = new ResizeObserver(check);
    ro.observe(el);
    check();
    return () => ro.disconnect();
  }, [page, gone]);
  if (gone) return null;
  // the whole card opens the page; a button, a row or the chart inside it keeps its own click
  const open = (e: React.MouseEvent) => {
    if (!to || (e.target as HTMLElement).closest(INTERACTIVE)) return;
    if (window.getSelection()?.toString()) return;
    navigate(to);
  };
  return (
    <div
      data-extension-card={`${place.extension}/${place.id}`}
      onClick={open}
      className={cx("group/card flex min-w-0 flex-col rounded-card bg-surface pb-1.5 shadow-card", to && "cursor-pointer transition-colors hover:bg-inset")}
      style={{ animation: `fade-up 420ms ${EASE} ${Math.min(i, 6) * 50}ms both` }}
    >
      <div className="flex min-w-0 items-center gap-2 px-4 pt-3 pb-0.5">
        {to ? (
          <a href={href(to)} onClick={(e) => onLinkClick(e, to)} className="group flex min-w-0 flex-1 items-center gap-1 outline-none focus-visible:underline">
            <span className="truncate text-[13px] font-medium text-ink">{place.title}</span>
            <Icon name="chevronRight" size={12} stroke={2.2} className="shrink-0 text-ink-3 transition-transform duration-150 group-hover:translate-x-0.5" />
          </a>
        ) : (
          <span className="min-w-0 flex-1 truncate text-[13px] font-medium text-ink">{place.title}</span>
        )}
        {/* its last view is on screen while it is drawn again (KeptView): a quiet mark, only once that takes a moment */}
        <span aria-hidden data-card-updating className="hidden shrink-0 group-has-[[data-kept-view]]/card:inline-flex" style={{ animation: `fade-in 200ms ${EASE} 600ms both` }}>
          <Spinner size={10} />
        </span>
        {name && <span data-extension-name className="max-w-[45%] shrink-0 truncate text-[11px] text-ink-3">{name}</span>}
      </div>
      {/* `remember`: a card drawn before is there at once, as tall as it was, while its extension draws it again (#11091) */}
      <div data-card-view data-cut={cut || undefined} className={cx(to && `${CARD_MAX} overflow-hidden`, cut && CARD_FADE)}>
        <div ref={view} className="flex flex-col">
          <Suspense fallback={<Loading />}>
            <OwnerScope extension={place.extension}><ExtensionUI extension={place.extension} ui={place.id} context={context} compact remember onMissing={() => setGone(true)} /></OwnerScope>
          </Suspense>
        </div>
      </div>
      {cut && to && (
        // at the card's foot, also when its row neighbour makes the card taller than its own view
        <a data-card-see-all href={href(to)} onClick={(e) => onLinkClick(e, to)} className="group mt-auto flex items-center gap-1 px-4 pt-1.5 pb-1 text-label text-ink-3 outline-none hover:text-ink-2 focus-visible:underline">
          {t("extensions-extensionslots.card_see_all")}
          <Icon name="chevronRight" size={11} stroke={2.2} className="shrink-0 transition-transform duration-150 group-hover:translate-x-0.5" />
        </a>
      )}
    </div>
  );
}

/** The extensions' sections on a machine's page (machine_section), after its own; the extension is told which machine.
 *  A computer of another manager than the default gets that manager's extensions' sections, asked with its name
 *  (docs/assistant/managers.md "Clients"). */
export function MachineExtensionSections({ mid, heading }: { mid: string; heading: (title: string, right?: React.ReactNode) => React.ReactNode }) {
  const manager = useStore((s) => s.machines.find((m) => m.id === mid)?.manager);
  const known = useKnownDefault();
  const sections = useExtensionSlot("machine_section", manager);
  // whether the computer is another manager's goes by the roster's default: nothing is drawn before it is known
  if (manager && !known) return null;
  return (
    <>
      {sections.map((s) => (
        <MachineSection key={`${manager ?? ""}/${s.extension}/${s.id}`} place={s} mid={mid} manager={manager} heading={heading} />
      ))}
    </>
  );
}

function MachineSection({ place, mid, manager, heading }: { place: UIPlace; mid: string; manager?: string; heading: (title: string, right?: React.ReactNode) => React.ReactNode }) {
  const [gone, setGone] = useState(false);
  const context = useMemo(() => ({ machine: mid }), [mid]);
  const name = nameBesideTitle(place.extension_name, place.title);
  if (gone) return null;
  return (
    <section data-extension-section={`${place.extension}/${place.id}`}>
      {heading(
        place.title,
        name && (
          <span data-extension-name className="flex min-w-0 items-center gap-1 text-[11.5px] text-ink-3">
            <Icon name="extension" size={11} className="shrink-0" />
            <span className="truncate">{name}</span>
          </span>
        ),
      )}
      <Suspense fallback={<Loading />}>
        <OwnerScope extension={place.extension}><ExtensionUI extension={place.extension} ui={place.id} context={context} manager={manager} bare title={place.title} pages="open" onMissing={() => setGone(true)} /></OwnerScope>
      </Suspense>
    </section>
  );
}
