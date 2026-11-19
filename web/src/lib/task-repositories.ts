import type { Todo, TodoCreateOptions } from "./todos";
import { confirmTodoCreate } from "./todos";
import { api, ApiError, credentialOwner, getToken, pastHub } from "./api";
import { MANAGER_HEADER } from "./manager-choice";
import { parseRepository, type RepositoryIdentity } from "./repository-identity";

export type RepositoryBinding = RepositoryIdentity & {
  id: string; name?: string; aliases?: string[]; purpose?: string; verified_at: string; clone_url?: string;
  /** An extension source's name when the reference was saved, so it still reads once the source is gone. */
  provider_name?: string;
};
export type RepositoryLinkKind = "issue" | "pull_request";
/** A source's own word for a link kind (MR for a pull request); the kinds stay these two. */
export type RepositoryLinkLabel = { kind: RepositoryLinkKind; label: string };
/** How a to-do's extension row's source is now: `missing` once it is uninstalled. */
export type RepositoryProviderState = "available" | "off" | "missing";
/** A source of repositories (docs/extensions/repository-providers.md): GitHub and GitLab built in, then extensions'. */
export type RepositoryProvider = {
  id: string; name: string; builtin: boolean; hosts: string[]; state: "available" | "off"; link_kinds?: RepositoryLinkLabel[];
};
export type RequestedRef = { kind: "default"; value?: never } | { kind: "branch" | "tag" | "commit" | "ref"; value: string };
export type RepositoryLayout = "root" | "container";
export type RepositorySourceSnapshot = {
  /** `provider`: an extension's row cloned from the address its source answered (never a GitHub or GitLab row). */
  identity: RepositoryIdentity; clone_url: string; transport_origin: "catalog" | "canonical_default" | "provider"; catalog_revision: number;
  provider_name?: string;
};
export type RepositoryLinkInput = { url: string; kind?: never; number?: never; title?: string } | { kind: "issue" | "pull_request"; number: string; url?: never; title?: string };
export type TaskRepositoryLink = { kind: "issue" | "pull_request"; number: string; canonical_url: string; title?: string };
export type TaskRepositoryInput = { repo_id: string; directory_alias?: string; requested_ref?: RequestedRef; links?: RepositoryLinkInput[] };
export type TaskRepository = Omit<TaskRepositoryInput, "links" | "requested_ref"> & {
  requested_ref: RequestedRef; source_snapshot: RepositorySourceSnapshot; links: TaskRepositoryLink[];
  /** `unlisted`: the Agent linked a repository that is not in the saved catalog (its source is kept). */
  catalog_state?: "current" | "removed" | "identity_changed" | "unlisted";
  /** An extension source's rows only, computed on reads like `catalog_state`: how the source is now, its link words. */
  provider_state?: RepositoryProviderState; link_kinds?: RepositoryLinkLabel[];
};
export type TaskRepositorySet = { schema_version: 1; manager: string; todo_id: string; revision: number; repositories: TaskRepository[] };
export type RepositoryCatalog = { manager: string; catalog_revision: number; repositories: RepositoryBinding[] };
export type CreateRepositoryBindings = { catalog_revision: number; repositories: TaskRepositoryInput[] };
export type TaskRepositoryPreparationSnapshot = {
  schema_version: 1; manager: string; todo_id: string; revision: number; digest: string;
  repositories: Array<Pick<TaskRepository, "repo_id" | "directory_alias" | "requested_ref" | "source_snapshot">>;
};

/** The same conservative directory-name rule as repository.ValidateAlias in Go. Empty is no override. */
export function validRepositoryAlias(alias: string): boolean {
  if (!alias) return true;
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(alias) || alias.endsWith(".")) return false;
  return !/^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\.|$)/i.test(alias);
}
export function repositoryRelativePath(layout: RepositoryLayout, alias: string, count: number): string | undefined {
  if (layout === "root" && count === 1) return ".";
  return layout === "container" && !!alias && validRepositoryAlias(alias) ? alias : undefined;
}
const MAX_LINK_TITLE_BYTES = 1024;
/**
 * The server trims a source title and refuses controls or more than 1,024 UTF-8 bytes. Reading and writing with the
 * same rule keeps a saved title displayable and a draft saveable; an absent or empty title stays absent, never guessed.
 */
