// Interactive editor for a ModelProfile (name, description, purpose bindings, managed launcher overrides,
// and Agent presets/tiers). Lazy-loaded from ModelProfiles so it stays out of Settings' initial chunk.
import { useEffect, useMemo, useRef, useState } from "react";
import { t } from "../../i18n";
import { api } from "../../lib/api";
import { launcherBindingState, useCanonicalLauncherBinding } from "../../lib/launcher-model-binding";
import { takesEffort } from "../../lib/launcher-models";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import { providerAgent, type ModelBinding, type ModelProvider, type ModelPurpose } from "../../lib/model-config";
import { chooseProvider, effortOffered, modelConfiguration, permissionModesForLauncher, permissionModesForProvider } from "../../lib/model-config-execution";
import {
  draftToProfileInput,
  isProfileDraftDirty,
  profileDraftProblems,
  profileToDraft,
  type LauncherDraftEntry,
  type ProfileDraft,
  type PurposeDraftEntry,
  type TierDraftEntry,
} from "../../lib/model-profile-editor";
import {
  LAUNCHER_EXTRA_MODEL_FIELDS,
  MAX_MODEL_TIERS,
  MAX_PROFILE_DESCRIPTION,
  MAX_PROFILE_NAME,
  MAX_TIER_DESCRIPTION,
  type LauncherModelField,
  type ModelProfile,
  type ModelProfileSaveInput,
} from "../../lib/model-profiles";
import { namedLauncher, namesFromProviders, providerIdNote, providerLabel, providerTitle, type Named } from "../../lib/provider-names";
import { EFFORT_LEVELS, type ProviderView } from "../../lib/providers";
import { BYPASS_MODE, BYPASS_WARNING, effortTitle, modeTitle } from "../../lib/session-actions";
import { Button, cx, Switch } from "../../ui/atoms";
import {
  ApiModelSelect,
  LauncherModelSelect,
  pickerInputClass,
  ProfileEffortSelect,
  ProviderSelect,
  useDiscoveredApiModels,
  useLauncherModels,
} from "./ModelBindingPickers";

interface ManagedLauncherInfo extends ProviderView {
  source?: string;
}

const UNSET_EFFORT = "__unset__";

function fallbackCategoryPurpose(id: string): ModelPurpose {
  const isLauncher = id === "assistant" || id === "agent.new";
  return {
    id,
    category_id: id,
    name: categoryTitle(id),
    group: "",
    kind: isLauncher ? "launcher" : "api",
    override: {},
    effective: {},
    revision: 0,
    fields: isLauncher ? ["provider", "model", "effort", "permission_mode"] : ["provider", "model", "effort"],
  };
}

export function categoryTitle(id: string, purposes?: ModelPurpose[]): string {
  if (id === "agent.new") return t("newagent.title");
  const found = purposes?.find((p) => p.id === id);
  if (found?.name) return found.name;
  const map: Record<string, string> = {
    assistant: t("settings-modelprofiles.cat_assistant"),
    "agent.new": t("settings-modelprofiles.cat_agent_new"),
    kb: t("settings-modelprofiles.cat_kb"),
    "inbox.triage": t("settings-modelprofiles.cat_inbox_triage"),
    "agent.delegate": t("settings-modelprofiles.cat_agent_delegate"),
    translate: t("settings-modelprofiles.cat_translate"),
  };
  return map[id] ?? id;
}

