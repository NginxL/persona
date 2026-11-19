import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, ApiError, errorText, HubTimeoutError } from "../../lib/api";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import type { ModelDraft, ModelProvider, ModelPurpose } from "../../lib/model-config";
import { supportsField } from "../../lib/model-config-execution";
import { EFFORT_LEVELS, type ProviderModel, type TestResult } from "../../lib/providers";
import { effortTitle } from "../../lib/session-actions";
import { Button } from "../../ui/atoms";

const inputClass = "h-9 w-full rounded-control bg-field px-3 text-meta text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";
type ProbeResult = TestResult & { effort?: string };
type Failure = { kind: "auth" | "model" | "timeout" | "other" | "effort"; detail?: string };

function directCapabilityModel(provider: ModelProvider, model: string) {
  const alias = model.trim().toLowerCase().replace(/\[1m\]$/, "");
  const mapped = provider.api === "chat" && provider.model_aliases && Object.prototype.hasOwnProperty.call(provider.model_aliases, alias)
    ? provider.model_aliases[alias] : undefined;
  return mapped ?? model;
}

export function directEffortState(provider: ModelProvider, discovered: ProviderModel[], model: string) {
  const chat = provider.api === "chat";
  const capabilityModel = directCapabilityModel(provider, model);
  const selected = discovered.find((item) => item.id === capabilityModel);
  const stored = provider.models?.find((item) => item.id === capabilityModel);
  // Chat requests and named probes apply stored capabilities. Discovery is the fallback when no entry is stored.
  const capability = chat ? stored ?? selected : selected;
  const exact = chat && !!capability?.all_efforts?.length;
  const reported = exact ? capability?.all_efforts : capability?.efforts;
  // Purpose Save accepts proto.EffortLevels plus off/none. Chat's all_efforts is its exact runtime capability list.
  const reportedEfforts = chat && reported ? [...new Set(reported.flatMap((value) =>
    value === "none" || value === "off" ? ["off"] : EFFORT_LEVELS.includes(value) ? [value] : []))] : reported;
  // Chat reports an empty list as unknown; only no_effort explicitly rules the control out.
  const unsupported = chat ? capability?.no_effort === true : selected?.efforts?.length === 0;
  const unavailable = chat && !unsupported && !!reported?.length && !reportedEfforts?.length;
  // Efforts-only Chat metadata keeps Off as a separate runtime control.
  const reportedOptions = chat && reported?.length && !exact && !reportedEfforts?.includes("off")
    ? ["off", ...(reportedEfforts ?? [])] : reportedEfforts;
  const fallback = provider.api === "responses"
    ? ["off", "minimal", ...EFFORT_LEVELS, "ultra"] : ["off", ...EFFORT_LEVELS];
  return {
    efforts: unsupported || unavailable ? [] : reported?.length ? reportedOptions ?? [] : fallback,
    unsupported, unavailable, reported: !!reported?.length,
    noEffort: chat && capability?.no_effort === true,
  };
}

/** The echo that proves the requested effort reached the model. Undefined means no effort was requested; an empty
 * string means stored capabilities deliberately suppress it. Discovery cannot prove suppression on an older hub. */
export function expectedDirectProbeEffort(provider: ModelProvider, model: string, requested: string): string | undefined {
  const effort = requested || (provider.api === "chat" ? provider.effort?.trim() ?? "" : "");
  if (!effort) return undefined;
  if (provider.api !== "chat") return effort;
  const capabilityModel = directCapabilityModel(provider, model);
  const stored = provider.models?.find((item) => item.id === capabilityModel);
  if (!stored) return effort;
  if (stored.no_effort) return "";
  const exact = !!stored.all_efforts?.length;
  const levels = exact ? stored.all_efforts : stored.efforts;
  if (!levels?.length) return effort;
  let wanted = effort;
  if (["off", "none", "disabled"].includes(effort.trim().toLowerCase())) {
    if (!exact) return effort;
    wanted = "none";
  }
  // Keep in sync with chatEffortForModel in server/internal/assistant/models_resolve.go.
  return levels.includes(wanted) ? effort : "";
}

export function DirectEffortSelect({ state, value, inherited, disabled, onChange }: {
  state: ReturnType<typeof directEffortState>;
  value: string;
  inherited?: string;
  disabled: boolean;
  onChange: (value: string) => void;
}) {
  const effortLabel = (effort: string) => effort === "off" || effort === "none" ? t("settings-modelconfiguration.api_effort_off") : effortTitle(effort);
  return <label className="block">
    <span className="mb-1 block text-label font-medium text-ink-2">{t("settings-modelconfiguration.effort")}</span>
    <select className={inputClass} value={value} disabled={disabled || (state.unsupported || state.unavailable) && !value} onChange={(event) => onChange(event.target.value)}>
      <option value="">{inherited ? t("settings-modelconfiguration.api_effort_inherit", { effort: effortLabel(inherited) }) : t("settings-modelconfiguration.follow_default")}</option>
      {value && !state.efforts.includes(value) && <option value={value}>{t("settings-modelconfiguration.model_current", { id: effortLabel(value) })}</option>}
      {state.efforts.filter((effort) => value !== "none" || effort !== "off").map((effort) => <option key={effort} value={effort}>{effortLabel(effort)}</option>)}
    </select>
    <span className="mt-1 block text-submeta text-ink-3">{state.unsupported ? t("settings-modelconfiguration.api_effort_unsupported")
      : state.unavailable ? t("settings-modelconfiguration.api_effort_unavailable")
      : state.reported ? t("settings-modelconfiguration.api_effort_hint") : t("settings-modelconfiguration.api_effort_unknown")}</span>
  </label>;
}

