// Shared model, provider, and effort picker controls used by both ModelConfiguration and ModelProfileEditor
// so profile editing uses the exact same candidate filtering, labels, and /model list behavior.
import { Suspense, useEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, errorText } from "../../lib/api";
import { effortChoices, providerModels, takesEffort } from "../../lib/launcher-models";
import { lazyPage } from "../../lib/lazy-page";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import { modelProviderTitle, providerAgent, providerName, type ModelBinding, type ModelProvider, type ModelPurpose } from "../../lib/model-config";
import { providersFor } from "../../lib/model-config-execution";
import { EFFORT_LEVELS, type ProviderModel } from "../../lib/providers";
import { effortTitle } from "../../lib/session-actions";
import { Loading } from "../../ui/atoms";
import type { ModelList } from "../../ui/ModelPicker";
import { DirectEffortSelect, directEffortState } from "./DirectAPIEditor";

// Shared lazy ModelPicker chunk (loaded only when picking a model, outside Settings' preload dependencies).
export const ModelPicker = lazyPage(() => import("../../ui/ModelPicker").then((m) => m.ModelPicker));

export const pickerInputClass =
  "h-9 w-full rounded-control bg-field px-3 text-meta text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

export type LauncherModelState = ModelList & { machine?: string; provider?: string };

/** Loads a launcher provider's /model list (`providerModels`), keeping the previous list during explicit reloads. */
export function useLauncherModels(
  providerId: string,
  opts: { manager?: string; machine?: string; onProviderChange?: () => void } = {},
): { list: LauncherModelState; reload: () => void; hasPicker: boolean } {
  const [list, setList] = useState<LauncherModelState>({ status: "idle" });
  const [reloadSeq, setReloadSeq] = useState(0);
  const refreshNext = useRef(false);
  const onChangeRef = useRef(opts.onProviderChange);
  onChangeRef.current = opts.onProviderChange;

  useEffect(() => {
    const refresh = refreshNext.current;
    refreshNext.current = false;
    if (!refresh) onChangeRef.current?.();
    if (!providerId) {
      setList({ status: "idle" });
      return;
    }
    const controller = new AbortController();
    setList((prev) => ({
      status: "loading",
      provider: providerId,
      list: refresh && prev.provider === providerId ? prev.list : undefined,
    }));
    providerModels(providerId, { manager: opts.manager, refresh, machine: opts.machine, signal: controller.signal })
      .then((res) => setList({ status: "ready", list: res, machine: res?.machine, provider: providerId }))
      .catch((e) => {
        if (!controller.signal.aborted) setList({ status: "error", error: errorText(e), provider: providerId });
      });
    return () => controller.abort();
  }, [providerId, reloadSeq, opts.manager, opts.machine]);

  const reload = () => {
    refreshNext.current = true;
    setReloadSeq((n) => n + 1);
  };
  const hasPicker = list.status === "loading" || (list.status === "ready" && !!list.list?.models.length);
  return { list, reload, hasPicker };
}

/** Loads a Direct API provider's models via POST /launchers/discover, falling back to its saved `provider.models`. */
export function useDiscoveredApiModels(
  provider: ModelProvider | undefined,
  manager?: string,
): { models: ProviderModel[]; listing: boolean; error: string; reload: () => void } {
  const [discovered, setDiscovered] = useState<ProviderModel[]>([]);
  const [listing, setListing] = useState(false);
  const [error, setError] = useState("");
  const [reloadSeq, setReloadSeq] = useState(0);
  const name = provider?.kind === "api" && provider.id.startsWith("launcher.") ? provider.id.slice("launcher.".length) : "";

  useEffect(() => {
    setDiscovered([]);
    setError("");
    if (!name) {
      setListing(false);
      return;
    }
    const controller = new AbortController();
    setListing(true);
    api<{ models: ProviderModel[]; error?: string }>("/launchers/discover", {
      method: "POST",
      json: { name },
      signal: controller.signal,
      timeout: 35_000,
      headers: manager ? { [MANAGER_HEADER]: manager } : undefined,
      isAuthContextCurrent: () => !controller.signal.aborted,
    })
      .then((res) => {
        if (controller.signal.aborted) return;
        setDiscovered(res.models ?? []);
        setError(res.error ?? "");
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(errorText(e));
      })
      .finally(() => {
        if (!controller.signal.aborted) setListing(false);
      });
    return () => controller.abort();
  }, [manager, name, provider?.api, reloadSeq]);

  const models = discovered.length > 0 ? discovered : (provider?.models ?? []);
  return { models, listing, error, reload: () => setReloadSeq((n) => n + 1) };
}

