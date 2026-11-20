import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type MouseEvent, type ReactNode } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import { ago, baseName, sessionTitle, stateOf } from "../../lib/format";
import { agentLabel } from "../../lib/mentions";
import { machineWithHub } from "../../lib/mesh";
import { href, onLinkClick } from "../../lib/router";
import { runtimeIdentity, runtimeOrder, runtimeParentIdentity, runtimePlace, runtimeRemoval, runtimeSessions } from "../../lib/runtime-list";
import { SESSIONS_CATALOG_FEATURE, SESSIONS_JUST_FINISHED_FEATURE } from "../../lib/session-groups";
import { readWorkSessionCounts, WorkSessionList, workSessionCountsPath, workSessionQuery, type WorkSessionFilters } from "../../lib/work-session-list";
import { SESSIONS_LIST_FEATURE } from "../../lib/session-list";
import { STATE_LABEL } from "../../lib/state-labels";
import { onHubEvent, useStore } from "../../lib/store";
import { SESSIONS_SIDE_FOLD_FEATURE } from "../../lib/side-fold";
import type { AgentState, Machine } from "../../lib/types";
import { WORK_DIMENSIONS, WORK_SESSION_DIMENSIONS_FEATURE, WORK_SORTS, workSessionItems, type WorkDimension, type WorkFacet, type WorkSession, type WorkSessionDirection, type WorkSessionSort } from "../../lib/work-session-data";
import { defaultWorkSessionView, readWorkSessionView, writeWorkSessionView, type WorkSessionView } from "../../lib/work-session-preferences";
import { SessionMark } from "../../ui/AgentMark";
import { Button, cx, useNow } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { SESSION_LIST_METRICS } from "../../ui/list-metrics";
import { SearchField } from "../../ui/SearchField";
import { StatusMark } from "../../ui/StatusMark";
import { PagedSessionList, scrollParent } from "./PagedSessionList";

export interface WorkSessionBrowserProps {
  activeSession?: { machineId: string; sessionId: string };
  /** Plain selection can stay in the host's content area; modified clicks keep the canonical session link. */
  onSelect?: (session: WorkSession) => void;
}

const copy = (key: string, vars?: Record<string, string | number>) => t(`work-session-browser.${key}`, vars);
const states: AgentState[] = ["needs_you", "working", "unread", "idle", "offline", "unknown"];
const control = "rounded-control bg-inset px-2 py-1 text-xs text-ink shadow-hairline focus-visible:outline-accent";
const hit = "min-h-8 [@media(pointer:coarse)]:min-h-11";
const chip = cx(control, hit, "inline-flex max-w-full items-center gap-1 whitespace-nowrap");
const headingIndent = ["ps-2", "ps-4", "ps-6"];
const rowIndent = ["ps-2", "ps-4", "ps-6", "ps-8"];
type Selections = Partial<Record<WorkDimension, WorkFacet[]>>;

function onWorkSessionChange(fn: () => void) {
  const offSessions = onHubEvent("sessions_list", fn);
  const offHello = onHubEvent("hello", fn);
  return () => { offSessions(); offHello(); };
}

function useScopedCounts(path: string | null) {
  const [read, setRead] = useState<{ path: string; value?: Awaited<ReturnType<typeof readWorkSessionCounts>>; error?: string }>();
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    if (!path) return;
    const abort = new AbortController();
    let reading = false;
    let queued = false;
    const refresh = () => {
      if (reading) { queued = true; return; }
      reading = true;
      void readWorkSessionCounts(path, abort.signal).then(
        (value) => { if (!abort.signal.aborted) setRead({ path, value }); },
        (error: unknown) => { if (!abort.signal.aborted) setRead({ path, error: errorText(error) }); },
      ).finally(() => {
        reading = false;
        if (queued && !abort.signal.aborted) { queued = false; refresh(); }
      });
    };
    refresh();
    const off = onWorkSessionChange(refresh);
    return () => { abort.abort(); off(); };
  }, [path, retry]);
  return { ...(read?.path === path ? read : undefined), retry: () => setRetry((n) => n + 1) };
}

