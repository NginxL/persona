// Settings › hub (docs/operations/hub-update.md "In the app", the app's HubSettingsPage): the hub's version and channel, checking for
// and installing a release (waiting for the assistant and the agents to be idle, or now after a confirmation), a Docker
// hub's commands, auto-update, the personad on each computer, and the history. Follows the hub's `hub_update` pushes.
import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, errorText } from "../../lib/api";
import { ago, bytes, duration } from "../../lib/format";
import { formatNumber, t } from "../../i18n";
import { hubUpdate, setHubUpdateSettings, startHubUpdate, type HubDevelopmentProgress, type HubUpdateBackup, type HubUpdateRecord, type HubUpdateStatus } from "../../lib/hub-update";
import {
  applyHubUpdateBackup, channelTitle, checkHubUpdate, dockerSteps, hubUpdateBackups, hubUpdateHistory, latestWarning,
  recordTitle, recordTone, sourceBehindWarning, updateFinished, updateStateTitle, updateView, updateWorking,
} from "../../lib/hub-update-settings";
import { machineDeployment } from "../../lib/machine-deploy";
import { managerOf, multiManager, useRoster } from "../../lib/managers";
import { navigate } from "../../lib/router";
import { isResumed } from "../../lib/reconnect";
import { onHubEvent, useStore } from "../../lib/store";
import { Button, copyText, cx, Dialog, IconButton, Loading, Spinner, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Group, Pill, Row, Switch, SettingsHint, settingsInputClass } from "./SettingsParts";
import { ExtensionDownloadMirrors } from "./ExtensionDownloadMirrors";

export function HubSettings() {
  return <><HubUpdates /><ExtensionDownloadMirrors /></>;
}