export function repositoryLinkTitle(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const title = value.trim();
  if (!title || /\p{Cc}/u.test(title) || new TextEncoder().encode(title).length > MAX_LINK_TITLE_BYTES) return undefined;
  return title;
}
export function repositoryDraft(rows: TaskRepository[]): TaskRepositoryInput[] {
  return rows.map((row) => ({ repo_id: row.repo_id, directory_alias: row.directory_alias, requested_ref: { ...row.requested_ref },
    links: row.links.map(({ kind, number, title }) => {
      const kept = repositoryLinkTitle(title);
      return { kind, number, ...(kept ? { title: kept } : {}) };
    }) }));
}

const EXTENSION_PROVIDER = /^ext:[a-z0-9][a-z0-9_-]{0,31}\/[a-z0-9][a-z0-9_-]{0,31}$/;
/** An extension's source id, `ext:<extension>/<contribution>`, each part the manifest's id rule. */
export const isExtensionProvider = (provider: unknown): boolean => typeof provider === "string" && EXTENSION_PROVIDER.test(provider);
const LINK_NUMBER = /^[1-9][0-9]{0,19}$/;
const BUILTIN_NAMES: Record<string, string> = { github: "GitHub", gitlab: "GitLab" };

/**
 * One issue or pull request of a bound repository, ready to display. `pending`: an address pasted for an extension's
 * repository, which its source reads when the to-do is saved, so its kind and number are not known before then.
 */
export type RepositoryLinkView =
  | { kind: RepositoryLinkKind; number: string; url: string; label: string; title?: string; pending?: undefined }
  | { pending: true; url: string; label: string; kind?: undefined; number?: undefined; title?: undefined };
const LINK_SEGMENTS = {
  github: { issue: "issues", pull_request: "pull" },
  gitlab: { issue: "-/issues", pull_request: "-/merge_requests" },
} as const;
const linkURL = (identity: RepositoryIdentity, kind: "issue" | "pull_request", number: string) =>
  `${identity.canonical_url}/${LINK_SEGMENTS[identity.provider as keyof typeof LINK_SEGMENTS][kind]}/${number}`;

/**
 * The manager's rule for a link of an extension's repository (repository.underRepository): an https address strictly
 * below the repository's own, of plain path segments, so with no credentials, query or fragment. The web cannot build
 * such a link from a kind and number; it keeps and opens the address the manager stored.
 */
export function underRepository(identity: RepositoryIdentity, url: unknown): boolean {
  const prefix = `${identity.canonical_url}/`;
  if (typeof url !== "string" || url.length > 2048 || !url.startsWith("https://") || !url.startsWith(prefix)) return false;
  const rest = url.slice(prefix.length).replace(/\/$/, "");
  const parts = rest.split("/");
  return !!rest && rest.length <= 512 && parts.length <= 20 && parts.every((part) => /^[A-Za-z0-9._~-]+$/.test(part) && part !== "." && part !== "..");
}

/**
 * Reads an issue/PR link of a repository from either form the store accepts: a pasted URL (held to the saved identity
 * exactly as `repository.NormalizeLink` does) or a saved kind + number. Null when it does not belong to this repository.
 * An extension's repository has no layout to rebuild, so a kind + number is drawn from the saved link it names
 * (`saved`), and a new address under the repository is pending until the save asks its source.
 */