function Panel({ label, children, onToggle }: { label: string; children: ReactNode; onToggle?: (open: boolean) => void }) {
  return <details onToggle={onToggle ? (event) => onToggle(event.currentTarget.open) : undefined} className="group min-w-0 rounded-control bg-inset open:basis-full">
    <summary className={cx(chip, "cursor-pointer select-none list-none shadow-none")}>
      {label}<Icon name="chevron" size={12} className="group-open:rotate-180" />
    </summary>
    <div className="space-y-2 px-2 pb-2">{children}</div>
  </details>;
}

function ViewControls({ view, change }: { view: WorkSessionView; change: (next: WorkSessionView) => void }) {
  const setLevel = (index: number, dimension: WorkDimension | "") => {
    const groupBy = [...view.groupBy];
    if (!dimension) groupBy.splice(index);
    else if (!groupBy.includes(dimension)) groupBy[index] = dimension;
    change({ ...view, groupBy });
  };
  const moveUp = (index: number) => {
    const groupBy = [...view.groupBy];
    [groupBy[index - 1], groupBy[index]] = [groupBy[index], groupBy[index - 1]];
    change({ ...view, groupBy });
  };
  return <>
    <p className="text-xs font-medium text-ink-2">{copy("group_by")}</p>
    {WORK_DIMENSIONS.slice(0, Math.min(view.groupBy.length + 1, WORK_DIMENSIONS.length)).map((_, i) => (
      <div key={i} className="flex min-w-0 items-center gap-1">
        <label className="flex min-w-0 flex-1 items-center gap-2 text-xs text-ink-2">
          <span className="shrink-0">{copy("level", { number: i + 1 })}</span>
          <select aria-label={copy("level", { number: i + 1 })} value={view.groupBy[i] ?? ""} onChange={(e) => setLevel(i, e.target.value as WorkDimension | "")}
            className={cx(control, hit, "min-w-0 flex-1")}>
            <option value="">{copy(i ? "no_level" : "ungrouped")}</option>
            {WORK_DIMENSIONS.map((d) => <option key={d} value={d} disabled={view.groupBy.includes(d) && d !== view.groupBy[i]}>{copy(d)}</option>)}
          </select>
        </label>
        {i > 0 && view.groupBy[i] && <button type="button" title={copy("move_up", { name: copy(view.groupBy[i]) })}
          aria-label={copy("move_up", { name: copy(view.groupBy[i]) })} className={chip} onClick={() => moveUp(i)}>
          <Icon name="chevron" size={12} className="rotate-180" />
        </button>}
      </div>
    ))}
    <div className="flex flex-wrap gap-2">
      <label className="flex min-w-0 flex-1 flex-col gap-1 text-xs text-ink-2">
        {copy("sort_by")}
        <select aria-label={copy("sort_by")} className={cx(control, hit)} value={view.sort} onChange={(e) => {
          const sort = e.target.value as WorkSessionSort;
          change({ ...view, sort, direction: sort === "title" ? "asc" : "desc" });
        }}>
          {WORK_SORTS.map((sort) => <option key={sort} value={sort}>{copy(sort)}</option>)}
        </select>
      </label>
      <label className="flex min-w-0 flex-1 flex-col gap-1 text-xs text-ink-2">
        {copy("direction")}
        <select aria-label={copy("direction")} className={cx(control, hit)} value={view.direction} onChange={(e) => change({ ...view, direction: e.target.value as WorkSessionDirection })}>
          <option value="desc">{copy(view.sort === "title" ? "z_to_a" : "newest")}</option>
          <option value="asc">{copy(view.sort === "title" ? "a_to_z" : "oldest")}</option>
        </select>
      </label>
    </div>
    <Button tone="quiet" className={hit} onClick={() => change(defaultWorkSessionView())}>{copy("reset_view")}</Button>
  </>;
}

