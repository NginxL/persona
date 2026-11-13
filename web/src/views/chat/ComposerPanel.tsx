// The composer's one panel for + and @ (#5324, cindy's AtMentionPanel, at the owner's request). From the + it covers the
// box, a search box on top taking the keys; a typed @ opens it above the box, filtered by the text after the @, the keys
// staying in the box (ComposerPanelHandle). An empty query lists "Add" (files or images, Plan mode where the session has
// modes) and a few of each resource; a typed one every match in one list. The resources are Persona's (iOS
// PromptComposer.swift): files this chat's tools touched and the session's folder (a query with a slash browses that folder
// on the session's computer: `src/` lists src), the hub's workspaces in the Persona assistant, other sessions. Picking a
// folder keeps browsing, a file types `@path `, a workspace or a session becomes a context chip (Composer.tsx).
import { useEffect, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState, type ComponentProps, type ReactNode, type Ref } from "react";
import { api, enc } from "../../lib/api";
import { BUSY_MOTION } from "../../lib/busy-motion";
import type { FsListing } from "../../lib/files";
import { browseTarget, byPrefix, displayTitle, nameMatches, otherSessions, relPath, sessionDetail, workspaceDetail, workspaceMatches, workspaceTitle } from "../../lib/mentions";
import { machineWithHub } from "../../lib/mesh";
import { modeTitle } from "../../lib/session-actions";
import type { Machine, Session } from "../../lib/types";
import type { Workspace, WorkspaceList } from "../../lib/workspace-board";
import { useStore } from "../../lib/store";
import { hasFinePointer } from "../../lib/viewport";
import { cx, EASE } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { t } from "../../i18n";
import type { MentionSkill } from "../../lib/skill-mentions";

export interface MentionSource {
  /** The session's folder: relative paths and the first listing start here. */
  cwd?: string;
  /** This chat's draft key ("mid/sid"), left out of the sessions. */
  self?: string;
  /** Absolute paths the chat's tools read or wrote, newest first (read when the panel opens). */
  files: () => string[];
  /** Offer the hub's workspaces (GET /workspaces): the Persona assistant, which works on any computer. */
  workspaces?: boolean;
  /** Execution catalog. Ordinary Agents use session-owned facts; environment changes invalidate visible rows. */
  skills?: { manager: string; session?: string; launcher?: string; environment: string; agent?: boolean };
}

export type MentionRow =
  | { kind: "skill"; id: string; skill: MentionSkill }
  | { kind: "file"; id: string; name: string; detail: string; insert: string; dir: boolean }
  | { kind: "workspace"; id: string; name: string; detail: string; workspace: Workspace }
  | { kind: "session"; id: string; name: string; detail: string; session: Session };

/** The panel's own rows: the box's file picker and the session's Plan mode. */
export type PanelAction = "attach" | "plan";
export type PanelPick = { kind: "action"; action: PanelAction } | { kind: "row"; row: MentionRow };

export interface ComposerPanelHandle {
  /** A key pressed in the message box while the panel is open from a typed @ (↑ ↓ move, Enter / Tab pick): true when the
   *  panel took it, so the box does nothing else with it. */
  key: (key: string) => boolean;
}

type Entry = { kind: "action"; action: PanelAction; label: string; icon: IconName; hint?: string; checked?: boolean } | { kind: "row"; row: MentionRow };
type PanelSkills = { rows: (query: string) => MentionRow[]; loading: boolean };

const NO_SESSIONS: Record<string, Session[]> = {};
const NO_MACHINES: Machine[] = [];
const MAX_ROWS = 40;
/** An empty query shows this many of each resource (cindy's EMPTY_QUERY_CONTEXT_SECTION_LIMIT); typing finds the rest. */
const EMPTY_PER_GROUP = 3;

/** The rows for what is typed; the folder listings come from the session's computer (`/fs`), read once per folder while
 *  the panel is open. */
