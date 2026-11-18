// Assistant-owned references, not checkout locations or task authorization.
import { useEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, credentialOwner, errorText, getToken, HubTimeoutError, pastHub } from "../../lib/api";
import { parseRepository, remoteHost, type RepositoryIdentity as Identity } from "../../lib/repository-identity";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import {
  isExtensionProvider, loadRepositoryProviders, repositoryProviderName, repositorySource, validProviderName, validRepositoryIdentity,
  type RepositoryBinding as Binding, type RepositoryProvider,
} from "../../lib/task-repositories";
import { Button, Dialog, Loading, toast } from "../../ui/atoms";
import { RepositoryMark } from "../../ui/RepositoryMark";
import { Group, Pill, Row, settingsInputClass } from "./SettingsParts";

type Draft = { id?: string; url: string; provider: string; name: string; aliases: string; purpose: string };
type Reply = { manager: string; repositories?: Binding[]; repository?: Binding; identity?: Identity; provider_name?: string; receipt?: string };
/** An extension source's reading of an address (`key`: its source and the address), or why it could not read it. */
type Preview = { key: string; identity?: Identity; name?: string; error?: string };
const blank = (): Draft => ({ url: "", provider: "", name: "", aliases: "", purpose: "" });
const validBinding = (value?: Binding): value is Binding => validRepositoryIdentity(value) && typeof value.id === "string"
  && validProviderName(value.provider_name) && typeof value.verified_at === "string" && Number.isFinite(Date.parse(value.verified_at));
/** How long typing pauses before an extension source is asked to read the address. */
const PREVIEW_PAUSE_MS = 400;

