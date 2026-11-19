// The on-demand launcher column reuses the existing setup and family projection (#10249).
import { panelSections, sameSetup, type SectionsInput, type PanelSection, type PanelRow } from "./model-panel";

/** Launcher-first projection. Preserve row setup and family rules without promoting models out of their launcher. */
export function launcherSections(
  input: Omit<SectionsInput, "view">,
): PanelSection[] {
  return input.providers.map((provider) => {
    const section = panelSections({
      ...input,
      view: { kind: "provider", provider: provider.id },
      query: "",
    }).find((s) => s.kind === "provider");
    return (
      section ?? {
        key: `provider:${provider.id}`,
        kind: "provider",
        provider,
        rows: [],
      }
    );
  });
}

/** At most three distinct saved setups, favorites first, then recent. The original rows retain their setup controls. */
export function quickPicks(input: Omit<SectionsInput, "view">): PanelRow[] {
  const saved = ["favorites", "recent"].flatMap((kind) =>
    panelSections({
      ...input,
      query: "",
      view: { kind: kind as "favorites" | "recent" },
    }).flatMap((s) => s.rows),
  );
  return saved
    .filter(
      (row, i) =>
        !saved.slice(0, i).some((before) => sameSetup(before.setup, row.setup)),
    )
    .slice(0, 3);
}

/** Search is one flat model list in provider order, with no family headings or recommended duplicates. */
export function launcherSearch(input: Omit<SectionsInput, "view">): PanelRow[] {
  return panelSections({
    ...input,
    promote: false,
    view: { kind: "all" },
    favorites: [],
    presets: [],
  }).flatMap((s) => s.rows);
}