function HubUpdates() {
  const [status, setStatus] = useState<HubUpdateStatus | null>(null);
  const [history, setHistory] = useState<HubUpdateRecord[]>([]);
  const [unsupported, setUnsupported] = useState(false);
  const [error, setError] = useState("");
  const [checking, setChecking] = useState(false);
  const [starting, setStarting] = useState(false);
  const [savingSource, setSavingSource] = useState(false);
  const [savingAuto, setSavingAuto] = useState(false);
  const [confirmNow, setConfirmNow] = useState(false);
  const [backups, setBackups] = useState<HubUpdateBackup[]>([]);
  const [backupError, setBackupError] = useState("");
  const [loadingBackups, setLoadingBackups] = useState(false);
  const [selectedVersion, setSelectedVersion] = useState("");
  const [confirmBackup, setConfirmBackup] = useState(false);
  const last = useRef<string | undefined>(undefined);
  const revision = useRef(0);

  const apply = useCallback((s: HubUpdateStatus) => {
    revision.current++;
    const finished = updateFinished(last.current, s.state);
    last.current = s.state;
    setStatus(s);
    if (finished) hubUpdateHistory().then(setHistory, () => {});
  }, []);

  const load = useCallback(() => {
    const before = revision.current;
    setError("");
    return hubUpdate()
      .then((s) => {
        if (revision.current === before) apply(s);
        setError("");
        hubUpdateHistory().then(setHistory, () => {});
      })
      .catch((e) => {
        if (revision.current !== before) return;
        if (e instanceof ApiError && e.status === 404) setUnsupported(true);
        else setError(errorText(e));
      });
  }, [apply]);

  const loadBackups = useCallback(async () => {
    setLoadingBackups(true);
    try {
      const versions = await hubUpdateBackups();
      setBackups(versions);
      setSelectedVersion(id => versions.some(v => v.id === id) ? id : "");
      setBackupError("");
    } catch (e) { setBackupError(errorText(e)); }
    finally { setLoadingBackups(false); }
  }, []);

  useEffect(() => {
    if (status?.source === "web-dev") {
      if (!updateWorking(status)) void loadBackups();
    }
    else { setBackups([]); setSelectedVersion(""); setConfirmBackup(false); }
    // The effective folder too: after a change the list must come from the new
    // one, or a selection would name a snapshot the hub cannot find there.
  }, [status?.source, status?.state, status?.backup?.id, status?.development?.backup_directory, loadBackups]);

  useEffect(() => {
    void load();
    const off = onHubEvent("hub_update", (m) => m.update && apply(m.update as HubUpdateStatus));
    // a reconnect the hub could not resume (a restart into the new version, often): the hub_update pushes missed
    // meanwhile (confirming, updated, rolled back) are not replayed (#4954)
    const offHello = onHubEvent("hello", (m) => !isResumed(m) && void load());
    return () => {
      off();
      offHello();
    };
  }, [apply, load]);

  const phase = status?.state;
  const streaming = phase === "building" || phase === "backing_up" || phase === "applying_backup";
  useEffect(() => {
    if (!streaming) return;
    let stopped = false;
    let timer: number;
    // Phase transitions arrive by push. Read the counters between pushes and
    // ignore any reply overtaken by a newer state or an unmount.
    async function poll() {
      const before = revision.current;
      try {
        const next = await hubUpdate();
        if (!stopped && before === revision.current) apply(next);
      } catch {
        // Keep the last confirmed progress during a brief connection loss.
      }
      if (!stopped) timer = window.setTimeout(poll, 1000);
    }
    timer = window.setTimeout(poll, 1000);
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, [apply, streaming]);

  const check = async () => {
    setChecking(true);
    try {
      apply(await checkHubUpdate());
    } catch (e) {
      toast(t("settings-hubsettings.check_failed", { error: errorText(e) }), "error");
    } finally {
      setChecking(false);
    }
  };

  const update = async (now: boolean) => {
    setStarting(true);
    try {
      apply(await startHubUpdate(now));
      setConfirmNow(false);
    } catch (e) {
      toast(t("settings-hubsettings.start_failed", { error: errorText(e) }), "error");
    } finally {
      setStarting(false);
    }
  };

  const restore = async () => {
    setStarting(true);
    try {
      apply(await applyHubUpdateBackup(selectedVersion));
      setConfirmBackup(false);
    } catch (e) { toast(t("settings-hubsettings.start_failed", { error: errorText(e) }), "error"); }
    finally { setStarting(false); }
  };

  const setAuto = async (on: boolean, computers = false) => {
    setSavingAuto(true);
    try {
      apply(await setHubUpdateSettings(computers ? { auto_update_nodes: on } : { auto: on }));
    } catch (e) {
      toast(t("settings-hubsettings.save_failed", { error: errorText(e) }), "error");
    } finally { setSavingAuto(false); }
  };
  const setSource = async (source: "release" | "beta" | "web-dev") => {
    setSavingSource(true);
    try { apply(await setHubUpdateSettings({ source })); }
    catch (e) { toast(t("settings-hubsettings.save_failed", { error: errorText(e) }), "error"); }
    finally { setSavingSource(false); }
  };

  if (unsupported) {
    return (
      <Group>
        <p className="px-4 py-3 text-[12.5px] leading-relaxed text-ink-3">{t("settings-hubsettings.unsupported")}</p>
      </Group>
    );
  }
  if (!status) {
    return error ? (
      <div className="flex items-center gap-3 px-1">
        <p role="alert" className="min-w-0 flex-1 text-[13px] break-words text-red">{t("settings-hubsettings.version_error", { error })}</p>
        <Button icon="retry" onClick={() => void load()}>{t("settings-hubsettings.retry")}</Button>
      </div>
    ) : (
      <Loading />
    );
  }

  const working = updateWorking(status);
  const view = updateView(status);
  const warning = latestWarning(status);
  const activeBackup = working ? status.latest?.backup_id : undefined;
  const selectedID = activeBackup || selectedVersion;
  const selectedBackup = status.source === "web-dev" ? backups.find(v => v.id === selectedID) : undefined;
  const updateDate = (value?: string) => {
    const date = value ? new Date(value) : null;
    if (!date || Number.isNaN(date.getTime())) return t("settings-hubsettings.unknown");
    return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
  };
  const datedCommit = (commit?: string, date?: string) => commit ? `${updateDate(date)} · ${commit.slice(0, 12)}` : t("settings-hubsettings.unknown");
  const behind = sourceBehindWarning(status);

  return (
    <>
      {status.auto_notice && (
        <div className="mb-6 flex items-center gap-3 rounded-card bg-accent-tint px-4 py-2.5 text-[12.5px] text-accent-ink">
          <Icon name="info" size={14} className="shrink-0" />
          <span className="min-w-0 flex-1">{t("settings-hubsettings.auto_notice")}</span>
          <Button tone="quiet" onClick={() => void setAuto(false)} className="h-7">{t("settings-hubsettings.turn_off")}</Button>
          <Button tone="quiet" onClick={() => void setHubUpdateSettings({ auto_notice_seen: true }).then(apply, (e) => toast(errorText(e), "error"))} className="h-7">
            {t("settings-hubsettings.got_it")}
          </Button>
        </div>
      )}

      <Group title={t("settings-hubsettings.version")} anchor="hub-version">
        <Row label={t("settings-hubsettings.version")} mono stackOnNarrow>
          <span className="max-w-[320px] truncate" title={status.version}>{status.version}</span>
        </Row>
        <Row label={t("settings-hubsettings.channel")} anchor="hub-channel">{status.version.startsWith("web-dev-") ? t("settings-hubsettings.web_dev") : channelTitle(status.channel)}</Row>
        {status.development && <>
          <Row label={t("settings-hubsettings.source")} stackOnNarrow>
            <select aria-label={t("settings-hubsettings.source")} className={settingsInputClass}
              value={status.source ?? "release"} disabled={working || checking || starting || savingSource || savingAuto}
              onChange={(e) => void setSource(e.target.value as "release" | "beta" | "web-dev")}>
              <option value="release">{t("lib-hub-update.channel_release")}</option>
              <option value="beta">{t("settings-hubsettings.beta_channel")}</option>
              <option value="web-dev" disabled={!status.development.configured}>{t("settings-hubsettings.web_dev")}</option>
            </select>
          </Row>
          {!status.development.configured && <SettingsHint>{status.development.why || t("settings-hubsettings.dev_setup")}</SettingsHint>}
        </>}
        {status.source === "beta" && <SettingsHint>{t("settings-hubsettings.beta_scope")}</SettingsHint>}
        {status.source === "web-dev" && <>
          <SettingsHint>{t("settings-hubsettings.dev_scope")}</SettingsHint>
          <Row label={t("settings-hubsettings.branch")} wrapValue>{status.development?.branch}</Row>
          <Row label={t("settings-hubsettings.current_commit")} mono wrapValue>{datedCommit(status.development?.current_commit, status.development?.current_updated_at)}</Row>
          <Row label={t("settings-hubsettings.latest_commit")} mono wrapValue>{!status.latest?.backup_id ? datedCommit(status.latest?.commit, status.latest?.updated_at) : t("settings-hubsettings.unknown")}</Row>
        </>}
        {status.last_check && <Row label={t("settings-hubsettings.last_check")}>{ago(status.last_check)}</Row>}
        <div className="flex justify-end px-4 py-2.5">
          <Button icon="retry" busy={checking} disabled={checking || working || savingSource} onClick={() => void check()}>
            {checking ? t("settings-hubsettings.checking") : t("settings-hubsettings.check")}
          </Button>
        </div>
      </Group>

      <Group title={t("settings-hubsettings.update")} anchor="hub-update" footer={status.can_update ? t("settings-hubsettings.update_footer") : undefined}>
        {status.source === "web-dev" && status.can_update && <>
          <Row label={t("settings-hubsettings.update_version")} stackOnNarrow>
            <select aria-label={t("settings-hubsettings.update_version")} className={cx(settingsInputClass, "min-w-0 max-w-full")}
              value={selectedID} disabled={working || checking || starting || savingSource || loadingBackups}
              onChange={e => setSelectedVersion(e.target.value)}>
              <option value="">{t("settings-hubsettings.latest_main")}{status.latest?.commit && !status.latest.backup_id ? ` · ${datedCommit(status.latest.commit, status.latest.updated_at)}` : ""}</option>
              {backups.map(v => <option key={v.id} value={v.id}>{t("settings-hubsettings.backup_version")} · {datedCommit(v.commit, v.updated_at)}</option>)}
              {activeBackup && !backups.some(v => v.id === activeBackup) && <option value={activeBackup}>{t("settings-hubsettings.backup_version")} · {datedCommit(status.latest?.commit, status.latest?.updated_at)}</option>}
            </select>
          </Row>
          {backupError && <div className="flex items-center gap-3 px-4 py-2.5">
            <p role="alert" className="min-w-0 flex-1 text-label break-words text-red">{t("settings-hubsettings.backup_error", { error: backupError })}</p>
            <Button disabled={loadingBackups} onClick={() => void loadBackups()}>{t("settings-hubsettings.retry")}</Button>
          </div>}
          {selectedBackup && <>
            <Row label={t("settings-hubsettings.target_commit")} mono wrapValue>{datedCommit(selectedBackup.commit, selectedBackup.updated_at)}</Row>
            <Row label={t("settings-hubsettings.backup_created_at")} wrapValue>{updateDate(selectedBackup.created_at)}</Row>
            <SettingsHint>{t("settings-hubsettings.program_backup_hint")}</SettingsHint>
            {!working && <div className="flex justify-end px-4 py-2.5">
              <Button tone="accent" busy={starting} disabled={starting || checking || savingSource || loadingBackups} onClick={() => setConfirmBackup(true)}>{t("settings-hubsettings.apply_backup")}</Button>
            </div>}
          </>}
        </>}
        {view.kind === "docker" && <DockerRows s={status} />}
        {view.kind === "cannot" && <p className="px-4 py-3 text-[12.5px] leading-relaxed text-ink-3">{view.why}</p>}
        {!selectedBackup && view.kind === "newer" && status.latest && (
          <>
            <div className="px-4 py-3">
              <div className="flex items-center gap-2">
                <span className="truncate text-[13.5px] font-medium text-ink">{t("settings-hubsettings.new_version", { version: status.latest.version })}</span>
                {status.latest.released && <span className="shrink-0 text-[11.5px] text-ink-3">{t("settings-hubsettings.released", { time: ago(status.latest.released) })}</span>}
              </div>
              {status.latest.notes && <p className="mt-1 text-[12.5px] leading-relaxed text-ink-2">{status.latest.notes}</p>}
              {warning && (
                <p className="mt-1.5 flex items-start gap-1.5 text-[12px] leading-snug text-orange">
                  <Icon name="info" size={13} className="mt-px shrink-0" />
                  {warning}
                </p>
              )}
            </div>
            {view.working ? (
              <Progress s={status} onNow={() => setConfirmNow(true)} />
            ) : (
              <div className="flex justify-end px-4 py-2.5">
                <Button tone="accent" busy={starting} disabled={starting} onClick={() => void update(false)}>
                  {t("settings-hubsettings.update_hub")}
                </Button>
              </div>
            )}
          </>
        )}
        {(view.kind === "working" || (selectedBackup && working) || (streaming && view.kind === "cannot")) && <Progress s={status} onNow={() => setConfirmNow(true)} />}
        {!selectedBackup && view.kind === "current" && (status.source !== "web-dev" || status.last_check) && (behind ? (
          <p className="flex items-start gap-1.5 px-4 py-3 text-meta leading-snug break-words text-orange">
            <Icon name="info" size={13} className="mt-0.5 shrink-0" />
            <span className="min-w-0">{behind}</span>
          </p>
        ) : !status.error && (
          <div className="flex items-center gap-2 px-4 py-3 text-[13px] text-green">
            <Icon name="check" size={14} stroke={2.4} />
            {t("settings-hubsettings.up_to_date")}
          </div>
        ))}
        {status.error && !working && <p className={cx("px-4 py-2.5 text-[12px] break-words", status.state === "rolled_back" ? "text-orange" : "text-red")}>{status.error}</p>}
      </Group>

      {status.source === "web-dev" && status.development && <DevelopmentBackup s={status} onSaved={apply} />}

      {status.can_update && (
        <Group footer={t(status.source === "web-dev" ? "settings-hubsettings.dev_auto_footer" : "settings-hubsettings.auto_footer")} anchor="hub-auto-update">
          <Row label={t("settings-hubsettings.auto_update")}>
            <Switch on={status.auto} disabled={savingAuto || savingSource} onChange={(on) => void setAuto(on)} label={t("settings-hubsettings.auto_update")} />
          </Row>
        </Group>
      )}
      {status.source === "web-dev" && status.auto_update_nodes !== undefined && <Group footer={t("settings-hubsettings.computer_auto_footer")}>
        <Row label={t("settings-hubsettings.computer_auto")} wrapHint>
          <Switch on={status.auto_update_nodes} disabled={working || savingSource || savingAuto} label={t("settings-hubsettings.computer_auto")}
            onChange={(on) => void setAuto(on, true)} />
        </Row>
      </Group>}

      <Nodes development={status.source === "web-dev"} />

      {history.length > 0 && (
        <Group title={t("settings-hubsettings.history")}>
          {history.slice(0, 10).map((r, i) => (
            <div key={`${r.to_build}/${r.started ?? i}`} className="px-4 py-2.5">
              <div className="flex flex-wrap items-center gap-2">
                <span className={cx("text-[13px] font-medium", { green: "text-green", orange: "text-orange", red: "text-red" }[recordTone(r.result)])}>{recordTitle(r.result)}</span>
                {r.auto && <Pill>{t("settings-hubsettings.auto")}</Pill>}
                {r.source === "web-dev" && <Pill>{t("settings-hubsettings.web_dev")}</Pill>}
                <span className="flex-1" />
                {(r.ended || r.started) && <span className="shrink-0 text-[11.5px] text-ink-3">{updateDate(r.ended || r.started)}</span>}
              </div>
              <div className="mt-0.5 truncate font-mono text-[11.5px] text-ink-3">{`${r.from_version} → ${r.to_version}`}</div>
              {r.commit && <div className="mt-0.5 font-mono text-submeta text-ink-3">{datedCommit(r.commit, r.ended || r.started)}</div>}
              {r.reason && <p className="mt-0.5 text-[11.5px] leading-snug text-ink-3">{r.reason}</p>}
            </div>
          ))}
        </Group>
      )}

      <Dialog open={confirmBackup} onClose={() => setConfirmBackup(false)} title={t("settings-hubsettings.apply_backup")} width={400}
        footer={<>
          <Button tone="quiet" onClick={() => setConfirmBackup(false)}>{t("common.cancel")}</Button>
          <Button tone="accent" busy={starting} disabled={starting || working || !selectedBackup} onClick={() => void restore()}>{t("settings-hubsettings.apply_backup")}</Button>
        </>}>
        <p className="text-meta leading-relaxed text-ink-2">{t("settings-hubsettings.apply_backup_confirm")}</p>
      </Dialog>

      <Dialog
        open={confirmNow}
        onClose={() => setConfirmNow(false)}
        title={t("settings-hubsettings.confirm_title")}
        width={400}
        footer={
          <>
            <Button tone="quiet" onClick={() => setConfirmNow(false)}>{t("common.cancel")}</Button>
            <Button tone="danger" busy={starting} onClick={() => void update(true)}>{t("settings-hubsettings.update_now")}</Button>
          </>
        }
      >
        <p className="text-[12.5px] leading-relaxed text-ink-2">
          {t("settings-hubsettings.confirm_body")}
        </p>
      </Dialog>
    </>
  );
}

/** Web development backups: where snapshots go and how many are kept. Only a
 *  folder and a count — the hub pauses its own writers; no command is accepted. */
function DevelopmentBackup({ s, onSaved }: { s: HubUpdateStatus; onSaved: (s: HubUpdateStatus) => void }) {
  const d = s.development!;
  const recommended = d.backup_default_directory ?? "";
  const effective = d.backup_directory ?? recommended;
  const isDefault = d.backup_directory_is_default ?? (!effective || effective === recommended);
  const initialDir = isDefault ? "" : effective;
  const [dir, setDir] = useState(initialDir);
  const [retain, setRetain] = useState(String(d.backup_retain ?? 3));
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    setDir(initialDir);
    setRetain(String(d.backup_retain ?? 3));
  }, [initialDir, d.backup_retain]);
  const save = async () => {
    setSaving(true);
    try {
      const trimmed = dir.trim();
      const payload: { development_backup_directory?: string; development_backup_retain: number } = {
        development_backup_retain: Number(retain),
      };
      if (trimmed !== initialDir) {
        payload.development_backup_directory = trimmed;
      }
      onSaved(await setHubUpdateSettings(payload));
      toast(t("settings-hubsettings.backup_saved"), "ok");
    } catch (e) {
      toast(t("settings-hubsettings.save_failed", { error: errorText(e) }), "error");
    } finally { setSaving(false); }
  };
  return (
    <Group title={t("settings-hubsettings.dev_backup")} anchor="hub-dev-backup" footer={t("settings-hubsettings.dev_backup_footer")}>
      <Row label={t("settings-hubsettings.backup_folder")} stackOnNarrow>
        <input aria-label={t("settings-hubsettings.backup_folder")} spellCheck={false}
          className={cx(settingsInputClass, "w-[320px] max-w-full font-mono text-xs")}
          placeholder={recommended} value={dir} onChange={(e) => setDir(e.target.value)} />
      </Row>
      <SettingsHint>{t("settings-hubsettings.backup_folder_hint", { path: recommended })}</SettingsHint>
      <Row label={t("settings-hubsettings.backup_retain")} wrapHint>
        <select aria-label={t("settings-hubsettings.backup_retain")} className={settingsInputClass} value={retain} disabled={saving}
          onChange={(e) => setRetain(e.target.value)}>
          {[...new Set([1, 2, 3, 5, 10, 20, 50, Number(retain)])].sort((a, b) => a - b).map((n) => <option key={n} value={String(n)}>{n}</option>)}
        </select>
      </Row>
      <SettingsHint>{t(d.builtin_quiesce ? "settings-hubsettings.backup_builtin" : "settings-hubsettings.backup_external")}</SettingsHint>
      <div className="flex justify-end gap-2 px-4 py-2.5">
        <Button tone="quiet" disabled={saving || dir === ""} onClick={() => setDir("")}>{t("settings-hubsettings.use_recommended")}</Button>
        <Button busy={saving} disabled={saving} onClick={() => void save()}>{t("settings-hubsettings.save_backup")}</Button>
      </div>
    </Group>
  );
}

