// Renaming a meeting or note from its page (#8877, docs/assistant/meeting-minutes.md "Titles (#8873)"): the hub keeps
// the name until the phone renames it again; an empty name gives it back to the phone, the calendar or the title written
// from what was said.
import { useState } from "react";
import { t } from "../../i18n";
import { api, enc, errorText } from "../../lib/api";
import { composing } from "../../lib/send-history";
import { Button, Dialog, toast } from "../../ui/atoms";
import { inputClass } from "../../views/assistant/parts";

/** The most of a name the hub takes. */
const TITLE_MAX = 200;

/** Whether the shown name is someone's or written from what was said (the hub says where it comes from); absent, the
 *  recording is named only by when it started, and the field starts empty. */
export function namedBy(meta?: Record<string, unknown> | null): string {
  return typeof meta?.name_source === "string" ? meta.name_source : "";
}

export function RecordingRenameDialog({
  id,
  name,
  source,
  auth,
  onClose,
  onDone,
}: {
  id: string;
  /** The name shown now. */
  name: string;
  /** Where it comes from (`name_source`): "" while it is named only by when it started. */
  source: string;
  /** The page's credentials and manager. */
  auth: { headers: Record<string, string> } & Record<string, unknown>;
  onClose: () => void;
  /** Saved: the page reads the recording again. */
  onDone: () => void;
}) {
  const initial = source ? name : "";
  const [title, setTitle] = useState(initial);
  const [busy, setBusy] = useState(false);
  const trimmed = title.trim();
  const changed = trimmed !== initial && trimmed.length <= TITLE_MAX;
  const save = async () => {
    if (!changed || busy) return;
    setBusy(true);
    try {
      await api(`/recordings/${enc(id)}/title`, { method: "PUT", json: { title: trimmed }, ...auth });
      toast(trimmed ? t("recording.renamed") : t("recording.rename_cleared"), "ok");
      onDone();
      onClose();
    } catch (e) {
      toast(t("recording.rename_failed", { error: errorText(e) }), "error");
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      title={t("recording.rename_title")}
      width={420}
      footer={
        <>
          <Button tone="quiet" disabled={busy} onClick={onClose}>{t("common.cancel")}</Button>
          <Button tone="primary" busy={busy} disabled={!changed} onClick={() => void save()}>{t("common.save")}</Button>
        </>
      }
    >
      <input
        className={inputClass}
        value={title}
        maxLength={TITLE_MAX}
        autoFocus
        onFocus={(e) => e.currentTarget.select()}
        onChange={(e) => setTitle(e.target.value.replace(/[\r\n]+/g, " "))}
        onKeyDown={(e) => e.key === "Enter" && !composing(e.nativeEvent) && void save()}
        placeholder={t("recording.rename_placeholder")}
        aria-label={t("recording.rename_title")}
      />
      <p className="mt-1.5 text-submeta leading-relaxed text-ink-3">{t("recording.rename_hint")}</p>
    </Dialog>
  );
}