/** Provider `<select>` matching ModelConfiguration's filtering, hidden, and unavailable labels. */
export function ProviderSelect({
  purpose,
  providers,
  value,
  disabled,
  emptyLabel,
  ariaLabel,
  onChange,
}: {
  purpose: ModelPurpose;
  providers: ModelProvider[];
  value: string;
  disabled?: boolean;
  emptyLabel?: string;
  ariaLabel?: string;
  onChange: (provider: string) => void;
}) {
  const candidates = providersFor(
    { ...purpose, override: { ...purpose.override, provider: value || purpose.override?.provider } },
    providers,
  );
  return (
    <select
      aria-label={ariaLabel}
      className={pickerInputClass}
      value={value}
      disabled={disabled}
      onChange={(e) => onChange(e.target.value)}
    >
      <option value="">{emptyLabel ?? t("settings-modelconfiguration.follow_default")}</option>
      {value && !candidates.some((v) => v.id === value) && (
        <option value={value}>
          {value.startsWith("legacy.")
            ? t("settings-modelconfiguration.not_configured")
            : t("settings-modelconfiguration.provider_unavailable", { id: providerName(value, providers) ?? value })}
        </option>
      )}
      {candidates.map((v) => (
        <option key={v.id} value={v.id}>
          {v.hidden ? t("settings-modelconfiguration.provider_hidden", { name: modelProviderTitle(v) }) : modelProviderTitle(v)}
        </option>
      ))}
    </select>
  );
}

/** Launcher model picker with the same lazy ModelPicker + custom ID fallback + retry bar as ModelConfiguration. */
export function LauncherModelSelect({
  models,
  hasPicker,
  onReload,
  model,
  followed,
  defaultLabel,
  disabled,
  ariaLabel,
  placeholder,
  hint,
  onChange,
}: {
  models: LauncherModelState;
  hasPicker: boolean;
  onReload: () => void;
  model: string;
  followed?: ModelBinding;
  defaultLabel?: string;
  disabled?: boolean;
  ariaLabel: string;
  placeholder?: string;
  hint?: string;
  onChange: (model: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const search = useRef<HTMLInputElement>(null);

  useEffect(() => {
    setOpen(false);
    setQuery("");
  }, [models.provider]);

  return (
    <div>
      {hasPicker ? (
        <div inert={disabled}>
          <Suspense fallback={<Loading />}>
            <ModelPicker
              models={models}
              model={model}
              followed={followed}
              defaultLabel={defaultLabel}
              open={open}
              onToggle={() => setOpen((o) => !o)}
              query={query}
              onQuery={setQuery}
              search={search}
              onPick={(id) => {
                onChange(id);
                setOpen(false);
                setQuery("");
              }}
              onReload={onReload}
              custom
              failedNote={t("settings-modelconfiguration.list_failed")}
            />
          </Suspense>
        </div>
      ) : (
        <input
          aria-label={ariaLabel}
          className={`${pickerInputClass} font-mono`}
          value={model}
          disabled={disabled}
          placeholder={placeholder ?? defaultLabel ?? t("settings-modelconfiguration.follow_default")}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => onChange(e.target.value)}
        />
      )}
      {hint && <span className="mt-1 block text-submeta text-ink-3">{hint}</span>}
      {models.status === "error" && (
        <span role="alert" className="mt-1 flex items-center gap-2 text-submeta text-ink-3">
          <span className="min-w-0 truncate" title={models.error}>
            {t("settings-modelconfiguration.list_error", { error: models.error ?? "" })}
          </span>
          <button
            type="button"
            disabled={disabled}
            onClick={onReload}
            className="shrink-0 whitespace-nowrap font-medium text-accent-ink hover:underline"
          >
            {t("settings-modelconfiguration.retry")}
          </button>
        </span>
      )}
    </div>
  );
}

/** Direct API model picker matching DirectAPIEditor's discovered-model select + manual Model ID input. */
export function ApiModelSelect({
  models,
  listing,
  error,
  onReload,
  value,
  placeholder,
  emptyLabel,
  disabled,
  selectLabel,
  inputLabel,
  onChange,
}: {
  models: ProviderModel[];
  listing: boolean;
  error: string;
  onReload: () => void;
  value: string;
  placeholder?: string;
  emptyLabel?: string;
  disabled?: boolean;
  selectLabel?: string;
  inputLabel?: string;
  onChange: (model: string) => void;
}) {
  return (
    <div className="flex flex-col gap-2">
      <label className="block">
        <span className="mb-1 block text-label font-medium text-ink-2">
          {selectLabel ?? t("settings-modelconfiguration.api_models")}
        </span>
        <select className={pickerInputClass} value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)}>
          <option value="">{emptyLabel ?? t("settings-modelconfiguration.follow_default")}</option>
          {value && !models.some((item) => item.id === value) && (
            <option value={value}>{t("settings-modelconfiguration.model_current", { id: value })}</option>
          )}
          {models.map((item) => (
            <option key={item.id} value={item.id}>
              {item.name && item.name !== item.id ? `${item.name} (${item.id})` : item.id}
            </option>
          ))}
        </select>
      </label>
      <label className="block">
        <span className="mb-1 block text-label font-medium text-ink-2">
          {inputLabel ?? t("settings-modelconfiguration.api_model_id")}
        </span>
        <input
          className={`${pickerInputClass} font-mono`}
          value={value}
          disabled={disabled}
          placeholder={placeholder ?? t("settings-modelconfiguration.follow_default")}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => onChange(e.target.value)}
        />
      </label>
      {listing && <p role="status" className="text-submeta text-ink-3">{t("settings-modelconfiguration.api_models_loading")}</p>}
      {error && (
        <div role="alert" className="flex items-start gap-2 text-submeta text-ink-3">
          <span className="min-w-0 break-words">{t("settings-modelconfiguration.list_error", { error })}</span>
          <button
            type="button"
            disabled={disabled || listing}
            onClick={onReload}
            className="shrink-0 whitespace-nowrap font-medium text-accent-ink hover:underline"
          >
            {t("settings-modelconfiguration.retry")}
          </button>
        </div>
      )}
    </div>
  );
}

