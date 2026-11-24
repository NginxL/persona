import { t } from "../i18n";
import type { MenuItem } from "../ui/menu-model";
import { formatTokens } from "./compaction";
import { contextTooltip, contextWarn, type ContextUsage } from "./context-usage";

/** What happens when the window fills, and what to do once it is close (#3668): the header's tooltip under the share. */
export function contextHint(u: ContextUsage): string {
  return contextWarn(u) ? t("context.hint_warn") : t("context.hint_ok");
}

/** The header's popover (#4054), the tooltip's words for a screen without hover: the share with the token count beside
 *  it, then what happens when the window fills (and, once close, what to do). */
export function contextNotes(u: ContextUsage): MenuItem[] {
  const none = () => {};
  return [
    { key: "share", note: true, label: contextTooltip(u), hint: t("context.tokens", { used: formatTokens(u.tokens), window: formatTokens(u.window) }), onSelect: none },
    { key: "hint", note: true, label: contextHint(u), onSelect: none },
  ];
}
