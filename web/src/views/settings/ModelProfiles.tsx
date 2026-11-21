import { Suspense, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { ApiError, credentialOwner, errorText, getToken, subscribeCredentialOwner } from "../../lib/api";
import { lazyPage } from "../../lib/lazy-page";
import { onDefaultManager, onManagerChoice, shownManager } from "../../lib/manager-choice";
import { providerName, type ModelProvider, type ModelPurpose } from "../../lib/model-config";
import {
  LAUNCHER_MODEL_FIELDS,
  modelProfiles,
  PROFILE_CATEGORY_IDS,
  rollbackModelProfile,
  saveModelProfile,
  switchModelProfile,
  type ModelProfileSaveInput,
  type ModelProfiles as Profiles,
} from "../../lib/model-profiles";
import { namedLauncher, namesFromProviders, providerIdNote, providerTitle } from "../../lib/provider-names";
import { effortTitle, modeTitle } from "../../lib/session-actions";
import { onHubEvent } from "../../lib/store";
import { t } from "../../i18n";
import { Button, Dialog, Loading, toast } from "../../ui/atoms";
import { Group, Pill, Row } from "./SettingsParts";

// Load the interactive profile editor only when the user chooses Edit, keeping Settings' initial chunk light.
const ModelProfileEditor = lazyPage(() => import("./ModelProfileEditor").then((m) => m.ModelProfileEditor));

// A dispatched write can outlive its view and credential. Admit at most one per manager, including across remounts,
// so a late server commit cannot silently undo a later action from this browser.
const pendingWrites = new Set<string>();
const writeListeners = new Set<() => void>();
const subscribeWrites = (listener: () => void) => { writeListeners.add(listener); return () => { writeListeners.delete(listener); }; };
const writesChanged = () => writeListeners.forEach((listener) => listener());
let managerGeneration = 0;
onManagerChoice(() => { managerGeneration++; });
onDefaultManager(() => { managerGeneration++; });
const profileContext = () => JSON.stringify([credentialOwner(), managerGeneration]);
const subscribeProfileContext = (listener: () => void) => {
  const stops = [subscribeCredentialOwner(listener), onManagerChoice(listener), onDefaultManager(listener)];
  return () => stops.forEach((stop) => stop());
};

function purposeLabel(id: string, purposes?: ModelPurpose[]): string {
  if (id === "agent.new") return t("newagent.title");
  const found = purposes?.find((p) => p.id === id);
  if (found?.name) return found.name;
  const fallback: Record<string, string> = {
    assistant: t("settings-modelprofiles.cat_assistant"),
    "agent.new": t("settings-modelprofiles.cat_agent_new"),
    kb: t("settings-modelprofiles.cat_kb"),
    "inbox.triage": t("settings-modelprofiles.cat_inbox_triage"),
    "agent.delegate": t("settings-modelprofiles.cat_agent_delegate"),
    translate: t("settings-modelprofiles.cat_translate"),
  };
  return fallback[id] ?? id;
}

/** `providers` and `purposes`: the model configuration's, to name launchers/categories cleanly and share picker filtering (#5029). */
export function ModelProfiles({
  manager,
  providers,
  purposes,
  onChanged,
}: {
  manager?: string;
  providers?: ModelProvider[];
  purposes?: ModelPurpose[];
  onChanged: () => void;
}) {
  const context = useSyncExternalStore(subscribeProfileContext, profileContext, profileContext);
  const owner = credentialOwner();
  const target = manager ?? shownManager();
  return (
    <ModelProfilesForOwner
      key={JSON.stringify([target, context])}
      manager={target}
      owner={owner}
      context={context}
      providers={providers}
      purposes={purposes}
      onChanged={onChanged}
    />
  );
}

function ModelProfilesForOwner({
  manager,
  owner,
  context,
  providers,
  purposes,
  onChanged,
}: {
  manager: string;
  owner: string;
  context: string;
  providers?: ModelProvider[];
  purposes?: ModelPurpose[];
  onChanged: () => void;
}) {
  const [data, setData] = useState<Profiles | null>(null);
  const [readError, setReadError] = useState("");
  const [actionError, setActionError] = useState("");
  const [conflict, setConflict] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [editRevision, setEditRevision] = useState(0);
  const busy = useSyncExternalStore(subscribeWrites, () => pendingWrites.has(manager), () => false);
  const token = useRef(getToken()).current;
  const mounted = useRef(false);
  const readNumber = useRef(0);
  const readAbort = useRef<AbortController | null>(null);
  const interaction = useRef(0);
  const changed = useRef(onChanged);
  changed.current = onChanged;
  const current = () => mounted.current && profileContext() === context && credentialOwner() === owner;
  const invalidateRead = () => { readNumber.current++; readAbort.current?.abort(); readAbort.current = null; };
  useLayoutEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; invalidateRead(); };
  }, []);
  const load = async () => {
    invalidateRead();
    const number = readNumber.current;
    const edit = interaction.current;
    const controller = new AbortController();
    readAbort.current = controller;
    const isCurrent = () => current() && number === readNumber.current;
    try {
      const result = await modelProfiles(manager, { token, tokenOwner: owner, isAuthContextCurrent: isCurrent, signal: controller.signal });
      if (isCurrent()) {
        setData(result);
        if (edit === interaction.current) setReadError("");
      }
      return result;
    } catch (e) {
      if (isCurrent() && edit === interaction.current && !(e instanceof ApiError && e.status === 404)) setReadError(errorText(e));
      return null;
    } finally {
      if (isCurrent()) readAbort.current = null;
    }
  };
  useEffect(() => {
    void load();
    let wasPending = pendingWrites.has(manager);
    const stopWrites = subscribeWrites(() => {
      const pending = pendingWrites.has(manager);
      // A write from this or a retired view may have committed after our last read.
      if (wasPending && !pending && current()) void load();
      wasPending = pending;
    });
    const stopEvents = onHubEvent("model_profile", () => {
      if (!current()) return;
      // Read after the pending write settles: an event read during it can precede its server commit.
      if (!pendingWrites.has(manager)) void load();
      changed.current();
    });
    return () => { stopWrites(); stopEvents(); invalidateRead(); };
  }, []);

  const apply = async (id?: string) => {
    if (!current() || pendingWrites.has(manager)) return;
    pendingWrites.add(manager);
    writesChanged();
    invalidateRead();
    const edit = ++interaction.current;
    setReadError(""); setActionError(""); setConflict(false);
    const reqContext = { token, tokenOwner: owner, isAuthContextCurrent: current };
    try {
      const result = await (id ? switchModelProfile(id, manager, reqContext) : rollbackModelProfile(manager, reqContext));
      if (current()) {
        setData(result);
        if (edit === interaction.current) { setEditing(false); setSelected(null); }
        changed.current();
      }
    } catch (e) {
      if (current() && edit === interaction.current) setActionError(errorText(e));
    } finally {
      pendingWrites.delete(manager);
      writesChanged();
    }
  };

  const save = async (id: string, input: ModelProfileSaveInput) => {
    if (!current() || pendingWrites.has(manager)) return;
    pendingWrites.add(manager);
    writesChanged();
    invalidateRead();
    const edit = ++interaction.current;
    setReadError(""); setActionError(""); setConflict(false);
    const reqContext = { token, tokenOwner: owner, isAuthContextCurrent: current };
    try {
      const result = await saveModelProfile(id, input, manager, reqContext);
      if (current()) {
        setData(result);
        if (edit === interaction.current) {
          setEditRevision(result.revision ?? 0);
          setEditing(false);
        }
        toast(t("settings-modelprofiles.saved"), "ok");
        changed.current();
      }
    } catch (e) {
      if (current() && edit === interaction.current) {
        const isConflict = e instanceof ApiError && e.status === 409;
        setConflict(isConflict);
        setActionError(isConflict ? t("settings-modelprofiles.conflict") : errorText(e));
      }
    } finally {
      pendingWrites.delete(manager);
      writesChanged();
    }
  };

  const reloadForEdit = async () => {
    clearErrors();
    const fresh = await load();
    if (fresh && current()) {
      setEditRevision(fresh.revision ?? 0);
    }
  };

  const clearErrors = () => { interaction.current++; setReadError(""); setActionError(""); setConflict(false); };
  const closeDialog = () => {
    if (busy) return;
    setEditing(false);
    setSelected(null);
    clearErrors();
  };
  const startEditing = () => {
    clearErrors();
    setEditRevision(data?.revision ?? 0);
    setEditing(true);
  };

  const error = actionError || readError;
  const profile = data?.profiles.find((p) => p.id === selected);
  const tiers = Object.entries(profile?.tiers ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const purposeEntries = Object.entries(profile?.purposes ?? {}).sort(([a], [b]) => {
    const ai = (PROFILE_CATEGORY_IDS as readonly string[]).indexOf(a);
    const bi = (PROFILE_CATEGORY_IDS as readonly string[]).indexOf(b);
    if (ai >= 0 && bi >= 0) return ai - bi;
    if (ai >= 0) return -1;
    if (bi >= 0) return 1;
    return a.localeCompare(b);
  });
  const launcherEntries = Object.entries(profile?.launchers ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const names = namesFromProviders(providers);
  if (!data && !error) return null; // old hubs do not expose profiles

  return <>
    <Group anchor="model-profiles" title={t("settings-modelprofiles.title")}
      footer={data && !data.active ? `${t("settings-modelprofiles.footer")} ${t("settings-modelprofiles.none_active")}` : t("settings-modelprofiles.footer")}>
      {data?.profiles.map((p) => (
        <Row key={p.id} label={p.name} hint={p.description} onClick={() => { setSelected(p.id); setEditing(false); clearErrors(); }}>
          {p.active && <Pill>{t("settings-modelprofiles.active")}</Pill>}
        </Row>
      ))}
      {data?.rollback && (
        <Row label={t("settings-modelprofiles.rollback")} hint={data.rollback.to || t("settings-modelprofiles.custom")}>
          <Button tone="quiet" busy={busy} onClick={() => void apply()}>{t("settings-modelprofiles.rollback")}</Button>
        </Row>
      )}
    </Group>
    {!profile && error && <p role="alert" className="mb-4 text-meta text-red">{error}</p>}
    <Dialog
      open={!!profile}
      width={editing ? 560 : 480}
      title={profile ? (editing ? t("settings-modelprofiles.edit_title", { name: profile.name }) : profile.name) : ""}
      onClose={closeDialog}
      footer={
        editing ? undefined : <>
          <Button tone="quiet" disabled={busy} onClick={closeDialog}>{t("common.cancel")}</Button>
          <Button tone="secondary" disabled={busy} onClick={startEditing}>{t("settings-modelprofiles.edit")}</Button>
          <Button tone="primary" busy={busy} onClick={() => profile && void apply(profile.id)}>{t("settings-modelprofiles.apply")}</Button>
        </>
      }
    >
      {profile && editing ? (
        <Suspense fallback={<Loading />}>
          <ModelProfileEditor
            manager={manager}
            profile={profile}
            revision={editRevision}
            purposes={purposes}
            providers={providers}
            busy={busy}
            error={error}
            conflict={conflict}
            onSave={(input) => save(profile.id, input)}
            onReload={reloadForEdit}
            onCancel={() => { setEditing(false); clearErrors(); }}
          />
        </Suspense>
      ) : profile ? (
        <>
          {profile.description && <p className="mb-3 text-label text-ink-3">{profile.description}</p>}
          <p className="mb-3 text-label text-ink-3">{t("settings-modelprofiles.changes", { count: profile.changes.length })}</p>
          {profile.active && profile.changes.length > 0 && (
            <p className="mb-4 rounded-control bg-inset px-3 py-2 text-label text-ink-2">{t("settings-modelprofiles.active_drift")}</p>
          )}

          <p className="mb-1.5 text-label font-semibold text-ink-2">{t("settings-modelprofiles.tiers")}</p>
          {tiers.length ? (
            <ul data-testid="model-profile-tiers" className="mb-4 flex flex-col gap-1.5">
              {tiers.map(([name, x]) => {
                // the launcher by its display name, its startup ID after it in small mono (#5029); an rc alias by its ID
                const l = namedLauncher(x.launcher, names);
                const id = providerIdNote(l);
                const speed = x.speed === "fast" || x.speed === "ultrafast" ? x.speed : "";
                return (
                  <li key={name} className="rounded-control bg-field px-3 py-2 text-label">
                    <div className="flex min-w-0 items-baseline gap-2">
                      <span className="shrink-0 font-mono text-ink">{name}</span>
                      <span className="min-w-0 [overflow-wrap:anywhere] text-ink-3">
                        {providerTitle(l)}
                        {id && <> <span className="font-mono text-submeta">{id}</span></>}
                        {[x.model, x.effort, x.permission_mode && modeTitle(x.permission_mode), speed].filter(Boolean).map((v) => ` · ${v}`).join("")}
                      </span>
                    </div>
                    {x.description && <p className="mt-0.5 text-ink-2">{x.description}</p>}
                  </li>
                );
              })}
            </ul>
          ) : (
            <p className="mb-4 text-label text-ink-3">{t("settings-modelprofiles.no_tiers")}</p>
          )}

          <p className="mb-1.5 text-label font-semibold text-ink-2">{t("settings-modelprofiles.purposes_section")}</p>
          {purposeEntries.length ? (
            <ul data-testid="model-profile-purposes" className="mb-4 flex flex-col gap-1.5">
              {purposeEntries.map(([id, b]) => {
                const summary = [
                  b.provider ? (providerName(b.provider, providers) ?? b.provider) : t("settings-modelprofiles.purpose_follow_default"),
                  b.model,
                  b.effort && effortTitle(b.effort),
                  b.permission_mode && modeTitle(b.permission_mode),
                ].filter(Boolean).join(" · ");
                return (
                  <li key={id} className="flex items-baseline justify-between gap-2 rounded-control bg-field px-3 py-2 text-label">
                    <span className="shrink-0 font-medium text-ink">{purposeLabel(id, purposes)}</span>
                    <span className="min-w-0 truncate text-right text-ink-3">{summary}</span>
                  </li>
                );
              })}
            </ul>
          ) : (
            <p className="mb-4 text-label text-ink-3">{t("settings-modelprofiles.no_purposes")}</p>
          )}

          <p className="mb-1.5 text-label font-semibold text-ink-2">{t("settings-modelprofiles.launchers_section")}</p>
          {launcherEntries.length ? (
            <ul data-testid="model-profile-launchers" className="mb-2 flex flex-col gap-1.5">
              {launcherEntries.map(([name, fields]) => {
                const l = namedLauncher(name, names);
                const id = providerIdNote(l);
                const items = LAUNCHER_MODEL_FIELDS.filter((f) => fields[f] !== undefined).map((f) => ({
                  field: f,
                  value: fields[f]!,
                }));
                return (
                  <li key={name} className="rounded-control bg-field px-3 py-2 text-label">
                    <div className="font-medium text-ink">
                      {providerTitle(l)}
                      {id && <> <span className="font-mono text-submeta text-ink-3">{id}</span></>}
                    </div>
                    <div className="mt-1 flex flex-col gap-0.5 text-submeta text-ink-3">
                      {items.map(({ field, value }) => (
                        <div key={field} className="flex items-baseline justify-between gap-2">
                          <span>{t(`settings-modelprofiles.field_${field}`)}</span>
                          <span className="min-w-0 truncate font-mono text-ink-2">
                            {value === "" ? t("settings-modelprofiles.clear_on_switch") : field === "effort" ? effortTitle(value) : value}
                          </span>
                        </div>
                      ))}
                    </div>
                  </li>
                );
              })}
            </ul>
          ) : (
            <p className="mb-2 text-label text-ink-3">{t("settings-modelprofiles.no_launchers")}</p>
          )}

          {error && <p role="alert" className="mt-3 text-label text-red">{error}</p>}
        </>
      ) : null}
    </Dialog>
  </>;
}
