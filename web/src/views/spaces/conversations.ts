// A domain space's conversations (#13500), read as the health page reads its own (modules/health/Health.tsx): the
// default manager's list for the space, with the live sessions of the space's computer merged in for their titles and
// last lines, read again on every hello.
import { useCallback, useEffect, useMemo, useState } from "react";
import { errorText } from "../../lib/api";
import { noteConversation } from "../../lib/manager-choice";
import { onHubEvent, useStore } from "../../lib/store";
import { listSpaceConversations } from "../../lib/tracks";
import type { Session } from "../../lib/types";
import { newestFirst } from "../assistant/conversations";

/** The space's open conversations, newest first (null until read), the computer they run on, and why the list could
 *  not be read. */
export function useSpaceConversations(slug: string) {
  const [list, setList] = useState<Session[] | null>(null);
  const [mid, setMid] = useState<string | undefined>(undefined);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    try {
      const a = await listSpaceConversations(slug);
      for (const c of a.conversations ?? []) noteConversation(c.session_id, undefined);
      setList(a.conversations ?? []);
      setMid(a.machine_id || undefined);
      setError("");
    } catch (e) {
      setError(errorText(e));
    }
  }, [slug]);
  useEffect(() => {
    void load();
    return onHubEvent("hello", () => void load());
  }, [load]);
  const live = useStore((s) => (mid ? s.sessions[mid] : undefined));
  const merged = useMemo(() => mergeSpace(list, live, slug), [list, live, slug]);
  return { list: merged, mid, error, reload: load };
}

/** `list` with the live sessions of the space (`space` equal to its slug) laid over it, archived ones left out. */
export function mergeSpace(list: readonly Session[] | null, live: readonly Session[] | undefined, slug: string): Session[] | null {
  if (!list) return null;
  const by = new Map(list.map((s) => [s.session_id, s]));
  for (const s of live ?? []) if (s.space === slug) by.set(s.session_id, { ...by.get(s.session_id), ...s });
  return newestFirst([...by.values()].filter((s) => !s.archived));
}