function DimensionChoices({ dimension, options, selected, toggle, loading }: {
  dimension: WorkDimension;
  options: WorkFacet[];
  selected: WorkFacet[];
  toggle: (option: WorkFacet) => void;
  loading: boolean;
}) {
  const byId = new Map(options.map((o) => [o.id, o]));
  for (const choice of selected) if (!byId.has(choice.id)) byId.set(choice.id, { ...choice, count: 0 });
  const choices = [...byId.values()];
  return <fieldset className="min-w-0 rounded-control border border-line px-2">
    <legend className="px-1 text-xs font-medium text-ink-2">{copy(dimension)}</legend>
    <div className="max-h-40 overflow-y-auto">
      {choices.map((choice) => (
        <label key={choice.id} className={cx(hit, "flex cursor-pointer items-center gap-2 rounded-control px-1 text-xs hover:bg-hover")}>
          <input type="checkbox" checked={selected.some((s) => s.id === choice.id)} onChange={() => toggle(choice)} />
          <span className="min-w-0 flex-1 break-words">{choice.name}</span>
          {!loading && <span className="tabular-nums text-ink-3">{choice.count}</span>}
        </label>
      ))}
      {!choices.length && <p className="py-1 text-xs text-ink-3">{loading ? t("sidebar.loading") : copy("no_options")}</p>}
    </div>
  </fieldset>;
}

export function WorkSessionRow({ session, name, active, onSelect, now, depth }: {
  session: WorkSession; name: string; active: boolean; onSelect?: WorkSessionBrowserProps["onSelect"]; now: number; depth: number;
}) {
  const [expanded, setExpanded] = useState(false);
  const summaryId = useId();
  const to = { name: "session" as const, mid: session.machine_id, sid: session.session_id };
  const select = (event: MouseEvent<HTMLAnchorElement>) => {
    if (!onSelect) { onLinkClick(event, to); return; }
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    onSelect(session);
  };
  const project = session.project_name || session.space_name || baseName(session.repo_root || session.cwd) || copy("unknown_project");
  const harness = session.harness_name || agentLabel(session);
  const byName = session.project_id?.startsWith("name:");
  const preview = session.activity || session.last_text;
  return <div className={cx("flex min-w-0 items-center rounded-control pe-2 hover:bg-hover", rowIndent[depth], active && "bg-accent-tint")}>
    <a href={href(to)} onClick={select} aria-current={active ? "true" : undefined}
      className="flex min-h-11 min-w-0 flex-1 items-start gap-2 rounded-control py-1.5 focus-visible:outline-accent">
    <span className="flex shrink-0 items-center gap-1 pt-0.5">
      <StatusMark state={stateOf(session)} size="sm" />
      <SessionMark s={session} decorative size={SESSION_LIST_METRICS.icon} />
    </span>
    <span className="min-w-0 flex-1">
      <span className="flex min-w-0 items-baseline gap-2">
        <span className="min-w-0 flex-1 truncate text-sm font-medium text-ink">{sessionTitle(session)}</span>
        {session.updated_at && <time className="shrink-0 text-xs text-ink-3" dateTime={session.updated_at}>{ago(session.updated_at, now, "narrow")}</time>}
      </span>
      {preview && <span className={cx("text-xs text-ink-2", expanded ? "block break-words" : "hidden truncate @sm/work-sessions:block")}>{preview}</span>}
      <span id={summaryId} className={cx("flex min-w-0 items-baseline gap-x-1 text-xs text-ink-3",
        expanded ? "flex-wrap break-words" : "overflow-hidden @sm/work-sessions:flex-wrap")}>
        {([["device", name], ["project", byName ? `${project} (${copy("by_name")})` : project], ["harness", harness]] as const).map(([dimension, label], i) =>
          <span key={dimension} className={cx("inline-flex min-w-0 gap-1", expanded && "max-w-full")}>
            {i > 0 && <span aria-hidden="true">·</span>}
            <span className={expanded ? "min-w-0 break-words" : "min-w-0 truncate"} title={label}>
              <span className="sr-only">{copy(dimension)}: </span>{label}
            </span>
          </span>
        )}
      </span>
    </span>
    </a>
    <button type="button" aria-label={copy("details")} aria-controls={summaryId} aria-expanded={expanded}
      className={cx(hit, "shrink-0 rounded-control px-2 text-ink-3 hover:bg-surface @sm/work-sessions:hidden")}
      onClick={() => setExpanded((open) => !open)}>
      <Icon name="chevron" size={12} className={expanded ? "rotate-180" : ""} />
    </button>
  </div>;
}

