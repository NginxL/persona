// Used only by the lazy To-dos detail (views/Todos.tsx ReviewChecklist), so its code and i18n keys need not load with
// the first screen.
import { t } from "../i18n";
import { sessionTitle } from "./format";
import { sameManager } from "./manager-choice";
import type { SessionLookup } from "./todos";
import type { Session } from "./types";
import { conversationTitle, MAIN_TITLE } from "../views/assistant/conversations";

export interface EvidenceAuthorContext {
  /** The todo's owning manager; lookups are scoped to this manager so another manager's names never leak across. */
  manager?: string;
  /** Cached direction names scoped to the todo's manager (lib/direction-names.ts). */
  directionNames?: ReadonlyMap<string, string>;
  /** Conversations scoped to the todo's manager. */
  conversations?: readonly Pick<Session, "session_id" | "title" | "manager" | "deleted">[];
  /** Main conversation id of the todo's manager, if known. */
  mainConversationId?: string;
  /** Live session lookup for resolving `agent:<machine>/<session>`. */
  lookup?: SessionLookup;
}

/**
 * Maps a raw structured acceptance criterion author (`supervisor:<id>`, `agent:<machine>/<session>`, `judge`, etc.)
 * to a human-readable display name at the Web UI boundary without mutating the underlying `by` field.
 * Falls back to localized role labels when unavailable, deleted, out of manager scope, or unrecognized.
 */
export function evidenceAuthorLabel(by: string | undefined, ctx: EvidenceAuthorContext = {}): string | undefined {
  const raw = by?.trim();
  if (!raw) return undefined;

  if (raw.startsWith("supervisor:")) {
    const id = raw.slice("supervisor:".length).trim();
    if (!id) return t("assistant-tasks.evidence_author_supervisor");
    const direction = ctx.directionNames?.get(id)?.trim();
    if (direction) return direction;
    const conv = ctx.conversations?.find((c) => !c.deleted && c.session_id === id && sameManager(c.manager, ctx.manager));
    if (conv) return conversationTitle(conv, Boolean(ctx.mainConversationId && id === ctx.mainConversationId));
    if (ctx.mainConversationId && id === ctx.mainConversationId) return MAIN_TITLE;
    return t("assistant-tasks.evidence_author_supervisor");
  }

  if (raw.startsWith("agent:")) {
    const rest = raw.slice("agent:".length);
    const slash = rest.indexOf("/");
    if (slash > 0 && slash < rest.length - 1) {
      const machineId = rest.slice(0, slash);
      const sessionId = rest.slice(slash + 1);
      const session = ctx.lookup?.(machineId, sessionId);
      if (session && !session.deleted && sameManager(session.manager, ctx.manager)) {
        return sessionTitle(session);
      }
    }
    return t("assistant-tasks.evidence_author_agent");
  }

  if (raw === "judge") {
    return t("assistant-tasks.evidence_author_reviewer");
  }

  return t("assistant-tasks.evidence_author_reviewer");
}
