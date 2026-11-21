// What a project's accepted tasks taught (#12295, docs/assistant/todos.md "Lessons from accepted tasks"): the lessons
// Persona hands to the project's new Agents, on the project's page for the owner to read, correct and delete. The hub
// keeps them (GET /kb/projects/{id}/lessons, PUT and DELETE …/lessons/{lesson}); every answer is the list as it is
// after the change. A lesson changed on another device meanwhile answers 409 with the list as it is now, which takes
// this one's place. Nothing is drawn while the project has no lessons (or the hub has no such route).
import { useCallback, useEffect, useId, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, ApiError, enc, errorText } from "../../lib/api";
import { ago } from "../../lib/format";
import { composing } from "../../lib/send-history";
import { Button, Dialog, toast } from "../../ui/atoms";
import { Card, MoreRow, SectionTitle } from "./KBParts";

export interface ProjectLesson {
  id: string;
  kind: string;
  text: string;
  seen: number;
  first_seen_at?: string;
  last_seen_at?: string;
  /** Not taught again for 90 days: no longer handed to new Agents. */
  stale: boolean;
  /** The owner corrected its text. */
  edited: boolean;
  /** The to-dos that taught it, the newest first. */
  sources: { todo_id: string; title?: string }[];
}

export interface ProjectLessonsList {
  /** Whether Persona learns new lessons (Settings › Persona). */
  enabled: boolean;
  lessons: ProjectLesson[];
}

/** The hub keeps a lesson's text within this many characters (lessonTextMax). */
export const LESSON_TEXT_MAX = 200;
/** Lessons shown before "Show all". */
const LESSONS_SHOWN = 5;
const KINDS = new Set(["repeated_mistake", "missing_context", "wasted_step", "improvement"]);

/** The hub calls; tests swap them. */
export interface ProjectLessonsDeps {
  list: () => Promise<ProjectLessonsList>;
  edit: (lesson: string, text: string) => Promise<ProjectLessonsList>;
  remove: (lesson: string) => Promise<ProjectLessonsList>;
}

type Auth = { token: string; tokenOwner: string; headers: Record<string, string> };

export function projectLessonsDeps(project: string, auth: Auth): ProjectLessonsDeps {
  const base = `/kb/projects/${enc(project)}/lessons`;
  return {
    list: () => api<ProjectLessonsList>(base, auth),
    edit: (lesson, text) => api<ProjectLessonsList>(`${base}/${enc(lesson)}`, { method: "PUT", json: { text }, ...auth }),
    remove: (lesson) => api<ProjectLessonsList>(`${base}/${enc(lesson)}`, { method: "DELETE", ...auth }),
  };
}

/** The list a 409 lesson_changed carries: the lessons as they are now. */
function changedList(err: unknown): ProjectLessonsList | null {
  if (!(err instanceof ApiError) || err.status !== 409 || err.code !== "lesson_changed" || !Array.isArray(err.body.lessons)) return null;
  return { enabled: err.body.enabled === true, lessons: err.body.lessons as ProjectLesson[] };
}

export function ProjectLessons({ deps, current }: { deps: ProjectLessonsDeps; current: () => boolean }) {
  const [list, setList] = useState<ProjectLessonsList | null>(null);
  const [all, setAll] = useState(false);
  const [editing, setEditing] = useState<ProjectLesson | null>(null);
  const [deleting, setDeleting] = useState<ProjectLesson | null>(null);
  const alive = useRef(true);
  const titleId = useId();
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  const valid = useCallback(() => alive.current && current(), [current]);
  useEffect(() => {
    let live = true;
    deps.list().then((l) => live && valid() && setList(l), () => live && valid() && setList(null));
    return () => { live = false; };
  }, [deps, valid]);
  // A change's answer is the list after it; a conflict's is the list as another device left it.
  const apply = async (change: () => Promise<ProjectLessonsList>): Promise<string> => {
    try {
      const l = await change();
      if (valid()) setList(l);
      return "";
    } catch (err) {
      const now = changedList(err);
      if (now && valid()) {
        setList(now);
        toast(t("knowledge-projectlessons.changed_elsewhere"), "info");
        return "";
      }
      return errorText(err);
    }
  };
  const lessons = list?.lessons ?? [];
  if (lessons.length === 0) return null;
  const shown = all ? lessons : lessons.slice(0, LESSONS_SHOWN);
  return (
    <section aria-labelledby={titleId}>
      <SectionTitle title={<span id={titleId}>{t("knowledge-projectlessons.title")}</span>} count={lessons.length} />
      <Card>
        <ul className="divide-y divide-line">
          {shown.map((l) => (
            <li key={l.id} className="px-4 py-3">
              <LessonRow lesson={l} onEdit={() => setEditing(l)} onDelete={() => setDeleting(l)} />
            </li>
          ))}
        </ul>
        {lessons.length > LESSONS_SHOWN && <MoreRow open={all} label={t("knowledge-projectlessons.show_all", { count: lessons.length })} onClick={() => setAll((x) => !x)} />}
      </Card>
      <p className="mt-2 px-1 text-submeta leading-relaxed text-ink-3">
        {t(list?.enabled ? "knowledge-projectlessons.footer_on" : "knowledge-projectlessons.footer_off")}
      </p>
      {editing && <EditLesson lesson={editing} onClose={() => setEditing(null)} save={(text) => apply(() => deps.edit(editing.id, text))} />}
      <DeleteLesson lesson={deleting} onClose={() => setDeleting(null)} remove={(l) => apply(() => deps.remove(l.id))} />
    </section>
  );
}

