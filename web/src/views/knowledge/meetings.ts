import type { KBList } from "../../lib/kb";

// Knowledge's Meetings list (#8871, #8872), merged here beside its only page: lib/kb.ts loads with the first screen, and
// this needn't.

/** The meetings list (#8871): the meetings imported from connected apps and the recordings made in Persona, read from
 *  both lists; tags that both have appear once. A meeting both recorded and imported is one row (#8872): the recording's,
 *  which carries the imported meeting (`imported`), and the imported meeting's own row goes. */
export function mergeMeetingLists(meetings: KBList, recordings: KBList): KBList {
  const tags = [...(meetings.tags ?? []), ...(recordings.tags ?? [])];
  const byID = new Map((meetings.items ?? []).map((m) => [m.id, m]));
  const covered = new Set<string>();
  const recs = (recordings.items ?? []).map((r) => {
    const m = r.meeting_id ? byID.get(r.meeting_id) : undefined;
    if (!m) return r;
    covered.add(m.id);
    return { ...r, imported: m };
  });
  return { items: [...(meetings.items ?? []).filter((m) => !covered.has(m.id)), ...recs], tags: [...new Set(tags)] };
}
