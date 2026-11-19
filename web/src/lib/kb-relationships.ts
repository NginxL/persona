import type { KBDetail, KBEntry, KBUpdate } from "./kb";

type List = "people" | "chats" | "projects" | "items";
type End = { kind: string; id: string; canonicalId: string; name: string; field: List; member: string };
export type Removal = { ends: [End, End]; target: { kind: string; id: string }; update: KBUpdate };
/** Lets Persona make a link the user removed again on its own; links nothing (docs/assistant/knowledge.md "Removed links"). */
export type Allowance = { target: { kind: string; id: string }; update: KBUpdate };

const items = new Set(["docs", "meetings", "events", "recordings"]);
const strings = (value: unknown): value is string[] => Array.isArray(value) && value.every((v) => typeof v === "string");
const record = (value: unknown): Record<string, unknown> | undefined =>
  value != null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;

/** The KB's SafeID storage identity; requests and metadata still carry the original source ID. */
export function relationshipCanonicalId(kind: string, id: string): string {
  const safe = (kind === "recordings" ? id.toLowerCase() : id).replace(/[^\p{L}\p{N}._@+-]+/gu, "_").replace(/^[._]+|[._]+$/g, "");
  let result = "", bytes = 0;
  const encoder = new TextEncoder();
  for (const char of safe) {
    bytes += encoder.encode(char).length;
    if (bytes > 120) break;
    result += char;
  }
  return result;
}

/** How a link between the detail and an entry is held: each side's list and member, and which side a write names. */
function pair(a: KBDetail, b: KBEntry) {
  const aid = typeof a.meta?.id === "string" ? a.meta.id : a.id;
  let af: List, bf: List, reverse = false;
  if (a.kind === "projects" && (b.kind === "people" || b.kind === "chats")) {
    af = b.kind; bf = "projects";
  } else if (a.kind === "projects" && items.has(b.kind)) {
    af = "items"; bf = "projects"; reverse = true;
  } else if (b.kind === "projects" && (a.kind === "people" || a.kind === "chats" || items.has(a.kind))) {
    af = "projects"; bf = a.kind === "people" || a.kind === "chats" ? a.kind : "items";
  } else if (a.kind === "people" && b.kind === "chats") {
    af = "chats"; bf = "people";
  } else if (a.kind === "chats" && b.kind === "people") {
    af = "people"; bf = "chats";
  } else return null;
  const bid = relationshipCanonicalId(b.kind, b.id);
  return { aid, af, bf, reverse, bid, am: af === "items" ? `${b.kind}/${bid}` : b.id, bm: bf === "items" ? `${a.kind}/${a.id}` : aid };
}

/** Detail.links also includes provenance. Only raw membership in a supported relationship list is editable. */
export function relationshipRemoval(detail: KBDetail, name: string, linked: KBEntry): Removal | null {
  const a = detail, b = linked, p = pair(a, b);
  if (!p) return null;
  const members = a.meta?.[p.af];
  if (!strings(members) || !members.includes(p.am)) return null;
  return {
    ends: [{ kind: a.kind, id: p.aid, canonicalId: a.id, name, field: p.af, member: p.am }, { kind: b.kind, id: b.id, canonicalId: p.bid, name: b.name, field: p.bf, member: p.bm }],
    target: p.reverse ? { kind: b.kind, id: b.id } : { kind: a.kind, id: p.aid },
    update: { [p.reverse ? "remove_projects" : `remove_${p.af}`]: [p.reverse ? p.aid : b.id] },
  };
}

/** The removal's request: lock_lists false keeps out only this link; true also stops automatic changes to both lists. */
export function removalUpdate(removal: Removal, lockLists: boolean): KBUpdate {
  return { ...removal.update, lock_lists: lockLists };
}

/** A link in detail.removed that the detail's own record keeps out, and the update that allows it again. */
export function relationshipAllowance(detail: KBDetail, removed: KBEntry): Allowance | null {
  const p = pair(detail, removed);
  if (!p) return null;
  const excluded = record(detail.meta?.excluded)?.[p.af];
  if (!strings(excluded) || !excluded.includes(p.am)) return null;
  return {
    target: p.reverse ? { kind: removed.kind, id: removed.id } : { kind: detail.kind, id: p.aid },
    update: { [p.reverse ? "allow_projects" : `allow_${p.af}`]: [p.reverse ? p.aid : removed.id] },
  };
}

export function matchesRemovalEnd(removal: Removal, detail: KBDetail, index: number): boolean {
  const end = removal.ends[index];
  return detail?.kind === end.kind && detail.id === end.canonicalId;
}

/** Both lists must be without the link, keep it out, and be user-held (a failed attribution cleanup can leave an
 *  extension in control); with lockLists both lists must also be locked. */
export function removalConfirmed(removal: Removal, details: KBDetail[], lockLists: boolean): boolean {
  return removal.ends.every((end, i) => {
    const d = details[i], members = d?.meta?.[end.field], locks = d?.meta?.locked_fields, provenance = d?.meta?.provenance;
    const excluded = record(d?.meta?.excluded)?.[end.field];
    const userHeld = provenance == null || (typeof provenance === "object" && !Array.isArray(provenance) && !Object.hasOwn(provenance, end.field));
    return matchesRemovalEnd(removal, d, i) &&
      (members == null || (strings(members) && !members.includes(end.member))) &&
      strings(excluded) && excluded.includes(end.member) &&
      (!lockLists || (strings(locks) && locks.includes(end.field))) && userHeld;
  });
}
