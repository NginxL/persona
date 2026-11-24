// A direction's notes (#10086, docs/assistant/unified-entry.md "Direction notes"): what the owner writes for each
// current direction — its rules, the readings that prove progress, its terms. The hub gives them to that direction's
// leader chat as part of its own instructions and to the Agents newly started for it; no other chat sees them. The list
// is GET /api/v1/assistant/leaders (current directions only), a save PUT …/directions/{id}/notes, one direction at a
// time; the hub answers with what it kept (trimmed, clipped), which becomes the saved text.
import { useEffect, useState } from "react";
import { t } from "../../i18n";
import { api, enc, errorText } from "../../lib/api";
import { ago } from "../../lib/format";
import { Button, cx, Loading, toast } from "../../ui/atoms";
import { textareaClass } from "../assistant/parts";
import { Group, Row } from "./SettingsParts";

/** The hub keeps at most this many characters (directionNotesMost). */
export const DIRECTION_NOTES_MAX = 4000;

export interface DirectionNotesEntry {
  id: string;
  name?: string;
  title?: string;
  project?: string;
  ended?: boolean;
  notes?: string;
  notes_at?: string;
}

/** The hub calls; tests swap them. */
export interface DirectionNotesDeps {
  list: () => Promise<DirectionNotesEntry[]>;
  save: (id: string, notes: string) => Promise<string>;
}

export function directionNotesDeps(scoped: typeof api = api): DirectionNotesDeps {
  return {
    list: async () => (await scoped<{ leaders?: DirectionNotesEntry[] }>("/assistant/leaders")).leaders ?? [],
    save: async (id, notes) => (await scoped<{ notes?: string }>(`/assistant/directions/${enc(id)}/notes`, { method: "PUT", json: { notes } })).notes ?? "",
  };
}

export function DirectionNotes({ deps, onOpenPermissions }: { deps: DirectionNotesDeps; onOpenPermissions?: () => void }) {
  const [list, setList] = useState<DirectionNotesEntry[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    deps.list().then(
      (all) => live && setList(all.filter((d) => d.id && !d.ended)),
      (e) => live && (setError(errorText(e)), setList([])),
    );
    return () => { live = false; };
  }, [deps]);
  return (
    <Group anchor="direction-notes" title={t("settings-directionnotes.title")} footer={t("settings-directionnotes.footer")}>
      {onOpenPermissions && (
        <Row anchor="direction-permissions" label={t("settings-directionnotes.permissions")} hint={<span className="whitespace-normal">{t("settings-directionnotes.permissions_hint")}</span>} wrapHint onClick={onOpenPermissions} />
      )}
      {list === null ? (
        <div className="px-4 py-4"><Loading /></div>
      ) : list.length === 0 ? (
        <p className="px-4 py-4 text-label leading-relaxed text-ink-3">
          {error ? t("settings-directionnotes.load_failed", { error }) : t("settings-directionnotes.empty")}
        </p>
      ) : (
        list.map((d) => <DirectionNotesRow key={d.id} entry={d} deps={deps} />)
      )}
    </Group>
  );
}

function DirectionNotesRow({ entry, deps }: { entry: DirectionNotesEntry; deps: DirectionNotesDeps }) {
  const name = entry.name?.trim() || entry.title?.trim() || entry.id;
  const [base, setBase] = useState(entry.notes ?? "");
  const [text, setText] = useState(entry.notes ?? "");
  const [at, setAt] = useState(entry.notes_at);
  const [busy, setBusy] = useState(false);
  const changed = text !== base;
  const over = [...text].length > DIRECTION_NOTES_MAX;
  const save = async () => {
    setBusy(true);
    try {
      const kept = await deps.save(entry.id, text);
      setBase(kept);
      setText(kept);
      setAt(kept ? new Date().toISOString() : undefined);
      toast(t("settings-directionnotes.saved", { name }), "ok");
    } catch (e) {
      toast(t("settings-directionnotes.save_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };
  const meta = [entry.project ? t("settings-directionnotes.project", { name: entry.project }) : "", at && base ? t("settings-directionnotes.edited", { when: ago(at) }) : ""]
    .filter(Boolean).join(" · ");
  return (
    <div className="px-4 py-3" data-direction={entry.id}>
      <div className="mb-2 flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-meta font-medium text-ink">{name}</span>
        {meta && <span className="shrink-0 truncate text-submeta text-ink-3">{meta}</span>}
      </div>
      <textarea
        className={cx(textareaClass, "min-h-[88px]")}
        rows={4}
        value={text}
        aria-label={t("settings-directionnotes.title") + " · " + name}
        placeholder={t("settings-directionnotes.placeholder")}
        onChange={(e) => setText(e.target.value)}
      />
      <div className="mt-2 flex items-center gap-3">
        <span className={cx("min-w-0 flex-1 text-submeta tabular-nums", over ? "text-red" : "text-ink-3")}>
          {changed || over ? t("settings-directionnotes.count", { count: [...text].length, max: DIRECTION_NOTES_MAX }) : ""}
        </span>
        {changed && <Button tone="quiet" disabled={busy} onClick={() => setText(base)}>{t("settings-assistantsettings.revert")}</Button>}
        <Button tone="primary" busy={busy} disabled={!changed || over} onClick={() => void save()}>{t("common.save")}</Button>
      </div>
    </div>
  );
}
