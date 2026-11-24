// Task-local execution choices. Discovery uses the selected computer and the same launcher/model APIs as chat.
import { useEffect, useRef, useState } from "react";
import { errorText } from "../../lib/api";
import { modelPurpose, type ModelPurpose } from "../../lib/model-config";
import { launcherBindingState, useCanonicalLauncherBinding } from "../../lib/launcher-model-binding";
import { machineLaunchers, providerModels, takesEffort, type MachineLaunchers } from "../../lib/launcher-models";
import { launcherTitle } from "../../lib/provider-names";
import { useChosenManager, useKnownDefault } from "../../lib/manager-choice";
import { taskExecutionNeedsOwnChat, taskExecutionSummary, taskStartsFresh, type Task } from "../../lib/routines";
import { useStore } from "../../lib/store";
import { LauncherEffortSelect } from "../../ui/LauncherEffortSelect";
import { ModelPicker, type ModelList } from "../../ui/ModelPicker";
import { Button } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Field, inputClass } from "./parts";
import { t } from "../../i18n";

export function TaskExecutionFields({ draft, saved, disabled, onChange }: { draft: Task; saved?: Task | null; disabled: boolean; onChange: (patch: Partial<Task>) => void }) {
  const machines = useStore((s) => s.machines);
  const picked = useChosenManager();
  const knownDefault = useKnownDefault();
  const manager = picked ?? knownDefault;
  const availableMachines = machines.filter((m) => !m.manager || m.manager === manager);
  const [expanded, setExpanded] = useState(false);
  const [mainCatalog, setMainCatalog] = useState<{ key: string; purpose?: ModelPurpose; error?: string }>();
  const [launcherCatalog, setLauncherCatalog] = useState<{ key: string; list?: MachineLaunchers; error?: string }>();
  const [modelCatalog, setModelCatalog] = useState<{ key: string; models: ModelList }>();
  const [reload, setReload] = useState(0);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const search = useRef<HTMLInputElement>(null);
  const mainKey = JSON.stringify([manager, reload]);
  const main = mainCatalog?.key === mainKey ? mainCatalog.purpose : undefined;
  useEffect(() => {
    if (!manager) return;
    let live = true;
    modelPurpose("assistant", manager).then((r) => {
      if (live) setMainCatalog({ key: mainKey, purpose: r.purpose });
    }, (e) => { if (live) setMainCatalog({ key: mainKey, error: errorText(e) }); });
    return () => { live = false; };
  }, [manager, reload, mainKey]);
  const machine = draft.execution_machine_id || main?.machine_id || draft.execution?.machine_id || "";
  const launcherKey = JSON.stringify([manager, machine, reload]);
  const launchers = launcherCatalog?.key === launcherKey ? launcherCatalog.list : undefined;
  const problem = (mainCatalog?.key === mainKey && mainCatalog.error) || (launcherCatalog?.key === launcherKey && launcherCatalog.error) || "";
  const base = launchers?.default
    ? { ...launchers.default, provider: launchers.default.provider ? `launcher.${launchers.default.provider}` : "" }
    : machine === main?.machine_id ? main?.effective ?? {} : {};
  const inheritedLauncher = base.provider?.replace(/^launcher\./, "") || (draft.execution?.machine_id === machine ? draft.execution.launcher : "") || "";
  const launcher = draft.launcher || inheritedLauncher;
  const inheritsAssistant = !draft.launcher || draft.launcher === inheritedLauncher;
  const inherited = inheritsAssistant ? base : {};
  const modelKey = JSON.stringify([manager, machine, launcher, reload]);
  const models: ModelList = modelCatalog?.key === modelKey ? modelCatalog.models : { status: expanded ? "loading" : "idle" };
  const modelsPending = models.status === "idle" || models.status === "loading";
  useEffect(() => {
    if (!manager || !machine) return;
    const controller = new AbortController();
    machineLaunchers(machine, { purpose: "task", manager, signal: controller.signal }).then((r) => {
      if (!controller.signal.aborted) setLauncherCatalog({ key: launcherKey, list: r });
    }, (e) => { if (!controller.signal.aborted) setLauncherCatalog({ key: launcherKey, error: errorText(e) }); });
    return () => controller.abort();
  }, [manager, machine, reload, launcherKey]);
  useEffect(() => {
    setOpen(false); setQuery("");
    if (!expanded) return;
    if (!manager || !machine || !launcher) return;
    const controller = new AbortController();
    setModelCatalog({ key: modelKey, models: { status: "loading" } });
    providerModels(`launcher.${launcher}`, { manager, machine, signal: controller.signal, refresh: reload > 0 })
      .then((list) => { if (!controller.signal.aborted) setModelCatalog({ key: modelKey, models: { status: "ready", list } }); })
      .catch((e) => { if (!controller.signal.aborted) setModelCatalog({ key: modelKey, models: { status: "error", error: errorText(e) } }); });
    return () => controller.abort();
  }, [expanded, manager, machine, launcher, reload, modelKey]);
  const known = launchers?.launchers ?? [];
  const knownLauncher = known.find((l) => l.name === launcher);
  const agent = knownLauncher?.agent_type || knownLauncher?.agent || models.list?.agent || (draft.execution?.launcher === launcher ? draft.execution.agent : "");
  const binding = launcherBindingState(models.list, draft, inherited, {
    allowUnknown: !!models.list && (takesEffort(agent || models.list.agent, knownLauncher) || agent === "codex"),
  });
  // Source-chat tasks can keep historical metadata during unrelated edits (taskExecutionNeedsOwnChat).
  useCanonicalLauncherBinding(draft.session_id && !taskStartsFresh(draft) ? undefined : binding.legacy, onChange);
  const defaultLabel = t(inheritsAssistant ? "assistant-taskeditor.follow_assistant" : "assistant-taskeditor.follow_launcher");
  const summary = taskExecutionSummary({ launcher, agent, model: binding.effectiveModel, effort: binding.value || binding.defaultEffort }, known, defaultLabel);
  const incompatibleOrigin = taskExecutionNeedsOwnChat(draft, saved);
  return <details onToggle={(e) => setExpanded(e.currentTarget.open)} className="rounded-card bg-inset p-3 shadow-hairline">
    <summary className="flex cursor-pointer list-none items-center gap-2 text-meta font-medium text-ink [&::-webkit-details-marker]:hidden">
      <Icon name="chevron" size={13} className={expanded ? "shrink-0" : "shrink-0 -rotate-90"} />
      <span className="shrink-0">{t("assistant-taskeditor.execution_options")}</span>
      <span className="min-w-0 flex-1 truncate text-label font-normal text-ink-2" title={summary}>{summary}</span>
    </summary>
    <div className="mt-3 flex flex-col gap-3">
      <p className="text-submeta leading-relaxed text-ink-3">{t("assistant-taskeditor.execution_inheritance")}</p>
      <Field label={t("assistant-taskeditor.execution_computer")}>
        <select className={inputClass} value={draft.execution_machine_id ?? ""} disabled={disabled} onChange={(e) => onChange({ execution_machine_id: e.target.value })}>
          <option value="">{t("assistant-taskeditor.computer_default", { name: availableMachines.find((m) => m.id === main?.machine_id)?.name || main?.machine_id || "…" })}</option>
          {draft.execution_machine_id && !availableMachines.some((m) => m.id === draft.execution_machine_id) && <option value={draft.execution_machine_id}>{draft.execution_machine_id}</option>}
          {availableMachines.map((m) => <option key={m.id} value={m.id}>{m.name}{m.online ? "" : ` · ${t("assistant-taskeditor.computer_offline")}`}</option>)}
        </select>
      </Field>
      <Field label={t("assistant-taskeditor.execution_agent")}>
        <select className={inputClass} value={draft.launcher ?? ""} disabled={disabled} onChange={(e) => onChange({ launcher: e.target.value, model: "", effort: "" })}>
          <option value="">{t("assistant-taskeditor.agent_default", { name: launcherTitle(inheritedLauncher, known) || "…" })}</option>
          {draft.launcher && !known.some((l) => l.name === draft.launcher) && <option value={draft.launcher}>{draft.launcher} · {t("assistant-taskeditor.execution_unavailable")}</option>}
          {known.map((l) => <option key={l.name} value={l.name}>{launcherTitle(l.name, known)}{l.agent ? ` · ${l.agent}` : ""}</option>)}
        </select>
      </Field>
      <div>
        <span className="mb-1 block text-label font-medium text-ink-2">{t("settings-modelconfiguration.specific_model")}</span>
        <fieldset className="min-w-0" disabled={disabled || modelsPending}><ModelPicker models={models} model={binding.model} followed={inherited} defaultLabel={defaultLabel} ariaLabel={t("settings-modelconfiguration.specific_model")} open={open} onToggle={() => setOpen((v) => !v)} query={query} onQuery={setQuery} search={search}
          onPick={(model) => { if (!disabled && !modelsPending) { onChange(binding.pickModel(model)); setOpen(false); setQuery(""); } }} onReload={() => setReload((v) => v + 1)} custom failedNote={t("settings-modelconfiguration.list_failed")} /></fieldset>
        {models.status === "error" && <p role="alert" className="mt-1 break-words text-label text-red">{models.error}</p>}
      </div>
      <LauncherEffortSelect {...binding} className={inputClass} defaultLabel={defaultLabel} disabled={disabled || modelsPending}
        onChange={(effort) => onChange(binding.pickEffort(effort))} />
      <Field label={t("assistant-taskeditor.execution_directory")} note={t("assistant-taskeditor.directory_hint")}>
        <input className={inputClass} value={draft.cwd ?? ""} disabled={disabled} placeholder={t("assistant-taskeditor.directory_follow")} spellCheck={false} autoComplete="off" onChange={(e) => onChange({ cwd: e.target.value })} />
      </Field>
      <p className="text-submeta leading-relaxed text-ink-3">{t("assistant-taskeditor.execution_sessions")}</p>
      {incompatibleOrigin && <p role="alert" className="text-label text-red">{t("assistant-taskeditor.execution_own_chat")}</p>}
      {(problem || draft.execution_error) && <div role="alert" className="text-label text-red">{problem || draft.execution_error}<Button tone="quiet" onClick={() => setReload((v) => v + 1)}>{t("settings-modelconfiguration.retry")}</Button></div>}
    </div>
  </details>;
}
