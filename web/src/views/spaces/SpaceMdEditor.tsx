// Editing a space's SPACE.md (#13500, docs/web/web.md "Domain spaces"): what the user tells the assistant about the
// space. The parts the user wrote are fields; the blocks Persona keeps up to date are shown read-only and go back
// unchanged (lib/space-md.ts). Saving names the version the edit started from: changed meanwhile (409) or a managed
// block touched (400), the dialog says so plainly and keeps what was typed.
import { useCallback, useEffect, useId, useMemo, useState } from "react";
import { errorText } from "../../lib/api";
import { assembleSpaceMd, editableParts, sameManagedBlocks, splitSpaceMd, type SpaceMdPart } from "../../lib/space-md";
import { readSpaceMd, spaceMdError, writeSpaceMd, type SpaceMd, type SpaceMdError } from "../../lib/tracks";
import { Button, Dialog, Loading, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";

/** The words for a failed read or save. */
export function spaceMdErrorText(kind: SpaceMdError, e: unknown): string {
  switch (kind) {
    case "stale":
      return t("spaces.md_stale");
    case "managed":
      return t("spaces.md_managed");
    case "missing":
      return t("spaces.md_missing");
    case "offline":
      return t("spaces.md_offline");
    default:
      return t("spaces.md_failed", { error: errorText(e) });
  }
}

export function SpaceMdEditor({ open, slug, onClose, onSaved }: { open: boolean; slug: string; onClose: () => void; onSaved?: (md: SpaceMd) => void }) {
  const [md, setMd] = useState<SpaceMd | null>(null);
  const [readError, setReadError] = useState("");
  const [saveError, setSaveError] = useState<{ kind: SpaceMdError; text: string } | null>(null);
  const [edits, setEdits] = useState<Record<number, string>>({});
  const [saving, setSaving] = useState(false);
  const fieldId = useId();

  const load = useCallback(async () => {
    setReadError("");
    try {
      const r = await readSpaceMd(slug);
      setMd(r);
      setEdits({});
      setSaveError(null);
    } catch (e) {
      setReadError(spaceMdErrorText(spaceMdError(e), e));
    }
  }, [slug]);
  useEffect(() => {
    if (open) void load();
  }, [open, load]);

  const shape = useMemo(() => (md ? editableParts(splitSpaceMd(md.content)) : null), [md]);
  const changed = Object.keys(edits).length > 0 && !!shape && assembleSpaceMd(shape.parts, edits) !== md?.content;

  const save = async () => {
    if (!md || !shape) return;
    const content = assembleSpaceMd(shape.parts, edits);
    // a field that now holds a block's marker would change what Persona keeps: refused here as the hub would
    if (!sameManagedBlocks(md.content, content)) {
      setSaveError({ kind: "managed", text: t("spaces.md_managed") });
      return;
    }
    setSaving(true);
    setSaveError(null);
    try {
      const r = await writeSpaceMd(slug, content, md.digest);
      setMd(r);
      setEdits({});
      onSaved?.(r);
      toast(t("spaces.md_saved"), "ok");
      onClose();
    } catch (e) {
      const kind = spaceMdError(e);
      setSaveError({ kind, text: spaceMdErrorText(kind, e) });
    } finally {
      setSaving(false);
    }
  };

  const part = (p: SpaceMdPart, i: number) => {
    if (p.kind === "managed") {
      return (
        <div key={i} className="rounded-control bg-inset px-3 py-2.5 shadow-hairline" data-managed={p.name}>
          <p className="mb-1 flex items-center gap-1.5 text-label font-medium text-ink-3"><Icon name="lock" size={11} />{t("spaces.md_managed_block")}</p>
          <p className="whitespace-pre-wrap break-words text-meta leading-relaxed text-ink-2">{p.body.trim() || t("spaces.md_managed_empty")}</p>
        </div>
      );
    }
    if (!shape?.editable.includes(i)) return null;
    const first = shape.editable[0] === i;
    return (
      <textarea
        key={i}
        id={first ? fieldId : undefined}
        value={edits[i] ?? p.text}
        onChange={(e) => setEdits((x) => ({ ...x, [i]: e.target.value }))}
        rows={first ? 10 : 5}
        spellCheck
        placeholder={t("spaces.md_placeholder")}
        className="w-full resize-y rounded-control bg-surface px-3 py-2.5 font-sans text-meta leading-relaxed text-ink shadow-hairline outline-none placeholder:text-ink-3 focus:shadow-btn"
      />
    );
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      width={640}
      title={t("spaces.md_title")}
      footer={
        <>
          {saveError?.kind === "stale" && <Button tone="quiet" icon="retry" onClick={() => void load()}>{t("spaces.md_reload")}</Button>}
          <Button tone="quiet" onClick={onClose}>{t("spaces.cancel")}</Button>
          <Button tone="primary" busy={saving} disabled={!changed} onClick={() => void save()}>{t("spaces.save")}</Button>
        </>
      }
    >
      <label htmlFor={fieldId} className="mb-3 block text-meta leading-relaxed text-ink-2">{t("spaces.md_intro")}</label>
      {readError ? (
        <div role="alert" className="flex flex-col items-start gap-2 text-meta text-ink-2">
          <p>{readError}</p>
          <Button icon="retry" onClick={() => void load()}>{t("spaces.retry")}</Button>
        </div>
      ) : !shape ? (
        <div className="flex justify-center py-8"><Loading /></div>
      ) : (
        <div className="flex flex-col gap-2">{shape.parts.map(part)}</div>
      )}
      {saveError && <p role="alert" className="mt-3 rounded-control bg-orange-tint px-3 py-2 text-meta text-ink">{saveError.text}</p>}
    </Dialog>
  );
}
