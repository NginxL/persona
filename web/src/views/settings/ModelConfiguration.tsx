import { useEffect, useId, useMemo, useRef, useState } from "react";
import { api, ApiError, enc, errorText } from "../../lib/api";
import { extRoot } from "../../lib/extension-api";
import {
  providerAgent, providerName, modelPurpose, modelSummary, runtimeLabel, usageTokens,
  type ModelBinding, type ModelConfigurationBody, type ModelDraft, type ModelProvider, type ModelPurpose, type ModelPurposeBody,
} from "../../lib/model-config";
import {
  agentsOverride, catalogLists, chooseProvider, claudeCodeOnly, configDirChoices, configDirLabel, draftFor, effortOffered, executionForm, followedCategory, modelConfiguration, modelPatch, overridden,
  ownRecord, parentCategory, providersFor, purposeDescription, purposeGroups, purposeLaunch, resetPatch, runtimeChoices, saveModelPurpose, supportsField, withPurposeAfter,
  withUsesAfterCategories,
} from "../../lib/model-config-execution";
import { ASSISTANT_MODEL_PICK_FEATURE, mainChatAgentMismatch } from "../../lib/assistant-models";
import { SUPERVISE_PURPOSE } from "../../lib/background-supervisor";
import type { AssistantLaunchers } from "../../lib/session-models";
import { effortChoices, providerModels, takesEffort } from "../../lib/launcher-models";
import { useStore } from "../../lib/store";
import { formatTokens } from "../../lib/tokens";
import { formatNumber, t } from "../../i18n";
import { Button, Dialog, IconButton, Loading, toast } from "../../ui/atoms";
import { Segmented } from "../assistant/parts";
import { Icon } from "../../ui/icons";
import { ModelPicker, type ModelList } from "../../ui/ModelPicker";
import { Group, Pill, Row } from "./SettingsParts";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import { ConfigurationManager } from "./ConfigurationManager";
import { DirectAPIEditor, DirectEffortSelect, directEffortState } from "./DirectAPIEditor";
import { ModelProfiles } from "./ModelProfiles";

const UNVERIFIED_STATUS = "未验证"; // i18n-ignore: stable wire sentinel, not user-facing text.

function agentsOverrideStatus(binding: ModelBinding): string {
  return overridden(binding)
    ? t("settings-modelconfiguration.separately_configured")
    : t("settings-modelconfiguration.follow_agents");
}

/** A use set apart from its category (#7980): **Separately configured** with anything of its own, else **Follows <the
 *  category>**, the words the background supervisor's row has, with the category's name from the catalog. */
function categoryStatus(binding: ModelBinding, category: ModelPurpose): string {
  return overridden(binding)
    ? t("settings-modelconfiguration.separately_configured")
    : t("settings-modelconfiguration.follows", { name: category.name });
}

export function ModelConfiguration({ open, onOpen, onProviders }: { open: string | null; onOpen: (id: string | null) => void; onProviders: () => void }) {
  return <ConfigurationManager>{(manager) => <ModelConfigurationForManager manager={manager} open={open} onOpen={onOpen} onProviders={onProviders} />}</ConfigurationManager>;
}

