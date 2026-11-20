// What the sidebar's files share: the row and menu types, a row's key, the section heading and count (SidebarSection,
// passed on from here), the badge and dot colors, the pages hook and navigation. It imports none of them (Sidebar,
// SidebarRows, SidebarTree, SidebarRail).
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { t } from "../i18n";
import { resolveTab } from "../lib/herdr-tree";
import { useInboxBadge } from "../lib/inbox";
import { useLarkHome } from "../lib/lark-chats";
import { agentKey } from "../lib/pins";
import { NAV_PAGES, extensionNavKey, sidebarActiveKey } from "../lib/persona-nav";
import { arrangeSections, useSidebarPrefs } from "../lib/sidebar-pages";
import { useStartPageCheck } from "../lib/start-page";
import { useExtensionSlot } from "../lib/extension-slots";
import { toneOf } from "../lib/extension-ui";
import { colorOf, hue, type UIColor } from "../lib/extension-ui-v6";
import { BUILTIN_PAGE_COLORS, useSidebarIconMode } from "../lib/sidebar-icons";
import { BADGE_TONE, navIcon, pageRoute } from "./extensions/ExtensionSlots";
import { href, inWorkView, navigate, type Route } from "../lib/router";
import { useAgentSessions } from "../lib/sessions";
import { scoped, useScope, useScopedCounts } from "../lib/current-assistant";
import { useOwnerNeeds } from "../lib/todo-owner-needs";
import { useLarkOn, useStore } from "../lib/store";
import { connPhase } from "../lib/connection";
import { useWorkspaceBoardSetting } from "../lib/workspace-board-setting";
import type { Session } from "../lib/types";
import type { MenuItem } from "../ui/atoms";
import { agentsFoldAction, setAgentsFolded } from "../lib/sidebar-api";
import { SESSION_LIST_STYLE } from "../ui/list-metrics";
import { FoldAllButton } from "../ui/FoldAllButton";
import type { IconName } from "../ui/icons";
import type { MenuNode } from "../lib/node-actions";
import type { MenuAt } from "../ui/menu-model";

/** The open row's own fill (#2283): it stays while the pointer is elsewhere in the list, a step darker than hover. */
export const CURRENT_ROW = "bg-hover-2";
/** The least an agent row's title gets behind a prefix (styles.css `--row-title-min`, about 4.5 characters). */
export const ROW_TITLE_MIN = "min-w-(--row-title-min)";

/** The row a route lights up in the sidebar: a tab's page is the pane it shows (its row in the tree is the tab's). */
export function useActiveKey(route: Route): string {
  const all = useAgentSessions();
  const assistant = useStore((s) => s.assistant);
  const tabPath = route.name === "tab" ? href(route) : "";
  // eslint-disable-next-line react-hooks/exhaustive-deps -- `route` is parsed anew each render; its path says it all
  const tabPane = useMemo(() => (route.name === "tab" ? resolveTab(all, route) : undefined), [all, tabPath]);
  return inWorkView(route) ? "terminal" : route.name === "tab" ? (tabPane ? rowKey(tabPane) : "") : sidebarActiveKey(route, assistant?.main);
}

// One function for the component's life, so the rows given it skip a re-render (memo); a click still calls the
// latest onNavigate (the phone's drawer closing).
export function useGo(onNavigate?: () => void): GoFn {
  const latest = useRef(onNavigate);
  useLayoutEffect(() => {
    latest.current = onNavigate;
  });
  return useCallback(
    (to: Route) => (e: React.MouseEvent) => {
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
      e.preventDefault();
      navigate(to);
      latest.current?.();
    },
    [],
  );
}

/** One entry of the pages under Persona ("Overview", "Inbox" … "Settings", and the extensions' pages). `badgeLabel`:
 *  what a screen reader hears for the badge in place of its digits, when the number alone does not say what it counts.
 *  `color`: its icon's, only with Settings › Appearance › Sidebar icons on Colored (#10822, lib/sidebar-icons.ts). */
export type NavPage = { k: string; to: Route; icon: IconName; label: string; short?: string; badge?: number | string; badgeTone?: keyof typeof BADGE_CLASS; badgeLabel?: string; hint?: string; title?: string; group?: { id: string; title: string }; color?: UIColor };

/** A page's icon in its color (#10822), unless it is the open page, whose icon keeps the open row's ink; none without a
 *  color, so Monochrome draws what it always drew. */
export const pageIconStyle = (color: UIColor | undefined, active: boolean) => (color && !active ? { color: hue(color) } : undefined);

/** A page's spoken name with its badge ("Inbox 3", "To-dos 2 to-dos need you"): the phone's grid, the rail and the folded
 *  titles say what the list's row says. */
export function pageLabel(p: NavPage): string {
  return p.badge === undefined ? p.label : `${p.label} ${p.badgeLabel ?? p.badge}`;
}

