// Small pieces the knowledge pages share: the kind tile, tinted pills, marked text, section headers, local icons.
import { importanceTone, initial, isKind, markParts, projectKindLabel, relevanceTone, topicPointCount, type KBDetail, type KBEntry, type KBKind, type Tone } from "../../lib/kb";
import { cx } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { t } from "../../i18n";
import { connectorBrand } from "../../lib/connectors";

// Icons the shared set does not have yet (person, video, calendar, tag); same grammar as ui/icons.tsx.
const LOCAL: Record<string, React.ReactNode> = {
  person: (
    <g>
      <circle cx="9" cy="8" r="3.5" />
      <path d="M2.5 20a6.5 6.5 0 0 1 13 0M16 4.5a3.5 3.5 0 0 1 0 7M18.5 14.5A6.5 6.5 0 0 1 21.5 20" />
    </g>
  ),
  video: (
    <g>
      <rect x="2" y="6" width="14" height="12" rx="2.5" />
      <path d="m16 10.5 6-3.5v10l-6-3.5" />
    </g>
  ),
  calendar: (
    <g>
      <rect x="3" y="4.5" width="18" height="16.5" rx="2.5" />
      <path d="M3 9.5h18M8 2.5v4M16 2.5v4" />
    </g>
  ),
  tag: (
    <g>
      <path d="M3 12.6V4a1 1 0 0 1 1-1h8.6a1 1 0 0 1 .7.3l7.4 7.4a1.5 1.5 0 0 1 0 2.1l-7.6 7.6a1.5 1.5 0 0 1-2.1 0L3.3 13.3a1 1 0 0 1-.3-.7z" />
      <circle cx="7.5" cy="7.5" r="1.4" fill="currentColor" stroke="none" />
    </g>
  ),
};

export type KBIconName = IconName | keyof typeof LOCAL;

export function KBIcon({ name, size = 15, stroke = 1.9, className }: { name: KBIconName; size?: number; stroke?: number; className?: string }) {
  if (!(name in LOCAL)) return <Icon name={name as IconName} size={size} stroke={stroke} className={className} />;
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={stroke} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" className={className}>
      {LOCAL[name]}
    </svg>
  );
}

export const KIND_ICON: Record<KBKind, KBIconName> = { projects: "layers", topics: "light", chats: "chat", people: "person", docs: "read", meetings: "video", events: "calendar", recordings: "mic" };

export const TINT: Record<Tone, string> = {
  red: "bg-red-tint text-red",
  orange: "bg-orange-tint text-orange",
  accent: "bg-accent-tint text-accent-ink",
  neutral: "bg-field text-ink-2",
};

/** A rounded tile carrying the importance: a person's or chat's first character, else the kind's icon. */
export function Tile({ entry, size = 36 }: { entry: Pick<KBEntry, "kind" | "name" | "importance">; size?: number }) {
  const tone = importanceTone(entry.importance);
  const ch = entry.kind === "people" || entry.kind === "chats" ? initial(entry.name) : "";
  return (
    <span
      aria-hidden
      className={cx("flex shrink-0 items-center justify-center font-semibold", TINT[tone], tone === "neutral" && "text-ink-3")}
      style={{ width: size, height: size, borderRadius: Math.round(size * 0.28), fontSize: Math.round(size * 0.42) }}
    >
      {ch || <KBIcon name={isKind(entry.kind) ? KIND_ICON[entry.kind] : "book"} size={Math.round(size * 0.44)} stroke={2} />}
    </span>
  );
}

export function TonePill({ tone = "neutral", strong, children, title }: { tone?: Tone; strong?: boolean; children: React.ReactNode; title?: string }) {
  return (
    <span title={title} className={cx("inline-flex h-[18px] shrink-0 items-center whitespace-nowrap rounded-full px-1.5 text-[11px] tabular-nums", strong ? "font-semibold" : "font-medium", TINT[tone])}>
      {children}
    </span>
  );
}

/** P0–P3 with a word saying how important that is; any other level as "Importance X". */
export function importanceLabel(level: string): string {
  switch (level) {
    case "P0":
      return t("knowledge-kbparts.importance_P0");
    case "P1":
      return t("knowledge-kbparts.importance_P1");
    case "P2":
      return t("knowledge-kbparts.importance_P2");
    case "P3":
      return t("knowledge-kbparts.importance_P3");
    default:
      return t("knowledge-kbparts.importance", { level });
  }
}