function ModelConfigurationForManager({ manager, open, onOpen, onProviders }: { manager: string; open: string | null; onOpen: (id: string | null) => void; onProviders: () => void }) {
  const [data, setData] = useState<ModelConfigurationBody | null>(null);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  // Each load's number: only the latest one's answer is shown, so an older GET still on its way (the first one, a
  // Refresh) cannot put back what a save's reload has just replaced.
  const loads = useRef(0);
  const load = async () => {
    const at = ++loads.current;
    try {
      // the background supervisor's purpose is listed right after Main chat (#5082). A hub since #5806 has it in the
      // catalog; one before that answers it by its own id only, so only then is it asked for. There, only a hub that
      // says it has none (404) lists none: any other failure is the page's error, with its retry, not a missing row
      const absent = (e: unknown) => { if (e instanceof ApiError && e.status === 404) return undefined; throw e; };
      const catalog = await modelConfiguration(manager);
      const supervise = catalogLists(catalog, SUPERVISE_PURPOSE) ? undefined : await modelPurpose(SUPERVISE_PURPOSE, manager).catch(absent);
      if (at !== loads.current) return;
      // each use set apart from a category right after it (#7980): the In-meeting conversation after the supervisor,
      // Meeting minutes after Information processing
      setData(withUsesAfterCategories(withPurposeAfter(catalog, SUPERVISE_PURPOSE, "assistant", supervise?.purpose)));
      setError("");
    } catch (e) {
      if (at !== loads.current) return;
      setError(e instanceof ApiError && e.status === 404 ? t("settings-modelconfiguration.unsupported") : errorText(e));
    }
  };
  useEffect(() => { void load(); }, []);
  const groups = useMemo(() => purposeGroups(data?.purposes ?? [], query, data?.providers), [data, query]);
  const sourceName = (id?: string) => data?.purposes.find((p) => p.id === id)?.name ?? id;
  const categoryOf = (p: ModelPurpose) => data ? followedCategory(p, data.purposes) : undefined;

  return (
    <>
      <h2 className="mb-3 px-1 text-[15px] font-semibold text-ink">{t("settings-nav.tab_model_config")}</h2>
      <ModelProfiles manager={manager} providers={data?.providers} onChanged={() => void load()} />
      <div className="mb-4 flex items-center gap-2">
        <label className="flex h-9 min-w-0 flex-1 items-center gap-2 rounded-control bg-surface px-3 shadow-btn focus-within:shadow-[inset_0_0_0_1px_var(--accent)]">
          <Icon name="search" size={13} stroke={2} className="text-ink-3" />
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t("settings-modelconfiguration.search_placeholder")} aria-label={t("settings-modelconfiguration.search_label")}
            className="min-w-0 flex-1 bg-transparent text-[13px] text-ink outline-none placeholder:text-ink-3" />
          {query && <IconButton icon="x" label={t("settings-modelconfiguration.clear")} size={22} iconSize={12} onClick={() => setQuery("")} />}
        </label>
        <IconButton icon="retry" label={t("settings-modelconfiguration.refresh")} onClick={() => void load()} />
      </div>
      <p className="mb-5 px-1 text-[12px] leading-relaxed text-ink-3">{t("settings-modelconfiguration.intro")}</p>
      {!data && !error && <Loading />}
      {error && <p role="alert" className="mb-4 text-[13px] text-red">{error}</p>}
      {data && groups.length === 0 && <p className="py-10 text-center text-[13px] text-ink-3">{query ? t("settings-modelconfiguration.no_match", { query }) : t("settings-modelconfiguration.no_purposes")}</p>}
      {groups.map(([group, purposes]) => (
        <Group key={group} title={group} footer={purposes.some((p) => p.id === "assistant") ? t("settings-modelconfiguration.schedule_default") : undefined}>
          {purposes.map((p) => (
            <PurposeRow key={p.id} purpose={p} providers={data?.providers ?? []} sourceName={sourceName} category={categoryOf(p)} onOpen={() => onOpen(p.id)} />
          ))}
        </Group>
      ))}
      <Button tone="quiet" onClick={onProviders}>{t("settings-modelconfiguration.manage_providers")}</Button>
      <PurposeEditor manager={manager} key={open} id={open} sourceName={sourceName} catalog={data?.purposes} onClose={() => onOpen(null)} onSaved={() => void load()} />
    </>
  );
}

/** A sibling help button keeps the editing row a single, unnested button.
 * Details expand inside the card so they stay reachable on narrow screens and at larger text sizes. */
