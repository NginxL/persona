// Settings › General › Start page (docs/web/web.md "Start page", #9071): the page the app opens on, the main chat by
// default or any page of the sidebar, an extension's too, in the sidebar's order. The hub keeps it for every device of
// the user (lib/start-page.ts); where the hub has no such route it stays in this browser, and the footer says which. A
// choice whose page is not there now stays chosen, and the row says the main chat opens until it is back.
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import { hasSlots } from "../../lib/extension-slots";
import { LOCKED, ordered, useSidebarPrefs } from "../../lib/sidebar-pages";
import { saveStartPage, startPageGone, useExtensionPagesKnown, useStartPage, useStartPageSynced } from "../../lib/start-page";
import { useStore } from "../../lib/store";
import { useWorkspaceBoardSetting } from "../../lib/workspace-board-setting";
import { toast } from "../../ui/atoms";
import { useNavPages } from "../Sidebar";
import { Group, Row } from "./SettingsParts";

/** Chooses a page and shows a failed save beside the user's other work, without putting those words in the first screen. */
export async function chooseStartPage(key: string) {
  const error = await saveStartPage(key);
  if (error !== undefined) toast(t("settings.start_page_failed", { error: errorText(error) }), "error");
}

export function StartPageGroup() {
  const { all, starredKnown } = useNavPages();
  const order = useSidebarPrefs().order;
  const key = useStartPage();
  const synced = useStartPageSynced() !== false;
  const slots = useStore((s) => hasSlots(s.features));
  const online = useStore((s) => s.conn === "online");
  const extensionsKnown = useExtensionPagesKnown();
  const boardKnown = useWorkspaceBoardSetting().enabled !== null;
  const byKey = new Map(all.map((p) => [p.k, p]));
  const pages = ordered(all.map((p) => p.k), order).filter((k) => k !== LOCKED).map((k) => byKey.get(k)!);
  const listed = !key || byKey.has(key);
  const gone = !listed && online && startPageGone(key, false, { extensions: extensionsKnown || !slots, board: boardKnown, starred: starredKnown }) === true;
  // an extension's page with the extension it comes from (its NavPage.title is "Page · Extension", as in Sidebar pages)
  const name = (p: (typeof all)[number]) => (p.k.startsWith("p:") && p.title?.startsWith(`${p.label} · `) ? p.title : p.label);

  return (
    <Group anchor="start-page" title={t("settings.start_page")} footer={synced ? t("settings.start_page_footer_synced") : t("settings.start_page_footer_local")}>
      <Row label={t("settings.start_page_open_on")} hint={gone ? t("settings.start_page_gone") : t("settings.start_page_hint")} wrapHint stackOnNarrow anchor="open-on">
        {/* on a phone under its sentence, the width of the row (as Language) */}
        <select
          value={key}
          onChange={(e) => void chooseStartPage(e.target.value)}
          aria-label={t("settings.start_page_open_on")}
          className="h-8 min-w-36 max-w-full rounded-control bg-field px-2.5 text-meta text-ink shadow-inset-field outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)] max-sm:w-full"
        >
          <option value="">{t("settings.start_page_main")}</option>
          {pages.map((p) => (
            <option key={p.k} value={p.k}>
              {name(p)}
            </option>
          ))}
          {/* the chosen page while the sidebar does not list it: not there now, or not known yet */}
          {!listed && (
            <option value={key} disabled>
              {gone ? t("settings.start_page_unavailable") : t("settings.start_page_checking")}
            </option>
          )}
        </select>
      </Row>
    </Group>
  );
}