export function ModelProfileEditor({
  manager,
  profile,
  revision,
  purposes: propPurposes,
  providers: propProviders,
  busy,
  error,
  conflict,
  onSave,
  onReload,
  onCancel,
}: {
  manager: string;
  profile: ModelProfile;
  revision: number;
  purposes?: ModelPurpose[];
  providers?: ModelProvider[];
  busy: boolean;
  error: string;
  conflict: boolean;
  onSave: (input: ModelProfileSaveInput) => Promise<void>;
  onReload: () => Promise<void>;
  onCancel: () => void;
}) {
  const [fetchedPurposes, setFetchedPurposes] = useState<ModelPurpose[] | undefined>(undefined);
  const [fetchedProviders, setFetchedProviders] = useState<ModelProvider[] | undefined>(undefined);
  const [managedLaunchers, setManagedLaunchers] = useState<ManagedLauncherInfo[]>([]);
  const [addingLauncher, setAddingLauncher] = useState("");
  const tierCounter = useRef(0);

  const purposes = propPurposes ?? fetchedPurposes ?? [];
  const providers = propProviders ?? fetchedProviders ?? [];

  // Fetch /model-config only when the parent did not already pass purposes and providers.
  useEffect(() => {
    if (propPurposes && propProviders) return;
    let alive = true;
    modelConfiguration(manager)
      .then((res) => {
        if (!alive) return;
        if (!propPurposes) setFetchedPurposes(res.purposes ?? []);
        if (!propProviders) setFetchedProviders(res.providers ?? []);
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [manager, propPurposes, propProviders]);

  // Fetch managed launchers (source === "persona") for the Launcher overrides section; never returns credentials.
  useEffect(() => {
    const controller = new AbortController();
    api<{ launchers?: ManagedLauncherInfo[] }>("/launchers", {
      signal: controller.signal,
      headers: manager ? { [MANAGER_HEADER]: manager } : undefined,
    })
      .then((res) => {
        if (controller.signal.aborted) return;
        const managed = (res.launchers ?? []).filter((l) => l.source === "persona");
        setManagedLaunchers(managed);
      })
      .catch(() => {});
    return () => controller.abort();
  }, [manager]);

  const catalogIds = useMemo(() => purposes.map((p) => p.id), [purposes]);
  const initialDraft = useMemo(() => profileToDraft(profile, catalogIds), [profile, revision]);
  const [draft, setDraft] = useState<ProfileDraft>(() => initialDraft);

  // Reset the draft only when opening a different profile or after an explicit reload bumps revision.
  const loadedKey = `${profile.id}:${revision}`;
  const prevKey = useRef(loadedKey);
  useEffect(() => {
    if (prevKey.current !== loadedKey) {
      prevKey.current = loadedKey;
      setDraft(profileToDraft(profile, catalogIds));
    }
  }, [loadedKey, profile, catalogIds]);

  const providerNames = useMemo<Named[]>(() => {
    const list = namesFromProviders(providers);
    for (const l of managedLaunchers) {
      if (!list.some((x) => x.name === l.name)) {
        list.push(l.display_name ? { name: l.name, display_name: l.display_name } : { name: l.name });
      }
    }
    return list;
  }, [providers, managedLaunchers]);

  const launcherProviders = useMemo(
    () =>
      providers.filter(
        (p) => p.kind === "launcher" && p.id.startsWith("launcher.") && !p.hidden,
      ),
    [providers],
  );

  const launcherKinds = useMemo<Record<string, "launcher" | "api" | undefined>>(() => {
    const map: Record<string, "launcher" | "api" | undefined> = {};
    for (const l of managedLaunchers) map[l.name] = l.kind ?? "launcher";
    for (const p of providers) {
      if (p.id.startsWith("launcher.")) {
        const name = p.id.slice("launcher.".length);
        if (!(name in map)) map[name] = p.kind === "api" ? "api" : "launcher";
      }
    }
    return map;
  }, [managedLaunchers, providers]);

  const availableManagedToAdd = useMemo(() => {
    const used = new Set(Object.keys(draft.launchers));
    if (managedLaunchers.length > 0) {
      return managedLaunchers.filter((l) => !used.has(l.name));
    }
    // Fallback when /launchers has not loaded or on mock setups without /launchers: managed-like providers excluding built-in claude.
    return providers
      .filter((p) => p.id.startsWith("launcher.") && p.id !== "launcher.claude" && (p.kind === "launcher" || p.kind === "api"))
      .map((p): ManagedLauncherInfo => ({
        name: p.id.slice("launcher.".length),
        display_name: p.name !== p.id.slice("launcher.".length) ? p.name : undefined,
        kind: p.kind === "api" ? "api" : "launcher",
        agent: p.agent,
        api: p.api,
        model: p.model,
        models: p.models,
      }))
      .filter((l) => !used.has(l.name));
  }, [managedLaunchers, providers, draft.launchers]);

  const problems = useMemo(() => profileDraftProblems(draft, launcherKinds), [draft, launcherKinds]);
  const dirty = useMemo(() => isProfileDraftDirty(draft, initialDraft), [draft, initialDraft]);
  const canSave = !busy && !conflict && problems.length === 0;

  const updatePurpose = (id: string, patch: Partial<PurposeDraftEntry>) => {
    setDraft((prev) => ({
      ...prev,
      purposes: {
        ...prev.purposes,
        [id]: { ...prev.purposes[id], ...patch },
      },
    }));
  };

  const updateLauncher = (name: string, next: LauncherDraftEntry) => {
    setDraft((prev) => ({
      ...prev,
      launchers: { ...prev.launchers, [name]: next },
    }));
  };

  const removeLauncher = (name: string) => {
    setDraft((prev) => {
      const launchers = { ...prev.launchers };
      delete launchers[name];
      return { ...prev, launchers };
    });
  };

  const addLauncherOverride = (name: string) => {
    if (!name) return;
    const info = availableManagedToAdd.find((l) => l.name === name);
    setDraft((prev) => ({
      ...prev,
      launchers: {
        ...prev.launchers,
        [name]: info?.model ? { model: info.model } : {},
      },
    }));
    setAddingLauncher("");
  };

  const addTier = () => {
    if (draft.tiers.length >= MAX_MODEL_TIERS) return;
    const defaultLauncher =
      launcherProviders[0]?.id.slice("launcher.".length) ??
      managedLaunchers.find((l) => l.kind !== "api")?.name ??
      "claude";
    const key = `new-tier-${++tierCounter.current}`;
    setDraft((prev) => ({
      ...prev,
      tiers: [
        ...prev.tiers,
        { key, name: "", launcher: defaultLauncher, model: "", effort: "", permission_mode: "", speed: "", description: "" },
      ],
    }));
  };

  const updateTier = (key: string, patch: Partial<TierDraftEntry>) => {
    setDraft((prev) => ({
      ...prev,
      tiers: prev.tiers.map((item) => (item.key === key ? { ...item, ...patch } : item)),
    }));
  };

  const removeTier = (key: string) => {
    setDraft((prev) => ({
      ...prev,
      tiers: prev.tiers.filter((item) => item.key !== key),
    }));
  };

  const handleSave = () => {
    if (!canSave) return;
    void onSave(draftToProfileInput(draft, revision));
  };

  const chatEffortKnown = providers.some((p) => p.chat_effort === true);
  const purposeIds = Object.keys(draft.purposes);
  const launcherNames = Object.keys(draft.launchers).sort((a, b) => a.localeCompare(b));

  return (
    <div data-testid="model-profile-editor" className="flex flex-col gap-5">
      <p className="rounded-control bg-inset px-3 py-2 text-label leading-relaxed text-ink-2">
        {profile.active
          ? t("settings-modelprofiles.save_note_active")
          : t("settings-modelprofiles.save_note_inactive")}
      </p>

      {/* Profile identity */}
      <div className="flex flex-col gap-3">
        <label className="block">
          <span className="mb-1 block text-label font-medium text-ink-2">{t("settings-modelprofiles.name")}</span>
          <input
            aria-label={t("settings-modelprofiles.name")}
            className={pickerInputClass}
            value={draft.name}
            maxLength={MAX_PROFILE_NAME}
            disabled={busy}
            placeholder={t("settings-modelprofiles.name_placeholder")}
            onChange={(e) => setDraft((prev) => ({ ...prev, name: e.target.value }))}
          />
        </label>
        <label className="block">
          <span className="mb-1 block text-label font-medium text-ink-2">{t("settings-modelprofiles.description")}</span>
          <input
            aria-label={t("settings-modelprofiles.description")}
            className={pickerInputClass}
            value={draft.description}
            maxLength={MAX_PROFILE_DESCRIPTION}
            disabled={busy}
            placeholder={t("settings-modelprofiles.description_placeholder")}
            onChange={(e) => setDraft((prev) => ({ ...prev, description: e.target.value }))}
          />
        </label>
      </div>

      {/* Section 1: Purpose bindings */}
      <section data-testid="profile-editor-purposes" className="flex flex-col gap-2.5">
        <div>
          <h3 className="text-label font-semibold text-ink-2">{t("settings-modelprofiles.purposes_section")}</h3>
          <p className="mt-0.5 text-submeta text-ink-3">{t("settings-modelprofiles.purposes_hint")}</p>
        </div>
        <div className="flex flex-col gap-2">
          {purposeIds.map((id) => {
            const entry = draft.purposes[id];
            const catalogPurpose = purposes.find((p) => p.id === id) ?? fallbackCategoryPurpose(id);
            return (
              <PurposeBindingCard
                key={id}
                id={id}
                title={categoryTitle(id, purposes)}
                purpose={catalogPurpose}
                parent={purposes.find((p) => p.id === catalogPurpose.parent)?.effective}
                providers={providers}
                chatEffortKnown={chatEffortKnown}
                manager={manager}
                entry={entry}
                busy={busy}
                onChange={(patch) => updatePurpose(id, patch)}
              />
            );
          })}
        </div>
      </section>

      {/* Section 2: Managed launcher model overrides */}
      <section data-testid="profile-editor-launchers" className="flex flex-col gap-2.5">
        <div>
          <h3 className="text-label font-semibold text-ink-2">{t("settings-modelprofiles.launchers_section")}</h3>
          <p className="mt-0.5 text-submeta text-ink-3">{t("settings-modelprofiles.launchers_hint")}</p>
        </div>
        {launcherNames.length === 0 ? (
          <p className="text-label text-ink-3">{t("settings-modelprofiles.no_launchers")}</p>
        ) : (
          <div className="flex flex-col gap-2.5">
            {launcherNames.map((name) => {
              const managed = managedLaunchers.find((l) => l.name === name);
              const provider = providers.find((p) => p.id === `launcher.${name}`);
              return (
                <LauncherOverrideCard
                  key={name}
                  name={name}
                  named={namedLauncher(name, providerNames)}
                  managed={managed}
                  provider={provider}
                  manager={manager}
                  entry={draft.launchers[name]}
                  busy={busy}
                  onChange={(next) => updateLauncher(name, next)}
                  onRemove={() => removeLauncher(name)}
                />
              );
            })}
          </div>
        )}
        {availableManagedToAdd.length > 0 ? (
          <div className="flex items-center gap-2">
            <select
              aria-label={t("settings-modelprofiles.add_launcher")}
              className={`${pickerInputClass} flex-1`}
              value={addingLauncher}
              disabled={busy}
              onChange={(e) => setAddingLauncher(e.target.value)}
            >
              <option value="">{t("settings-modelprofiles.select_launcher")}</option>
              {availableManagedToAdd.map((l) => (
                <option key={l.name} value={l.name}>
                  {providerLabel({ name: l.name, display_name: l.display_name })}
                </option>
              ))}
            </select>
            <Button
              tone="secondary"
              disabled={busy || !addingLauncher}
              onClick={() => addLauncherOverride(addingLauncher)}
            >
              {t("settings-modelprofiles.add_launcher")}
            </Button>
          </div>
        ) : (
          launcherNames.length > 0 && (
            <p className="text-submeta text-ink-3">{t("settings-modelprofiles.no_more_launchers")}</p>
          )
        )}
      </section>

      {/* Section 3: Agent presets (tiers) */}
      <section data-testid="profile-editor-tiers" className="flex flex-col gap-2.5">
        <div className="flex items-start justify-between gap-2">
          <div>
            <h3 className="text-label font-semibold text-ink-2">
              {t("settings-modelprofiles.tiers")} ({draft.tiers.length}/{MAX_MODEL_TIERS})
            </h3>
            <p className="mt-0.5 text-submeta text-ink-3">{t("settings-modelprofiles.tiers_hint")}</p>
          </div>
          <Button
            tone="secondary"
            size="sm"
            disabled={busy || draft.tiers.length >= MAX_MODEL_TIERS}
            onClick={addTier}
          >
            {t("settings-modelprofiles.add_tier")}
          </Button>
        </div>
        {draft.tiers.length === 0 ? (
          <p className="text-label text-ink-3">{t("settings-modelprofiles.no_tiers")}</p>
        ) : (
          <div className="flex flex-col gap-2.5">
            {draft.tiers.map((tier) => (
              <TierEditorCard
                key={tier.key}
                tier={tier}
                providers={providers}
                launcherProviders={launcherProviders}
                providerNames={providerNames}
                manager={manager}
                busy={busy}
                onChange={(patch) => updateTier(tier.key, patch)}
                onRemove={() => removeTier(tier.key)}
              />
            ))}
          </div>
        )}
      </section>

      {/* Inline validation problems */}
      {problems.length > 0 && (
        <ul role="alert" data-testid="profile-editor-problems" className="flex flex-col gap-1 rounded-control bg-field px-3 py-2 text-label text-red">
          {problems.map((msg, i) => (
            <li key={i}>{msg}</li>
          ))}
        </ul>
      )}

      {/* Server/action error and 409 reload */}
      {error && <p role="alert" className="text-label text-red">{error}</p>}
      {conflict && (
        <div>
          <Button tone="secondary" disabled={busy} onClick={() => void onReload()}>
            {t("settings-modelprofiles.reload_discard")}
          </Button>
        </div>
      )}

      {/* Footer actions inside editor */}
      <div className="flex items-center justify-end gap-2 border-t border-line pt-3">
        <Button tone="quiet" disabled={busy} onClick={onCancel}>
          {t("common.cancel")}
        </Button>
        <Button
          tone="primary"
          busy={busy}
          disabled={!canSave || (!dirty && !conflict)}
          onClick={handleSave}
        >
          {t("settings-modelprofiles.save")}
        </Button>
      </div>
    </div>
  );
}

function PurposeBindingCard({
  id,
  title,
  purpose,
  parent,
  providers,
  chatEffortKnown,
  manager,
  entry,
  busy,
  onChange,
}: {
  id: string;
  title: string;
  purpose: ModelPurpose;
  parent?: ModelBinding;
  providers: ModelProvider[];
  chatEffortKnown: boolean;
  manager: string;
  entry: PurposeDraftEntry;
  busy: boolean;
  onChange: (patch: Partial<PurposeDraftEntry>) => void;
}) {
  const [expanded, setExpanded] = useState(entry.included);
  const inherited = parent ?? purpose.effective;
  const runsOn = entry.provider || inherited?.provider || "";
  const chosen = providers.find((v) => v.id === runsOn);
  const effectiveProvider = providers.find((v) => v.id === purpose.effective?.provider);
  const isDirectApi = chosen?.kind === "api";
  const listProvider = entry.included && chosen?.kind === "launcher" ? chosen.id : "";
  const { list: launcherList, reload: reloadLauncher, hasPicker } = useLauncherModels(listProvider, {
    manager,
    machine: purpose.machine_id,
  });
  const apiModels = useDiscoveredApiModels(entry.included && expanded && isDirectApi ? chosen : undefined, manager);
  const effortAllowed = effortOffered(purpose, chosen, effectiveProvider, false, chatEffortKnown);
  const chosenAgent = launcherList.list?.agent || providerAgent(chosen, runsOn);
  const permissionModes = !isDirectApi ? permissionModesForProvider(purpose, chosen, chosenAgent) : [];
  const followed: ModelBinding = {
    model: (runsOn === inherited.provider ? inherited.model : undefined) || chosen?.model,
    effort: runsOn === inherited.provider ? inherited.effort : chosen?.effort,
  };
  const allowOff = purpose.kind === "api" || id === "inbox.triage";
  const binding = launcherBindingState(launcherList.list, entry, followed, {
    allowed: allowOff ? ["off", ...EFFORT_LEVELS] : EFFORT_LEVELS,
    allowUnknown: allowOff || takesEffort(chosenAgent, chosen) || chosenAgent === "codex",
  });
  useCanonicalLauncherBinding(binding.legacy, onChange);

  const summary = !entry.included
    ? t("settings-modelprofiles.purpose_unchanged")
    : [
        entry.provider
          ? (providers.find((v) => v.id === entry.provider)?.name ?? entry.provider)
          : t("settings-modelprofiles.purpose_follow_default"),
        binding.model,
        binding.value && effortTitle(binding.value),
        entry.permission_mode && modeTitle(entry.permission_mode),
      ]
        .filter(Boolean)
        .join(" · ");

  return (
    <div data-testid={`profile-purpose-${id}`} className="rounded-control bg-field p-3 shadow-hairline">
      <div className="flex items-center justify-between gap-2">
        <button
          type="button"
          className="min-w-0 flex-1 text-left"
          disabled={!entry.included}
          aria-expanded={entry.included && expanded}
          onClick={() => entry.included && setExpanded((o) => !o)}
        >
          <div className="truncate text-meta font-medium text-ink">{title}</div>
          <div className="truncate text-submeta text-ink-3">{summary}</div>
        </button>
        <Switch
          on={entry.included}
          disabled={busy}
          label={`${title} — ${t("settings-modelprofiles.purpose_included")}`}
          onChange={(on) => {
            onChange({ included: on });
            setExpanded(on);
          }}
        />
      </div>

      {entry.included && expanded && (
        <div className="mt-3 flex flex-col gap-3 border-t border-line pt-3">
          <label className="block">
            <span className="mb-1 block text-label font-medium text-ink-2">
              {t("settings-modelconfiguration.model_provider")}
            </span>
            <ProviderSelect
              purpose={purpose}
              providers={providers}
              value={entry.provider}
              disabled={busy}
              ariaLabel={`${title} ${t("settings-modelconfiguration.model_provider")}`}
              onChange={(nextProvider) => {
                const updated = chooseProvider(
                  { provider: entry.provider, model: entry.model, effort: entry.effort, permission_mode: entry.permission_mode, runtime: "" },
                  nextProvider,
                );
                onChange({ provider: updated.provider, model: updated.model, effort: updated.effort, permission_mode: updated.permission_mode ?? "" });
              }}
            />
          </label>

          {isDirectApi ? (
            <ApiModelSelect
              models={apiModels.models}
              listing={apiModels.listing}
              error={apiModels.error}
              onReload={apiModels.reload}
              value={entry.model}
              placeholder={chosen?.model || purpose.effective?.model || t("settings-modelconfiguration.follow_default")}
              disabled={busy}
              onChange={(model) => onChange({ model })}
            />
          ) : (
            <div className="block">
              <span className="mb-1 block text-label font-medium text-ink-2">
                {t("settings-modelconfiguration.specific_model")}
              </span>
              <LauncherModelSelect
                models={launcherList}
                hasPicker={hasPicker}
                onReload={reloadLauncher}
                model={binding.model}
                followed={followed}
                defaultLabel={t("settings-modelconfiguration.follow_default")}
                disabled={busy}
                ariaLabel={`${title} ${t("settings-modelconfiguration.specific_model")}`}
                placeholder={chosen?.model || purpose.effective?.model || t("settings-modelconfiguration.follow_default")}
                hint={
                  !hasPicker
                    ? t("settings-modelconfiguration.field_hint")
                    : entry.provider
                      ? t("settings-modelconfiguration.picker_hint_provider")
                      : t("settings-modelconfiguration.picker_hint_inherit")
                }
                onChange={(model) => onChange(binding.pickModel(model))}
              />
            </div>
          )}

          {effortAllowed && (
            <ProfileEffortSelect
              chosen={chosen}
              launcherBinding={binding}
              discoveredModels={apiModels.models}
              model={entry.model}
              followed={followed}
              value={entry.effort}
              disabled={busy || launcherList.status === "loading"}
              allowOff={allowOff}
              onChange={(effort) => onChange(binding.pickEffort(effort))}
            />
          )}

          {permissionModes.length > 0 && (
            <label className="block">
              <span className="mb-1 block text-label font-medium text-ink-2">
                {t("settings-modelconfiguration.permission_mode")}
              </span>
              <select
                data-permission-mode-select
                aria-label={`${title} ${t("settings-modelconfiguration.permission_mode")}`}
                className={pickerInputClass}
                value={entry.permission_mode}
                disabled={busy}
                onChange={(e) => onChange({ permission_mode: e.target.value })}
              >
                <option value="">{t("settings-modelconfiguration.follow_default")}</option>
                {entry.permission_mode && !permissionModes.some((m) => m.id === entry.permission_mode) && (
                  <option value={entry.permission_mode}>{modeTitle(entry.permission_mode)}</option>
                )}
                {permissionModes.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.title}
                  </option>
                ))}
              </select>
              <span className={`mt-1 block text-submeta ${entry.permission_mode === BYPASS_MODE ? "text-red" : "text-ink-3"}`}>
                {entry.permission_mode === BYPASS_MODE
                  ? BYPASS_WARNING
                  : permissionModes.find((m) => m.id === entry.permission_mode)?.detail ?? t("settings-modelconfiguration.permission_mode_hint")}
              </span>
            </label>
          )}
        </div>
      )}
    </div>
  );
}