function LessonRow({ lesson, onEdit, onDelete }: { lesson: ProjectLesson; onEdit: () => void; onDelete: () => void }) {
  const kind = KINDS.has(lesson.kind) ? lesson.kind : "improvement";
  const source = lesson.sources[0];
  const meta = [
    lesson.seen > 1 ? t("knowledge-projectlessons.seen", { count: lesson.seen }) : "",
    lesson.last_seen_at ? t("knowledge-projectlessons.last_seen", { when: ago(lesson.last_seen_at) }) : "",
    source ? t("knowledge-projectlessons.from", { task: source.title || t("knowledge-projectlessons.untitled_task") }) : "",
    lesson.edited ? t("knowledge-projectlessons.edited") : "",
  ].filter(Boolean);
  return (
    <div className="flex min-w-0 items-start gap-3">
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5 text-submeta">
          <span className="shrink-0 font-semibold text-ink-2">{t(`knowledge-projectlessons.kind_${kind}`)}</span>
          {lesson.stale && <span className="shrink-0 text-ink-3">{t("knowledge-projectlessons.stale")}</span>}
        </div>
        <p className="mt-0.5 break-words text-meta leading-relaxed text-ink select-text">{lesson.text}</p>
        {meta.length > 0 && <p className="mt-1 break-words text-submeta leading-relaxed text-ink-3">{meta.join(" · ")}</p>}
      </div>
      <div className="flex shrink-0 items-center gap-1">
        <Button tone="quiet" onClick={onEdit}>{t("knowledge-projectlessons.edit")}</Button>
        <Button tone="quiet" onClick={onDelete}>{t("knowledge-projectlessons.delete")}</Button>
      </div>
    </div>
  );
}

function EditLesson({ lesson, onClose, save }: { lesson: ProjectLesson; onClose: () => void; save: (text: string) => Promise<string> }) {
  const [draft, setDraft] = useState(lesson.text);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const label = useId();
  const text = draft.trim();
  const over = [...draft].length > LESSON_TEXT_MAX;
  const submit = async () => {
    if (busy || !text || over || text === lesson.text) return;
    setBusy(true);
    setError("");
    const failed = await save(text);
    setBusy(false);
    if (failed) setError(failed);
    else onClose();
  };
  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      title={t("knowledge-projectlessons.edit_title")}
      width={480}
      footer={
        <>
          <Button tone="quiet" disabled={busy} onClick={onClose}>{t("common.cancel")}</Button>
          <Button tone="primary" busy={busy} disabled={!text || over || text === lesson.text} onClick={() => void submit()}>{t("common.save")}</Button>
        </>
      }
    >
      <label htmlFor={label} className="mb-2 block text-label text-ink-2">{t(`knowledge-projectlessons.kind_${KINDS.has(lesson.kind) ? lesson.kind : "improvement"}`)}</label>
      <textarea
        id={label}
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        // ⌘↩ / Ctrl+↩ saves, as the other multi-line fields do; Enter alone is a new line
        onKeyDown={(event) => {
          if (event.key !== "Enter" || !(event.metaKey || event.ctrlKey) || composing(event.nativeEvent)) return;
          event.preventDefault();
          void submit();
        }}
        disabled={busy}
        rows={3}
        autoFocus
        className="w-full resize-y rounded-control bg-field p-3 text-sm leading-relaxed text-ink shadow-btn outline-none focus:ring-1 focus:ring-accent disabled:opacity-70"
      />
      <p className="mt-2 text-submeta leading-relaxed text-ink-3">
        {over ? t("knowledge-projectlessons.too_long", { max: LESSON_TEXT_MAX }) : t("knowledge-projectlessons.edit_hint")}
      </p>
      {error && <p role="alert" className="mt-2 break-words text-label text-red">{t("knowledge-projectlessons.save_failed", { error })}</p>}
    </Dialog>
  );
}

function DeleteLesson({ lesson, onClose, remove }: { lesson: ProjectLesson | null; onClose: () => void; remove: (l: ProjectLesson) => Promise<string> }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [shown, setShown] = useState(lesson);
  useEffect(() => { if (lesson) { setShown(lesson); setError(""); } }, [lesson]);
  const confirm = async () => {
    if (!lesson || busy) return;
    setBusy(true);
    setError("");
    const failed = await remove(lesson);
    setBusy(false);
    if (failed) setError(failed);
    else onClose();
  };
  return (
    <Dialog
      open={!!lesson}
      onClose={() => !busy && onClose()}
      title={t("knowledge-projectlessons.delete_title")}
      width={400}
      footer={
        <>
          <Button tone="quiet" disabled={busy} onClick={onClose}>{t("common.cancel")}</Button>
          <Button tone="danger" icon="trash" busy={busy} onClick={() => void confirm()}>{t("knowledge-projectlessons.delete")}</Button>
        </>
      }
    >
      {shown && <p className="break-words text-meta leading-relaxed text-ink">{shown.text}</p>}
      <p className="mt-2 text-label leading-relaxed text-ink-2">{t("knowledge-projectlessons.delete_body")}</p>
      {error && <p role="alert" className="mt-2 break-words text-label text-red">{t("knowledge-projectlessons.delete_failed", { error })}</p>}
    </Dialog>
  );
}