export function WorkSessionBrowser(props: WorkSessionBrowserProps) {
  const features = useStore((s) => s.features);
  const conn = useStore((s) => s.conn);
  const machines = useStore((s) => s.machines);
  if (features.includes(WORK_SESSION_DIMENSIONS_FEATURE)) return <DimensionsBrowser {...props} machines={machines} />;
  if (!features.includes(SESSIONS_LIST_FEATURE) && conn === "connecting") {
    return <p role="status" className="px-2 py-2 text-xs text-ink-3">{t("sidebar.loading")}</p>;
  }
  return <LegacyBrowser {...props} machines={machines} />;
}

function LegacyBrowser({ activeSession, machines, onSelect }: WorkSessionBrowserProps & { machines: Machine[] }) {
  const now = useNow();
  const catalog = useStore((s) => s.features.includes(SESSIONS_CATALOG_FEATURE));
  const justFinished = useStore((s) => s.features.includes(SESSIONS_JUST_FINISHED_FEATURE));
  // a hub that folds side sessions (#7944) leaves them out: nothing to nest under a parent
  const sideFold = useStore((s) => s.features.includes(SESSIONS_SIDE_FOLD_FEATURE));
  return <div className="@container/work-sessions">
    <p className="px-2 pt-2 text-xs text-ink-3">{copy("legacy")}</p>
    <PagedSessionList root={runtimeSessions} path="/sessions" identity={runtimeIdentity} compare={runtimeOrder} removeIdentity={runtimeRemoval} parentOf={sideFold ? undefined : runtimeParentIdentity}
      event="sessions_list" catalog={catalog} justFinished={justFinished} placeOf={runtimePlace} label={(dimension, id) => dimension === "machine" ? machineWithHub(id, machines) : id}
      renderRow={(session) => <WorkSessionRow session={session} name={machineWithHub(session.machine_id, machines)}
        active={activeSession?.machineId === session.machine_id && activeSession.sessionId === session.session_id}
        onSelect={onSelect} now={now} depth={0} />} />
  </div>;
}