/** The sidebar's pages ("Overview" … "Settings", the extensions' pages) and the connection, shared by the sidebar and its rail. */
export function useNavPages() {
  const phase = connPhase(useStore((s) => s.conn), useStore((s) => s.link));
  // the counts on the pages are the current assistant's (docs/assistant/managers.md "Clients"); the other assistants' are in
  // the switcher at the sidebar's head. Numbers, not the computers: a push of a computer's load or memory leaves them
  // equal and doesn't draw the sidebar again.
  const scope = useScope();
  const online = useStore((s) => scoped(s.machines, scope).filter((m) => m.online).length);
  const total = useStore((s) => scoped(s.machines, scope).length);
  const assistant = useStore((s) => s.assistant);
  const todosSupported = useStore((s) => s.todosSupported);
  // The open to-dos that need the owner, not those Agents are on (#11085, lib/todo-triage ownerNeeds); quiet tasks
  // (#1731) are told about in the main conversation, not counted here. The To-dos page's "Not done" counts every open one,
  // and the page marks the rows counted here from the same hook (#11838), so the number and the marks agree.
  const todosBadge = useOwnerNeeds().size;
  const todosBadgeLabel = todosBadge ? t("sidebar.todos_badge_title", { count: todosBadge }) : undefined;
  const counts = useScopedCounts();
  const inbox = useInboxBadge();
  const larkSwitch = useLarkHome();
  const lark = useLarkOn();
  // "Messages" needs the hub to read Feishu (feature `lark`) as well as this browser's switch
  const larkHome = larkSwitch && lark;
  const extensionPages = useExtensionSlot("nav_page");
  // Colored sidebar icons (#10822): a built-in page's color, an extension's page the one its manifest names; none otherwise
  const colored = useSidebarIconMode() === "colored";
  const tint = (p: NavPage, color: UIColor | undefined): NavPage => (colored && color ? { ...p, color } : p);
  // off unless turned on in Settings › General › Agent (#2475); null until the hub answered
  const board = useWorkspaceBoardSetting().enabled;
  const boardOn = board === true;

  const main = assistant?.main ? assistant.conversations.find((c) => c.session_id === assistant.main) : undefined;
  const page: Record<(typeof NAV_PAGES)[number], NavPage | false> = {
    home: { k: "home", to: { name: "home" }, icon: "home", label: t("sidebar.nav_home"), short: t("sidebar.nav_home_short"), badge: counts.badge || undefined, badgeTone: "attention" },
    inbox: { k: "inbox", to: { name: "inbox" }, icon: "inbox", label: t("sidebar.nav_inbox"), short: t("sidebar.nav_inbox_short"), badge: inbox || undefined, badgeTone: "attention", title: inbox ? t("sidebar.inbox_badge_title", { count: inbox }) : undefined },
    health: { k: "health", to: { name: "health" }, icon: "heart", label: t("sidebar.nav_health"), short: t("sidebar.nav_health_short") },
    messages: larkHome && { k: "messages", to: { name: "messages" }, icon: "chat", label: t("sidebar.nav_messages") },
    "workspace-board": boardOn && { k: "workspace-board", to: { name: "workspace-board" }, icon: "layers", label: t("sidebar.nav_board"), short: t("sidebar.nav_board_short") },
    todos: todosSupported !== false && { k: "todos", to: { name: "todos" }, icon: "todo", label: t("sidebar.nav_todos"), badge: todosBadge || undefined, badgeLabel: todosBadgeLabel, title: todosBadgeLabel },
    routines: { k: "routines", to: { name: "routines" }, icon: "clock", label: t("sidebar.nav_routines"), short: t("sidebar.nav_routines_short") },
    kb: { k: "kb", to: { name: "kb" }, icon: "book", label: t("sidebar.nav_kb"), short: t("sidebar.nav_kb_short") },
    machines: { k: "machines", to: { name: "machines" }, icon: "machine", label: t("sidebar.nav_machines"), short: t("sidebar.nav_machines_short"), hint: total ? `${online}/${total}` : undefined },
    terminal: false, // History belongs to Agents, not the general Pages grid (#10240).
    settings: { k: "settings", to: { name: "settings" }, icon: "gear", label: t("sidebar.nav_settings") },
  };
  const all = NAV_PAGES.flatMap((k): NavPage[] => [
    // the extensions' pages (docs/extensions/extensions.md "UI slots" nav_page): one entry each, right before "Settings"
    ...(k === "settings"
      ? extensionPages.map((u) => {
          const key = extensionNavKey(u.extension, u.id);
          return tint({ k: key, to: pageRoute(u.extension, u.id), icon: navIcon(u.icon), label: u.title, badge: u.badge?.text || undefined, badgeTone: BADGE_TONE[toneOf(u.badge?.tone)], title: u.extension_name ? `${u.title} · ${u.extension_name}` : u.title, group: u.group }, colorOf(u.icon_color));
        })
      : []),
    ...(page[k] ? [tint(page[k], k === "terminal" ? undefined : BUILTIN_PAGE_COLORS[k])] : []),
  ]);

  // as arranged in Settings › General › Sidebar pages: the list, and the pages that wait under More (docs/web/web.md "The sidebar's pages")
  // and in their groups (#9071): `pages` is every group's in order, for the rail, the drawer and ⌘K
  const { sections, more } = arrangeSections(all, useSidebarPrefs());
  const pages = sections.flatMap((s) => s.pages);
  // the start page this load opened on, if it is not there after all, gives way to the main chat (lib/start-page.ts)
  useStartPageCheck((k) => all.some((p) => p.k === k), extensionPages, board !== null);

  return { phase, online, total, main, pages, sections, more, all };
}

