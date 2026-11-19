import { useEffect, useId, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { formatDate, t } from "../../i18n";
import { apiBlob, credentialOwner, enc, errorText, getToken, isAbort, subscribeCredentialOwner } from "../../lib/api";
import { boundedBlobReader } from "../../lib/bounded-blob";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import { composing } from "../../lib/send-history";
import { getSession, useStore } from "../../lib/store";
import { backgroundDurationMs, backgroundStatus, backgroundTitle } from "../../lib/tasks";
import type { BackgroundTask } from "../../lib/types";
import { documentPage, visiblePoll } from "../../lib/visible-poll";
import { workDuration } from "../../lib/work-groups";
import { Button, copyText, cx, IconButton, useNow } from "../../ui/atoms";
import { taskStatusWord } from "./TaskCard";

const TAIL_BYTES = 65_536;
const POLL_MS = 2000;
type Props = {
  mid: string; sid: string; cwd?: string; task: BackgroundTask; running: boolean; focus: boolean;
  owner: string; manager?: string; onBack: () => void; onOpenFile: () => void;
};
type Output = { key: string; text?: string; tail?: boolean; error?: string; loading?: boolean };

/** A bounded suffix, through the ordinary authenticated file API. Never accept a server ignoring Range. */
async function readTail(path: string, signal: AbortSignal, manager: string | undefined, token: string, owner: string, current: () => boolean) {
  let tail = false;
  const blob = await apiBlob(path, {
    signal, token, tokenOwner: owner, headers: { Range: `bytes=-${TAIL_BYTES}`, ...(manager ? { [MANAGER_HEADER]: manager } : {}) },
    isAuthContextCurrent: current,
    readBlob: async (response, readSignal) => {
      const range = /^bytes (\d+)-(\d+)\/(\d+)$/.exec(response.headers.get("Content-Range") ?? "");
      const [start, end, total] = range ? range.slice(1).map(Number) : [];
      const whole = response.status === 200;
      const valid = response.status === 206 && range && [start, end, total].every(Number.isSafeInteger)
        && total > 0 && end === total - 1 && start === Math.max(0, total - TAIL_BYTES);
      if (!whole && !valid) {
        await response.body?.cancel();
        throw new Error(t("chat-activity.output_failed"));
      }
      const mime = response.headers.get("Content-Type")?.split(";")[0].trim().toLowerCase() ?? "";
      const result = await boundedBlobReader({ maxBytes: TAIL_BYTES, mimeTypes: [mime] })(response, readSignal).catch(error => {
        if (isAbort(error)) throw error;
        throw new Error(t("chat-activity.output_failed"));
      });
      if (result.size !== (whole ? 0 : end - start + 1)) throw new Error(t("chat-activity.output_failed"));
      tail = !whole && start > 0;
      return result;
    },
  });
  const bytes = new Uint8Array(await blob.arrayBuffer());
  if (bytes.includes(0)) throw new Error(t("chat-activity.output_failed"));
  // A suffix can begin in a UTF-8 character or line. Discard only that incomplete leading fragment.
  let from = 0;
  if (tail) while (from < bytes.length && (bytes[from] & 0xc0) === 0x80) from++;
  let text = new TextDecoder().decode(bytes.subarray(from));
  if (tail && text.includes("\n") && text.indexOf("\n") < text.length - 1) text = text.slice(text.indexOf("\n") + 1);
  return { text, tail };
}

/** Command output stays within Background tasks; the parent retains the selected task after it leaves the list. */
export function BackgroundCommandDetail({ mid, sid, cwd, task, running, focus, owner, manager, onBack, onOpenFile }: Props) {
  const liveOwner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  const sameManager = useStore(() => {
    const session = getSession(mid, sid);
    return Boolean(session && session.manager === manager);
  });
  const scopeCurrent = () => credentialOwner() === owner && Boolean(getSession(mid, sid)) && getSession(mid, sid)?.manager === manager;
  const current = liveOwner === owner && sameManager;
  const started = Date.parse(task.started_at ?? "");
  const status = backgroundStatus(task, running);
  const active = status === "running";
  const now = useNow(1000, current && active && Number.isFinite(started));
  const duration = backgroundDurationMs(task, active, now);
  const commandId = useId();
  const [fullCommand, setFullCommand] = useState(false);
  const key = JSON.stringify([mid, sid, cwd, task.id, task.output_file, owner, manager]);
  const [output, setOutput] = useState<Output>({ key });
  const [retry, setRetry] = useState(0);
  const bar = useRef<HTMLDivElement>(null);
  const body = useRef<HTMLDivElement>(null);
  const follows = useRef(true);
  const abort = useRef<AbortController | null>(null);
  const shown = current && output.key === key ? output : { key };
  const outputLines = shown.text === undefined ? undefined : shown.text ? shown.text.replace(/\r\n/g, "\n").replace(/\n$/, "").split("\n").length : 0;
  const back = () => { abort.current?.abort(); onBack(); };

  useEffect(() => {
    if (focus && current) bar.current?.querySelector<HTMLButtonElement>("button")?.focus();
    // Focus only on opening a different command, not on output refreshes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  useEffect(() => {
    if (!current || !task.output_file) return;
    const ac = new AbortController();
    const token = getToken();
    abort.current = ac;
    let live = true, busy = false;
    const requestActive = () => live && !ac.signal.aborted && credentialOwner() === owner
      && Boolean(getSession(mid, sid)) && getSession(mid, sid)?.manager === manager;
    const read = async () => {
      if (busy || !requestActive()) return;
      busy = true;
      setOutput(old => ({ ...(old.key === key ? old : { key }), loading: true }));
      try {
        const path = `/machines/${enc(mid)}/file/download?path=${enc(task.output_file!)}&cwd=${enc(cwd ?? "")}`;
        const value = await readTail(path, ac.signal, manager, token, owner, requestActive);
        if (requestActive()) setOutput({ key, ...value, loading: false });
      } catch (error) {
        if (requestActive() && !isAbort(error)) setOutput(old => ({ ...(old.key === key ? old : { key }), error: errorText(error), loading: false }));
      } finally { busy = false; }
    };
    // Ending a task starts a fresh effect and one final read, even if its previous read was still in flight.
    const stop = visiblePoll(() => { void read(); }, active ? POLL_MS : 0, documentPage);
    return () => { live = false; stop(); ac.abort(); };
    // The key contains every request primitive; object identity updates must not restart polling.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, current, active, retry]);
  useLayoutEffect(() => {
    follows.current = true;
  }, [key]);
  useLayoutEffect(() => {
    if (follows.current && body.current) body.current.scrollTop = body.current.scrollHeight;
  }, [shown.text]);
  if (!current) return null;
  return (
    <div className="flex min-h-0 flex-1 flex-col" data-bg-command-detail={task.id} onKeyDown={event => {
      if (event.key === "Escape" && composing(event.nativeEvent)) { event.stopPropagation(); return; }
      if (event.key !== "Escape" || event.defaultPrevented
        || document.querySelector("[role=menu],[role=dialog]")) return;
      event.preventDefault(); event.stopPropagation(); back();
    }}>
      <div ref={bar} className="flex shrink-0 items-center gap-1.5 border-b border-line px-2 py-1.5">
        <IconButton icon="arrowLeft" label={t("chat-activity.back")} size={24} iconSize={14} onClick={back} />
        <h3 className="min-w-0 flex-1 truncate text-meta font-medium text-ink">{backgroundTitle(task)}</h3>
        <Button tone="quiet" size="sm" data-bg-open-file onClick={() => { if (scopeCurrent()) onOpenFile(); }}>{t("chat-activity.open_output_file")}</Button>
      </div>
      <dl className="shrink-0 space-y-2 border-b border-line px-3 py-3 text-label">
        <div className="flex items-start gap-3">
          <dt className="w-16 shrink-0 text-ink-3">{t("chat-activity.command_status")}</dt>
          <dd className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-0.5">
            <span role="status" data-command-status={status} className={cx(status === "failed" ? "text-red" : status === "completed" ? "text-green" : status === "running" ? "text-orange" : "text-ink-2")}>
              {status ? taskStatusWord(status) : t("chat-activity.output_not_running")}
            </span>
            {!active && task.exit_code !== undefined && <span className="text-ink-2" data-command-exit-code>{t("lib-tasks.exit_code", { code: task.exit_code })}</span>}
          </dd>
        </div>
        <div className="flex items-start gap-3">
          <dt className="w-16 shrink-0 text-ink-3">{t("chat-activity.command_runtime")}</dt>
          <dd className="min-w-0 flex-1 tabular-nums text-ink" data-command-runtime>{duration === null ? "—" : workDuration(duration)}</dd>
        </div>
        {Number.isFinite(started) && <div className="flex items-start gap-3">
          <dt className="w-16 shrink-0 text-ink-3">{t("chat-activity.command_started")}</dt>
          <dd className="min-w-0 flex-1 text-ink" data-command-started>{formatDate(started, { dateStyle: "medium", timeStyle: "short" })}</dd>
        </div>}
        {task.command && <div className="flex items-start gap-3">
          <dt className="w-16 shrink-0 text-ink-3">{t("chat-activity.command")}</dt>
          <dd className="min-w-0 flex-1">
            <div className="flex items-start gap-1">
              <code id={commandId} data-command-full className={cx("min-w-0 flex-1 font-mono whitespace-pre-wrap break-words [overflow-wrap:anywhere] text-ink", fullCommand ? "max-h-48 overflow-y-auto" : "line-clamp-3")}>{task.command}</code>
              <IconButton className="-mt-1 shrink-0" icon="copy" label={t("chat-activity.copy_command")} size={20} iconSize={12} onClick={() => { if (scopeCurrent()) void copyText(task.command!); }} />
            </div>
            <Button tone="quiet" size="sm" className="touch-hit -ml-2" expanded={fullCommand} aria-controls={commandId} onClick={() => setFullCommand(value => !value)}>
              {t(fullCommand ? "chat-activity.command_show_less" : "chat-activity.command_show_more")}
            </Button>
          </dd>
        </div>}
      </dl>
      <div className="flex shrink-0 items-center justify-between gap-2 px-3 pt-2 text-label">
        <h4 className="font-medium text-ink">{t("chat-activity.output")}</h4>
        {outputLines !== undefined && <span className="text-ink-3" data-command-lines>{t(shown.tail ? "chat-activity.output_tail_lines" : "chat-activity.output_lines", { count: outputLines })}</span>}
      </div>
      {shown.error && <div role="alert" className="flex items-center gap-2 px-3 py-2 text-label text-red"><span>{shown.error}</span><Button tone="quiet" size="sm" busy={shown.loading} onClick={() => { if (!shown.loading) { setOutput(old => ({ ...old, loading: true })); setRetry(n => n + 1); } }}>{t("app.retry")}</Button></div>}
      {shown.tail && <p className="px-3 pt-1 text-label text-ink-3">{t("chat-activity.output_tail")}</p>}
      <div ref={body} className="min-h-0 flex-1 overflow-auto px-3 pt-1 pb-3" data-command-output onScroll={() => {
        const el = body.current!; follows.current = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
      }}>
        {shown.text ? <pre className="font-mono text-label whitespace-pre-wrap break-words text-ink">{shown.text}</pre>
          : !shown.error && <p className="text-label text-ink-3">{t(shown.text === undefined ? "chat-activity.output_loading" : active ? "chat-activity.output_empty_running" : "chat-activity.output_empty_ended")}</p>}
      </div>
    </div>
  );
}
