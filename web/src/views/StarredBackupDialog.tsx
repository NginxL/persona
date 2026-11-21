import { useLayoutEffect, useRef, useState } from "react";
import { t } from "../i18n";
import { api, ApiError, credentialOwner, errorText, getToken, subscribeCredentialOwner } from "../lib/api";
import { onDefaultManager, onManagerChoice, shownManager, stampedHeaders } from "../lib/manager-choice";
import { decodeStarredBackup, starredBackup, starredMarkdown, STARRED_BACKUP_MAX_BYTES, type StarredBackup } from "../lib/starred-backup";
import type { Star } from "../lib/starred-rows";
import { Button, Dialog, toast } from "../ui/atoms";

const IMPORT = "/assistant/starred/import";

type ImportPreview = { revision: number; total: number; to_add: number; already_saved: number; available: number };
type ImportResult = { revision: number; added: number; already_saved: number };
type Support = "checking" | "available" | "unsupported" | "error";
type Reviewed = { bundle: StarredBackup; preview: ImportPreview };

function backupScope() {
  const manager = shownManager();
  const token = getToken();
  const owner = credentialOwner();
  const controller = new AbortController();
  const current = () => !controller.signal.aborted && shownManager() === manager && credentialOwner() === owner;
  return {
    current,
    retire: () => controller.abort(),
    options: { token, tokenOwner: owner, headers: stampedHeaders(manager), signal: controller.signal, isAuthContextCurrent: current },
  };
}

