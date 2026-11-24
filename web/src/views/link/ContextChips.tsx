// Chat and scheduled prompts share the same chip view. Only a non-empty selection loads it.
import { Suspense } from "react";
import { lazyPage } from "../../lib/lazy-page";
import { removeContext, useContexts, type ContextRef } from "../../lib/links";
import { t } from "../../i18n";
import { OverlayBoundary } from "../LazyOverlay";

const ChipList = lazyPage(() => import("./ContextChipList").then((m) => m.ContextChipList));

export function ContextChips({ draftKey }: { draftKey: string }) {
  const refs = useContexts(draftKey).filter((r) => r.kind !== "forward");
  return <ContextChipList refs={refs} onRemove={(id) => removeContext(draftKey, id)} />;
}

export function ContextChipList({ refs, onRemove }: { refs: ContextRef[]; onRemove: (id: string) => void }) {
  if (!refs.length) return null;
  const fallback = (
    <div className="flex flex-wrap gap-1">
      {refs.map((ref) => (
        <span key={ref.id} className="flex min-w-0 items-center gap-1 rounded-full bg-accent-tint px-2 text-sm text-accent-ink">
          <span className="truncate">{ref.title || ref.text}</span>
          <button type="button" className="size-11 shrink-0" aria-label={t("link-contextchips.remove_label")} onClick={() => onRemove(ref.id)}>×</button>
        </span>
      ))}
    </div>
  );
  return (
    <OverlayBoundary open onClose={() => {}} fallback={fallback}>
      <Suspense fallback={fallback}><ChipList refs={refs} onRemove={onRemove} /></Suspense>
    </OverlayBoundary>
  );
}