/** Effort selector reusing DirectEffortSelect for API providers and effortChoices/EFFORT_LEVELS for launchers. */
export function ProfileEffortSelect({
  chosen,
  kind,
  launcherList,
  discoveredModels = [],
  model,
  followed,
  value,
  disabled,
  allowOff = false,
  onChange,
}: {
  chosen?: ModelProvider;
  kind: "api" | "launcher";
  launcherList?: LauncherModelState;
  discoveredModels?: ProviderModel[];
  model: string;
  followed?: ModelBinding;
  value: string;
  disabled?: boolean;
  allowOff?: boolean;
  onChange: (effort: string) => void;
}) {
  if (chosen?.kind === "api") {
    const effortModel = model.trim() || followed?.model || chosen.model || "";
    const state = directEffortState(chosen, discoveredModels, effortModel);
    // Purpose/profile Save accepts proto.EffortLevels plus off/none for API purposes; filter out levels Save rejects.
    const allowed = new Set(allowOff || kind === "api" ? ["off", "none", ...EFFORT_LEVELS] : EFFORT_LEVELS);
    const filteredState = {
      ...state,
      efforts: state.efforts.filter((e) => allowed.has(e)),
    };
    return (
      <DirectEffortSelect
        state={filteredState}
        value={value}
        disabled={!!disabled}
        onChange={onChange}
      />
    );
  }

  const agent = (launcherList?.list?.agent) || providerAgent(chosen);
  const rawChoices =
    launcherList?.status === "ready" && launcherList.list && takesEffort(launcherList.list.agent)
      ? effortChoices(launcherList.list, model, followed)
      : allowOff || kind === "api"
        ? ["off", ...EFFORT_LEVELS]
        : EFFORT_LEVELS;
  const allowed = new Set(allowOff || kind === "api" ? ["off", "none", ...EFFORT_LEVELS] : EFFORT_LEVELS);
  const choices = rawChoices.filter((e) => allowed.has(e));
  const unsupported = !takesEffort(agent) && kind === "launcher" && !allowOff;
  const effortLabel = (effort: string) =>
    effort === "off" || effort === "none" ? t("settings-modelconfiguration.api_effort_off") : effortTitle(effort);

  return (
    <label className="block">
      <span className="mb-1 block text-label font-medium text-ink-2">{t("settings-modelconfiguration.effort")}</span>
      <select
        className={pickerInputClass}
        value={value}
        disabled={disabled || (unsupported && !value)}
        onChange={(e) => onChange(e.target.value)}
      >
        <option value="">{t("settings-modelconfiguration.follow_default")}</option>
        {value && !choices.includes(value) && (
          <option value={value}>{t("settings-modelconfiguration.model_current", { id: effortLabel(value) })}</option>
        )}
        {!unsupported && choices.map((effort) => (
          <option key={effort} value={effort}>
            {effortLabel(effort)}
          </option>
        ))}
      </select>
      {unsupported && (
        <span className="mt-1 block text-submeta text-ink-3">{t("settings-modelconfiguration.api_effort_unsupported")}</span>
      )}
    </label>
  );
}