export function useMentionRows(query: string, source: MentionSource | undefined, machineId: string | undefined, skills?: PanelSkills): { rows: MentionRow[]; loading: boolean } {
  const sessions = useStore((s) => (source ? s.sessions : NO_SESSIONS));
  const machines = useStore((s) => (source ? s.machines : NO_MACHINES));
  const workspaces = useWorkspaces(Boolean(source?.workspaces));
  const cache = useRef(new Map<string, FsListing | null>());
  const [, setTick] = useState(0);
  const [loading, setLoading] = useState(false);
  // read once per opening (the chat may grow meanwhile)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const files = useMemo(() => source?.files() ?? [], []);

  const cwd = source?.cwd;
  const target = source ? browseTarget(query, cwd) : null;
  const dir = !source || !machineId ? null : target ? target.dir : cwd ?? null;

  useEffect(() => {
    if (!dir || !machineId || cache.current.has(dir)) return;
    const ac = new AbortController();
    const timer = setTimeout(() => {
      setLoading(true);
      api<FsListing>(`/machines/${enc(machineId)}/fs?path=${enc(dir)}&cwd=${enc(cwd ?? "")}`, { signal: ac.signal })
        .then((l) => cache.current.set(dir, l))
        .catch(() => {
          if (!ac.signal.aborted) cache.current.set(dir, null); // not a folder / unreadable: no rows from it
        })
        .finally(() => {
          if (ac.signal.aborted) return;
          setLoading(false);
          setTick((n) => n + 1);
        });
    }, 120);
    return () => {
      clearTimeout(timer);
      ac.abort();
      setLoading(false);
    };
  }, [dir, machineId, cwd]);

  const listing = dir ? cache.current.get(dir) : undefined;
  const rows = useMemo<MentionRow[]>(() => {
    if (!source) return [];
    if (target) {
      const entries = (listing?.entries ?? []).filter((e) => nameMatches(e.name, target.filter));
      const ordered = [...byPrefix(entries.filter((e) => e.dir), (e) => e.name, target.filter), ...byPrefix(entries.filter((e) => !e.dir), (e) => e.name, target.filter)];
      return ordered.slice(0, MAX_ROWS).map((e) => ({
        kind: "file",
        id: `fs:${target.typed}${e.name}`,
        name: e.dir ? `${e.name}/` : e.name,
        detail: target.typed,
        insert: `@${target.typed}${e.name}${e.dir ? "/" : " "}`,
        dir: e.dir,
      }));
    }
    const q = query.toLowerCase();
    const out: MentionRow[] = skills?.rows(q) ?? [];
    const used = new Set<string>();
    for (const p of files) {
      const rel = relPath(p, cwd);
      if (q && !rel.toLowerCase().includes(q)) continue;
      used.add(rel);
      const slash = rel.lastIndexOf("/");
      out.push({ kind: "file", id: `used:${p}`, name: rel.slice(slash + 1), detail: slash > 0 ? rel.slice(0, slash) : t("chat-composerpanel.used_files"), insert: `@${rel} `, dir: false });
    }
    const top = (listing?.entries ?? []).filter((e) => nameMatches(e.name, q) && !used.has(e.name));
    for (const e of [...byPrefix(top.filter((x) => x.dir), (x) => x.name, q), ...byPrefix(top.filter((x) => !x.dir), (x) => x.name, q)].slice(0, 12)) {
      out.push({ kind: "file", id: `top:${e.name}`, name: e.dir ? `${e.name}/` : e.name, detail: e.dir ? t("chat-composerpanel.folder") : t("chat-composerpanel.session_dir"), insert: `@${e.name}${e.dir ? "/" : " "}`, dir: e.dir });
    }
    if (source.workspaces) {
      const name = (id: string) => machineWithHub(id, machines);
      for (const w of workspaceMatches(workspaces ?? [], q, name)) {
        out.push({ kind: "workspace", id: `workspace:${w.id}`, name: workspaceTitle(w), detail: workspaceDetail(w, name), workspace: w });
      }
    }
    for (const s of otherSessions(Object.values(sessions).flat(), source.self, q)) {
      out.push({ kind: "session", id: `session:${s.machine_id}/${s.session_id}`, name: displayTitle(s), detail: sessionDetail(s), session: s });
    }
    // A large skill inventory must not crowd the existing resource groups out of the empty panel.
    return (q.trim() ? out : firstOfEach(out)).slice(0, MAX_ROWS);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query, source, listing, sessions, cwd, files, workspaces, machines, skills?.rows]);

  return { rows, loading: Boolean((loading && !listing) || (source?.workspaces && workspaces === null) || skills?.loading) };
}