/** The update under way, and while it waits for idle what it waits for, with "Update now". */
function Progress({ s, onNow }: { s: HubUpdateStatus; onNow: () => void }) {
  const progress = s.development_progress;
  if (progress?.phase === s.state) {
    const key = `settings-hubsettings.progress_step_${progress.step}`;
    const step = t(key);
    if (step !== key) return <DevelopmentProgress title={updateStateTitle(s)} step={step} p={progress} />;
  }
  return (
    <>
      <div className="flex items-center gap-2 px-4 py-3 text-[13px] text-ink">
        <Spinner />
        <span className="truncate">{updateStateTitle(s)}</span>
        {s.progress && s.state === "downloading" && <span className="shrink-0 font-mono text-[11.5px] text-ink-3">{s.progress}</span>}
      </div>
      {s.state === "waiting_idle" && s.busy.length > 0 && (
        <>
          {s.busy.map((b) => (
            <div key={`${b.machine_id}/${b.session_id ?? b.title}`} className="flex items-center gap-3 px-4 py-2">
              <span className="size-1.5 shrink-0 rounded-full bg-accent" />
              <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink">{b.title}</span>
              <span className="shrink-0 text-[11.5px] text-ink-3">{b.machine}</span>
            </div>
          ))}
          <div className="flex justify-end px-4 py-2.5">
            <Button tone="quiet" onClick={onNow} className="text-red enabled:hover:text-red">{t("settings-hubsettings.update_now_ellipsis")}</Button>
          </div>
        </>
      )}
    </>
  );
}

