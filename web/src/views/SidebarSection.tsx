// A sidebar group: its heading stays in view while its rows scroll, a section folds under it, and its count says what
// it counts. Kept apart from the sidebar's own parts so that a list outside the sidebar can draw the same sections.
import { createContext, useContext, useLayoutEffect, useRef, useState } from "react";
import { t } from "../i18n";
import { stateCount } from "../lib/state-labels";
import { Collapse, cx, sidebarFoldMotion } from "../ui/atoms";
import { contextMenuProps } from "../ui/context-menu";
import { Icon } from "../ui/icons";
import type { MenuAt } from "../ui/menu-model";

const GroupHeadingOffset = createContext({ offset: 0, depth: 0 });

/** Real headings stay interactive while pinned; each nested group reserves its parent's measured height. */
export function SidebarGroup({ heading, children, className }: { heading: React.ReactNode; children: React.ReactNode; className?: string }) {
  const { offset: above, depth } = useContext(GroupHeadingOffset);
  const ref = useRef<HTMLDivElement>(null);
  const [height, setHeight] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const scroller = el.closest<HTMLElement>("[data-sidebar-rows]");
    const controls = el.querySelector("[data-sidebar-agents-controls]");
    const measure = () => {
      const height = el.getBoundingClientRect().height;
      setHeight(height);
      if (controls) scroller?.style.setProperty("--sidebar-controls-height", `${height}px`);
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => { observer.disconnect(); if (controls) scroller?.style.removeProperty("--sidebar-controls-height"); };
  }, []);
  return <div className={className} data-sidebar-group>
    <div ref={ref} data-sidebar-group-heading className="sidebar-group-heading" style={{ top: `calc(var(--sidebar-sticky-top, 0px) + ${above}px)`, zIndex: 24 - depth }}>{heading}</div>
    <GroupHeadingOffset.Provider value={{ offset: above + height, depth: depth + 1 }}>{children}</GroupHeadingOffset.Provider>
  </div>;
}

/** A sidebar group: its heading folds it. The heading selects no text, so a press-and-drag over the list paints no
 *  selection behind it (#6458); what a group holds, when its name does not say, is an ⓘ in `right` (InfoTip). */
export function Section({ title, children, folded, onToggle, right, onMenu, first }: { title: React.ReactNode; children: React.ReactNode; folded: boolean; onToggle: () => void; right?: React.ReactNode; onMenu?: (at: MenuAt, el: HTMLElement) => void; first?: boolean }) {
  return (
    <SidebarGroup className={first ? "mt-1" : "mt-3"} heading={
      <div className="group/sec relative flex select-none items-center gap-1 px-2 pb-1" {...(onMenu ? contextMenuProps(onMenu) : {})}>
        <button type="button" aria-expanded={!folded} onClick={onToggle} className="touch-hit relative z-10 flex min-w-0 flex-1 items-center gap-1 whitespace-nowrap text-left text-[11px] font-semibold text-ink-3 hover:text-ink-2">
          <span className="min-w-0 truncate">{title}</span>
          <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 transition-transform" style={{ ...sidebarFoldMotion(!folded), transform: folded ? "rotate(-90deg)" : "none" }} />
        </button>
        {right}
      </div>
    }>
      <Collapse open={!folded} sidebarFold unclipped>
        <div className="flex flex-col gap-px" inert={folded || undefined}>{children}</div>
      </Collapse>
    </SidebarGroup>
  );
}

/** A group title's count with what it counts: a tooltip, and the same words for a screen reader. */
export function GroupCount({ n, label, className }: { n: number; label: string; className?: string }) {
  return (
    <span title={label} className={cx("text-[10.5px] text-ink-3 tabular-nums", className)}>
      <span aria-hidden>{n}</span>
      <span className="sr-only">{label}</span>
    </span>
  );
}

/** On a folded group's heading, how many of its sessions need the user (DESIGN_SYSTEM 3.4): the number in the
 *  attention tone, with the words for its tooltip and for a screen reader. Nothing when none do. */
export function NeedsCount({ n }: { n: number }) {
  if (n <= 0) return null;
  return (
    <span title={stateCount("needs_you", n)} className="flex h-4 min-w-4 shrink-0 items-center justify-center rounded-full bg-field px-1 text-[10.5px] font-semibold text-attention tabular-nums">
      {n}<span className="sr-only"> {t("sidebar.needs_you_suffix", { count: n })}</span>
    </span>
  );
}

/** "Load more" (or "Show N more") under a list's rows: a quiet line in the look of the sidebar's own (Recent, a group's
 *  rows), never a button's pill: the paged list's "Load more" looked like no other row (#6662, the owner). */
export function ShowMoreRow({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className="relative z-10 w-full whitespace-nowrap rounded-[7px] px-2 py-1 text-left text-label text-ink-3 hover:text-ink-2 [@media(pointer:coarse)]:min-h-11">
      {label}
    </button>
  );
}