function LauncherOverrideCard({
  name,
  named,
  managed,
  provider,
  manager,
  entry,
  busy,
  onChange,
  onRemove,
}: {
  name: string;
  named: Named;
  managed?: ManagedLauncherInfo;
  provider?: ModelProvider;
  manager: string;
  entry: LauncherDraftEntry;
  busy: boolean;
  onChange: (next: LauncherDraftEntry) => void;
  onRemove: () => void;
}) {
  const isApi = (managed?.kind ?? provider?.kind) === "api";
  const providerId = `launcher.${name}`;
  const { list: launcherList, reload: reloadLauncher, hasPicker } = useLauncherModels(isApi ? "" : providerId, {
    manager,
  });
  const apiProvider: ModelProvider | undefined = isApi
    ? (provider ?? {
        id: providerId,
        name: providerTitle(named),
        kind: "api",
        api: managed?.api,
        model: managed?.model,
        models: managed?.models,
      })
    : undefined;
  const apiModels = useDiscoveredApiModels(apiProvider, manager);

  const hasExtraFields = LAUNCHER_EXTRA_MODEL_FIELDS.some((f) => entry[f] !== undefined);
  const idNote = providerIdNote(named);

  const setField = (field: LauncherModelField, value: string | undefined) => {
    const next: LauncherDraftEntry = { ...entry };
    if (value === undefined) delete next[field];
    else next[field] = value;
    onChange(next);
  };

  const binding = launcherBindingState(launcherList.list, entry, { model: managed?.model || provider?.model });
  useCanonicalLauncherBinding(binding.legacy, (patch) => onChange({ ...entry, ...patch }));
  const explicitVariant = Boolean(entry.model && binding.folded);
  const effortValue = !explicitVariant && entry.effort === undefined ? UNSET_EFFORT : binding.value;

  return (
    <div data-testid={`profile-launcher-${name}`} className="flex flex-col gap-3 rounded-control bg-field p-3 shadow-hairline">
      <div className="flex items-center justify-between gap-2">
        <div className="min-w-0 flex-1">
          <span className="text-meta font-medium text-ink">{providerTitle(named)}</span>
          {idNote && <span className="ml-1.5 font-mono text-submeta text-ink-3">{idNote}</span>}
        </div>
        <Button tone="quiet" size="sm" disabled={busy} onClick={onRemove}>
          {t("settings-modelprofiles.remove")}
        </Button>
      </div>

      {isApi ? (
        <>
          <ApiModelSelect
            models={apiModels.models}
            listing={apiModels.listing}
            error={apiModels.error}
            onReload={apiModels.reload}
            value={entry.model ?? ""}
            emptyLabel={t("settings-modelprofiles.leave_unchanged")}
            selectLabel={t("settings-modelprofiles.field_model")}
            inputLabel={t("settings-modelconfiguration.api_model_id")}
            placeholder={managed?.model || provider?.model || t("settings-modelprofiles.leave_unchanged")}
            disabled={busy}
            onChange={(value) => setField("model", value.trim() ? value : undefined)}
          />
          <p className="text-submeta text-ink-3">{t("settings-modelprofiles.launcher_api_only_model")}</p>
        </>
      ) : (
        <>
          <div className="block">
            <span className="mb-1 block text-label font-medium text-ink-2">
              {t("settings-modelprofiles.field_model")}
            </span>
            <LauncherModelSelect
              models={launcherList}
              hasPicker={hasPicker}
              onReload={reloadLauncher}
              model={binding.model}
              followed={{ model: managed?.model || provider?.model }}
              defaultLabel={t("settings-modelprofiles.leave_unchanged")}
              disabled={busy}
              ariaLabel={`${name} ${t("settings-modelprofiles.field_model")}`}
              placeholder={managed?.model || provider?.model || t("settings-modelprofiles.leave_unchanged")}
              onChange={(value) => {
                const patch = binding.pickModel(value);
                if (patch.effort === "") onChange({ ...entry, ...patch });
                else setField("model", value.trim() ? value : undefined);
              }}
            />
          </div>

          <label className="block">
            <span className="mb-1 block text-label font-medium text-ink-2">
              {t("settings-modelprofiles.field_effort")}
            </span>
            <select
              aria-label={`${name} ${t("settings-modelprofiles.field_effort")}`}
              className={pickerInputClass}
              value={effortValue}
              disabled={busy || launcherList.status === "loading" || (!binding.choices.length && entry.effort === undefined)}
              onChange={(e) => {
                const v = e.target.value;
                if (v === UNSET_EFFORT) setField("effort", undefined);
                else onChange({ ...entry, ...binding.pickEffort(v) });
              }}
            >
              {!explicitVariant && <option value={UNSET_EFFORT}>{t("settings-modelprofiles.leave_unchanged")}</option>}
              <option value="">{t("settings-modelprofiles.clear_on_switch")}{explicitVariant && binding.defaultEffort ? ` · ${effortTitle(binding.defaultEffort)}` : ""}</option>
              {binding.value && !binding.choices.includes(binding.value) && (
                <option value={binding.value}>{t("settings-modelconfiguration.model_current", { id: effortTitle(binding.value) })}</option>
              )}
              {binding.choices.map((level) => (
                <option key={level} value={level}>
                  {effortTitle(level)}
                </option>
              ))}
            </select>
            {!binding.choices.length && <span className="mt-1 block text-submeta text-ink-3">{t("settings-modelconfiguration.api_effort_unsupported")}</span>}
          </label>

          <details open={hasExtraFields}>
            <summary className="cursor-pointer text-label font-medium text-ink-2">
              {t("settings-modelprofiles.more_launcher_fields")}
            </summary>
            <div className="mt-2.5 flex flex-col gap-3">
              {LAUNCHER_EXTRA_MODEL_FIELDS.map((field) => {
                const val = entry[field];
                const isClearing = val === "";
                const label = t(`settings-modelprofiles.field_${field}`);
                return (
                  <div key={field} className="flex flex-col gap-1">
                    <div className="flex items-center justify-between gap-2">
                      <span className="text-label font-medium text-ink-2">{label}</span>
                      <button
                        type="button"
                        disabled={busy}
                        onClick={() => setField(field, isClearing ? undefined : "")}
                        className={cx(
                          "text-submeta font-medium hover:underline",
                          isClearing ? "text-accent-ink" : "text-ink-3",
                        )}
                      >
                        {isClearing
                          ? t("settings-modelprofiles.leave_unchanged")
                          : t("settings-modelprofiles.clear_on_switch")}
                      </button>
                    </div>
                    {isClearing ? (
                      <div className="rounded-control bg-inset px-3 py-2 text-label text-ink-3">
                        {t("settings-modelprofiles.clear_on_switch")}
                      </div>
                    ) : (
                      <LauncherModelSelect
                        models={launcherList}
                        hasPicker={hasPicker}
                        onReload={reloadLauncher}
                        model={val ?? ""}
                        defaultLabel={t("settings-modelprofiles.leave_unchanged")}
                        disabled={busy}
                        ariaLabel={`${name} ${label}`}
                        foldVariants={false}
                        placeholder={t("settings-modelprofiles.leave_unchanged")}
                        onChange={(next) => setField(field, next.trim() ? next : undefined)}
                      />
                    )}
                  </div>
                );
              })}
            </div>
          </details>
        </>
      )}
    </div>
  );
}