function DevelopmentProgress({ title, step, p }: { title: string; step: string; p: HubDevelopmentProgress }) {
  const stepNumber = `${formatNumber(p.step_index)}/${formatNumber(p.step_total)}`;
  const size = p.bytes_done === undefined ? undefined :
    p.bytes_total === undefined ? bytes(p.bytes_done) : `${bytes(p.bytes_done)} / ${bytes(p.bytes_total)}`;
  const speed = p.bytes_per_second === undefined ? undefined : t("settings-hubsettings.progress_speed", {
    speed: formatNumber(p.bytes_per_second / (1024 * 1024), { maximumFractionDigits: 2 }),
  });
  const countKey = p.step === "archive_files" ? "settings-hubsettings.progress_files" :
    p.step === "database_dump" ? "settings-hubsettings.progress_databases" : "settings-hubsettings.progress_items";
  const items = p.item_done === undefined || p.item_total === undefined ? undefined :
    t(countKey, { done: formatNumber(p.item_done), total: formatNumber(p.item_total) });
  const elapsed = t("settings-hubsettings.progress_elapsed", { time: duration(p.elapsed_seconds * 1000) });
  return (
    <div className="flex flex-col gap-1.5 px-4 py-3">
      <div className="flex items-center gap-2 text-meta text-ink">
        {p.percent === undefined && <Spinner />}
        <span>{title}</span>
      </div>
      <div className="flex items-baseline gap-2 text-label text-ink-2 tabular-nums">
        <span role="status" className="min-w-0 flex-1">{stepNumber} · {step}</span>
        {p.percent !== undefined && <span className="shrink-0">{formatNumber(p.percent / 100, { style: "percent" })}</span>}
      </div>
      {p.percent !== undefined && (
        <div role="progressbar" aria-label={step} aria-valuenow={p.percent} aria-valuemin={0} aria-valuemax={100}
          className="h-1 overflow-hidden rounded-full bg-inset">
          <div className="h-full rounded-full bg-accent" style={{ width: `${p.percent}%` }} />
        </div>
      )}
      <p className="text-label text-ink-3 tabular-nums">{[p.item_label, size, speed, items, elapsed].filter(Boolean).join(" · ")}</p>
    </div>
  );
}