export function StarredBackupDialog({ stars, canWrite, speaker, onClose, onRestored }: {
  stars: readonly Star[];
  canWrite: boolean;
  speaker: (star: Star) => string;
  onClose: () => void;
  onRestored: () => void;
}) {
  const origin = useRef<ReturnType<typeof backupScope> | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const pending = useRef(false);
  const [working, setWorking] = useState<"preview" | "restore">();
  const [support, setSupport] = useState<Support>("checking");
  const [capabilityError, setCapabilityError] = useState("");
  const [file, setFile] = useState<File>();
  const [reviewed, setReviewed] = useState<Reviewed>();
  const [restored, setRestored] = useState<ImportResult>();
  const [error, setError] = useState("");

  const check = async (scope = origin.current) => {
    if (!scope?.current()) return;
    setSupport("checking");
    try {
      const response = await api<{ can_import: boolean }>(IMPORT, scope.options);
      if (scope.current()) setSupport(response.can_import ? "available" : "unsupported");
    } catch (failure) {
      if (!scope.current()) return;
      if (failure instanceof ApiError && (failure.status === 404 || failure.status === 405 || failure.status === 403)) {
        setSupport("unsupported");
      } else {
        setCapabilityError(errorText(failure));
        setSupport("error");
      }
    }
  };

  useLayoutEffect(() => {
    const scope = backupScope();
    origin.current = scope;
    const changed = () => {
      if (!scope.current()) {
        scope.retire();
        onClose();
      }
    };
    const unsubscribe = [onManagerChoice(changed), onDefaultManager(changed), subscribeCredentialOwner(changed)];
    if (canWrite) void check(scope);
    else setSupport("unsupported");
    return () => {
      unsubscribe.forEach((off) => off());
      scope.retire();
      origin.current = null;
    };
  }, [canWrite, onClose]);

  const download = (format: "json" | "md") => {
    if (!origin.current?.current()) return;
    const json = format === "json";
    const content = json
      ? JSON.stringify(starredBackup(stars), null, 2) + "\n"
      : starredMarkdown(stars, {
          title: t("starred.title"), conversation: t("starred.conversation"), speaker,
          omitted: (count) => t("starred.truncated", { count }),
        });
    const file = `persona-starred-${new Date().toISOString().slice(0, 10)}.${format}`;
    const type = json ? "application/json;charset=utf-8" : "text/markdown;charset=utf-8";
    const url = URL.createObjectURL(new Blob([content], { type }));
    const link = document.createElement("a");
    link.href = url;
    link.download = file;
    link.rel = "noopener";
    document.body.append(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 10_000);
  };

  const pick = (candidate?: File) => {
    setReviewed(undefined);
    setRestored(undefined);
    const tooLarge = candidate !== undefined && candidate.size > STARRED_BACKUP_MAX_BYTES;
    setFile(tooLarge ? undefined : candidate);
    setError(tooLarge ? t("starred.backup_too_large") : "");
  };

  const preview = async () => {
    const scope = origin.current;
    if (pending.current || support !== "available" || !file || !scope?.current()) return;
    pending.current = true;
    setWorking("preview");
    setError("");
    setReviewed(undefined);
    setRestored(undefined);
    try {
      const bytes = await file.arrayBuffer();
      if (!scope.current()) return;
      const decoded = decodeStarredBackup(bytes);
      if (!decoded) {
        setError(t("starred.backup_invalid"));
        return;
      }
      const preview = await api<ImportPreview>(IMPORT + "/preview", {
        ...scope.options, method: "POST",
        headers: { ...scope.options.headers, "Content-Type": "application/json" },
        body: decoded.raw,
      });
      if (scope.current()) setReviewed({ bundle: decoded.bundle, preview });
    } catch (failure) {
      if (scope.current()) setError(errorText(failure));
    } finally {
      pending.current = false;
      if (scope.current()) setWorking(undefined);
    }
  };

  const canRestore = reviewed && reviewed.preview.to_add > 0 && reviewed.preview.to_add <= reviewed.preview.available;
  const restore = async () => {
    const scope = origin.current;
    if (pending.current || support !== "available" || !canRestore || !reviewed || !scope?.current()) return;
    pending.current = true;
    setWorking("restore");
    setError("");
    try {
      const result = await api<ImportResult>(IMPORT, {
        ...scope.options, method: "POST",
        json: { ...reviewed.bundle, revision: reviewed.preview.revision },
      });
      if (!scope.current()) return;
      setReviewed(undefined);
      setFile(undefined);
      setRestored(result);
      onRestored();
      toast(t("starred.backup_restored", { count: result.added }), "ok");
    } catch (failure) {
      if (scope.current()) {
        setReviewed(undefined);
        setError(failure instanceof ApiError && failure.status === 409 ? t("starred.backup_conflict") : errorText(failure));
      }
    } finally {
      pending.current = false;
      if (scope.current()) setWorking(undefined);
    }
  };

  return (
    <Dialog open title={t("starred.backup_title")} onClose={() => { if (!pending.current) onClose(); }}
      footer={<Button tone="quiet" disabled={!!working} onClick={onClose}>{t("starred.backup_done")}</Button>}>
      <div className="flex flex-col gap-4">
        <div className="flex flex-wrap gap-2">
          <Button onClick={() => download("json")} disabled={!!working}>{t("starred.backup_json")}</Button>
          <Button onClick={() => download("md")} disabled={!!working}>{t("starred.backup_markdown")}</Button>
        </div>
        {support === "checking" && <p className="text-label text-ink-3" role="status">{t("common.loading")}</p>}
        {support === "error" && <div className="flex flex-wrap items-center gap-2">
          <p className="min-w-0 flex-1 break-words text-label text-red" role="alert">{capabilityError}</p>
          <Button onClick={() => void check()}>{t("starred.retry")}</Button>
        </div>}
        {support === "available" && <div className="flex flex-col gap-3 border-t border-line pt-3">
          <div>
            <p className="mb-1 text-meta font-medium text-ink-2">{t("starred.backup_file")}</p>
            <div className="flex min-w-0 items-center gap-2">
              <Button disabled={!!working} onClick={() => input.current?.click()}>{t("starred.backup_choose_file")}</Button>
              {file && <span className="min-w-0 truncate text-label text-ink-2" title={file.name}>{file.name}</span>}
            </div>
            <input ref={input} type="file" accept=".json,application/json" disabled={!!working}
              aria-label={t("starred.backup_file")} className="sr-only" tabIndex={-1}
              onChange={(event) => {
                const candidate = event.target.files?.[0];
                event.target.value = "";
                pick(candidate);
              }} />
          </div>
          {error && <p role="alert" className="break-words text-label text-red">{error}</p>}
          {reviewed && <div className="rounded-control bg-inset p-3">
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-label text-ink-2">
              <dt>{t("starred.backup_total")}</dt><dd className="text-right">{reviewed.preview.total}</dd>
              <dt>{t("starred.backup_will_restore")}</dt><dd className="text-right">{reviewed.preview.to_add}</dd>
              <dt>{t("starred.backup_already_saved")}</dt><dd className="text-right">{reviewed.preview.already_saved}</dd>
              <dt>{t("starred.backup_available")}</dt><dd className="text-right">{reviewed.preview.available}</dd>
            </dl>
            {!canRestore && <p role="status" className="mt-2 text-label text-ink-3">
              {t(reviewed.preview.to_add === 0 ? "starred.backup_no_changes" : "starred.backup_capacity")}
            </p>}
          </div>}
          {restored && <p role="status" className="text-label text-ink-2">
            {t("starred.backup_result", { added: restored.added, already: restored.already_saved })}
          </p>}
          <div className="flex flex-wrap gap-2">
            <Button disabled={!file || !!working} busy={working === "preview"} onClick={() => void preview()}>{t("starred.backup_preview")}</Button>
            {reviewed && <Button tone="primary" disabled={!canRestore || !!working} busy={working === "restore"} onClick={() => void restore()}>
              {t("starred.backup_restore")}
            </Button>}
          </div>
        </div>}
      </div>
    </Dialog>
  );
}
