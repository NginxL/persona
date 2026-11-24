// The strip at the top of an Agent's page opened from a conversation's hand-off card (#5096, cindy's HandoffSourcePill)
// or from inside another page (a to-do, a knowledge project, a meeting, #12055): "Came from <conversation or page> ·
// click to return", with ✕ to put it away. Returning goes back in the history, so the conversation's chat is where it was
// left (lib/handoff.ts).
import { handoffFrom } from "../../lib/handoff";
import { sessionTitle } from "../../lib/format";
import { navigate, parse, useRoute, type Route } from "../../lib/router";
import { useStore, type State } from "../../lib/store";
import { IconButton } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { conversationTitle } from "../assistant/conversations";
import { t } from "../../i18n";
import { moduleNavItem } from "../../core/module-host";

/** A page the strip names by its row in the sidebar when it has no title of its own to say (#12055). */
const PAGE_NAMES: Partial<Record<Route["name"], string>> = {
  home: "sidebar.nav_home",
  inbox: "sidebar.nav_inbox",
  "workspace-board": "sidebar.nav_board",
  todos: "sidebar.nav_todos",
  routines: "sidebar.nav_routines",
  kb: "sidebar.nav_kb",
  machines: "sidebar.nav_machines",
  machine: "sidebar.nav_machines",
  settings: "sidebar.nav_settings",
};

/** What the conversation or page at `path` is called now: a conversation's title (Persona for the main one), a to-do's
 *  or an Agent's title, else the page's name in the sidebar; undefined for a page with neither (#12055). */
export function fromTitle(s: Pick<State, "assistant"> & Partial<Pick<State, "todos" | "sessions">>, path: string): string | undefined {
  const [p, q] = path.split("?");
  const r = parse(p, q ? `?${q}` : "");
  if (r.name === "persona") {
    const main = s.assistant?.main ?? "";
    const sid = r.sid || main;
    const row = s.assistant?.conversations.find((c) => c.session_id === sid);
    return conversationTitle(row ?? { title: "" }, sid === main);
  }
  const todo = r.name === "todos" && r.id ? s.todos?.find((x) => x.id === r.id) : undefined;
  if (todo?.title.trim()) return todo.title.trim();
  const agent = r.name === "session" ? s.sessions?.[r.mid]?.find((x) => x.session_id === r.sid) : undefined;
  if (agent) return sessionTitle(agent);
  // a feature module's page: its sidebar entry's name (core/module-host.ts)
  if (r.name === "module") return moduleNavItem(r)?.label();
  const name = PAGE_NAMES[r.name];
  return name ? t(name) : undefined;
}

export function BackStrip({ label, dismissLabel, onBack, onDismiss, inbox = false }: { label: string; dismissLabel: string; onBack: () => void; onDismiss: () => void; inbox?: boolean }) {
  return (
    <div data-handoff-back={inbox ? undefined : ""} data-inbox-back={inbox ? "" : undefined} className="shrink-0 px-4 pt-3">
      <div className="mx-auto flex h-10 w-full max-w-[880px] items-center rounded-xl border border-line bg-surface text-[13px] leading-none text-ink-2">
        <button
          type="button"
          onClick={onBack}
          className="flex h-full min-w-0 flex-1 items-center gap-2 rounded-l-xl px-3 text-left transition-colors hover:text-ink focus-visible:ring-2 focus-visible:ring-ink focus-visible:outline-none"
        >
          <Icon name="cornerUpLeft" size={14} stroke={2} className="shrink-0" />
          <span className="min-w-0 truncate">{label}</span>
        </button>
        <IconButton icon="x" label={dismissLabel} onClick={onDismiss} size={28} iconSize={14} className="mr-2" />
      </div>
    </div>
  );
}

export function HandoffBack({ onDismiss }: { onDismiss: () => void }) {
  // The origin can be replaced at the same address when the open Agent is reselected as a root (#12055).
  useRoute();
  const from = handoffFrom();
  const title = useStore((s) => from ? fromTitle(s, from.path) : undefined);
  if (!from) return null;
  // the entry before this one is the conversation or page (the card or row pushed this one); a page opened on its own has
  // none to go to
  const back = () => (history.length > 1 ? history.back() : navigate(from.path));
  const label = title === undefined ? t("chat-backstrip.handoff_previous") : t("chat-backstrip.handoff_from", { title });
  return <BackStrip label={label} dismissLabel={t("chat-backstrip.handoff_dismiss")} onBack={back} onDismiss={onDismiss} />;
}