function DimensionsBrowser({ activeSession, onSelect, machines }: WorkSessionBrowserProps & { machines: Machine[] }) {
  const [view, setView] = useState(readWorkSessionView);
  const [search, setSearch] = useState("");
  const [q, setQ] = useState("");
  const [state, setState] = useState<AgentState | "">("");
  const [selections, setSelections] = useState<Selections>({});
  const [filterOpen, setFilterOpen] = useState(false);
  const now = useNow();
  useEffect(() => {
    if (!search.trim()) { setQ(""); return; }
    const timer = setTimeout(() => setQ(search.trim()), SESSION_LIST_METRICS.searchDelay);
    return () => clearTimeout(timer);
  }, [search]);
  const query = useMemo(() => {
    const filters: WorkSessionFilters = {};
    for (const dimension of WORK_DIMENSIONS) {
      filters[dimension] = selections[dimension]?.map(({ id }) => id).sort();
    }
    return workSessionQuery(filters, q, state, view);
  }, [selections, q, state, view]);
  const list = useMemo(() => new WorkSessionList(query), [query]);
  const page = useSyncExternalStore(list.subscribe, list.getSnapshot, list.getSnapshot);
  useEffect(() => {
    void list.load().catch(() => {});
    const off = onWorkSessionChange(() => list.invalidate());
    return () => { off(); list.stop(); };
  }, [list]);

  const statuses = useScopedCounts(workSessionCountsPath(query, "state"));
  const device = useScopedCounts(filterOpen && selections.device?.length ? workSessionCountsPath(query, "device") : null);
  const project = useScopedCounts(filterOpen && selections.project?.length ? workSessionCountsPath(query, "project") : null);
  const harness = useScopedCounts(filterOpen && selections.harness?.length ? workSessionCountsPath(query, "harness") : null);
  const alternatives = { device, project, harness };
  const names = useMemo(() => {
    const out = new Map<string, string>();
    for (const machine of machines) out.set(machine.id, machineWithHub(machine.id, machines));
    return out;
  }, [machines]);
  const facets = useMemo(() => {
    return Object.fromEntries(WORK_DIMENSIONS.map((d) => [d, new Map(page.dimensions?.facets[d].map((o) => [o.id, o.name]))])) as Record<WorkDimension, Map<string, string>>;
  }, [page.dimensions]);
  const items = useMemo(() => workSessionItems(page.rows, view.groupBy, page.dimensions?.groups), [page.rows, page.dimensions?.groups, view.groupBy]);
  const container = useRef<HTMLDivElement>(null);
  const getScrollElement = useCallback(() => scrollParent(container.current), []);
  const [margin, setMargin] = useState(0);
  const virtual = useVirtualizer({
    count: items.length, getScrollElement, getItemKey: (i) => items[i].key, scrollMargin: margin,
    estimateSize: () => SESSION_LIST_METRICS.estimate, overscan: SESSION_LIST_METRICS.overscan,
  });
  useLayoutEffect(() => {
    const el = container.current;
    const parent = getScrollElement();
    if (!el || !parent) return;
    const measure = () => setMargin(el.getBoundingClientRect().top - parent.getBoundingClientRect().top + parent.scrollTop);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(parent);
    if (el.parentElement) ro.observe(el.parentElement);
    return () => ro.disconnect();
  }, [getScrollElement, filterOpen, items.length]);
  useEffect(() => {
    const el = getScrollElement();
    if (el) el.scrollTop = 0;
  }, [list, getScrollElement]);

  const nameOf = (dimension: WorkDimension, id: string) =>
    (dimension === "device" ? names.get(id) : undefined) || facets[dimension].get(id) || copy(`unknown_${dimension}`);
  const toggle = (dimension: WorkDimension, option: WorkFacet) => setSelections((current) => {
    const chosen = current[dimension] ?? [];
    return { ...current, [dimension]: chosen.some((o) => o.id === option.id) ? chosen.filter((o) => o.id !== option.id) : [...chosen, option] };
  });
  const reset = () => { setSearch(""); setQ(""); setState(""); setSelections({}); };
  const hasFilters = Boolean(search || state || WORK_DIMENSIONS.some((d) => selections[d]?.length));
  const countError = [statuses, device, project, harness].find((result) => result.error);
  const stateCounts = statuses.value?.state;
  return <section aria-label={copy("browser")} className="@container/work-sessions min-w-0" data-work-session-browser data-loaded-sessions={page.rows.length}>
    <div className="space-y-2 px-2 py-2">
      <SearchField value={search} onChange={setSearch} placeholder={t("paged-agents.search")} />
      <div role="group" aria-label={t("sidebar.quick_filters")} className="flex flex-wrap gap-1">
        {(["", ...states.filter((s) => state === s || (stateCounts?.[s] ?? 0) > 0)] as const).map((s) => (
          <button key={s} type="button" aria-pressed={state === s} onClick={() => setState(s)}
            className={cx(chip, state === s ? "bg-surface font-medium" : "text-ink-2 hover:bg-hover")}>
            {s ? STATE_LABEL[s] : t("paged-agents.all")}
            {s && stateCounts && <span className="tabular-nums text-ink-3">{stateCounts[s] ?? 0}</span>}
          </button>
        ))}
      </div>
      <div className="flex flex-wrap gap-1">
        <Panel label={copy("filters")} onToggle={setFilterOpen}>
          {filterOpen && WORK_DIMENSIONS.map((dimension) => {
            const selected = selections[dimension] ?? [];
            const values = selected.length ? alternatives[dimension].value?.facets[dimension] : page.dimensions?.facets[dimension];
            return <DimensionChoices key={dimension} dimension={dimension} options={values ?? []}
              selected={selected} loading={!values} toggle={(option) => toggle(dimension, option)} />;
          })}
        </Panel>
        <Panel label={copy("view")}><ViewControls view={view} change={(next) => { setView(next); writeWorkSessionView(next); }} /></Panel>
        {hasFilters && <Button tone="quiet" className={hit} onClick={reset}>{copy("reset_filters")}</Button>}
      </div>
      <div className="flex flex-wrap gap-1">
        {WORK_DIMENSIONS.flatMap((dimension) => selections[dimension]?.map((option) => (
          <button key={`${dimension}:${option.id}`} className={chip} type="button"
            aria-label={copy("remove_filter", { name: copy(dimension), value: option.name })} onClick={() => toggle(dimension, option)}>
            <span className="truncate">{option.name}</span><Icon name="x" size={12} />
          </button>
        )) ?? [])}
      </div>
      {page.loaded && <p className="text-xs tabular-nums text-ink-3" data-session-total>{t("paged-agents.count", { count: page.total })}</p>}
    </div>
    {countError && <div className="px-2 text-xs text-ink-2" role="alert">
      {countError.error} <Button tone="quiet" onClick={countError.retry}>{t("sidebar.retry")}</Button>
    </div>}
    <div ref={container} className="relative" style={{ height: virtual.getTotalSize() }}>
      {virtual.getVirtualItems().map((entry) => {
        const item = items[entry.index];
        return <div key={item.key} ref={virtual.measureElement} data-index={entry.index} className="absolute top-0 left-0 w-full"
          style={{ transform: `translateY(${entry.start - margin}px)` }}>
          {item.kind === "group"
            ? <div role="heading" aria-level={item.depth + 2} className={cx("flex min-w-0 items-baseline gap-2 bg-inset pe-2 py-1.5 text-xs font-medium text-ink-2", headingIndent[item.depth])}>
              <span className="min-w-0 flex-1 break-words">
                {nameOf(item.dimension, item.id)}
                {item.dimension === "project" && item.id.startsWith("name:") && <> ({copy("by_name")})</>}
              </span>
              {item.count !== undefined && <span className="shrink-0 tabular-nums text-ink-3">{item.count}</span>}
            </div>
            : <WorkSessionRow session={item.session} name={nameOf("device", item.session.machine_id)}
              active={activeSession?.machineId === item.session.machine_id && activeSession.sessionId === item.session.session_id}
              onSelect={onSelect} now={now} depth={view.groupBy.length} />}
        </div>;
      })}
    </div>
    {page.loading && <p role="status" className="px-2 py-2 text-xs text-ink-3">{t("sidebar.loading")}</p>}
    {page.error && <div className="px-2 py-2"><p role="alert" className="text-xs text-red">{page.error}</p>
      <Button onClick={() => void list.retry().catch(() => {})}>{t("sidebar.retry")}</Button></div>}
    {page.loaded && !page.loading && !page.error && !page.rows.length &&
      <p className="px-2 py-2 text-xs text-ink-3">{hasFilters ? t("sidebar.no_match") : copy("empty")}</p>}
    {page.hasMore && !page.loading && !page.error &&
      <div className="px-2 py-2"><Button className={hit} onClick={() => void list.more().catch(() => {})}>{t("paged-agents.more")}</Button></div>}
  </section>;
}