function probeFailure(detail: string, code?: string, status?: number): Failure {
  if (code === "auth" || code === "authentication" || code === "invalid_api_key") return { kind: "auth", detail };
  if (code === "not_found" || code === "model_not_found" || code === "model_not_found_error") return { kind: "model", detail };
  if (code === "timeout") return { kind: "timeout", detail };
  if (code === "region_blocked" || code === "wrong_protocol" || code === "unsupported_effort") return { kind: "other", detail };
  if (status === 401 || status === 403 || /\b(401|403)\b|unauthori[sz]ed|authentication|invalid.api.key/i.test(detail)) return { kind: "auth", detail };
  if ((status === undefined && /\b404\b/.test(detail))
    || /model.{0,80}(not found|does not exist)|model_not_found/i.test(detail)) return { kind: "model", detail };
  if (status === 408 || status === 504
    || /timed? ?out|timeout|deadline exceeded/i.test(detail)) return { kind: "timeout", detail };
  return { kind: "other", detail };
}

/** Draft-only probes reuse the saved provider's connection, never its purpose settings. */
export function DirectAPIEditor({ manager, purpose, provider, draft, disabled, effortAllowed = false, onChange }: {
  manager: string;
  purpose: ModelPurpose;
  provider: ModelProvider;
  draft: ModelDraft;
  disabled: boolean;
  effortAllowed?: boolean;
  onChange: (patch: Partial<Pick<ModelDraft, "model" | "effort">>) => void;
}) {
  const [models, setModels] = useState<ProviderModel[]>([]);
  const [listing, setListing] = useState(true);
  const [listError, setListError] = useState("");
  const [reload, setReload] = useState(0);
  const [testing, setTesting] = useState(false);
  const [verified, setVerified] = useState<ProbeResult | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);
  const probe = useRef<AbortController | null>(null);
  const discovery = useRef<AbortController | null>(null);
  // Model-config IDs wrap saved launcher names, including providers whose kind is API.
  const name = provider.id.startsWith("launcher.") ? provider.id.slice("launcher.".length) : "";
  const sameProvider = provider.id === purpose.effective.provider;
  const unresolvedInheritance = (!draft.provider && !!purpose.override.provider)
    || (sameProvider && ((!draft.model && !!purpose.override.model) || (!draft.effort && !!purpose.override.effort)));
  const inheritedModel = sameProvider && !purpose.override.model ? purpose.effective.model : undefined;
  const model = draft.model.trim() || inheritedModel || provider.model || "";
  const inheritedEffort = sameProvider && !purpose.override.effort ? purpose.effective.effort : undefined;
  const effort = draft.effort.trim() || inheritedEffort || (purpose.id === "inbox.triage" ? "off" : "");
  const chat = provider.api === "chat";
  const capabilities = directEffortState(provider, models, model);
  const expectedEffort = expectedDirectProbeEffort(provider, model, effort);

  const retire = () => {
    probe.current?.abort();
    probe.current = null;
  };
  useLayoutEffect(() => {
    // Retire at commit, including A -> B -> A edits and close/reopen, not only when a response arrives.
    retire();
    setTesting(false);
    setVerified(null);
    setFailure(null);
    return retire;
  }, [manager, purpose.id, purpose.revision, provider.id, provider.api, provider.effort, draft.provider, draft.model, draft.effort, model, effort, expectedEffort, capabilities.noEffort, unresolvedInheritance]);
  useLayoutEffect(() => () => discovery.current?.abort(), [manager, provider.id, provider.api, draft.provider]);

  useEffect(() => {
    setModels([]);
    setListError("");
    if (!name || (!draft.provider && purpose.override.provider)) { setListing(false); return; }
    const controller = new AbortController();
    discovery.current = controller;
    setListing(true);
    api<{ models: ProviderModel[]; error?: string }>("/launchers/discover", {
      method: "POST", json: { name }, signal: controller.signal, timeout: 35_000,
      headers: { [MANAGER_HEADER]: manager }, isAuthContextCurrent: () => !controller.signal.aborted,
    }).then((result) => {
      if (controller.signal.aborted) return;
      setModels(result.models ?? []);
      setListError(result.error ?? "");
    }).catch((error) => {
      if (!controller.signal.aborted) setListError(errorText(error));
    }).finally(() => {
      if (!controller.signal.aborted) setListing(false);
    });
    return () => controller.abort();
  }, [manager, name, provider.api, reload, draft.provider, purpose.override.provider]);

  const change = (patch: Partial<Pick<ModelDraft, "model" | "effort">>) => {
    retire();
    setTesting(false);
    setVerified(null);
    setFailure(null);
    onChange(patch);
  };
  const test = async () => {
    if (disabled || (chat && listing) || !name || unresolvedInheritance || probe.current) return;
    const controller = new AbortController();
    probe.current = controller;
    const current = () => probe.current === controller && !controller.signal.aborted;
    setTesting(true);
    setVerified(null);
    setFailure(null);
    try {
      const requestedEffort = effort;
      const result = await api<ProbeResult>("/launchers/test", {
        method: "POST", json: { name, model, effort: requestedEffort || undefined }, signal: controller.signal, timeout: 35_000,
        headers: { [MANAGER_HEADER]: manager }, isAuthContextCurrent: current,
      });
      if (!current()) return;
      if (!result.ok) setFailure(probeFailure(result.error ?? "", result.code));
      else if (expectedEffort !== undefined && result.effort !== expectedEffort) setFailure({ kind: "effort" });
      else setVerified(result);
    } catch (error) {
      if (!current()) return;
      setFailure(error instanceof HubTimeoutError ? { kind: "timeout" }
        : probeFailure(errorText(error), error instanceof ApiError ? error.code : undefined, error instanceof ApiError ? error.status : undefined));
    } finally {
      if (current()) { probe.current = null; setTesting(false); }
    }
  };

  return <>
    {supportsField(purpose, "model") && <div className="flex flex-col gap-2">
      <label className="block">
        <span className="mb-1 block text-label font-medium text-ink-2">{t("settings-modelconfiguration.api_models")}</span>
        <select className={inputClass} value={draft.model} disabled={disabled} onChange={(event) => change({ model: event.target.value })}>
          <option value="">{t("settings-modelconfiguration.follow_default")}</option>
          {draft.model && !models.some((item) => item.id === draft.model) && <option value={draft.model}>{t("settings-modelconfiguration.model_current", { id: draft.model })}</option>}
          {models.map((item) => <option key={item.id} value={item.id}>{item.name && item.name !== item.id ? `${item.name} (${item.id})` : item.id}</option>)}
        </select>
      </label>
      <label className="block">
        <span className="mb-1 block text-label font-medium text-ink-2">{t("settings-modelconfiguration.api_model_id")}</span>
        <input className={`${inputClass} font-mono`} value={draft.model} disabled={disabled} placeholder={model || t("settings-modelconfiguration.follow_default")}
          spellCheck={false} autoComplete="off" onChange={(event) => change({ model: event.target.value })} />
      </label>
      <p className="text-submeta text-ink-3">{t("settings-modelconfiguration.api_model_hint")}</p>
      {listing && <p role="status" className="text-submeta text-ink-3">{t("settings-modelconfiguration.api_models_loading")}</p>}
      {listError && <div role="alert" className="flex items-start gap-2 text-submeta text-ink-3">
        <span className="min-w-0 break-words">{t("settings-modelconfiguration.list_error", { error: listError })}</span>
        <button type="button" disabled={disabled || listing} onClick={() => setReload((value) => value + 1)} className="shrink-0 whitespace-nowrap font-medium text-accent-ink hover:underline">{t("settings-modelconfiguration.retry")}</button>
      </div>}
    </div>}
    {effortAllowed && <DirectEffortSelect state={capabilities} value={draft.effort} inherited={inheritedEffort} disabled={disabled} onChange={(value) => change({ effort: value })} />}
    <div className="flex flex-col items-start gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <Button tone="secondary" busy={testing} disabled={disabled || (chat && listing) || !name || unresolvedInheritance} onClick={() => void test()}>{t("settings-modelconfiguration.api_test")}</Button>
        {testing && <p role="status" className="text-label text-ink-3">{t("settings-modelconfiguration.api_test_running")}</p>}
      {!testing && !verified && !failure && <p role="status" className="text-label text-ink-3">{t("settings-modelconfiguration.api_test_unverified")}</p>}
        {verified && <p role="status" className="text-label text-accent-ink">{t("settings-modelconfiguration.api_test_ok", { ms: verified.ms })}</p>}
      </div>
      {!name && <p role="status" className="text-label text-ink-3">{t("settings-modelconfiguration.api_provider_unsupported")}</p>}
      {unresolvedInheritance && <p role="status" className="text-label text-ink-3">{t("settings-modelconfiguration.api_test_inheritance")}</p>}
      {failure && <div role="alert" className="text-label text-red">
        <p>{t(`settings-modelconfiguration.api_test_${failure.kind}`)}</p>
        {failure.detail && <p className="mt-1 break-words text-submeta">{failure.detail}</p>}
      </div>}
      <p className="text-submeta leading-relaxed text-ink-3">{t("settings-modelconfiguration.api_test_hint")}</p>
    </div>
  </>;
}
