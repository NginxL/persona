import { Suspense, type ComponentProps, type ReactNode } from "react";
import { hasSlashSkillQuery } from "../../lib/composer-commands";
import type { ImeState } from "../../lib/ime";
import type { ComposerCommand as Command } from "../../lib/extension-prompts";
import { t } from "../../i18n";
import { lazyPage } from "../../lib/lazy-page";
import { OverlayBoundary } from "../LazyOverlay";
import type { MentionRow, MentionSource } from "./ComposerPanel";
import { SlashPalette } from "./SlashPalette";
import { SkillCatalogStatus } from "./SkillCatalogStatus";
import type { CatalogMemory } from "./useSkillCatalog";

const SlashSkillPalette = lazyPage(() => import("./SlashSkillPalette").then((m) => m.SlashSkillPalette));

export type SlashPaletteInput = {
  /** Commands already ranked by the composer, with the existing command cap. */
  rows: Command[];
  active: number;
  onHover: (i: number) => void;
  onPick: (c: Command | MentionRow) => void;
  /** A press outside `within` (the composer) closes it. */
  onClose: () => void;
  /** What follows the slash; the catalog is read once for this opening, not for each query. */
  query?: string | null;
  source?: MentionSource;
  machineId?: string;
  memory?: CatalogMemory;
  within: React.RefObject<HTMLElement | null>;
  /** The composer's own input-method state (`useComposition().tracker`, lib/ime.ts). The palette mounts just after a
   *  composition ends, so a state of its own would miss that end and take Safari's committing Enter as a pick. */
  ime?: { readonly current: ImeState };
  /** The skill stage's chunk or catalog state; loading also suppresses a premature empty message. */
  status?: "loading" | "error" | "partial";
};

export type SlashSkillViewProps = SlashPaletteInput & {
  /** Display rows; `rows` remains the command-filter generation used to restore scroll. */
  entries?: Command[];
  renderDetail?: (row: Command) => ReactNode;
  renderRow?: (row: Command, common: ComponentProps<"button">) => ReactNode;
};

/** The reliable intent-loaded entry: commands keep their view while the catalog stage loads or fails. */
export function SlashSkillEntry(props: SlashPaletteInput) {
  return (
    <OverlayBoundary open onClose={props.onClose} fallback={<SlashSkillView {...props} status="error" />}>
      <Suspense fallback={<SlashSkillView {...props} status="loading" />}>
        <SlashSkillPalette {...props} />
      </Suspense>
    </OverlayBoundary>
  );
}

export function SlashSkillView({ entries, rows, source, status, ...props }: SlashSkillViewProps) {
  const items = entries ?? rows;
  const withSkills = Boolean(source?.skills) && hasSlashSkillQuery(props.query);
  return <SlashPalette {...props} rows={items} active={Math.min(props.active, items.length - 1)}
    groups={withSkills ? c => t("skill" in c ? "chat-composerpanel.skills" : "chat-slashpalette.label") : undefined}
    status={withSkills ? <SkillCatalogStatus status={status} /> : undefined}
    loading={withSkills && status === "loading"} />;
}