/** The hub's workspaces, read once each time the panel opens (null while reading; empty when the hub can't say). */
function useWorkspaces(on: boolean): Workspace[] | null {
  const [list, setList] = useState<Workspace[] | null>(null);
  useEffect(() => {
    if (!on) return;
    const ac = new AbortController();
    api<WorkspaceList>("/workspaces", { signal: ac.signal })
      .then((l) => setList(l.workspaces ?? []))
      .catch(() => {
        if (!ac.signal.aborted) setList((cur) => cur ?? []); // an old hub (404) or the manager away (503): no workspace rows
      });
    return () => ac.abort();
  }, [on]);
  return list;
}

/** With an empty query, a few of each resource: the files used here, the folder's first entries, workspaces, sessions. */
function firstOfEach(rows: MentionRow[]): MentionRow[] {
  const seen = new Map<string, number>();
  return rows.filter((r) => {
    const group = r.id.split(":", 1)[0];
    const n = seen.get(group) ?? 0;
    seen.set(group, n + 1);
    return n < EMPTY_PER_GROUP;
  });
}

export function ComposerPanel({
  from,
  query = "",
  source,
  machineId,
  attach,
  plan,
  onPick,
  onClose,
  handle,
  skills,
}: {
  /** "plus": opened from the + with its own search box; "at": a typed @, filtered by `query`. */
  from: "plus" | "at";
  query?: string;
  source?: MentionSource;
  machineId?: string;
  /** The Add files row: files and images where the session has a computer, images only elsewhere; none when the box is off. */
  attach?: "files" | "images";
  /** The Plan mode check row, where the session has modes. */
  plan?: { on: boolean };
  onPick: (pick: PanelPick) => void;
  /** Esc, Backspace in an empty search, a click outside: `refocus` puts the caret back in the message box. */
  onClose: (refocus: boolean) => void;
  handle?: Ref<ComposerPanelHandle>;
  skills?: PanelSkills & {
    row: (skill: MentionSkill, common: ComponentProps<"button">) => ReactNode;
    status?: ReactNode;
  };
}) {
  const [search, setSearch] = useState("");
  const q = from === "plus" ? search : query;
  const searchLabel = t("chat-composerpanel.search");
  const browsing = Boolean(source && browseTarget(q, source.cwd));
  const empty = !q.trim();
  const { rows, loading } = useMentionRows(q, source, machineId, skills);

  const entries = useMemo<Entry[]>(() => {
    const actions: Extract<Entry, { kind: "action" }>[] = [];
    if (attach) {
      actions.push({ kind: "action", action: "attach", label: t(attach === "files" ? "chat-composerpanel.add_files" : "chat-composerpanel.add_images"), icon: "clip", hint: attach === "files" ? t("chat-composerpanel.add_files_hint") : undefined });
    }
    if (plan) actions.push({ kind: "action", action: "plan", label: modeTitle("plan"), icon: "list", checked: plan.on });
    const resources: Entry[] = rows.map((row) => ({ kind: "row", row }));
    if (browsing) return resources;
    if (empty) return [...actions, ...resources];
    // a typed query is a resource first: an Add row comes after them, and only when a word of it starts with the query,
    // so `@a` + Enter picks api.ts rather than opening the file picker
    const lq = q.trim().toLowerCase();
    const starts = (label: string) => {
      const l = label.toLowerCase();
      return l.startsWith(lq) || l.split(/[\s…]+/).some((w) => w.startsWith(lq));
    };
    return [...resources, ...actions.filter((a) => starts(a.label))];
  }, [attach, plan, rows, empty, browsing, q]);

  const [active, setActive] = useState(0);
  useEffect(() => setActive(0), [q]);
  const current = Math.min(active, Math.max(entries.length - 1, 0));

  const choose = (e: Entry) => {
    if (e.kind === "action") return onPick({ kind: "action", action: e.action });
    // from the +, a folder opens in the search box; from a typed @ the box's token keeps browsing
    if (from === "plus" && e.row.kind === "file" && e.row.dir) return setSearch(e.row.insert.slice(1));
    onPick({ kind: "row", row: e.row });
  };
  const key = (k: string): boolean => {
    if (!entries.length) return false;
    if (k === "ArrowDown" || k === "ArrowUp") {
      setActive((a) => (Math.min(a, entries.length - 1) + (k === "ArrowDown" ? 1 : entries.length - 1)) % entries.length);
      return true;
    }
    if (k === "Enter" || k === "Tab") {
      choose(entries[current]);
      return true;
    }
    return false;
  };
  useImperativeHandle(handle, () => ({ key }));

  // the glide highlight (the slash menu's): one fill moving to the active row
  const rowRefs = useRef<(HTMLElement | null)[]>([]);
  const [box, setBox] = useState<{ top: number; height: number } | null>(null);
  useLayoutEffect(() => {
    const el = rowRefs.current[current];
    setBox(el ? { top: el.offsetTop, height: el.offsetHeight } : null);
    el?.scrollIntoView({ block: "nearest" });
  }, [current, entries]);

  // from the +: a click anywhere else closes it (the + itself toggles it)
  const root = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (from !== "plus") return;
    const down = (e: MouseEvent) => {
      const target = e.target as Element | null;
      if (root.current?.contains(target) || target?.closest?.("[data-composer-plus]")) return;
      onClose(false);
    };
    document.addEventListener("mousedown", down);
    return () => document.removeEventListener("mousedown", down);
  }, [from, onClose]);

  // the search box takes the keys at once; not on a touch screen alone, where the focus would raise the keyboard over the rows
  const searchBox = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (from === "plus" && hasFinePointer()) searchBox.current?.focus({ preventScroll: true });
  }, [from]);

  const hasActions = entries.some((e) => e.kind === "action");
  const row = (e: Entry, i: number) => {
    const on = i === current;
    const common = {
      ref: (el: HTMLElement | null) => {
        rowRefs.current[i] = el;
      },
      type: "button" as const,
      role: "option",
      "aria-selected": on,
      // on a move, not on entering: the panel opening under a resting pointer leaves the first row active
      onMouseMove: () => setActive(i),
      onClick: () => choose(e),
    };
    if (e.kind === "action") {
      return (
        <button key={`action:${e.action}`} {...common} data-panel-action={e.action} aria-checked={e.checked === undefined ? undefined : e.checked} title={e.hint} className="relative z-10 flex w-full items-center gap-2 rounded-[8px] px-3 py-2 text-left text-[14px] leading-[20px] text-ink">
          <Icon name={e.icon} size={14} stroke={2} className="shrink-0" />
          <span className="min-w-0 flex-1 truncate">{e.label}</span>
          {e.checked && <Icon name="check" size={13} stroke={2.5} className="shrink-0 text-ink" />}
        </button>
      );
    }
    const r = e.row;
    if (r.kind === "skill") return skills?.row(r.skill, common);
    return (
      <button key={r.id} {...common} title={r.detail ? `${r.name} · ${r.detail}` : r.name} className="relative z-10 flex h-[44px] w-full items-center gap-2 rounded-[8px] px-[10px] text-left text-[14px] text-ink">
        <Icon name={r.kind === "session" ? "chat" : r.kind === "workspace" || r.dir ? "folder" : "file"} size={16} className="shrink-0 text-ink-3" />
        <span className="min-w-0 truncate">{r.name}</span>
        {r.detail && <span className="ml-auto max-w-[min(240px,50%)] shrink-0 truncate pl-2 text-[12px] text-ink-3">{r.detail}</span>}
      </button>
    );
  };

  return (
    <div
      ref={root}
      data-composer-panel={from}
      onMouseDown={(e) => {
        // the caret stays where it is (the message box, or the search box from the +)
        if (!(e.target as Element).closest("input")) e.preventDefault();
      }}
      onBlur={(e) => {
        if (from === "plus" && !root.current?.contains(e.relatedTarget as Node | null)) onClose(false);
      }}
      className={cx(
        "absolute left-0 z-20 flex max-h-[min(60vh,400px)] w-[480px] max-w-full flex-col overflow-hidden rounded-[12px] border border-line bg-surface p-[6px] shadow-raised max-sm:w-full",
        // from the + it covers the box, growing up from the +; from a typed @ it sits above the box
        from === "plus" ? "bottom-0" : "bottom-full mb-2",
      )}
      style={{ animation: `pop-in 180ms ${EASE} both`, transformOrigin: "bottom left" }}
    >
      {from === "plus" && (
        <div className="relative shrink-0 px-1 pb-1.5">
          <Icon name="search" size={12} stroke={2} className="pointer-events-none absolute top-1/2 left-3.5 -translate-y-1/2 text-ink-3" />
          <input
            ref={searchBox}
            type="text"
            value={search}
            data-composer-panel-search
            aria-label={searchLabel}
            placeholder={searchLabel}
            onChange={(e) => setSearch(e.target.value)}
            onKeyDown={(e) => {
              if (e.nativeEvent.isComposing || e.keyCode === 229) {
                // the IME's own Escape (cancelling a candidate) must not reach ChatView's interrupt on the window
                if (e.key === "Escape") e.stopPropagation();
                return;
              }
              if (e.key === "Escape" || (e.key === "Backspace" && !search)) {
                // closing the panel only: kept from ChatView's interrupt on the window
                e.preventDefault();
                e.stopPropagation();
                onClose(true);
                return;
              }
              if (e.key !== "Tab" && key(e.key)) e.preventDefault();
            }}
            className="h-8 w-full truncate rounded-full border border-line bg-transparent pr-2 pl-7 text-[13px] text-ink outline-none placeholder:text-ink-3 focus:border-line-strong [@media(pointer:coarse)]:text-[16px]"
          />
        </div>
      )}
      <div role="listbox" aria-label={t("chat-composerpanel.label")} className="min-h-0 flex-1 overflow-y-auto">
        <div className="relative">
          <span aria-hidden className="pointer-events-none absolute inset-x-0 rounded-[8px] bg-hover" style={{ ...box, opacity: box ? 1 : 0, transition: `top 200ms ${EASE}, height 200ms ${EASE}` }} />
          {empty && !browsing && hasActions && <div className="flex h-6 items-center px-[10px] text-[12px] font-medium text-ink-3">{t("chat-composerpanel.add")}</div>}
          {entries.map(row)}
        </div>
        {loading && !rows.length && (
          <div role="status" aria-label={t("chat-composerpanel.reading_folder")} className="space-y-1 p-1">
            {[0, 1, 2].map((i) => <div key={i} className={`${BUSY_MOTION} h-[44px] animate-pulse rounded-[8px] bg-hover`} />)}
          </div>
        )}
        {!loading && !entries.length && (from === "at" || !empty) && (
          <div className="flex h-10 items-center justify-center text-[13px] text-ink-3">
            {browsing ? t("chat-composerpanel.no_match_in_folder") : empty ? searchLabel : t("chat-composerpanel.no_match")}
          </div>
        )}
      </div>
      {skills?.status}
      {/* under the rows, not scrolled with them: what typing after the @ does */}
      {from === "at" && empty && entries.length > 0 && <div className="mt-1.5 shrink-0 border-t border-line px-[10px] pt-2 pb-1 text-[12px] text-ink-3">{searchLabel}</div>}
    </div>
  );
}
