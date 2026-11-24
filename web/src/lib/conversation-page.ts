// A Persona conversation's page (`/persona/<id>`). With several assistants the conversation may be another assistant's
// than the current one (an inbox item's hand-off, a link, the palette, a to-do's origin), and the page must open it on
// the computer it runs on: what this tab noted at a hand-off (lib/outbox.ts adoptConversation) is gone after a reload
// and in a new tab, and a hub with the paged sessions list pushes no rows to look it up in. So the address names the
// computer (`?machine=`) and, when it is another manager's than the default, the manager (`?manager=`); the page asks the
// hub for that exact row, which the rest (its manager's offline state, its plan buttons) then reads (docs/web/web.md).
// One assistant: the plain address, as before.
import { chosenManager, defaultManager, otherManager } from "./manager-choice";
import { currentRoster, multiManager } from "./managers";
import type { Route } from "./router";
import type { AssistantInfo, Session } from "./types";

/** The page of conversation sid, on computer mid, of manager (none: the default's). Without the computer (a to-do names
 *  only its origin conversation) the manager is named even when it is the default: the page reads where its assistant
 *  runs. */
export function conversationPage(sid: string, mid?: string, manager?: string, at?: string): Route {
  const msg = at ? { at } : {};
  if (!multiManager(currentRoster())) return { name: "persona", sid, ...msg };
  if (!mid) return { name: "persona", sid, manager: manager || defaultManager(), ...msg };
  const other = otherManager(manager);
  return { name: "persona", sid, mid, ...(other ? { manager: other } : {}), ...msg };
}

/** A conversation's page: the assistant's are in Persona (/persona/<sid>, naming its computer and manager with several
 *  assistants: lib/conversation-page.ts), an agent's is /s/<machine>/<sid>. `manager`: whose it is when the row doesn't
 *  say (an inbox item's). */
export function conversationRoute(mid: string, sid: string, sessions: Record<string, Session[]>, assistant: AssistantInfo | null, manager?: string): Route {
  const s = (sessions[mid] ?? []).find((x) => x.session_id === sid);
  const own = assistant?.machine_id === mid && assistant.conversations.some((c) => c.session_id === sid);
  // the current assistant's own list has it: the current one's
  if (s?.assistant && s.space) return spacePage(s.space, sid);
  return s?.assistant || own ? conversationPage(sid, mid, s?.manager ?? manager ?? (own ? chosenManager() ?? undefined : undefined)) : { name: "session", mid, sid };
}

/** A space's conversation's page: its track's `/tracks/<slug>/<sid>` (the health space's too, which the health module's
 *  track page opens), where its plan cards, questions and tasks show (the bare `/s/…` chat is an agent's and shows none
 *  of them). */
export function spacePage(space: string, sid: string): Route {
  return { name: "track", slug: space, sid };
}
