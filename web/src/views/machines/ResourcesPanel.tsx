import { useEffect, useState } from "react";
import { t } from "../../i18n";
import { api, errorText } from "../../lib/api";
import { fromShownManager, onManagerChoice } from "../../lib/manager-choice";
import { onHubEvent } from "../../lib/store";
import { documentPage, visiblePoll } from "../../lib/visible-poll";
import { Button, Loading } from "../../ui/atoms";

export interface ResourceJob {
  ticket: string; run?: string; slot?: number; project: string; commit: string; groups: string[]; position?: number;
  started_at?: string; enqueued_at?: string; duplicate_of?: string;
}
interface ResourceDefinition {
  id: string; capacity: number;
  mirror: { machine: string; target: string; port?: number; identity?: string };
  repositories?: Record<string, string>;
}
export interface ResourceStatus extends ResourceDefinition {
  mirror_state: { unknown: boolean; observed_at: string; raw?: string; queue?: { slots: number; running: ResourceJob[]; waiting: ResourceJob[] } };
  leases: { id: string; state: string; holder: { label?: string; machine: string }; joiners?: string[] }[];
}
export function ageSeconds(at: string | undefined, now = Date.now()) {
  const parsed = at ? Date.parse(at) : NaN;
  return Number.isFinite(parsed) && parsed > 0 ? Math.max(0, Math.floor((now - parsed) / 1000)) : null;
}

export function jobAgeSeconds(job: ResourceJob, now: number) {
  return ageSeconds(job.enqueued_at, now) ?? ageSeconds(job.started_at, now);
}

export function ResourcesPanel() {
  const [resources, setResources] = useState<ResourceStatus[] | null>(null);
  const [error, setError] = useState("");
  const [now, setNow] = useState(Date.now());
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    let dead = false;
    let generation = 0;
    const load = async () => {
      const request = ++generation;
      if (!dead) setNow(Date.now());
      try {
        const result = await api<{ resources: ResourceStatus[] }>("/resources");
        if (!dead && request === generation) { setResources(result.resources); setError(""); setNow(Date.now()); }
      } catch (e) { if (!dead && request === generation) setError(errorText(e)); }
    };
    const choice = onManagerChoice(() => { setResources(null); void load(); });
    const stop = visiblePoll(() => void load(), 15_000, documentPage);
    const off = onHubEvent("resources", (event) => {
      if (fromShownManager(event) && Array.isArray(event.resources)) { ++generation; setError(""); setResources(event.resources as ResourceStatus[]); setNow(Date.now()); }
    });
    return () => { dead = true; stop(); off(); choice(); };
  }, []);
  const configure = () => {
    setDraft(JSON.stringify((resources ?? []).map(({ id, capacity, mirror, repositories }) => ({ id, capacity, mirror, repositories })), null, 2));
    setEditing(true);
  };
  const save = async () => {
    setSaving(true);
    try {
      const definitions = JSON.parse(draft) as ResourceDefinition[];
      if (!Array.isArray(definitions)) throw new Error(t("resources.invalid"));
      await api("/resources", { method: "PUT", json: { resources: definitions } });
      const result = await api<{ resources: ResourceStatus[] }>("/resources");
      setResources(result.resources); setEditing(false); setError("");
    } catch (e) { setError(errorText(e)); }
    finally { setSaving(false); }
  };
  return <section className="space-y-4" aria-label={t("resources.title")}>
    <div className="flex flex-wrap items-center gap-3">
      <h2 className="font-semibold text-ink">{t("resources.title")}</h2>
      <Button onClick={configure}>{t("resources.configure")}</Button>
    </div>
    {error && <p role="alert" className="text-red">{error}</p>}
    {resources === null && !error && <Loading />}
    {resources?.length === 0 && <p className="text-ink-2">{t("resources.empty")}</p>}
    {editing && <div className="space-y-3 rounded-control border border-line bg-surface p-4">
      <label className="block space-y-2"><span>{t("resources.registry")}</span>
        <textarea className="w-full rounded-control border border-line bg-field p-3 font-mono text-ink" rows={12} value={draft} onChange={(e) => setDraft(e.target.value)} />
      </label>
      <p className="text-ink-2">{t("resources.registry_help")}</p>
      <div className="flex gap-2"><Button onClick={() => void save()} disabled={saving}>{t("resources.save")}</Button><Button onClick={() => setEditing(false)}>{t("resources.cancel")}</Button></div>
    </div>}
    {resources?.map((resource) => <ResourceCard key={resource.id} resource={resource} now={now} />)}
  </section>;
}

export function ResourceCard({ resource, now }: { resource: ResourceStatus; now: number }) {
  const mirror = resource.mirror_state;
  const age = ageSeconds(mirror.observed_at, now);
  const stale = mirror.unknown || age === null || age > 30;
  const queue = mirror.queue;
  const jobs = [...(queue?.running ?? []), ...(queue?.waiting ?? [])];
  return <article className="space-y-3 rounded-control border border-line bg-surface p-4">
    <h3 className="font-semibold text-ink">{resource.id}</h3>
    <p className="text-ink-2">{t("resources.capacity", { used: queue?.running.length ?? t("resources.unknown"), capacity: queue?.slots ?? resource.capacity })}</p>
    <p role={stale ? "status" : undefined} className={stale ? "text-attention" : "text-ink-2"}>
      {age === null ? t("resources.never") : t(stale ? "resources.stale" : "resources.fresh", { seconds: age })}
    </p>
    {jobs.length > 0 && <div className="overflow-x-auto"><table className="w-full text-left">
      <caption className="sr-only">{t("resources.queue")}</caption>
      <thead className="text-ink-2"><tr>{["ticket", "project", "commit", "groups", "waiting", "start"].map((key) => <th key={key} className="p-2 font-medium">{t(`resources.${key}`)}</th>)}</tr></thead>
      <tbody>{jobs.map((job, index) => <tr key={job.ticket || job.run || `${job.slot ?? "q"}-${job.position ?? index}`} className="border-t border-line">
        <td className="p-2">{job.ticket || job.run || t("resources.unknown")}{job.duplicate_of && <span className="block text-attention">{t("resources.duplicate", { ticket: job.duplicate_of })}</span>}</td>
        <td className="p-2">{job.project}</td><td className="p-2 font-mono">{job.commit || t("resources.unknown")}</td>
        <td className="p-2">{job.groups.join(", ")}</td>
        <td className="p-2">{jobAgeSeconds(job, now) === null ? t("resources.unknown") : t("resources.seconds", { seconds: jobAgeSeconds(job, now)! })}</td>
        <td className="p-2">{job.position ? t("resources.position", { position: job.position }) : t("resources.running")}</td>
      </tr>)}</tbody>
    </table></div>}
    {!queue && <p className="text-ink-2">{t("resources.unknown")}</p>}
    {resource.leases.filter((lease) => lease.state === "queued" || lease.state === "granted").map((lease) => <p key={lease.id} className="text-ink-2">
      {lease.holder.label || lease.holder.machine} · {t(`resources.${lease.state}`)}
      {!!lease.joiners?.length && <> · {t("resources.joined", { count: lease.joiners.length })}</>}
    </p>)}
    {mirror.raw && <details><summary>{t("resources.raw")}</summary><pre className="overflow-x-auto whitespace-pre-wrap text-ink-2">{mirror.raw}</pre></details>}
  </article>;
}