export function Repositories({ manager, owner }: { manager: string; owner: string }) {
  const [token] = useState(getToken);
  const alive = useRef(true);
  const locked = useRef(false);
  const [expanded, setExpanded] = useState(false);
  const [rows, setRows] = useState<Binding[] | null>(null);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [draft, setDraft] = useState<Draft | null>(null);
  const [removing, setRemoving] = useState<Binding | null>(null);
  // The sources of repositories, built-ins first; undefined until read (or when it could not be read): only the
  // built-ins are offered then, and no saved row is said to be unavailable.
  const [providers, setProviders] = useState<readonly RepositoryProvider[]>();
  const [preview, setPreview] = useState<Preview>();
  // Bumped by Try again: the same address read once more after its source failed.
  const [retries, setRetries] = useState(0);
  const sources = providers?.filter((value) => !value.builtin);
  const original = rows?.find((value) => value.id === draft?.id);
  // The edited repository's source as listed now, followed through its extension's renames (the hub keeps the id it was
  // saved with); undefined when it is not installed or the sources are not read.
  const savedSource = original && isExtensionProvider(original.provider) ? repositorySource(original, providers) : undefined;
  // An extension's address goes to its source: the one picked, or the one that declares the address's host. GitHub and
  // GitLab, picked or detected, keep the local parse.
  const extensionId = !draft ? undefined : isExtensionProvider(draft.provider) ? draft.provider
    : draft.provider === "" ? sources?.find((value) => value.hosts.includes(remoteHost(draft.url) ?? ""))?.id : undefined;
  const ownSource = !!extensionId && !!original && (original.provider === extensionId || savedSource?.id === extensionId);
  const source = ownSource ? savedSource : sources?.find((value) => value.id === extensionId);
  const sourceName = (value: Pick<Binding, "provider" | "instance_url" | "provider_name">) =>
    repositorySource(value, providers)?.name || repositoryProviderName(value.provider, undefined, value.provider_name) || t("settings-repositories.another_source");
  const extensionName = !extensionId ? "" : ownSource ? sourceName(original!) : source?.name || t("settings-repositories.another_source");
  // A saved extension repository sent back with its own address keeps its identity: no source is asked (it may be off).
  const keep = ownSource && original!.canonical_url === draft?.url;
  const onHost = !!source && source.hosts.includes(remoteHost(draft?.url ?? "") ?? "");
  // Until the sources are read (or when they could not be), an extension picked for a saved repository is asked
  // anyway: the hub says whether its source can read the address, rather than this page calling it unavailable.
  const previewKey = extensionId && !keep && (!providers || source?.state === "available" && onHost) ? `${extensionId}\n${draft!.url}` : "";
  const previewed = previewKey && preview?.key === previewKey ? preview : undefined;
  const identity: Identity | undefined = !draft ? undefined : !extensionId ? parseRepository(draft.url, draft.provider)
    : keep ? original : previewed?.identity;
  const needsCheck = !original || original.canonical_url !== identity?.canonical_url || original.provider !== identity?.provider;
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  const current = () => alive.current && owner === credentialOwner();
  const managerNow = useRef(manager);
  managerNow.current = manager;
  async function request(path: string, method = "GET", json?: unknown, signal?: AbortSignal) {
    const result = await api<Reply>(`/assistant/repositories${path}`, {
      method, json, token, tokenOwner: owner, isAuthContextCurrent: current, headers: { [MANAGER_HEADER]: manager },
      timeout: method === "GET" ? undefined : pastHub(8_000), signal,
    });
    if (result.manager !== manager) throw new Error(t("settings-repositories.load_failed"));
    return result;
  }
  // Read beside the list, never in its way: a failure (an older manager's 404 included) leaves only the built-ins.
  const providersRead = useRef(0);
  const loadProviders = () => {
    const read = ++providersRead.current;
    const isCurrent = () => current() && read === providersRead.current && managerNow.current === manager;
    loadRepositoryProviders({ manager, owner, token, isCurrent }).then((list) => { if (isCurrent()) setProviders(list); }, () => {});
  };
  // The source reads an extension's address once typing pauses; a newer address or a closed dialog retires the read.
  const previewRead = useRef(0);
  useEffect(() => {
    if (!previewKey) return;
    const read = ++previewRead.current;
    const controller = new AbortController();
    const split = previewKey.indexOf("\n");
    const [provider, url] = [previewKey.slice(0, split), previewKey.slice(split + 1)];
    const settle = (value: Omit<Preview, "key">) => { if (read === previewRead.current && current()) setPreview({ key: previewKey, ...value }); };
    const timer = setTimeout(() => {
      request("/preview", "POST", { url, provider }, controller.signal).then((result) => {
        if (!validRepositoryIdentity(result.identity) || !isExtensionProvider(result.identity.provider) || !validProviderName(result.provider_name)) {
          throw new Error(t("settings-repositories.load_failed"));
        }
        settle({ identity: result.identity, name: result.provider_name });
      }).catch((e) => { if (!controller.signal.aborted) settle({ error: errorText(e) }); });
    }, PREVIEW_PAUSE_MS);
    return () => { clearTimeout(timer); controller.abort(); };
    // request and current read only this render's manager and credentials, which a new key reads again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [previewKey, retries]);
  // A source's failure is never final: Try again asks once more, and a changed address or source asks afresh (an
  // address typed back to one that failed reads again, rather than showing the old failure).
  const retryPreview = () => { setPreview(undefined); setRetries((n) => n + 1); };
  const dropFailedPreview = () => setPreview((value) => value?.error ? undefined : value);
  async function run(kind: string, action: () => Promise<void>) {
    if (locked.current || !current()) return;
    locked.current = true; setBusy(kind); setError("");
    try { await action(); }
    catch (e) { if (current()) setError(e instanceof HubTimeoutError && kind !== "load" ? t("settings-repositories.save_unconfirmed") : errorText(e)); }
    finally { locked.current = false; if (current()) setBusy(""); }
  }
  const load = () => run("load", async () => {
    const result = await request("");
    if (!Array.isArray(result.repositories) || !result.repositories.every(validBinding)) throw new Error(t("settings-repositories.load_failed"));
    if (current()) setRows(result.repositories);
  });
  const body = () => ({
    url: identity!.canonical_url, provider: identity!.provider, name: draft!.name,
    aliases: draft!.aliases.split(",").map((v) => v.trim()).filter(Boolean), purpose: draft!.purpose,
  });
  const save = () => run("save", async () => {
    const result = await request(draft!.id ? `/${encodeURIComponent(draft!.id)}` : "", draft!.id ? "PUT" : "POST", body());
    if (!validBinding(result.repository) || result.repository.provider !== identity!.provider
      || result.repository.instance_url !== identity!.instance_url || result.repository.path !== identity!.path
      || result.repository.canonical_url !== identity!.canonical_url || (draft!.id && result.repository.id !== draft!.id)) {
      throw new Error(t("settings-repositories.save_unconfirmed"));
    }
    if (!current()) return;
    const saved = result.repository;
    setRows((values) => [...(values ?? []).filter((v) => v.id !== saved.id), saved]);
    setDraft(null); toast(t("settings-repositories.saved"), "ok");
  });
  const unbind = () => run("unbind", async () => {
    const target = removing!;
    const result = await request(`/${encodeURIComponent(target.id)}`, "DELETE");
    if (!result.receipt) throw new Error(t("settings-repositories.save_unconfirmed"));
    if (!current()) return;
    setRows((values) => values!.filter((v) => v.id !== target.id)); setRemoving(null);
    toast(t("settings-repositories.unbound"), "info", { label: t("settings-repositories.undo"), run: () => {
      void run("restore", async () => {
        const restored = await request(`/${encodeURIComponent(target.id)}/restore`, "POST", { receipt: result.receipt });
        if (!validBinding(restored.repository)) throw new Error(t("settings-repositories.save_unconfirmed"));
        if (current()) setRows((values) => [...(values ?? []).filter((v) => v.id !== target.id), restored.repository!]);
      });
    } });
  });
  const edit = (value: Binding) => {
    setError(""); setPreview(undefined);
    setDraft({ id: value.id, url: value.canonical_url, provider: value.provider, name: value.name ?? "", aliases: value.aliases?.join(", ") ?? "", purpose: value.purpose ?? "" });
  };
  // The list first, the sources beside it: the list never waits on them.
  const refresh = () => { void load(); loadProviders(); };
  const showSources = !!providers && (!!sources?.length || !!rows?.some((value) => isExtensionProvider(value.provider)));
  const field = (key: "url" | "name" | "aliases" | "purpose", label: string, max: number) => (
    <label className="block text-xs text-ink-2">{label}
      <input className={`${settingsInputClass} mt-1`} value={draft![key]} maxLength={max} disabled={!!busy} autoComplete="off" spellCheck={false}
        onChange={(e) => { setDraft({ ...draft!, [key]: e.target.value }); if (key === "url") { setError(""); dropFailedPreview(); } }} />
    </label>
  );
  return <Group anchor="repositories" title={t("settings-repositories.title")} footer={t("settings-repositories.hint")}>
    <Row label={t("settings-repositories.title")} hint={t("settings-repositories.scope")}>
      <Button expanded={expanded} onClick={() => { setExpanded(!expanded); if (!expanded && rows === null) refresh(); }}>{t("settings-repositories.manage")}</Button>
    </Row>
    {expanded && <div className="space-y-3 px-4 py-3">
      <div className="flex flex-wrap gap-2">
        <Button disabled={!!busy || rows === null} onClick={() => { setError(""); setPreview(undefined); setDraft(blank()); }}>{t("settings-repositories.bind")}</Button>
        <Button tone="quiet" disabled={!!busy} onClick={refresh}>{t("settings-repositories.refresh")}</Button>
      </div>
      {!!busy && <Loading label={t(busy === "save" ? needsCheck ? "settings-repositories.checking" : "settings-repositories.saving" : "common.loading")} />}
      {error && !draft && !removing && <p role="alert" className="text-xs text-red">{error}</p>}
      {rows?.length === 0 && <p className="text-meta text-ink-3">{t("settings-repositories.empty")}</p>}
      {rows?.map((value) => <div key={value.id} className="space-y-1 border-t border-line pt-3" data-repository={value.id}>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="min-w-0 break-words text-meta font-medium text-ink">{value.name || value.path}</span>
          <div className="flex gap-1">
            <Button tone="quiet" disabled={!!busy} onClick={() => edit(value)}>{t("settings-repositories.edit")}<span className="sr-only"> {value.name || value.path}</span></Button>
            <Button tone="quiet" disabled={!!busy} onClick={() => { setError(""); setRemoving(value); }}>{t("settings-repositories.unbind")}<span className="sr-only"> {value.name || value.path}</span></Button>
          </div>
        </div>
        <p className="break-all font-mono text-xs text-ink-2">{value.canonical_url}</p>
        {isExtensionProvider(value.provider) && <SavedSource provider={value.provider} name={sourceName(value)} state={providers && (repositorySource(value, providers)?.state ?? "missing")} />}
        {value.aliases?.length ? <p className="text-label text-ink-2">{t("settings-repositories.aliases")}: {value.aliases.join(", ")}</p> : null}
        {value.purpose && <p className="break-words text-xs text-ink-2">{value.purpose}</p>}
        <p className="text-xs text-ink-3">{t("settings-repositories.verified", { time: new Date(value.verified_at).toLocaleString() })}</p>
      </div>)}
      {showSources && <div className="space-y-2 border-t border-line pt-3" data-repository-sources>
        <h3 className="text-meta font-medium text-ink">{t("settings-repositories.sources")}</h3>
        <p className="text-xs leading-relaxed text-ink-3">{t("settings-repositories.sources_hint")}</p>
        <ul className="space-y-1.5">
          {providers!.map((value) => <li key={value.id} data-repository-source={value.id} className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
            <RepositoryMark provider={value.id} />
            <span className="text-meta text-ink">{value.name}</span>
            {value.builtin ? <span className="text-xs text-ink-3">{t("settings-repositories.builtin")}</span>
              : <span className="min-w-0 break-all font-mono text-xs text-ink-3">{value.hosts.join(", ")}</span>}
            {value.state === "off" && <Pill>{t("settings-repositories.unavailable")}</Pill>}
          </li>)}
        </ul>
      </div>}
    </div>}
    <Dialog open={!!draft} onClose={() => { if (!busy) { setDraft(null); setError(""); } }} title={t(draft?.id ? "settings-repositories.edit_title" : "settings-repositories.bind")}
      footer={<><Button disabled={!!busy} onClick={() => { setDraft(null); setError(""); }}>{t("common.cancel")}</Button><Button tone="primary" busy={busy === "save"} disabled={!!busy || !identity} onClick={() => void save()}>{t(draft?.id ? "settings-repositories.save" : "settings-repositories.check_save")}</Button></>}>
      {draft && <div className="space-y-3">
        {field("url", t("settings-repositories.url"), 2048)}
        <label className="block text-xs text-ink-2">{t("settings-repositories.provider")}
          <select className={`${settingsInputClass} mt-1`} value={draft.provider} disabled={!!busy} onChange={(e) => { setDraft({ ...draft, provider: e.target.value }); setError(""); dropFailedPreview(); }}>
            <option value="">{t("settings-repositories.detect")}</option><option value="github">GitHub / GitHub Enterprise</option><option value="gitlab">GitLab</option>
            {/* An extension's source by its name; one that is off can't read a new address, but stays shown for the
                repository saved from it, under the id that repository keeps (renames included). */}
            {sources?.map((value) => {
              const own = !!savedSource && value.id === savedSource.id;
              return <option key={value.id} value={own ? original!.provider : value.id} disabled={value.state !== "available" && !own}>
                {value.state === "available" ? value.name : t("settings-repositories.option_unavailable", { provider: value.name })}
              </option>;
            })}
            {original && isExtensionProvider(original.provider) && !savedSource && <option value={original.provider}>
              {providers ? t("settings-repositories.option_unavailable", { provider: sourceName(original) }) : sourceName(original)}
            </option>}
          </select>
        </label>
        {identity && <div data-repository-preview role="status" aria-live="polite" className="rounded-control bg-field p-3 text-label text-ink-2">
          <p>{extensionId ? previewed?.name || extensionName : identity.provider} · {identity.instance_url}</p><p className="break-all font-mono">{identity.path}</p>
          <p className="break-all font-mono">{identity.canonical_url}</p>
          <p>{!needsCheck ? t("settings-repositories.edit_check") : extensionId ? t("settings-repositories.unchecked_extension", { provider: previewed?.name || extensionName })
            : t("settings-repositories.unchecked")}</p>
        </div>}
        {draft.url && !identity && !extensionId && <p role="status" className="text-label text-ink-2">{t(sources?.length ? "settings-repositories.invalid_url_sources" : "settings-repositories.invalid_url")}</p>}
        {draft.url && !identity && extensionId && <ExtensionStatus name={extensionName} source={source} known={!!providers} onHost={onHost}
          waiting={!!previewKey && !previewed} error={previewed?.error} onRetry={retryPreview} />}
        {field("name", t("settings-repositories.name"), 80)}
        {field("aliases", t("settings-repositories.aliases"), 809)}
        {field("purpose", t("settings-repositories.purpose"), 500)}
        <p className="text-xs leading-relaxed text-ink-3">{extensionId ? t("settings-repositories.check_hint_extension", { provider: extensionName }) : t("settings-repositories.check_hint")}</p>
        {busy === "save" && <Loading label={t(needsCheck ? "settings-repositories.checking" : "settings-repositories.saving")} />}
        {error && <p role="alert" className="text-xs text-red">{error}</p>}
      </div>}
    </Dialog>
    <Dialog open={!!removing} onClose={() => { if (!busy) { setRemoving(null); setError(""); } }} title={t("settings-repositories.unbind_title")}
      footer={<><Button disabled={!!busy} onClick={() => { setRemoving(null); setError(""); }}>{t("common.cancel")}</Button><Button tone="danger" busy={busy === "unbind"} disabled={!!busy} onClick={() => void unbind()}>{t("settings-repositories.unbind_title")}</Button></>}>
      <p className="break-all font-mono text-label text-ink">{removing?.canonical_url}</p>
      <p className="mt-3 text-meta leading-relaxed text-ink-2">{t("settings-repositories.unbind_hint")}</p>
      {error && <p role="alert" className="mt-3 text-xs text-red">{error}</p>}
    </Dialog>
  </Group>;
}

/**
 * A saved extension repository's source by name, and whether it can still check the repository. `state` is unknown
 * while the sources are not read: nothing is claimed then.
 */
function SavedSource({ provider, name, state }: { provider: string; name: string; state?: RepositoryProvider["state"] | "missing" }) {
  const unavailable = state === "off" || state === "missing";
  return <>
    <p className="flex flex-wrap items-center gap-1.5 text-xs text-ink-2" data-repository-source-name>
      <RepositoryMark provider={provider} size={12} />{name}{unavailable && <Pill>{t("settings-repositories.unavailable")}</Pill>}
    </p>
    {unavailable && <p data-repository-note className="text-xs leading-relaxed text-ink-3">{t("settings-repositories.unavailable_hint", { provider: name })}</p>}
  </>;
}

/**
 * Where an extension's address stands before its source answers: reading it, why it can't, or the source's refusal
 * with Try again. `known`: the sources were read; until then only the hub's answer says the source is unavailable.
 */
function ExtensionStatus({ name, source, known, onHost, waiting, error, onRetry }: {
  name: string; source?: RepositoryProvider; known: boolean; onHost: boolean; waiting: boolean; error?: string; onRetry: () => void;
}) {
  if (known && (!source || source.state !== "available")) return <p role="status" className="text-label text-ink-2">{t("settings-repositories.source_unavailable", { provider: name })}</p>;
  if (known && source && !onHost) return <p role="status" className="text-label text-ink-2">{t("settings-repositories.host_mismatch", { provider: name, hosts: source.hosts.join(", ") })}</p>;
  if (waiting) return <Loading label={t("settings-repositories.previewing", { provider: name })} />;
  return error ? <div className="space-y-2" data-repository-preview-failed>
    <p role="status" className="text-label text-ink-2">{error}</p>
    <Button tone="quiet" onClick={onRetry}>{t("settings-repositories.retry")}</Button>
  </div> : null;
}