function PurposeRow({ purpose: p, providers, sourceName, category, onOpen }: { purpose: ModelPurpose; providers: ModelProvider[]; sourceName: (id?: string) => string | undefined; category?: ModelPurpose; onOpen: () => void }) {
  const machines = useStore((s) => s.machines);
  const form = executionForm(p, providers, machines);
  const directAPI = p.kind === "api" && !ownRecord(p) && providers.find((v) => v.id === p.effective.provider)?.kind === "api";
  const status = directAPI && p.status === UNVERIFIED_STATUS ? "" : p.status;
  const [help, setHelp] = useState(false);
  const [pinned, setPinned] = useState(false);
  const row = useRef<HTMLDivElement>(null);
  const helpID = useId();
  useEffect(() => {
    if (!help) return;
    const close = (event: PointerEvent) => {
      if (!row.current?.contains(event.target as Node)) { setHelp(false); setPinned(false); }
    };
    document.addEventListener("pointerdown", close);
    return () => document.removeEventListener("pointerdown", close);
  }, [help]);
  const inheritance = agentsOverride(p) ? (overridden(p.override) ? agentsOverrideStatus(p.override) : t("settings-modelconfiguration.lark_headless_inheritance"))
    : category ? categoryStatus(p.override, category)
    : p.inherited_from ? t("settings-modelconfiguration.follows", { name: sourceName(p.inherited_from) ?? "" })
    : !p.status ? t("settings-modelconfiguration.custom") : "";
  const description = purposeDescription(p);
  // the last 7 days' calls and tokens (#10498): which tokens, and the rest, in the line's tooltip and the help below
  const usage = p.usage?.calls ? p.usage : undefined;
  const usageDetail = usage ? t("settings-modelconfiguration.usage_detail", {
    input: formatTokens(usage.input_tokens), output: formatTokens(usage.output_tokens), cache_write: formatTokens(usage.cache_creation_input_tokens),
    cache_read: formatTokens(usage.cache_read_input_tokens), failures: formatNumber(usage.failures),
  }) : "";
  return <div ref={row} data-model-purpose={p.id} onMouseLeave={() => { if (!pinned) setHelp(false); }}
    onKeyDown={(event) => { if (event.key === "Escape") { setHelp(false); setPinned(false); event.stopPropagation(); } }}>
    <div className="flex items-center pr-1">
      <div className="min-w-0 flex-1">
        <Row label={p.id === "agent.new" ? t("newagent.title") : p.name} onClick={onOpen} wrapHint hint={<>
          {/* what the purpose is for, right under its name (#5806); the model and how it runs follow */}
          {description && <div data-testid="purpose-description" className="mb-1 whitespace-normal">{description}</div>}
          <div className="truncate">{modelSummary(p, providers)}</div>
          <div className="mt-1 flex flex-wrap items-center gap-1.5">
            <Pill wrap>{form.label} · {form.backend}</Pill>
            {inheritance && <span className="whitespace-normal">{inheritance}</span>}
            {status && <span>{status}</span>}
          </div>
          {usage && <div data-testid="purpose-usage" className="mt-1 truncate" title={usageDetail}>
            {t("settings-modelconfiguration.usage", { count: usage.calls, calls: formatNumber(usage.calls), tokens: formatTokens(usageTokens(usage)) })}
          </div>}
        </>} />
      </div>
      <button type="button" className="flex size-11 shrink-0 items-center justify-center rounded-control text-ink-3 hover:bg-hover focus-visible:outline-accent"
        aria-label={t("settings-modelconfiguration.execution_help", { name: p.name })} aria-expanded={help} aria-controls={helpID}
        onMouseEnter={() => setHelp(true)} onFocus={() => setHelp(true)}
        onBlur={(event) => { if (!pinned && !row.current?.contains(event.relatedTarget as Node)) setHelp(false); }}
        onClick={() => { setPinned(!pinned); setHelp(!pinned); }}>
        <span aria-hidden className="flex size-5 items-center justify-center rounded-full bg-inset text-[12px] shadow-hairline">?</span>
      </button>
    </div>
    {help && <div id={helpID} role="region" aria-label={t("settings-modelconfiguration.execution_help", { name: p.name })}
      className="border-t border-line bg-inset px-4 py-3 text-[12px] leading-relaxed text-ink-2">
      <p>{t(`settings-modelconfiguration.execution_${purposeLaunch(p) || p.kind}`)}</p>
      <p className="mt-2 font-medium">{t("settings-modelconfiguration.execution_current", { backend: form.backend, provider: providerName(p.effective?.provider, providers) ?? t("settings-modelconfiguration.not_configured") })}</p>
      {p.kind === "launcher" && <p className="mt-2 text-ink-3">{t("settings-modelconfiguration.execution_unverified")}</p>}
      {usage && <p className="mt-2">{usageDetail}</p>}
    </div>}
  </div>;
}

const inputClass = "h-9 w-full rounded-control bg-field px-3 text-[13px] text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