export function repositoryLinkView(identity: RepositoryIdentity, link: TaskRepositoryLink | RepositoryLinkInput, saved: readonly TaskRepositoryLink[] = []): RepositoryLinkView | null {
  if (isExtensionProvider(identity.provider)) return extensionLinkView(identity, link, saved);
  const segments = LINK_SEGMENTS[identity.provider as keyof typeof LINK_SEGMENTS];
  if (!segments) return null;
  let kind: string | undefined, number: string | undefined;
  if ("url" in link && link.url !== undefined) {
    const raw = link.url.trim();
    if (!raw || raw.length > 2048) return null;
    let url: URL;
    try { url = new URL(raw); } catch { return null; }
    // The server accepts only http(s) link URLs, with no credentials, query (including a bare "?") or fragment.
    if (!["http:", "https:"].includes(url.protocol) || url.username || url.password
      || url.search || url.hash || (raw.includes("?") && !url.search)) return null;
    // Public HTTP aliases and repository-path case are normalized by the shared parser, as the server's Parse does;
    // self-hosted instances keep their exact scheme and port.
    const actual = parseRepository(raw, identity.provider);
    if (!actual || actual.provider !== identity.provider || actual.instance_url !== identity.instance_url
      || actual.path !== identity.path || actual.canonical_url !== identity.canonical_url) return null;
    const prefix = `/${identity.path.replace(/^\/+|\/+$/g, "").toLowerCase()}/`;
    const path = url.pathname.toLowerCase();
    for (const [candidate, segment] of Object.entries(segments)) {
      if (!path.startsWith(prefix + segment + "/")) continue;
      // The server reads the number to the end of the path, so a trailing slash or a deeper path is not this link.
      const rest = path.slice(prefix.length + segment.length + 1);
      if (!rest || rest.includes("/")) return null;
      kind = candidate; number = rest;
      break;
    }
  } else {
    kind = link.kind; number = link.number;
  }
  if ((kind !== "issue" && kind !== "pull_request") || !number || !LINK_NUMBER.test(number)) return null;
  const url = linkURL(identity, kind, number);
  // A saved link's canonical URL must still name this repository and this number. A title belongs to that exact link,
  // never to another repository or number that happens to be in the same response.
  if ("canonical_url" in link && link.canonical_url !== url) return null;
  const label = kind === "issue" ? `#${number}` : `${identity.provider === "gitlab" ? "MR !" : "PR #"}${number}`;
  const title = repositoryLinkTitle(link.title);
  return { kind, number, url, label, ...(title ? { title } : {}) };
}
function extensionLinkView(identity: RepositoryIdentity, link: TaskRepositoryLink | RepositoryLinkInput, saved: readonly TaskRepositoryLink[]): RepositoryLinkView | null {
  let stored: TaskRepositoryLink | undefined;
  if ("canonical_url" in link) stored = link;
  else if (link.url !== undefined) {
    const url = link.url.trim();
    if (!underRepository(identity, url)) return null;
    // The manager keeps a link the to-do has by its address too, without asking the source.
    stored = saved.find((value) => value.canonical_url === url);
    if (!stored) return { pending: true, url, label: url };
  } else stored = saved.find((value) => value.kind === link.kind && value.number === link.number);
  if (!stored || !validTaskRepositoryLink(identity, stored)) return null;
  const title = repositoryLinkTitle(link.title ?? stored.title);
  return { kind: stored.kind, number: stored.number, url: stored.canonical_url, label: `#${stored.number}`, ...(title ? { title } : {}) };
}
/** The same canonical-source check for a saved link, used before trusting a response's kind, number, URL or title. */
export function validTaskRepositoryLink(identity: RepositoryIdentity, link: TaskRepositoryLink): boolean {
  if (!link || !["issue", "pull_request"].includes(link.kind) || typeof link.number !== "string" || !LINK_NUMBER.test(link.number)) return false;
  if (isExtensionProvider(identity.provider)) {
    if (!underRepository(identity, link.canonical_url)) return false;
  } else {
    const segments = LINK_SEGMENTS[identity.provider as keyof typeof LINK_SEGMENTS];
    if (!segments || link.canonical_url !== linkURL(identity, link.kind, link.number)) return false;
  }
  // An empty title is the older servers' "none"; a nonempty one must be exactly what the server accepted (trimmed).
  return link.title === undefined || link.title === "" || repositoryLinkTitle(link.title) === link.title;
}
const revision = (n: unknown): n is number => Number.isSafeInteger(n) && (n as number) >= 0;
/**
 * Checks the server-owned envelope of an identity; repository normalization remains in the shared parser. An
 * extension's identity is always https (its source's instance), GitHub's and GitLab's may be self-hosted http.
 */
