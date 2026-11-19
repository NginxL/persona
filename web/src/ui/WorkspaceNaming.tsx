import { useId, useRef, useState } from "react";
import "./WorkspaceNaming.css";
import { t } from "../i18n";
import { api, ApiError } from "../lib/api";
import { useStore } from "../lib/store";
import { loadTaskRepositories, repositoryRequestOptions, type RepositoryLayout, type TaskRepository } from "../lib/task-repositories";
import { useRepositoryRequestContext } from "./TaskRepositoryBindingsEditor";
import { loadWorkspaceRules, previewWorkspaceNaming, RULE_FIELDS, rulesPatch, sameRules, saveWorkspaceRules, WORKSPACE_NAMING_VERSION,
  type NamingDiagnostic, type WorkspaceNamingPreview as Preview, type WorkspaceNamingSettings, type WorkspaceRules } from "../lib/workspace-naming";
import { settingsInputClass } from "../views/settings/SettingsParts";
import { Button, Loading } from "./atoms";

/** An empty field removes its override; placeholders describe the current fallback without materializing it. */
export function WorkspaceRulesEditor({ value, inherited, defaultSource = "system", disabled, diagnostics = [], onChange }: {
  value: WorkspaceRules; inherited: WorkspaceRules; defaultSource?: "system" | "assistant";
  disabled?: boolean; diagnostics?: NamingDiagnostic[]; onChange: (value: WorkspaceRules) => void;
}) {
  const id = useId();
  return <div className="space-y-3">
    {RULE_FIELDS.map((field) => {
      const error = diagnostics.find((d) => d.field === field && d.severity === "error");
      const placeholder = t(`workspace-naming.default_${defaultSource}`, { value: inherited[field] ?? "" });
      return <div key={field} className="grid min-w-0 grid-cols-1 items-baseline gap-2 sm:grid-cols-3">
        <label htmlFor={`${id}-${field}`} className="text-label font-medium text-ink">{t(`workspace-naming.${field}`)}</label>
        <div className="min-w-0 space-y-1 sm:col-span-2">
          <input id={`${id}-${field}`} className={`${settingsInputClass} min-w-0 font-mono pointer-coarse:min-h-11`} spellCheck={false} disabled={disabled}
            value={value[field] ?? ""} placeholder={placeholder} onChange={(e) => {
              const next = { ...value };
              if (e.target.value === "") delete next[field]; else next[field] = e.target.value;
              onChange(next);
            }} aria-invalid={!!error} aria-describedby={`${id}-${field}-help ${id}-${field}-default${error ? ` ${id}-${field}-error` : ""}`} />
          <span id={`${id}-${field}-default`} className="sr-only">{placeholder}</span>
          <p id={`${id}-${field}-help`} className="break-words text-label leading-relaxed text-ink-3">{t(`workspace-naming.help_${field}`)}</p>
          {error && <p id={`${id}-${field}-error`} role="alert" className="break-words text-label text-red">{t(`workspace-naming.${error.code}`, { field: t(`workspace-naming.${field}`) })}</p>}
        </div>
      </div>;
    })}
  </div>;
}

/** A compact result: the root folder once, then its branch or each repository's folder-to-branch mapping. */
export function WorkspaceNamingPreview({ result, loading, error }: { result?: Preview; loading?: boolean; error?: string }) {
  const plan = result?.plan;
  const repositories = plan?.repositories ?? [];
  const rootRepo = repositories.find((repo) => repo.relative_path === ".");
  const nested = repositories.filter((repo) => repo.relative_path !== ".");
  return <div aria-live="polite" className="min-w-0 space-y-2 text-label text-ink-2">
    {loading && <Loading label={t("workspace-naming.previewing")} />}
    {error && <p role="alert" className="text-red">{error}</p>}
    {result && <>
      <p className="font-medium text-ink">{t(result.sample ? "workspace-naming.sample" : "workspace-naming.preview_title")}</p>
      {result.diagnostics.length > 0 && <div className="space-y-1">{result.diagnostics.map((d, index) => <p key={index} className={d.severity === "error" ? "text-red" : "text-ink-2"}>
        {t(`workspace-naming.${d.code}`, { field: RULE_FIELDS.includes(d.field as typeof RULE_FIELDS[number]) ? t(`workspace-naming.${d.field}`) : t("workspace-naming.title") })}{d.repo_id ? ` (${d.repo_id})` : ""}
      </p>)}</div>}
      {plan && <>
        <div className="min-w-0 space-y-2 rounded-control bg-inset px-3 py-2.5">
          <div className="min-w-0">
            <p className="text-meta text-ink-3">{t("workspace-naming.result_folder")}</p>
            <p data-naming-root className="mt-0.5 break-all font-mono text-meta text-ink">{plan.root_path}</p>
          </div>
          {(rootRepo || nested.length > 0) && <div className="min-w-0 border-t border-line pt-2">
            {rootRepo
              ? <><p className="text-meta text-ink-3">{t("workspace-naming.result_branch")}</p>
                <p data-naming-branch className="mt-0.5 break-all font-mono text-meta text-ink">{rootRepo.task_branch}</p></>
              : <><p className="text-meta text-ink-3">{t("workspace-naming.result_repositories")}</p>
                <ul className="mt-1 space-y-1">{nested.map((repo) => <li key={repo.repo_id} data-naming-repo className="flex min-w-0 flex-wrap items-baseline gap-x-1.5 gap-y-0.5">
                  <span data-naming-folder className="break-all font-mono text-meta text-ink">{repo.relative_path}</span>
                  <span aria-hidden="true" className="text-ink-3">→</span>
                  <span className="text-label text-ink-3">{t("workspace-naming.result_branch")}</span>
                  <span data-naming-branch className="min-w-0 break-all font-mono text-meta text-ink-2">{repo.task_branch}</span>
                </li>)}</ul></>}
          </div>}
        </div>
      </>}
    </>}
  </div>;
}