function PurposeEditor({ manager, id, sourceName, catalog, onClose, onSaved }: {
  manager: string; id: string | null; sourceName: (id?: string) => string | undefined;
  /** The page's catalog, when it loaded: the category a use follows, its name and its binding. */
  catalog?: ModelPurpose[]; onClose: () => void; onSaved: () => void;
}) {
  const alive = useRef(true);
  const loads = useRef(0);
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  const [data, setData] = useState<ModelPurposeBody | null>(null);
  const [linkedParent, setLinkedParent] = useState<ModelPurpose>();
  const [draft, setDraft] = useState<ModelDraft>({ provider: "", model: "", effort: "", runtime: "" });
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const [busy, setBusy] = useState(false);
  const [models, setModels] = useState<{ id: string; name?: string }[]>([]);
  const [modelsError, setModelsError] = useState("");
  // A launcher provider's /model list (#943), from a machine that has it (`machine`), and the picker over it.
  const [launcherList, setLauncherList] = useState<ModelList & { machine?: string; provider?: string }>({ status: "idle" });
  const [picking, setPicking] = useState(false);
  const [query, setQuery] = useState("");
  // "Retry" / "Reload": the next load asks the CLI again (refresh) and keeps showing the list meanwhile, once.
  const [reload, setReload] = useState(0);
  const refreshNext = useRef(false);
  const search = useRef<HTMLInputElement>(null);
  const machines = useStore((s) => s.machines);
  const mainSession = useStore((s) => {
    const a = s.assistant;
    if (!a?.main) return undefined;
    return (s.sessions[a.machine_id] ?? []).find((row) => row.session_id === a.main)
      ?? a.conversations.find((row) => row.machine_id === a.machine_id && row.session_id === a.main);
  });
  const kept = useStore((s) => s.features.includes(ASSISTANT_MODEL_PICK_FEATURE));
  const load = async () => {
    if (!id) return;
    const at = ++loads.current;
    try {
      const r = await modelPurpose(id, manager);
      if (!alive.current || at !== loads.current) return;
      // A direct link can outlive the catalog request. A saved model override hides the parent model that clearing it
      // will inherit, so load that parent before offering model-specific efforts; failure keeps the retry visible.
      const parent = !catalog && r.purpose.override.model && r.purpose.parent
        ? (await modelPurpose(r.purpose.parent, manager)).purpose : undefined;
      if (!alive.current || at !== loads.current) return;
      setLinkedParent(parent);
      setData(r);
      setDraft(draftFor(r.purpose));
      setError("");
      setConflict(false);
    } catch (e) {
      if (!alive.current || at !== loads.current) return;
      setError(errorText(e));
    }
  };
  useEffect(() => { void load(); }, [id]);
  useEffect(() => {
    if (!id?.startsWith("plugin.")) return;
    const controller = new AbortController();
    api<{ models: { id: string; name?: string }[] }>(`${extRoot()}/${enc(id.slice("plugin.".length))}/models`, { signal: controller.signal, headers: { [MANAGER_HEADER]: manager } })
      .then((r) => setModels(r.models ?? []))
      .catch((e) => { if (!controller.signal.aborted) setModelsError(errorText(e)); });
    return () => controller.abort();
  }, [id]);

  const p = data?.purpose;
  const candidates = p ? providersFor(p, data.providers) : [];
  // The draft names no provider of its own: once saved it runs on what it follows. While nothing is overridden that is
  // what is in effect; with an override, the binding of the category a use with a record of its own follows (from the
  // page's catalog), as the reset makes it (#7980). Without that, what is in effect, as before.
  const parent = (p && catalog ? parentCategory(p, catalog) : undefined)
    ?? (linkedParent?.id === p?.parent ? linkedParent : undefined);
  const following = !!p && overridden(p.override) && !!parent;
  const runsOn = draft.provider || (following ? parent?.effective?.provider : p?.effective?.provider);
  const chosen = data?.providers.find((v) => v.id === runsOn);
  const followsAgents = !!p && agentsOverride(p);
  // a use set apart from its category (#7980): follows it by name, and goes back to it
  const category = p && !followsAgents && catalog ? followedCategory(p, catalog) : undefined;
  const follow = followsAgents ? t("settings-modelconfiguration.follow_agents") : category ? t("settings-modelconfiguration.follow_category", { name: category.name }) : "";
  const onlyClaudeID = useId();
  const writable = p && (["provider", "model", "effort"] as const).some((field) => supportsField(p, field));
  // The Direct API editor (model discovery, efforts by model, draft test: #6887) is for a direct API category's own
  // binding. A use with a record of its own under a category (ownRecord: Meeting minutes, an extension's purpose) keeps
  // the shared editor, whose empty draft follows that category's binding and whose effort goes by effortOffered (#7980).
  const directAPI = !!p && p.kind === "api" && !ownRecord(p) && chosen?.kind === "api" && writable;
  const effectiveProvider = data?.providers.find((v) => v.id === p?.effective?.provider);
  // by the provider the draft runs on, not only by the fields the hub sent for the one in effect
  const effortAllowed = !!p && effortOffered(p, chosen, effectiveProvider, followsAgents,
    !!data?.providers.some((provider) => provider.chat_effort === true));
  const listProvider = p?.kind !== "plugin" && chosen?.kind === "launcher" && supportsField(p!, "model") ? chosen.id : "";
  useEffect(() => {
    const refresh = refreshNext.current;
    refreshNext.current = false;
    if (!refresh) { setPicking(false); setQuery(""); } // another provider: start closed; a refresh keeps the list open
    if (!listProvider) { setLauncherList({ status: "idle" }); return; }
    const controller = new AbortController();
    // Another provider's list never stays up while this one's loads (a pick would be the other provider's id).
    setLauncherList((l) => ({ status: "loading", provider: listProvider, list: refresh && l.provider === listProvider ? l.list : undefined }));
    providerModels(listProvider, { manager, refresh, machine: p?.machine_id, signal: controller.signal })
      .then((list) => setLauncherList({ status: "ready", list, machine: list?.machine, provider: listProvider }))
      .catch((e) => { if (!controller.signal.aborted) setLauncherList({ status: "error", error: errorText(e), provider: listProvider }); });
    return () => controller.abort();
  }, [listProvider, reload, p?.machine_id]);
  const reloadList = () => { refreshNext.current = true; setReload((n) => n + 1); };
  // The picker while there is a list (or one coming); the plain field without one: an older hub, a failure.
  const picker = launcherList.status === "loading" || (launcherList.status === "ready" && !!launcherList.list?.models.length);
  // Left empty, the model is inherited while the provider is too (what runs now when nothing here is set; with an
  // override, the category's); with a provider picked here it is that provider's default (the list's "Default").
  // An own record that names its parent's provider still inherits an empty model or effort from that parent. Another
  // provider starts from its own default. Use the current draft, not the saved override being cleared.
  const sameParentProvider = !draft.provider || draft.provider === parent?.effective?.provider;
  const sameEffectiveProvider = !draft.provider || draft.provider === p?.effective?.provider;
  const inheritedModel = following
    ? sameParentProvider ? parent?.effective?.model : undefined
    : sameEffectiveProvider && !p?.override?.model ? p?.effective?.model : undefined;
  const inherited: ModelBinding = inheritedModel ? { model: inheritedModel } : {};
  const effortModel = draft.model.trim() || inheritedModel || chosen?.model || p?.effective?.model || "";
  const inheritedEffort = following
    ? sameParentProvider ? parent?.effective?.effort : undefined
    : sameEffectiveProvider && !p?.override.effort ? p?.effective?.effort : undefined;
  const chatEffort = !directAPI && chosen?.kind === "api" && chosen.api === "chat"
    ? directEffortState(chosen, [], effortModel) : undefined;
  const listedEfforts = launcherList.status === "ready" && launcherList.list && takesEffort(launcherList.list.agent)
    ? effortChoices(launcherList.list, draft.model, inherited) : (p?.id === "inbox.triage" || p?.kind === "api" ? ["off", "low", "medium", "high", "xhigh", "max"] : ["low", "medium", "high", "xhigh", "max"]);
  const effectiveAgent = providerAgent(effectiveProvider, p?.effective?.provider);
  const chosenAgent = (launcherList.provider === listProvider && launcherList.list?.agent) || providerAgent(chosen, runsOn);
  const mainDefaultProvider = draft.provider || p?.effective?.provider;
  const mainInfo: AssistantLaunchers = {
    launchers: (data?.providers ?? []).map((v) => ({ name: v.id, display_name: v.name, agent: providerAgent(v) })),
    def: mainDefaultProvider ? { provider: mainDefaultProvider } : undefined,
    unavailable: [],
  };
  const mainMismatch = p?.id === "assistant"
    ? mainChatAgentMismatch({ session: mainSession, isMain: true, kept, config: mainDefaultProvider }, mainInfo)
    : undefined;
  const mainLauncherName = (launcher: string) => {
    const all = data?.providers ?? [];
    // A session names its launcher; Model configuration prefixes launcher providers. Prefer an exact managed-provider id.
    return (all.find((v) => v.id === launcher) ?? all.find((v) => v.id === `launcher.${launcher}`))?.name ?? launcher;
  };
  const configuredMainName = mainMismatch ? mainLauncherName(mainMismatch.configuredLauncher) : "";
  const actualMainName = mainMismatch ? mainLauncherName(mainMismatch.actualLauncher) : "";
  const runtimes = p ? runtimeChoices(p, chosenAgent) : [];
  const configDirs = p ? configDirChoices(p, chosenAgent) : [];
  const previewForm = p ? executionForm({ ...p, effective: { ...p.effective, provider: runsOn, runtime: draft.runtime || p.effective?.runtime } }, data?.providers, machines) : undefined;
  const listMachine = machines.find((m) => m.id === launcherList.machine)?.name ?? launcherList.machine;
  const save = async (restore = false) => {
    if (!p || !writable || busy || conflict) return;
    setBusy(true);
    setError("");
    try {
      // one patch: the reset's, keeping the runtime and the profile (a use with a record of its own clears all three
      // whatever this page grouped it under),
      // else the draft's fields, effort with them where the provider it runs on takes one
      const patch = restore ? resetPatch(p, draft.runtime, draft.config_dir) : modelPatch(p, draft, effortAllowed);
      const r = await saveModelPurpose(p, patch, manager);
      if (!alive.current) return;
      setData(r);
      setDraft(draftFor(r.purpose));
      toast(restore ? t("settings-modelconfiguration.restored") : t("settings-modelconfiguration.saved"), "ok");
      onSaved();
      onClose();
    } catch (e) {
      const changed = e instanceof ApiError && e.status === 409;
      setConflict(changed);
      setError(changed ? t("settings-modelconfiguration.conflict") : errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const reset = <Button tone="quiet" disabled={busy || !writable || conflict} onClick={() => void save(true)}>{follow || t("settings-modelconfiguration.restore")}</Button>;
  const actions = <>
    <Button tone="quiet" disabled={busy} onClick={onClose}>{t("common.cancel")}</Button>
    <Button tone="primary" busy={busy} disabled={!writable || conflict} onClick={() => void save()}>{t("common.save")}</Button>
  </>;
  return (
    <Dialog open={id !== null} onClose={() => { if (!busy) onClose(); }} width={480} title={p?.name ?? t("settings-modelconfiguration.dialog_title")}
      footer={category
        // "Follow Information processing" (#7980) is longer than "Inherit": where the three do not fit on one line (a
        // phone, Large text) the reset takes a line of its own above Cancel and Save, its words whole (labels never wrap)
        ? <div data-editor-footer className="flex min-w-0 flex-1 flex-wrap items-center gap-2">{reset}<div className="ml-auto flex gap-2">{actions}</div></div>
        : <>{reset}<span className="flex-1" />{actions}</>}
    >
      {!p && !error && <Loading />}
      {p && <div className="flex flex-col gap-4">
        {followsAgents && <p className="text-[12px] text-ink-3">{agentsOverrideStatus(p.override)}</p>}
        {category && <p className="text-label text-ink-3">{categoryStatus(p.override, category)}</p>}
        <Group title={t("settings-modelconfiguration.effective")} footer={p.inherited_from ? t("settings-modelconfiguration.inherited_from", { name: sourceName(p.inherited_from) ?? "" }) : t("settings-modelconfiguration.effective_footer")}>
          <Row label={t("settings-modelconfiguration.provider")} mono><span className="max-w-[220px] truncate">{providerName(p.effective?.provider, data?.providers) ?? t("settings-modelconfiguration.not_configured")}</span></Row>
          <Row label={t("settings-modelconfiguration.model")} mono><span className="max-w-[220px] truncate">{p.effective?.model || t("settings-modelconfiguration.provider_default")}</span></Row>
          {runtimes.length > 0 && <Row label={t("settings-modelconfiguration.runtime")}><span>{runtimeLabel(p.effective?.runtime, effectiveAgent)}</span></Row>}
          {configDirChoices(p, effectiveAgent).length > 0 && <Row label={t("settings-modelconfiguration.config_dir")}><span>{configDirLabel(p.effective?.config_dir)}</span></Row>}
          {p.status && !(directAPI && p.status === UNVERIFIED_STATUS) && <Row label={t("settings-modelconfiguration.status")}><Pill>{p.status}</Pill></Row>}
        </Group>
        {supportsField(p, "provider") && <div>
          <label className="block">
            <span className="mb-1 block text-[12px] font-medium text-ink-2">{t("settings-modelconfiguration.model_provider")}</span>
            <select className={inputClass} value={draft.provider} disabled={busy} onChange={(e) => setDraft((d) => chooseProvider(d, e.target.value))} aria-describedby={claudeCodeOnly(p) ? onlyClaudeID : undefined}>
              <option value="">{follow || t("settings-modelconfiguration.follow_default")}</option>
              {draft.provider && !candidates.some((v) => v.id === draft.provider) && <option value={draft.provider}>{draft.provider.startsWith("legacy.") ? t("settings-modelconfiguration.not_configured") : t("settings-modelconfiguration.provider_unavailable", { id: providerName(draft.provider, data?.providers) ?? draft.provider })}</option>}
              {candidates.map((v) => <option key={v.id} value={v.id}>{v.hidden ? t("settings-modelconfiguration.provider_hidden", { name: v.name }) : v.name}</option>)}
            </select>
          </label>
          {/* the In-meeting conversation: why only these providers are offered (#7980) */}
          {claudeCodeOnly(p) && <span id={onlyClaudeID} data-testid="claude-code-only" className="mt-1 block text-submeta text-ink-3">{t("settings-modelconfiguration.claude_code_only")}</span>}
        </div>}
        {previewForm && <div data-execution-preview><Pill wrap>{previewForm.label} · {previewForm.backend}</Pill></div>}
        {directAPI && <DirectAPIEditor key={`${manager}:${chosen.id}:${draft.provider}`} manager={manager} purpose={p} provider={chosen} draft={draft}
          disabled={busy} effortAllowed={effortAllowed} onChange={(patch) => setDraft((value) => ({ ...value, ...patch }))} />}
        {!directAPI && supportsField(p, "model") && <div className="block">
          <span className="mb-1 block text-[12px] font-medium text-ink-2">{t("settings-modelconfiguration.specific_model")}</span>
          {p.kind === "plugin" ? <select aria-label={t("settings-modelconfiguration.specific_model")} className={inputClass} value={draft.model} disabled={busy || (!!modelsError && models.length === 0)} onChange={(e) => setDraft((d) => ({ ...d, model: e.target.value }))}>
            <option value="">{t("settings-modelconfiguration.follow_extension_default")}</option>
            {draft.model && !models.some((m) => m.id === draft.model) && <option value={draft.model}>{t("settings-modelconfiguration.model_current", { id: draft.model })}</option>}
            {models.map((m) => <option key={m.id} value={m.id}>{m.name || m.id}</option>)}
          </select> : picker ? <div inert={busy}><ModelPicker
            models={launcherList}
            model={draft.model}
            followed={inherited}
            open={picking}
            onToggle={() => setPicking((o) => !o)}
            query={query}
            onQuery={setQuery}
            search={search}
            onPick={(id) => {
              setDraft((d) => ({ ...d, model: id }));
              setPicking(false);
              setQuery("");
            }}
            onReload={reloadList}
            custom
            failedNote={t("settings-modelconfiguration.list_failed")}
          /></div> : <input aria-label={t("settings-modelconfiguration.specific_model")} className={`${inputClass} font-mono`} value={draft.model} disabled={busy}
            placeholder={(following && inherited.model) || chosen?.model || p.effective?.model || t("settings-modelconfiguration.follow_default")}
            spellCheck={false} autoComplete="off" onChange={(e) => setDraft((d) => ({ ...d, model: e.target.value }))} />}
          <span className="mt-1 block text-[11px] text-ink-3">
            {!picker ? t("settings-modelconfiguration.field_hint")
              : listMachine ? (draft.provider ? t("settings-modelconfiguration.picker_hint_provider_machine", { machine: listMachine }) : t("settings-modelconfiguration.picker_hint_inherit_machine", { machine: listMachine }))
              : draft.provider ? t("settings-modelconfiguration.picker_hint_provider") : t("settings-modelconfiguration.picker_hint_inherit")}
          </span>
          {modelsError && <span role="alert" className="mt-1 block text-[11px] text-red">{t("settings-modelconfiguration.extension_list_error", { error: modelsError })}</span>}
          {launcherList.status === "error" && (
            <span role="alert" className="mt-1 flex items-center gap-2 text-[11px] text-ink-3">
              <span className="min-w-0 truncate" title={launcherList.error}>{t("settings-modelconfiguration.list_error", { error: launcherList.error ?? "" })}</span>
              <button type="button" onClick={reloadList} className="shrink-0 font-medium whitespace-nowrap text-accent-ink hover:underline">{t("settings-modelconfiguration.retry")}</button>
            </span>
          )}
        </div>}
        {!directAPI && effortAllowed && (chatEffort ? <DirectEffortSelect state={chatEffort} value={draft.effort} inherited={inheritedEffort} disabled={busy}
          onChange={(effort) => setDraft((d) => ({ ...d, effort }))} /> : <label className="block">
          <span className="mb-1 block text-[12px] font-medium text-ink-2">{t("settings-modelconfiguration.effort")}</span>
          <input className={inputClass} value={draft.effort} disabled={busy} placeholder={(following && !draft.provider ? parent?.effective?.effort : p.effective?.effort) || t("settings-modelconfiguration.follow_default")} list="model-efforts"
            onChange={(e) => setDraft((d) => ({ ...d, effort: e.target.value }))} />
          <datalist id="model-efforts">{listedEfforts.map((v) => <option key={v} value={v} />)}</datalist>
        </label>)}
        {runtimes.length > 0 && <div className="block">
          <span className="mb-1 block text-[12px] font-medium text-ink-2">{t("settings-modelconfiguration.runtime")}</span>
          <Segmented label={t("settings-modelconfiguration.runtime")} value={chosenAgent === "claude" ? draft.runtime : "cli"} options={runtimes.map((r) => ({ v: r.id, label: r.label }))}
            onChange={(runtime) => { if (!busy && chosenAgent === "claude") setDraft((d) => ({ ...d, runtime })); }} />
          {chosenAgent === "claude" && <span className="mt-1 block text-[11px] text-ink-3">{t("settings-modelconfiguration.runtime_hint")}</span>}
        </div>}
        {mainMismatch && <p data-main-chat-agent-setting className="rounded-control bg-inset px-3 py-2 text-xs leading-relaxed text-ink-2">
          {t("settings-modelconfiguration.main_other_kind", { configured: configuredMainName, actual: actualMainName })}
        </p>}
        {configDirs.length > 0 && <div className="block">
          <span className="mb-1 block text-label font-medium text-ink-2">{t("settings-modelconfiguration.config_dir")}</span>
          <Segmented label={t("settings-modelconfiguration.config_dir")} value={draft.config_dir ?? "private"} options={configDirs.map((c) => ({ v: c.id, label: c.label }))}
            onChange={(config_dir) => { if (!busy) setDraft((d) => ({ ...d, config_dir })); }} />
          <span className="mt-1 block text-submeta text-ink-3">{t("settings-modelconfiguration.config_dir_hint")}</span>
        </div>}
        <p className="text-[11.5px] leading-relaxed text-ink-3">{p.kind === "plugin" ? t("settings-modelconfiguration.save_note_plugin") : t("settings-modelconfiguration.save_note")}</p>
      </div>}
      {error && <p role="alert" className="mt-3 text-[12px] text-red">{error}</p>}
      {(conflict || (!p && error)) && <Button tone="secondary" disabled={busy} onClick={() => void load()}>{conflict ? t("settings-modelconfiguration.reload_discard") : t("settings-modelconfiguration.retry")}</Button>}
    </Dialog>
  );
}