export function validRepositoryIdentity(identity: RepositoryIdentity | undefined): identity is RepositoryIdentity {
  const extension = !!identity && isExtensionProvider(identity.provider);
  if (!identity || (!extension && !["github", "gitlab"].includes(identity.provider)) || typeof identity.instance_url !== "string"
    || typeof identity.path !== "string" || !identity.path || identity.canonical_url !== `${identity.instance_url}/${identity.path}`) return false;
  try {
    const instance = new URL(identity.instance_url);
    return (extension ? instance.protocol === "https:" : ["https:", "http:"].includes(instance.protocol)) && instance.origin === identity.instance_url;
  } catch { return false; }
}
/** The same source-ref rule as repository.NormalizeRef in Go: an absent or empty ref is the default branch. */
export function validRequestedRef(ref?: RequestedRef | null): boolean {
  if (!ref) return true;
  if (typeof ref.kind !== "string") return false;
  const value = (ref as { value?: unknown }).value;
  if (!ref.kind || ref.kind === "default") return value === undefined || value === "";
  if (typeof value !== "string") return false;
  if (ref.kind === "commit") return /^[0-9a-fA-F]{40}$/.test(value) || /^[0-9a-fA-F]{64}$/.test(value);
  if (!["branch", "tag", "ref"].includes(ref.kind)) return false;
  if (!value || new TextEncoder().encode(value).length > 1024 || value === "@" || value.startsWith("-")
    || value.includes(" ") || /[~^:?*[\\]/.test(value) || value.includes("..") || value.includes("@{")
    || value.endsWith(".") || /\p{Cc}/u.test(value)) return false;
  if (ref.kind === "ref" && !value.startsWith("refs/")) return false;
  return value.split("/").every((part) => part !== "" && !part.startsWith(".") && !part.endsWith(".lock"));
}

/** A normalized link identity inside its repository: public aliases/case and structured kind+number meet here. */
export function repositoryLinkKey(identity: RepositoryIdentity, link: RepositoryLinkInput | TaskRepositoryLink, saved: readonly TaskRepositoryLink[] = []): string | undefined {
  const prefix = `${identity.provider}|${identity.instance_url}|${identity.path}|`;
  const view = repositoryLinkView(identity, link, saved);
  // A provider decides a new address's kind/number on save. Distinct pending addresses must not collapse into an
  // undefined kind/number; an address already saved can still meet its structured identity above.
  if (view) return prefix + (view.pending ? `url|${view.url}` : `${view.kind}|${view.number}`);
  if (isExtensionProvider(identity.provider) && !("canonical_url" in link) && link.url === undefined
    && (link.kind === "issue" || link.kind === "pull_request") && LINK_NUMBER.test(link.number)) return prefix + `${link.kind}|${link.number}`;
  return undefined;
}

/** True when two links would normalize to the same repository, kind and number (the server's duplicate_link). */
export function repositoryDuplicateLink(rows: TaskRepositoryInput[], identities?: ReadonlyMap<string, RepositoryIdentity>, snapshots: readonly TaskRepository[] = []): boolean {
  const seen = new Set<string>();
  for (const row of rows) {
    const identity = identities?.get(row.repo_id);
    const saved = snapshots.find((value) => value.repo_id === row.repo_id)?.links;
    for (const link of row.links ?? []) {
      const key = identity ? repositoryLinkKey(identity, link, saved)
        : "kind" in link ? `${row.repo_id}|${link.kind}|${link.number}` : undefined;
      if (key && seen.has(key)) return true;
      if (key) seen.add(key);
    }
  }
  return false;
}

/** The whole creation/edit draft: unique ids/aliases, valid refs, bounded links and no normalized duplicate link. */
export function validRepositoryDraft(rows: TaskRepositoryInput[], identities?: ReadonlyMap<string, RepositoryIdentity>, snapshots: readonly TaskRepository[] = []): boolean {
  if (!Array.isArray(rows) || rows.length > 20) return false;
  const ids = new Set<string>();
  const aliases = new Set<string>();
  const repositories = new Set<string>();
  const links = new Set<string>();
  return rows.every((row) => {
    if (!row || typeof row.repo_id !== "string" || !row.repo_id || row.repo_id.length > 80 || ids.has(row.repo_id)) return false;
    ids.add(row.repo_id);
    if (row.directory_alias !== undefined && typeof row.directory_alias !== "string") return false;
    if (!validRepositoryAlias(row.directory_alias ?? "")) return false;
    if (row.directory_alias) {
      const key = row.directory_alias.toLowerCase();
      if (aliases.has(key)) return false;
      aliases.add(key);
    }
    if (!validRequestedRef(row.requested_ref)) return false;
    const identity = identities?.get(row.repo_id);
    const saved = snapshots.find((value) => value.repo_id === row.repo_id)?.links;
    // An identity-aware draft must resolve every repository. Without identities a URL link cannot be proven to belong
    // to its repository or deduplicated, so that draft fails closed; structured/no-link metadata stays checkable.
    if (identities && !identity) return false;
    if (identity && !validRepositoryIdentity(identity)) return false;
    if (!identities && (row.links ?? []).some((link) => link && "url" in link)) return false;
    if (identity) {
      const key = `${identity.provider}|${identity.instance_url}|${identity.path}`;
      if (repositories.has(key)) return false;
      repositories.add(key);
    }
    const rowLinks = row.links ?? [];
    if (!Array.isArray(rowLinks) || rowLinks.length > 50) return false;
    return rowLinks.every((link) => {
      if (!link || !(("url" in link && typeof link.url === "string" && !!link.url.trim() && link.url.trim().length <= 2048)
        || (link.kind === "issue" || link.kind === "pull_request") && typeof link.number === "string" && /^[1-9][0-9]{0,19}$/.test(link.number))) return false;
      const key = identity ? repositoryLinkKey(identity, link, saved) : "kind" in link ? `${row.repo_id}|${link.kind}|${link.number}` : undefined;
      if (identity && !key) return false;
      if (key) {
        if (links.has(key)) return false;
        links.add(key);
      }
      return true;
    });
  });
}

/** A source's name as saved beside an identity: absent, or at most 80 characters (the manifest's `name`). */
export const validProviderName = (name: unknown): boolean => name === undefined || (typeof name === "string" && [...name].length <= 80);
const LINK_KINDS = ["issue", "pull_request"];
const validLinkKinds = (value: unknown): boolean => value === undefined || (Array.isArray(value) && value.length <= LINK_KINDS.length
  && value.every((entry: RepositoryLinkLabel) => !!entry && LINK_KINDS.includes(entry.kind) && typeof entry.label === "string"
    && !!entry.label.trim() && [...entry.label].length <= 24));
function validRepositoryProvider(value: RepositoryProvider): boolean {
  return !!value && typeof value.id === "string" && (value.builtin === true ? ["github", "gitlab"].includes(value.id) : value.builtin === false && isExtensionProvider(value.id))
    && typeof value.name === "string" && !!value.name.trim() && [...value.name].length <= 80
    && Array.isArray(value.hosts) && value.hosts.length <= 16 && value.hosts.every((host) => typeof host === "string")
    && ["available", "off"].includes(value.state) && validLinkKinds(value.link_kinds);
}
/** GitHub and GitLab, as the manager lists them first; all an older manager (no providers route) knows. */
export const BUILTIN_REPOSITORY_PROVIDERS: readonly RepositoryProvider[] = [
  { id: "github", name: "GitHub", builtin: true, hosts: ["github.com"], state: "available" },
  { id: "gitlab", name: "GitLab", builtin: true, hosts: ["gitlab.com"], state: "available" },
];
/**
 * The name a repository's source is shown by: the live source's, else the one saved with the repository, else the
 * built-in's. Never the raw `ext:` id: undefined lets the caller say "another source" in its own words.
 */
export function repositoryProviderName(provider: string, providers?: readonly RepositoryProvider[], saved?: string): string | undefined {
  return providers?.find((value) => value.id === provider)?.name || saved || BUILTIN_NAMES[provider] || undefined;
}
/**
 * The listed source a saved repository comes from. The hub keeps the provider id a repository was saved with and
 * follows its extension's renames, while the list has current ids only: so an extension's repository missing from it by
 * id is the listed extension source with the same contribution that claims the repository's host (an installed host
 * has one claim). Undefined when no listed source is it: not installed, or the sources are not read.
 */
export function repositorySource(identity: Pick<RepositoryIdentity, "provider" | "instance_url">, providers?: readonly RepositoryProvider[]): RepositoryProvider | undefined {
  const exact = providers?.find((value) => value.id === identity.provider);
  if (exact || !providers || !isExtensionProvider(identity.provider)) return exact;
  const contribution = identity.provider.slice(identity.provider.indexOf("/"));
  let host = "";
  try { host = new URL(identity.instance_url).hostname; } catch { return undefined; }
  return providers.find((value) => !value.builtin && value.id.endsWith(contribution) && value.hosts.includes(host));
}
/** A source's own word for a link kind (`link_kinds`), shown as given; undefined keeps the localized word. */
export const repositoryLinkKindLabel = (kind: RepositoryLinkKind, labels?: readonly RepositoryLinkLabel[]): string | undefined =>
  labels?.find((value) => value.kind === kind)?.label;
export function validRepositorySet(set: TaskRepositorySet, manager: string, todoId: string): boolean {
  return !!set && set.schema_version === 1 && set.manager === manager && set.todo_id === todoId && revision(set.revision)
    && Array.isArray(set.repositories) && set.repositories.length <= 20 && set.repositories.every((row) =>
      row && typeof row.repo_id === "string" && (row.directory_alias === undefined || typeof row.directory_alias === "string" && validRepositoryAlias(row.directory_alias))
      && validRequestedRef(row.requested_ref) && row.source_snapshot && validRepositoryIdentity(row.source_snapshot.identity)
      && validProviderName(row.source_snapshot.provider_name) && validLinkKinds(row.link_kinds)
      && (row.provider_state === undefined || ["available", "off", "missing"].includes(row.provider_state))
      && typeof row.source_snapshot.clone_url === "string" && revision(row.source_snapshot.catalog_revision)
      && (["catalog", "canonical_default"].includes(row.source_snapshot.transport_origin)
        || row.source_snapshot.transport_origin === "provider" && isExtensionProvider(row.source_snapshot.identity.provider))
      && Array.isArray(row.links) && row.links.every((link) => validTaskRepositoryLink(row.source_snapshot.identity, link)));
}
export type RepositoryRequestContext = { manager: string; owner: string; token: string; isCurrent: () => boolean };
export function repositoryRequestContext(manager: string, owner: string, active: () => boolean): RepositoryRequestContext {
  return { manager, owner, token: getToken(), isCurrent: () => active() && credentialOwner() === owner };
}
export function repositoryRequestOptions(context: RepositoryRequestContext) {
  if (!context.isCurrent()) throw new Error("The editor connection changed");
  return { token: context.token, tokenOwner: context.owner, isAuthContextCurrent: context.isCurrent, headers: { [MANAGER_HEADER]: context.manager } };
}
export async function loadRepositoryCatalog(context: RepositoryRequestContext): Promise<RepositoryCatalog> {
  const result = await api<RepositoryCatalog>("/assistant/repositories", repositoryRequestOptions(context));
  if (!context.isCurrent() || result.manager !== context.manager || !revision(result.catalog_revision)
    || !Array.isArray(result.repositories) || !result.repositories.every((r) => typeof r.id === "string" && validRepositoryIdentity(r) && validProviderName(r.provider_name))) throw new Error("Invalid repository catalog response");
  return result;
}
/**
 * The sources of repositories (`GET /assistant/repositories/providers`), built-ins first. An older manager has no such
 * route and answers 404 (its `/{id}` read): only the built-ins then. Any other failure throws, for the caller to fall
 * back to the built-ins without failing what it shows.
 */
export async function loadRepositoryProviders(context: RepositoryRequestContext): Promise<readonly RepositoryProvider[]> {
  let result: { manager?: string; providers?: RepositoryProvider[] };
  try {
    result = await api<typeof result>("/assistant/repositories/providers", repositoryRequestOptions(context));
  } catch (failure) {
    if (failure instanceof ApiError && failure.status === 404 && context.isCurrent()) return BUILTIN_REPOSITORY_PROVIDERS;
    throw failure;
  }
  if (!context.isCurrent() || result.manager !== context.manager || !Array.isArray(result.providers)
    || !result.providers.every(validRepositoryProvider)) throw new Error("Invalid repository providers response");
  return result.providers;
}
export async function loadTaskRepositories(context: RepositoryRequestContext, todoId: string): Promise<TaskRepositorySet> {
  const result = await api<TaskRepositorySet>(`/todos/${encodeURIComponent(todoId)}/repositories`, repositoryRequestOptions(context));
  if (!context.isCurrent() || !validRepositorySet(result, context.manager, todoId)) throw new Error("Invalid repository association response");
  return result;
}
export async function saveTaskRepositories(context: RepositoryRequestContext, todoId: string, revision: number, catalogRevision: number, rows: TaskRepositoryInput[]): Promise<TaskRepositorySet> {
  const result = await api<TaskRepositorySet>(`/todos/${encodeURIComponent(todoId)}/repositories`, {
    ...repositoryRequestOptions(context), method: "PUT", timeout: pastHub(8_000),
    json: { expected_revision: revision, expected_catalog_revision: catalogRevision, repositories: rows },
  });
  if (!context.isCurrent() || !validRepositorySet(result, context.manager, todoId) || result.revision !== revision + 1) throw new Error("Invalid repository association response");
  return result;
}

export function cleanRepositoryDraft(rows: TaskRepositoryInput[]): TaskRepositoryInput[] {
  return rows.map((row) => ({ ...row, links: (row.links ?? [])
    .filter((link) => !("url" in link) || !!link.url?.trim())
    .map((link): RepositoryLinkInput => {
      const title = repositoryLinkTitle(link.title);
      return "url" in link
        ? { url: link.url!.trim(), ...(title ? { title } : {}) }
        : { kind: link.kind, number: link.number, ...(title ? { title } : {}) };
    }) }));
}

/** Explicit association creation stays outside the ordinary Todo client's startup path. */
export async function createTodoWithRepositories(context: RepositoryRequestContext, title: string, notes: string, due: { due_at?: string; due_date?: string } | undefined, bindings: CreateRepositoryBindings, identities: ReadonlyMap<string, RepositoryIdentity>, creation?: TodoCreateOptions): Promise<Todo> {
  const repositories = bindings ? cleanRepositoryDraft(bindings.repositories) : [];
  // The form checks this too; the submit guard keeps an invalid optional draft from ever becoming a POST. A missing
  // runtime identity map must fail closed even when a JavaScript caller omits the TypeScript parameter.
  const hasIdentities = !!identities && typeof (identities as { get?: unknown }).get === "function";
  if (!bindings || !revision(bindings.catalog_revision) || !hasIdentities || !validRepositoryDraft(repositories, identities)) throw new Error("Invalid repository association draft");
  const withImages = Boolean(creation?.images?.length);
  const request = repositoryRequestOptions(context);
  const result = await api<{ todo?: Todo; state?: string }>("/todos", {
    ...request, method: "POST",
    headers: withImages ? { ...request.headers, "X-Persona-Op-Id": creation!.operationId! } : request.headers,
    json: { title: title.trim(), notes: notes.trim() || undefined, ...due, repository_bindings: { ...bindings, repositories }, ...(withImages ? { images: creation!.images } : {}) },
  });
  return confirmTodoCreate(result, creation?.images, request);
}