function DockerRows({ s }: { s: HubUpdateStatus }) {
  const { headline, steps } = dockerSteps(s);
  return (
    <div className="px-4 py-3">
      {headline && <div className="mb-1.5 text-[13.5px] font-medium text-ink">{headline}</div>}
      {steps.map((st) => (
        <div key={st.copy} className="mb-2 last:mb-0">
          <p className="text-[12.5px] leading-relaxed text-ink-3">{st.text}</p>
          <div className="mt-1 flex items-center gap-2 rounded-control bg-inset px-2.5 py-1.5 shadow-hairline">
            <code className="min-w-0 flex-1 truncate font-mono text-[12px] text-ink select-all">{st.copy}</code>
            <IconButton icon="copy" label={t("settings-hubsettings.copy")} size={24} iconSize={12} onClick={() => void copyText(st.copy).then(() => toast(t("settings-hubsettings.copied"), "ok"))} />
          </div>
        </div>
      ))}
    </div>
  );
}

function Nodes({ development }: { development: boolean }) {
  const machines = useStore((s) => s.machines);
  const roster = useRoster();
  if (!machines.length) return null;
  const showOwners = multiManager(roster);
  return (
    <Group title={t("settings-hubsettings.nodes")} anchor="hub-nodes"
      footer={t(development ? "settings-hubsettings.dev_nodes_footer" : "settings-hubsettings.nodes_footer")}>
      {machines.map((m) => {
        const deployment = machineDeployment(m);
        const owner = showOwners && m.manager ? managerOf(roster, m.manager) : null;
        const source = m.deploy_source === "hub_package" ? t("settings-hubsettings.hub_package")
          : m.deploy_source === "local_build" ? t("settings-hubsettings.local_build") : "";
        const detail = [owner && t("settings-hubsettings.managed_by", { name: owner.name }), source].filter(Boolean).join(" · ");
        const reason = !deployment.busy && m.can_deploy !== true && m.deploy_reason !== m.deploy_error ? m.deploy_reason : undefined;
        const offline = !m.online && t("settings-hubsettings.offline");
        const version = m.node_version || offline || t("settings-hubsettings.unknown");
        const badge = deployment.busy ? <Pill tone="accent">{deployment.label}</Pill>
          : m.node_mismatch ? <Pill tone="orange">{t("settings-hubsettings.version_differs")}</Pill>
          : m.node_version && offline && <Pill>{offline}</Pill>;
        return (
          <Row key={m.id} onClick={() => navigate({ name: "machine", mid: m.id })} wrapHint
            label={<span className="flex flex-wrap items-center gap-x-2 gap-y-1" aria-live="polite">
              <span className="min-w-0 truncate">{m.name}</span>
              {badge}
              <span className="min-w-0 truncate font-mono text-meta text-ink-3 sm:ml-auto" title={m.node_version}>{version}</span>
            </span>}
            hint={(detail || deployment.error || reason) && <span className="block whitespace-normal break-words">
              {detail && <span className="block">{detail}</span>}
              {deployment.error && <span className="block text-red">{deployment.error}</span>}
              {reason && <span className="block">{reason}</span>}
            </span>} />
        );
      })}
    </Group>
  );
}