/** The importance, the project's kind and status, and the relevance, as pills that say what they are without a hover (phones have none). */
export function Badges({ entry, status, asProject = false }: { entry: KBEntry; status?: string | null; asProject?: boolean }) {
  const kind = projectKindLabel(entry);
  const points = topicPointCount(entry);
  return (
    <>
      {entry.importance && (
        <TonePill tone={importanceTone(entry.importance)} strong>
          {importanceLabel(entry.importance)}
        </TonePill>
      )}
      {kind && <TonePill tone={entry.extra?.startsWith("long") ? "accent" : "neutral"}>{asProject ? t("knowledge-kbparts.project_kind", { kind }) : kind}</TonePill>}
      {status && <TonePill>{status}</TonePill>}
      {points !== null && <TonePill>{t("knowledge-kbparts.points", { count: points })}</TonePill>}
      {entry.relevance > 0 && (
        <TonePill tone={relevanceTone(entry.relevance)} title={t("knowledge-kbparts.relevance_title")}>
          {t("knowledge-kbparts.relevance", { value: entry.relevance })}
        </TonePill>
      )}
    </>
  );
}

/** Text with every searched word in bold ink. */
export function Marked({ text, query }: { text: string; query: string }) {
  return (
    <>
      {markParts(text, query).map((p, i) =>
        p.hit ? (
          <strong key={i} className="font-semibold text-ink">
            {p.text}
          </strong>
        ) : (
          <span key={i}>{p.text}</span>
        ),
      )}
    </>
  );
}

export function SectionTitle({ title, count, right }: { title: React.ReactNode; count?: number | string; right?: React.ReactNode }) {
  return (
    <div className="mb-1.5 flex min-h-5 items-center gap-1.5 px-1">
      <h2 className="truncate text-[12px] font-medium text-ink-3">{title}</h2>
      {count !== undefined && <span className="text-[12px] text-ink-3 tabular-nums opacity-70">{count}</span>}
      <span className="flex-1" />
      {right}
    </div>
  );
}

export function Card({ children, className }: { children: React.ReactNode; className?: string }) {
  return <div className={cx("divide-y divide-line overflow-hidden rounded-card bg-surface shadow-card", className)}>{children}</div>;
}

/** "Show all N" / "Show less", the last row of a card. */
export function MoreRow({ open, label, onClick }: { open: boolean; label: string; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className="flex h-9 w-full items-center gap-1.5 whitespace-nowrap px-4 text-left text-[12.5px] font-medium text-accent-ink transition-colors hover:bg-hover">
      {open ? t("knowledge-kbparts.collapse") : label}
      <Icon name="chevron" size={12} stroke={2.2} className={cx("transition-transform duration-200", open && "rotate-180")} />
    </button>
  );
}

/** A plain-click link that stays in the page (⌘ / middle click still opens a new tab). */
export function onPlainClick(e: React.MouseEvent, go: () => void) {
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
  e.preventDefault();
  go();
}

/** What a link to an imported meeting's own notes says (#8872): the app's name where this client knows the connector
 *  (Lark), else "Original Meeting Notes". */
export function importedNotesLabel(source?: string): string {
  return connectorBrand(source ?? "") === "lark" ? t("knowledge-kbparts.open_imported_lark") : t("knowledge-kbparts.open_imported");
}

/** A recording's imported meeting (#8872), from its detail: `meta.imported_meeting` and the link the hub resolves for it,
 *  which has its notes' URL (`extra`) and connector (`source`); null without one, or with nothing to open. */
export function importedMeetingOf(d?: KBDetail | null): { url: string; source?: string } | null {
  if (d?.kind !== "recordings") return null;
  const ref = d.meta?.imported_meeting as { kind?: unknown; id?: unknown } | undefined;
  if (!ref || typeof ref.id !== "string") return null;
  const link = (d.links ?? []).find((l) => l.kind === "meetings" && l.id === ref.id);
  const url = link?.extra?.trim();
  return url ? { url, source: link?.source } : null;
}