function TierEditorCard({
  tier,
  providers,
  launcherProviders,
  providerNames,
  manager,
  busy,
  onChange,
  onRemove,
}: {
  tier: TierDraftEntry;
  providers: ModelProvider[];
  launcherProviders: ModelProvider[];
  providerNames: Named[];
  manager: string;
  busy: boolean;
  onChange: (patch: Partial<TierDraftEntry>) => void;
  onRemove: () => void;
}) {
  const providerId = tier.launcher.trim() ? `launcher.${tier.launcher.trim()}` : "";
  const chosenProvider = providers.find((p) => p.id === providerId);
  const { list: launcherList, reload: reloadLauncher, hasPicker } = useLauncherModels(providerId, { manager });

  const launcherOptions = useMemo(() => {
    const opts = launcherProviders.map((p) => {
      const id = p.id.slice("launcher.".length);
      return { id, label: providerLabel(namedLauncher(id, providerNames)) };
    });
    if (!opts.some((o) => o.id === "claude")) {
      const claudeProvider = providers.find((p) => p.id === "launcher.claude");
      if (claudeProvider) opts.unshift({ id: "claude", label: claudeProvider.name });
    }
    return opts;
  }, [launcherProviders, providers, providerNames]);

  const speedLabel = (speed: string) =>
    speed === "fast"
      ? t("settings-modelprofiles.tier_speed_fast")
      : speed === "ultrafast"
        ? t("settings-modelprofiles.tier_speed_ultrafast")
        : t("settings-modelprofiles.tier_speed_default");

  const chosenAgent = launcherList.list?.agent || providerAgent(chosenProvider, providerId);
  const permissionModes = permissionModesForLauncher(chosenProvider, chosenAgent);
  const followed = { model: chosenProvider?.model, effort: chosenProvider?.effort };
  const binding = launcherBindingState(launcherList.list, tier, followed, {
    allowUnknown: takesEffort(chosenAgent, chosenProvider) || chosenAgent === "codex",
  });
  useCanonicalLauncherBinding(binding.legacy, onChange);

  return (
    <div data-testid={`profile-tier-edit-${tier.name || tier.key}`} className="flex flex-col gap-3 rounded-control bg-field p-3 shadow-hairline">
      <div className="flex items-end gap-2">
        <label className="min-w-0 flex-1">
          <span className="mb-1 block text-label font-medium text-ink-2">
            {t("settings-modelprofiles.tier_name")}
          </span>
          <input
            aria-label={t("settings-modelprofiles.tier_name")}
            className={`${pickerInputClass} font-mono`}
            value={tier.name}
            disabled={busy}
            spellCheck={false}
            autoComplete="off"
            placeholder={t("settings-modelprofiles.tier_name_placeholder")}
            onChange={(e) => onChange({ name: e.target.value })}
          />
        </label>
        <Button tone="quiet" size="sm" disabled={busy} onClick={onRemove}>
          {t("settings-modelprofiles.remove")}
        </Button>
      </div>

      <label className="block">
        <span className="mb-1 block text-label font-medium text-ink-2">
          {t("settings-modelprofiles.tier_launcher")}
        </span>
        <select
          aria-label={t("settings-modelprofiles.tier_launcher")}
          className={pickerInputClass}
          value={tier.launcher}
          disabled={busy}
          onChange={(e) => {
            const nextLauncher = e.target.value;
            if (nextLauncher === tier.launcher) return;
            onChange({ launcher: nextLauncher, model: "", effort: "", permission_mode: "" });
          }}
        >
          <option value="">{t("settings-modelprofiles.tier_launcher_placeholder")}</option>
          {tier.launcher && !launcherOptions.some((o) => o.id === tier.launcher) && (
            <option value={tier.launcher}>{providerLabel(namedLauncher(tier.launcher, providerNames))}</option>
          )}
          {launcherOptions.map((o) => (
            <option key={o.id} value={o.id}>
              {o.label}
            </option>
          ))}
        </select>
      </label>

      <div className="block">
        <span className="mb-1 block text-label font-medium text-ink-2">
          {t("settings-modelconfiguration.specific_model")}
        </span>
        <LauncherModelSelect
          models={launcherList}
          hasPicker={hasPicker}
          onReload={reloadLauncher}
          model={binding.model}
          followed={followed}
          disabled={busy}
          ariaLabel={`${tier.name || tier.key} ${t("settings-modelconfiguration.specific_model")}`}
          placeholder={chosenProvider?.model || t("settings-modelconfiguration.provider_default")}
          onChange={(model) => onChange(binding.pickModel(model))}
        />
      </div>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <ProfileEffortSelect
          chosen={chosenProvider}
          launcherBinding={binding}
          model={tier.model}
          followed={followed}
          value={tier.effort}
          disabled={busy || launcherList.status === "loading"}
          onChange={(effort) => onChange(binding.pickEffort(effort))}
        />

        <label className="block">
          <span className="mb-1 block text-label font-medium text-ink-2">
            {t("settings-modelprofiles.tier_speed")}
          </span>
          <select
            aria-label={t("settings-modelprofiles.tier_speed")}
            className={pickerInputClass}
            value={tier.speed === "standard" ? "" : tier.speed}
            disabled={busy || (chosenAgent !== "claude" && chosenAgent !== "codex" && !tier.speed)}
            onChange={(e) => onChange({ speed: e.target.value })}
          >
            <option value="">{speedLabel("")}</option>
            <option value="fast">{speedLabel("fast")}</option>
            <option value="ultrafast">{speedLabel("ultrafast")}</option>
          </select>
        </label>
      </div>

      {permissionModes.length > 0 && (
        <label className="block">
          <span className="mb-1 block text-label font-medium text-ink-2">
            {t("settings-modelconfiguration.permission_mode")}
          </span>
          <select
            data-permission-mode-select
            aria-label={t("settings-modelconfiguration.permission_mode")}
            className={pickerInputClass}
            value={tier.permission_mode}
            disabled={busy}
            onChange={(e) => onChange({ permission_mode: e.target.value })}
          >
            <option value="">{t("settings-modelconfiguration.follow_default")}</option>
            {tier.permission_mode && !permissionModes.some((m) => m.id === tier.permission_mode) && (
              <option value={tier.permission_mode}>{modeTitle(tier.permission_mode)}</option>
            )}
            {permissionModes.map((m) => (
              <option key={m.id} value={m.id}>
                {m.title}
              </option>
            ))}
          </select>
          <span className={`mt-1 block text-submeta ${tier.permission_mode === BYPASS_MODE ? "text-red" : "text-ink-3"}`}>
            {tier.permission_mode === BYPASS_MODE
              ? BYPASS_WARNING
              : permissionModes.find((m) => m.id === tier.permission_mode)?.detail ?? t("settings-modelconfiguration.permission_mode_hint")}
          </span>
        </label>
      )}

      <label className="block">
        <span className="mb-1 block text-label font-medium text-ink-2">
          {t("settings-modelprofiles.tier_description")}
        </span>
        <input
          aria-label={t("settings-modelprofiles.tier_description")}
          className={pickerInputClass}
          value={tier.description}
          maxLength={MAX_TIER_DESCRIPTION}
          disabled={busy}
          placeholder={t("settings-modelprofiles.tier_description_placeholder")}
          onChange={(e) => onChange({ description: e.target.value })}
        />
      </label>
    </div>
  );
}
