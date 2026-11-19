// A group title's pull-down in a section with a section-wide action (#10422, docs/assistant/inbox.md "Group titles"):
// FYI's "Mark all seen" and Collapsed's "Clear all", for the group's items or the whole section's. Group titles stick
// while their rows scroll by, so the section's own button is often out of sight. Needs you and In progress have none:
// their items need the owner (or the AI is on them), and a batch there is how things get missed. The same rules as
// iOS's InboxGroupClear.
import { t } from "../../i18n";
import type { InboxItem, SectionKey } from "../../lib/inbox";

export interface GroupClearChoice {
  key: "group" | "section";
  label: string;
  items: InboxItem[];
}

export interface GroupClear {
  /** The button's words: the section's own action. */
  label: string;
  /** The group's items, then the whole section's. */
  choices: GroupClearChoice[];
  /** With a filter, a folder or a search on, what "all" covers (Clean up all's words), said under each choice. */
  scope?: string;
}

/** A choice's words: the section's verb, how many and where ("Mark all 4 in “Assistant” seen"). */
export function groupClearLabel(tier: SectionKey, count: number, name: string): string | undefined {
  if (tier === "fyi") return t("inbox-inboxlist.clear_in_fyi", { count, name });
  if (tier === "noise") return t("inbox-inboxlist.clear_in_noise", { count, name });
  return undefined;
}

/** The pull-down of `group` in the section `tier` (titled `sectionTitle`), whose items under the filters and search are
 *  `section`; undefined where the section has no section-wide action, or where the group is the whole section (the
 *  section's own button then acts at once). */
export function groupClear(tier: SectionKey, sectionTitle: string, group: { title: string; items: InboxItem[] }, section: InboxItem[], scope?: string): GroupClear | undefined {
  const label = tier === "fyi" ? t("inbox-inboxlist.clear_fyi") : tier === "noise" ? t("inbox-inboxlist.clear_noise") : undefined;
  const mine = groupClearLabel(tier, group.items.length, group.title);
  const all = groupClearLabel(tier, section.length, sectionTitle);
  if (!label || !mine || !all || !group.items.length || section.length <= group.items.length) return undefined;
  return {
    label,
    choices: [
      { key: "group", label: mine, items: group.items },
      { key: "section", label: all, items: section },
    ],
    scope: scope || undefined,
  };
}
