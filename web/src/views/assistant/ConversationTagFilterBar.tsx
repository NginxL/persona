// A controlled tag-filter bar (#12018): entries/order and selection belong to the caller, not to a list-page layout.
// The same component can later sit in its own pane. Editing is progressive disclosure; without it the original chips are unchanged.
import { useState } from "react";
import { t } from "../../i18n";
import { Button, cx, IconButton } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Chip } from "../inbox/InboxParts";
import { inputClass } from "./parts";
import { normalizeTags, TAG_MAX_LENGTH } from "./conversations";
import { TAG_FILTERS_MAX } from "../../lib/conversation-tag-filters";

export function ConversationTagFilterBar({ entries, active, customized, editing, onSelect, onChange, onDone }: {
  entries: readonly string[];
  active: string;
  customized: boolean;
  editing: boolean;
  onSelect: (tag: string) => void;
  onChange: (entries: string[] | null) => Promise<boolean>;
  onDone: () => void;
}) {
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if (!entries.length && !customized && !editing) return null;

  const change = async (next: string[] | null) => {
    setBusy(true);
    setError("");
    const ok = await onChange(next);
    setBusy(false);
    return ok;
  };
  const add = async () => {
    const tag = draft.trim();
    if (!tag || busy) return;
    if (Array.from(tag).length > TAG_MAX_LENGTH) {
      setError(t("conversation-tag-filters.too_long", { max: TAG_MAX_LENGTH }));
      return;
    }
    if (entries.includes(tag)) {
      setDraft("");
      setError("");
      return;
    }
    if (entries.length >= TAG_FILTERS_MAX) {
      setError(t("conversation-tag-filters.count_limit", { max: TAG_FILTERS_MAX }));
      return;
    }
    if (await change(normalizeTags([...entries, tag]))) setDraft("");
  };

  return (
    <div className="-mt-2 mb-5">
      <div role="group" aria-label={t("assistant-history.tag_filter")} className="flex flex-wrap items-center gap-1.5">
        {entries.map((tag) => (
          <span key={tag} className="inline-flex items-center gap-0.5">
            <Chip on={active === tag} onClick={() => onSelect(tag)}>
              <span className="inline-flex items-center gap-1"><Icon name="tag" size={11} stroke={2} />{tag}</span>
            </Chip>
            {editing && <IconButton icon="x" label={t("conversation-tag-filters.remove", { tag })} disabled={busy} onClick={() => void change(entries.filter((x) => x !== tag))} size={22} iconSize={11} className="touch-hit" />}
          </span>
        ))}
        {!entries.length && customized && !editing && <span className="px-1 py-1 text-label text-ink-3">{t("conversation-tag-filters.empty")}</span>}
        {editing && (
          <>
            <form className="inline-flex items-center gap-1" onSubmit={(event) => { event.preventDefault(); void add(); }}>
              <input value={draft} disabled={busy} onChange={(event) => { setDraft(event.target.value); setError(""); }} placeholder={t("conversation-tag-filters.placeholder")} aria-label={t("conversation-tag-filters.placeholder")} className={cx(inputClass, "h-8 w-36 px-2 text-label")} />
              <Button type="submit" size="sm" tone="quiet" icon="plus" disabled={busy || !draft.trim()}>{t("conversation-tag-filters.add")}</Button>
            </form>
            {customized && <Button size="sm" tone="quiet" disabled={busy} onClick={() => void change(null)}>{t("conversation-tag-filters.reset")}</Button>}
            <Button size="sm" tone="primary" disabled={busy} onClick={onDone}>{t("conversation-tag-filters.done")}</Button>
          </>
        )}
      </div>
      {error && <p role="alert" className="mt-1.5 px-1 text-label text-red">{error}</p>}
    </div>
  );
}