/** Secondary advanced area. It changes no creation mode and never starts an Agent. */
export function WorkspaceNaming({ manager, owner, todoId }: { manager: string; owner: string; todoId?: string }) {
  return <WorkspaceNamingSession key={`${owner}:${manager}:${todoId ?? "settings"}`} manager={manager} owner={owner} todoId={todoId} />;
}
function WorkspaceNamingSession({ manager, owner, todoId }: { manager: string; owner: string; todoId?: string }) {
  const context = useRepositoryRequestContext(manager, owner);
  const machines = useStore((s) => s.machines);
  const [open, setOpen] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [supported, setSupported] = useState(false);
  const [draft, setDraft] = useState<WorkspaceRules>({});
  const [saved, setSaved] = useState<WorkspaceRules>({});
  const [inherited, setInherited] = useState<WorkspaceRules>({});
  const [revision, setRevision] = useState(0);
  const [repositories, setRepositories] = useState<TaskRepository[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const [machine, setMachine] = useState("");
  const [layout, setLayout] = useState<RepositoryLayout>("container");
  const [busy, setBusy] = useState(false);
  const [previewBusy, setPreviewBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<Preview>();
  const sequence = useRef(0);
  const saving = useRef(false);
  const failure = (error: unknown, fallback: string) => {
    if (error instanceof ApiError && error.code?.startsWith("workspace_naming_")) {
      setResult({ status: "invalid", sample: !todoId, diagnostics: [{ code: error.code.slice("workspace_naming_".length), field: typeof error.body.field === "string" ? error.body.field : "workspace_rules", severity: "error" }] });
      setError(error.status === 400 ? t("workspace-naming.check_fields") : fallback);
      return;
    }
    setError(fallback);
  };
  const invalidate = () => { sequence.current++; setResult(undefined); setPreviewBusy(false); setError(""); };
  const refresh = async (keepDraft: boolean) => {
    const info = await api<WorkspaceNamingSettings>("/assistant", repositoryRequestOptions(context));
    if (!context.isCurrent()) return;
    if (info.manager !== manager || info.workspace_naming_version !== WORKSPACE_NAMING_VERSION) { setLoaded(true); setSupported(false); return; }
    let rules = info.workspace_rules ?? {};
    if (todoId) {
      const [record, set] = await Promise.all([loadWorkspaceRules(context, todoId), loadTaskRepositories(context, todoId)]);
      if (!context.isCurrent()) return;
      rules = record.workspace_rules; setRevision(record.revision); setRepositories(set.repositories);
      const available = set.repositories.map((r) => r.repo_id);
      // A removed repository must not survive invisibly in the next preview request.
      setSelected((current) => keepDraft ? current.filter((id) => available.includes(id)) : available);
    }
    invalidate();
    if (!keepDraft) setDraft(rules);
    // The freshly fetched rules are the dirty-comparison baseline even when the draft is kept, so a
    // clear the server did not persist stays visible as unsaved and can be retried.
    setSaved(rules);
    setInherited(todoId ? info.effective_workspace_rules ?? {} : { base_directory: "{home}/workspace", task_directory_template: "task-{task-id}", branch_template: "persona/{task-id}" });
    if (!keepDraft) setMachine(info.machine_id ?? "");
    setSupported(true); setLoaded(true);
  };
  const load = async (keepDraft = false) => {
    if (!context.isCurrent() || saving.current) return;
    saving.current = true; setBusy(true); setError("");
    try { await refresh(keepDraft); }
    catch { if (context.isCurrent()) setError(t("workspace-naming.failed")); }
    finally { if (context.isCurrent()) { saving.current = false; setBusy(false); } }
  };
  const save = async () => {
    if (!context.isCurrent() || saving.current) return;
    saving.current = true; setBusy(true); setError("");
    try {
      const response = await saveWorkspaceRules(context, todoId, revision, draft);
      if (!context.isCurrent()) return;
      setSaved({ ...draft }); if (todoId) setRevision(response.revision);
      if (todoId) {
        // Keep the confirmed write if a follow-up read fails; recovery retries
        // reads, while a fresh record supplies the next save's baseline and CAS.
        try { await refresh(true); }
        catch { if (context.isCurrent()) setError(t("workspace-naming.failed")); }
      }
    } catch (error) { if (context.isCurrent()) failure(error, t("workspace-naming.save_failed")); }
    finally { if (context.isCurrent()) { saving.current = false; setBusy(false); } }
  };
  const preview = async () => {
    const generation = ++sequence.current; setPreviewBusy(true); setError(""); setResult(undefined);
    try {
      const response = await previewWorkspaceNaming(context, todoId, { machine_id: machine, layout, selected_repo_ids: selected, workspace_rules: rulesPatch(draft) });
      if (context.isCurrent() && generation === sequence.current) setResult(response);
    } catch (error) { if (context.isCurrent() && generation === sequence.current) failure(error, t("workspace-naming.preview_failed")); }
    finally { if (context.isCurrent() && generation === sequence.current) setPreviewBusy(false); }
  };
  return <div data-workspace-naming data-settings-group="workspace-naming" className="min-w-0">
    <Button tone="quiet" icon={open ? "chevronDown" : "chevronRight"} expanded={open}
      className="workspace-naming-toggle min-h-11 w-full justify-start px-4 text-left font-normal text-ink"
      onClick={() => { setOpen(!open); if (!open && !loaded) void load(); }}>{t("workspace-naming.title")}</Button>
    {open && <div className="min-w-0 space-y-3 border-t border-line px-4 py-3">
      <p className="text-label text-ink-2">{t("workspace-naming.scope")}</p>
      {busy && !loaded && <Loading />}
      {loaded && !supported && <p className="text-label text-ink-2">{t("workspace-naming.unsupported")}</p>}
      {loaded && supported && <>
        <WorkspaceRulesEditor value={draft} inherited={inherited} defaultSource={todoId ? "assistant" : "system"} disabled={busy} diagnostics={result?.diagnostics} onChange={(value) => { invalidate(); setDraft(value); }} />
        <div className="workspace-naming-actions flex flex-wrap items-center gap-2">
          {!sameRules(draft, saved) && <span className="text-meta text-ink-3">{t("workspace-naming.unsaved")}</span>}
          <Button tone="primary" busy={busy} disabled={sameRules(draft, saved)} onClick={() => void save()}>{t("common.save")}</Button>
        </div>
        <details className="min-w-0 border-t border-line text-label text-ink-2">
          <summary className="min-h-11 cursor-pointer py-3">{t("workspace-naming.preview_options")}</summary>
          <div className="min-w-0 space-y-2">
            {todoId && <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1">
              {repositories.map((repo) => <label key={repo.repo_id} className="flex items-center gap-2 text-label text-ink-2 pointer-coarse:min-h-11">
                <input type="checkbox" checked={selected.includes(repo.repo_id)} onChange={(e) => { invalidate(); setSelected(e.target.checked ? [...selected, repo.repo_id] : selected.filter((id) => id !== repo.repo_id)); }} />{repo.source_snapshot.identity.path}
              </label>)}
            </div>}
            <div className="workspace-naming-options flex min-w-0 flex-col gap-2 sm:flex-row">
              <label className="block min-w-0 text-label text-ink-2 sm:flex-1">{t("workspace-naming.computer")}
                <select aria-label={t("workspace-naming.computer")} className={`${settingsInputClass} mt-1 min-w-0 pointer-coarse:min-h-11`} value={machine} onChange={(e) => { invalidate(); setMachine(e.target.value); }}>
                  <option value="">{t("workspace-naming.choose_computer")}</option>{machines.map((m) => <option key={m.id} value={m.id}>{m.name || m.id}</option>)}
                </select>
              </label>
              <label className="block min-w-0 text-label text-ink-2 sm:flex-1">{t("workspace-naming.layout")}
                <select aria-label={t("workspace-naming.layout")} className={`${settingsInputClass} mt-1 min-w-0 pointer-coarse:min-h-11`} value={layout} onChange={(e) => { invalidate(); setLayout(e.target.value as RepositoryLayout); }}>
                  <option value="container">{t("workspace-naming.container")}</option><option value="root">{t("workspace-naming.root")}</option>
                </select>
              </label>
              <Button tone="secondary" busy={previewBusy} disabled={!machine} className="w-full pointer-coarse:min-h-11 sm:w-auto" onClick={() => void preview()}>{t("workspace-naming.preview")}</Button>
            </div>
            <WorkspaceNamingPreview result={result} loading={previewBusy} />
          </div>
        </details>
      </>}
      {error && <><p role="alert" className="text-label text-red">{error}</p>{loaded && supported && <Button tone="quiet" disabled={busy} onClick={() => { invalidate(); void load(true); }}>{t("workspace-naming.reload")}</Button>}</>}
      {!loaded && !busy && <Button tone="quiet" onClick={() => void load()}>{t("task-repositories.retry")}</Button>}
    </div>}
  </div>;
}
