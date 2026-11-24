// Settings › General › Sidebar pages (docs/web/web.md "The sidebar's pages"): every page of the sidebar in its order, dragged (or moved with ↑ / ↓)
// into another, and switched between the list and More. Settings stays in the list; Reset forgets every choice.
// Groups (#9071): the pages under each group's heading, in the groups' order; a group the user makes, renames, moves or
// removes (with Undo), and a page moved to another group with its menu (or dragged onto a page of that group). The
// extensions' groups come in by themselves; removing one gives its pages back to Pages.
import { useState } from "react";
import { t } from "../../i18n";
import {
  addPageGroup, arrangeSections, EXTENSION_CAP, getSidebarPrefs, inList, LOCKED, movePageGroup, ordered, pageGroups, removePageGroup, renamePageGroup,
  resetSidebarPrefs, restoreSidebarPrefs, setPageGroup, setPageOrder, setPageShown, setMoreShown, useSidebarPrefs, chosen, moreShown,
} from "../../lib/sidebar-pages";
import { useStartPageSynced } from "../../lib/start-page";
import { Button, cx, IconButton, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { useNavPages } from "../Sidebar";
import { Group, Switch } from "./SettingsParts";

const FIELD = "h-8 min-w-0 rounded-control bg-field px-2.5 text-meta text-ink shadow-inset-field outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)]";

/** A group's name as typed: a new group's, or a new name for one (blank: an extension's group takes its own back). */
function NameField({ initial, label, onDone, onCancel }: { initial: string; label: string; onDone: (name: string) => void; onCancel: () => void }) {
  const [name, setName] = useState(initial);
  return (
    <form
      className="flex min-w-0 flex-1 items-center gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        onDone(name);
      }}
    >
      {/* focused as it opens: it opens on the user's own press */}
      <input autoFocus value={name} maxLength={40} onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === "Escape" && onCancel()} aria-label={label} className={cx(FIELD, "flex-1")} />
      <Button tone="primary" type="submit" disabled={!name.trim() && !initial} className="h-8">{t("settings.sidebar_groups_save")}</Button>
      <Button tone="quiet" onClick={onCancel} className="h-8">{t("settings.sidebar_groups_cancel")}</Button>
    </form>
  );
}