/** The pages under "More" as menu rows (the phone's grid, the rail): a choice opens the page, then `onNavigate`
 *  (the phone's drawer closes, as it does for a tap on any other page). */
export function morePagesMenu(more: NavPage[], activeKey: string, onNavigate?: () => void): MenuItem[] {
  const open = (to: Route) => () => {
    navigate(to);
    onNavigate?.();
  };
  return [
    ...more.map((p): MenuItem => ({ key: p.k, label: p.label, icon: p.icon, hint: p.badge !== undefined ? String(p.badge) : p.hint, checked: p.k === activeKey || undefined, onSelect: open(p.to) })),
    { key: "-arrange", label: t("sidebar.arrange_pages"), icon: "gear", divider: true, onSelect: open({ name: "settings" }) },
  ];
}

export { GroupCount, Section, SidebarGroup } from "./SidebarSection";

const FOLD_LABEL = { "open-block": "sidebar.open_all", "fold-groups": "sidebar.fold_all", "open-groups": "sidebar.open_all" } as const;

/** The Agents title row's one fold control (#3222): it acts on the list under the controls, never on the grouping
 *  tabs, quick filters, search or Filter. Its action follows the group headings on screen, folded by hand, by the
 *  keyboard shortcut or by it, and the block's own fold under the Agents title (`block`, absent where the list cannot
 *  fold), so a click always changes something; a list without groups shows no button (#6447, lib/sidebar-api.ts
 *  `agentsFoldAction`). The row around it is its tooltip's box. */
export function GroupFoldButton({ groups, onAction, block, flat }: { groups: React.RefObject<HTMLElement | null>; onAction: () => void; block?: { folded: boolean; toggle: () => void }; flat?: boolean }) {
  const [seen, setSeen] = useState({ groups: 0, open: false });
  useEffect(() => {
    const root = groups.current;
    if (!root) return;
    const read = () => {
      const shown = [...root.querySelectorAll<HTMLButtonElement>("[data-sidebar-group-heading] button[aria-expanded]")]
        .filter((button) => !button.closest("[data-sidebar-agents-controls], [inert]"));
      const open = shown.some((button) => button.getAttribute("aria-expanded") === "true");
      setSeen((s) => (s.groups === shown.length && s.open === open ? s : { groups: shown.length, open }));
    };
    read();
    const observer = new MutationObserver(read);
    observer.observe(root, { attributes: true, attributeFilter: ["aria-expanded", "inert"], childList: true, subtree: true });
    return () => observer.disconnect();
  }, [groups]);
  const action = agentsFoldAction({ blockFolded: Boolean(block?.folded), flat, groups: seen.groups, open: seen.open });
  if (!action) return null;
  const run = () => {
    if (action === "open-block") block?.toggle();
    setAgentsFolded(action === "fold-groups");
    onAction();
  };
  return <FoldAllButton label={t(FOLD_LABEL[action])} expand={action !== "fold-groups"} onClick={run} className={SESSION_LIST_STYLE.foldButton} testId="agents-fold-all" />;
}


export type RegFn = (k: string) => (el: HTMLElement | null) => void;
/** An agent row's key in the sidebar (highlight, active): sidebarActiveKey's for a session. */
export const rowKey = (s: Pick<Session, "machine_id" | "session_id">) => `s:${agentKey(s)}`;
/** Whether the open row (`activeKey`) is one of these agents': its group opens for it (useFolded). */
export const holdsRow = (sessions: readonly Session[], activeKey: string) => sessions.some((s) => rowKey(s) === activeKey);
export type GoFn = (to: Route) => (e: React.MouseEvent) => void;
/** `at`: where a right click or the menu key opened it; without it (the ⋯) the row's menu toggles beside its button.
 *  A row's ⋯ lies beside its link, not in it, and gives as `anchor` its parent, the box holding both: the menu
 *  closes on a pointerdown outside its anchor, and a press on the ⋯ has to stay inside it for the click that
 *  follows to toggle. */
export type RowMenuFn = (s: Session, anchor: HTMLElement, at?: MenuAt) => void;
/** A machine's, a workspace's or a tab's menu (src/lib/node-actions.ts); `toggle`: from its ⋯, which closes it again
 *  (a row's ⋯ anchors it on the box holding the row and the ⋯, as in RowMenuFn). */
export type NodeMenuFn = (node: MenuNode, at: MenuAt, header?: string, toggle?: boolean) => void;

export const BADGE_CLASS = { attention: "bg-field text-attention", red: "bg-red-tint text-red", orange: "bg-orange-tint text-orange", green: "bg-green-tint text-green", accent: "bg-accent-tint text-accent-ink" } as const;

/** The badge of a folded page, as a dot on its icon. */
export const DOT_CLASS = { attention: "bg-attention", red: "bg-red", orange: "bg-orange", green: "bg-green", accent: "bg-accent" } as const;
