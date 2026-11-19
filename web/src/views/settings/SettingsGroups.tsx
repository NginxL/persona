// Settings' three groups (docs/assistant/managers.md "The current assistant", docs/web/web.md "Settings"): "<name>'s
// settings" (the current assistant's avatar and name, a Switch that opens the sidebar's list of assistants, and a line
// saying only its own settings are shown), "This device" and "Hub" (with a line saying those two are the same whichever
// assistant is current). The phone's list and the wide sidebar both draw them from here.
import { t } from "../../i18n";
import { pickedName, useChosenManager, useShownManager } from "../../lib/manager-choice";
import { managerOf, statusLine, useRoster } from "../../lib/managers";
import { AssistantAvatar } from "../../ui/AssistantAvatar";
import { cx } from "../../ui/atoms";
import { AssistantSwitchButton } from "../AssistantSwitcher";
import type { NavGroup } from "./nav";

const SETTINGS_ROUTE = { name: "settings" } as const;

/** The current assistant as the group's title shows it: its id (null before any roster or pick), name and status. A hub
 *  without a roster has one assistant, which it calls Persona. */
export function useSettingsAssistant() {
  const roster = useRoster();
  const current = useShownManager();
  const picked = useChosenManager();
  const known = managerOf(roster, current);
  const id = known?.id ?? (!roster && picked ? picked : null);
  const name = known?.name ?? (!roster && picked ? pickedName() ?? picked : "Persona");
  return { id, name, isDefault: !!known && known.id === roster?.default, tone: known ? statusLine(known).tone : undefined };
}

/** A group's title: for the current assistant's, its avatar (with its status) and "<name>'s settings". */
export function GroupTitle({ group, compact = false }: { group: NavGroup; compact?: boolean }) {
  const me = useSettingsAssistant();
  if (group === "device") return <>{t("settings-nav.group_device")}</>;
  if (group === "hub") return <>{t("settings-nav.group_hub")}</>;
  if (group === "labs") return null;
  return (
    <span className="flex min-w-0 items-center gap-1.5" data-settings-assistant={me.id ?? ""}>
      {me.id && <AssistantAvatar id={me.id} name={me.name} isDefault={me.isDefault} size={compact ? 16 : 18} status={me.tone} ring={compact ? "ring-(--color-inset)" : "ring-page"} />}
      <span className={cx("min-w-0 truncate", !compact && "text-ink-2")}>{t("settings-nav.group_assistant", { name: me.name })}</span>
    </span>
  );
}

/** The Switch beside the current assistant's group title; nothing for the other groups. It takes no more height than
 *  the title's line (its padding reaches out above and below), so a group title row aligned at the bottom centres both. */
export function GroupAction({ group }: { group: NavGroup }) {
  return group === "assistant" ? <span className="-my-0.5 flex shrink-0"><AssistantSwitchButton route={SETTINGS_ROUTE} /></span> : null;
}

/** The line under a group: whose settings the first one shows, and that the other two don't depend on it (said once,
 *  under the last). */
export function GroupFooter({ group }: { group: NavGroup }) {
  const { name } = useSettingsAssistant();
  if (group === "assistant") return <>{t("settings-nav.assistant_footer", { name })}</>;
  if (group === "hub") return <>{t("settings-nav.hub_footer")}</>;
  return null;
}

/** Whether a group has a line under it. */
export const hasFooter = (group: NavGroup) => group === "assistant" || group === "hub";