export function SidebarPagesGroup() {
  const { all } = useNavPages();
  const prefs = useSidebarPrefs();
  const synced = useStartPageSynced() !== false;
  const keys = ordered(all.map((p) => p.k), prefs.order);
  const listed = inList(keys, prefs);
  const byKey = new Map(all.map((p) => [p.k, p]));
  const groups = pageGroups(all, prefs);
  const ids = groups.map((g) => g.id);
  // every page in its group, put away or not, in the order of the list
  const { sections } = arrangeSections(all.map((p) => ({ ...p })), { ...prefs, hidden: [], shown: keys });
  const groupOf = new Map(sections.flatMap((s) => s.pages.map((p) => [p.k, s.id] as const)));
  const name = (id: string) => groups.find((g) => g.id === id)?.title ?? t("sidebar.pages");
  const [dragging, setDragging] = useState<string | null>(null);
  const [over, setOver] = useState<string | null>(null);
  const [editing, setEditing] = useState<string | null>(null); // a group's id, or "+" for a new one

  const drop = (target: string) => {
    if (!dragging || dragging === target) return;
    const next = keys.filter((k) => k !== dragging);
    next.splice(next.indexOf(target), 0, dragging);
    setPageOrder(next);
    // dropped on a page of another group: it goes there too
    const to = groupOf.get(target) ?? "";
    if (dragging !== LOCKED && to !== (groupOf.get(dragging) ?? "")) setPageGroup(dragging, to);
  };
  // ↑ / ↓ swap a page with the one beside it in its group, in the whole list's order
  const move = (k: string, inGroup: string[], by: -1 | 1) => {
    const other = inGroup[inGroup.indexOf(k) + by];
    if (!other) return;
    const next = [...keys];
    const [i, j] = [next.indexOf(k), next.indexOf(other)];
    [next[i], next[j]] = [next[j], next[i]];
    setPageOrder(next);
  };
  const remove = (id: string) => {
    const before = getSidebarPrefs();
    const named = name(id);
    removePageGroup(id);
    toast(t("settings.sidebar_groups_removed", { name: named }), "info", { label: t("settings.sidebar_groups_undo"), run: () => restoreSidebarPrefs(before) });
  };

  return (
    <Group
      anchor="sidebar-pages"
      title={t("settings.sidebar_pages")}
      footer={`${t("settings.sidebar_pages_footer", { cap: EXTENSION_CAP })} ${synced ? t("settings.start_page_footer_synced") : t("settings.start_page_footer_local")}`}
      right={chosen(prefs) ? <Button tone="quiet" onClick={resetSidebarPrefs} className="-mb-1 h-6 px-2 text-[11.5px]">{t("settings.sidebar_pages_reset")}</Button> : undefined}
    >
      {sections.map((s, gi) => {
        const inGroup = s.pages.map((p) => p.k);
        return (
          <div key={s.id || "pages"} data-page-group={s.id}>
            <div className="flex min-h-11 items-center gap-2 bg-inset px-3 py-1.5">
              {editing === s.id ? (
                <NameField initial={s.title ?? ""} label={t("settings.sidebar_groups_name")} onCancel={() => setEditing(null)} onDone={(v) => (renamePageGroup(ids, s.id, v), setEditing(null))} />
              ) : (
                <>
                  <h3 className="min-w-0 flex-1 break-words text-meta font-medium text-ink-2">{name(s.id)}</h3>
                  {s.id && <IconButton icon="pencil" label={t("settings.sidebar_groups_rename", { name: name(s.id) })} onClick={() => setEditing(s.id)} size={26} iconSize={13} />}
                  <IconButton icon="up" label={t("settings.sidebar_pages_up", { name: name(s.id) })} disabled={gi === 0} onClick={() => movePageGroup(ids, s.id, -1)} size={26} iconSize={13} />
                  <IconButton icon="arrowDown" label={t("settings.sidebar_pages_down", { name: name(s.id) })} disabled={gi === sections.length - 1} onClick={() => movePageGroup(ids, s.id, 1)} size={26} iconSize={13} />
                  {s.id && <IconButton icon="trash" label={t("settings.sidebar_groups_remove", { name: name(s.id) })} onClick={() => remove(s.id)} size={26} iconSize={13} />}
                </>
              )}
            </div>
            {!inGroup.length && <p className="px-3 py-2 text-submeta text-ink-3">{t("settings.sidebar_groups_empty")}</p>}
            <ul aria-label={name(s.id)} className="divide-y divide-line">
              {inGroup.map((k, i) => {
                const p = byKey.get(k)!;
                const on = listed.has(k);
                const locked = k === LOCKED;
                // an extension's page: the extension it comes from (its NavPage.title is "Page · Extension"; a built-in page's
                // title is its badge's explanation, not shown here)
                const from = k.startsWith("p:") && p.title?.startsWith(`${p.label} · `) ? p.title.slice(p.label.length + 3) : "";
                return (
                  <li
                    key={k}
                    data-page={k}
                    draggable
                    onDragStart={(e) => {
                      setDragging(k);
                      e.dataTransfer.effectAllowed = "move";
                      e.dataTransfer.setData("text/plain", k);
                    }}
                    onDragOver={(e) => {
                      if (!dragging) return;
                      e.preventDefault();
                      setOver(k);
                    }}
                    onDragLeave={() => setOver((o) => (o === k ? null : o))}
                    onDrop={(e) => {
                      e.preventDefault();
                      drop(k);
                      setDragging(null);
                      setOver(null);
                    }}
                    onDragEnd={() => {
                      setDragging(null);
                      setOver(null);
                    }}
                    className={cx("flex flex-wrap items-center gap-x-3 gap-y-1.5 px-3 py-2 transition-colors", dragging === k && "opacity-50", over === k && dragging !== k && "shadow-[inset_0_2px_0_var(--accent)]")}
                  >
                    <span aria-hidden className="cursor-grab text-ink-3 active:cursor-grabbing" title={t("settings.sidebar_pages_drag")}>
                      <Icon name="menu" size={14} />
                    </span>
                    <span className="flex size-7 shrink-0 items-center justify-center rounded-control bg-inset text-ink-2 shadow-hairline">
                      <Icon name={p.icon} size={14} />
                    </span>
                    {/* the page's name and where it is wrap on a phone, beside the row's controls, rather than being cut */}
                    {/* at least a name's width: on a phone the controls go under it rather than squeezing it */}
                    <div className="min-w-32 flex-1">
                      <div className="break-words text-[13px] text-ink">{p.label}</div>
                      <div className="mt-0.5 break-words text-[11.5px] text-ink-3">
                        {locked ? t("settings.sidebar_pages_locked") : on ? t("settings.sidebar_pages_in_list") : t("settings.sidebar_pages_in_more")}
                        {from && ` · ${from}`}
                      </div>
                    </div>
                    <div className="ml-auto flex items-center gap-2">
                      {sections.length > 1 && !locked && (
                        <select value={s.id} onChange={(e) => setPageGroup(k, e.target.value)} aria-label={t("settings.sidebar_groups_page_group", { name: p.label })} className={cx(FIELD, "max-w-36")}>
                          {groups.map((g) => (
                            <option key={g.id || "pages"} value={g.id}>{name(g.id)}</option>
                          ))}
                        </select>
                      )}
                      <IconButton icon="up" label={t("settings.sidebar_pages_up", { name: p.label })} disabled={i === 0} onClick={() => move(k, inGroup, -1)} size={26} iconSize={13} />
                      <IconButton icon="arrowDown" label={t("settings.sidebar_pages_down", { name: p.label })} disabled={i === inGroup.length - 1} onClick={() => move(k, inGroup, 1)} size={26} iconSize={13} />
                      <Switch on={on} disabled={locked} onChange={(v) => setPageShown(k, v)} label={t("settings.sidebar_pages_show", { name: p.label })} />
                    </div>
                  </li>
                );
              })}
            </ul>
          </div>
        );
      })}
      <div data-sidebar-more className="flex min-h-11 items-center gap-3 border-t border-line px-3 py-2">
        <span className="flex size-7 shrink-0 items-center justify-center rounded-control bg-inset text-ink-2 shadow-hairline"><Icon name="more" size={14} /></span>
        <div className="min-w-0 flex-1">
          <div className="text-[13px] text-ink">{t("settings.sidebar_more_show")}</div>
          <div className="mt-0.5 break-words text-[11.5px] text-ink-3">{t(moreShown(prefs) ? "settings.sidebar_more_hint_on" : "settings.sidebar_more_hint_off")}</div>
        </div>
        <Switch on={moreShown(prefs)} onChange={setMoreShown} label={t("settings.sidebar_more_show")} />
      </div>
      <div className="flex min-h-11 items-center px-3 py-1.5">
        {editing === "+" ? (
          <NameField initial="" label={t("settings.sidebar_groups_name")} onCancel={() => setEditing(null)} onDone={(v) => (v.trim() && addPageGroup(ids, v), setEditing(null))} />
        ) : (
          <Button tone="quiet" icon="plus" onClick={() => setEditing("+")} className="h-8">{t("settings.sidebar_groups_new")}</Button>
        )}
      </div>
    </Group>
  );
}
