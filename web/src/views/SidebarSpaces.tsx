// The sidebar's Spaces section (#13500, docs/web/web.md "Domain spaces"): the user's domain spaces that are not
// archived, one row each (icon and title), then "New space". The health space keeps its own entry among the pages and
// is never repeated here. An assistant without tracks (an older one) shows no section.
import { t } from "../i18n";
import { spaceNavKey } from "../lib/persona-nav";
import { useFolded } from "../lib/use-folded";
import { sidebarSpaces, spaceIcon, useTracks } from "../lib/tracks";
import { Section, type GoFn, type RegFn } from "./SidebarParts";
import { NavRow } from "./SidebarRows";

export function SidebarSpaces({ reg, hover, activeKey, go }: { reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn }) {
  const { tracks, supported } = useTracks();
  const spaces = tracks ? sidebarSpaces(tracks) : [];
  const [folded, toggle] = useFolded("domain-spaces", false, { holds: spaces.some((s) => activeKey === spaceNavKey(s.slug)) || activeKey === "track-new" });
  if (!supported || !tracks) return null;
  return (
    <Section top title={t("sidebar.spaces")} folded={folded} onToggle={toggle}>
      {spaces.map((s) => (
        <NavRow key={s.slug} k={spaceNavKey(s.slug)} reg={reg} hover={hover} active={activeKey === spaceNavKey(s.slug)} to={{ name: "track", slug: s.slug }} go={go} icon={spaceIcon(s)} label={s.title || s.slug} title={s.description || undefined} />
      ))}
      <NavRow k="track-new" reg={reg} hover={hover} active={activeKey === "track-new"} to={{ name: "track-new" }} go={go} icon="plus" label={t("sidebar.new_space")} />
    </Section>
  );
}
