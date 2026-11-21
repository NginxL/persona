// Assistant-bound repositories in the @ mention panel (GET /api/v1/assistant/repositories).
// Loaded on demand via dynamic import from ComposerPanel so it stays off both the first-screen and idle tiers.
import { t } from "../i18n";
import { api } from "./api";
import type { ContextRef } from "./links";
import { MANAGER_HEADER } from "./manager-choice";

export interface MentionRepository {
  id: string;
  provider: string;
  instance_url?: string;
  /** Full repository path (e.g. "nwverse/persona"). */
  path: string;
  canonical_url: string;
  name?: string;
  aliases?: string[];
  purpose?: string;
  provider_name?: string;
}

export interface AssistantRepositoriesReply {
  manager?: string;
  catalog_revision?: number;
  repositories?: Array<Partial<MentionRepository> & { full_path?: string }>;
}

const BUILTIN_PROVIDER_NAMES: Record<string, string> = { github: "GitHub", gitlab: "GitLab" };

/** Normalizes and deduplicates rows from GET /api/v1/assistant/repositories; drops mismatched-manager or malformed replies. */
export function normalizeRepositories(reply: AssistantRepositoriesReply | null | undefined, expectedManager?: string): MentionRepository[] {
  if (!reply || !Array.isArray(reply.repositories)) return [];
  if (expectedManager && typeof reply.manager === "string" && reply.manager && reply.manager !== expectedManager) return [];
  const out: MentionRepository[] = [];
  const seenIds = new Set<string>();
  const seenUrls = new Set<string>();
  for (const raw of reply.repositories) {
    if (!raw || typeof raw !== "object") continue;
    const id = typeof raw.id === "string" ? raw.id.trim() : "";
    const path = (typeof raw.path === "string" ? raw.path : typeof raw.full_path === "string" ? raw.full_path : "").trim();
    const canonical_url = typeof raw.canonical_url === "string" ? raw.canonical_url.trim() : "";
    if (!id || !path || !canonical_url || seenIds.has(id) || seenUrls.has(canonical_url)) continue;
    seenIds.add(id);
    seenUrls.add(canonical_url);
    const provider = typeof raw.provider === "string" ? raw.provider.trim() : "";
    const instance_url = typeof raw.instance_url === "string" && raw.instance_url.trim() ? raw.instance_url.trim() : undefined;
    const name = typeof raw.name === "string" && raw.name.trim() ? raw.name.trim() : undefined;
    const aliases = Array.isArray(raw.aliases)
      ? raw.aliases.map((a) => (typeof a === "string" ? a.trim() : "")).filter(Boolean)
      : undefined;
    const purpose = typeof raw.purpose === "string" && raw.purpose.trim() ? raw.purpose.trim() : undefined;
    const provider_name = typeof raw.provider_name === "string" && raw.provider_name.trim() ? raw.provider_name.trim() : undefined;
    out.push({
      id,
      provider,
      ...(instance_url ? { instance_url } : {}),
      path,
      canonical_url,
      ...(name ? { name } : {}),
      ...(aliases?.length ? { aliases } : {}),
      ...(purpose ? { purpose } : {}),
      ...(provider_name ? { provider_name } : {}),
    });
  }
  return out;
}

/** Primary display label for a bound repository: custom name if set, otherwise its full path. */
export function repositoryTitle(r: Pick<MentionRepository, "name" | "path">): string {
  return r.name?.trim() || r.path;
}

/** Human-readable provider label (never exposes raw `ext:` ids). */
export function repositoryProviderLabel(r: Pick<MentionRepository, "provider" | "provider_name">): string {
  const saved = r.provider_name?.trim();
  if (saved) return saved;
  const builtin = BUILTIN_PROVIDER_NAMES[r.provider];
  if (builtin) return builtin;
  return r.provider && !r.provider.startsWith("ext:") ? r.provider : "";
}

/** Secondary row detail in the @ menu: path (when title differs) · provider · purpose. */
export function repositoryDetail(r: MentionRepository): string {
  const title = repositoryTitle(r);
  const parts: string[] = [];
  if (r.path && r.path !== title) parts.push(r.path);
  const provider = repositoryProviderLabel(r);
  if (provider) parts.push(provider);
  if (r.purpose?.trim()) parts.push(r.purpose.trim());
  return parts.join(" · ");
}

function isSubsequence(needle: string, haystack: string): boolean {
  if (!needle || needle.length > haystack.length) return false;
  let i = 0;
  for (let j = 0; j < haystack.length && i < needle.length; j++) {
    if (haystack[j] === needle[i]) i++;
  }
  return i === needle.length;
}

function repositoryScore(r: MentionRepository, q: string): number {
  const title = repositoryTitle(r).toLowerCase();
  const path = r.path.toLowerCase();
  const segment = path.split("/").at(-1) ?? "";
  const aliases = (r.aliases ?? []).map((a) => a.toLowerCase());
  const primaries = [title, segment, path, ...aliases].filter(Boolean);
  if (primaries.some((s) => s === q)) return 0;
  if (primaries.some((s) => s.startsWith(q))) return 1;
  if (primaries.some((s) => s.includes(q))) return 2;
  const url = r.canonical_url.toLowerCase();
  const purpose = (r.purpose ?? "").toLowerCase();
  if (url.includes(q) || purpose.includes(q)) return 3;
  if (q.length >= 2 && primaries.some((s) => isSubsequence(q, s))) return 4;
  return Infinity;
}

/** Matches bound repositories across name, aliases, full path, canonical URL, and purpose; exact/prefix rank first. */
export function repositoryMatches(all: readonly MentionRepository[], query: string, limit = 12): MentionRepository[] {
  const q = query.trim().toLowerCase();
  if (!q) return all.slice(0, limit);
  const scored: Array<{ repo: MentionRepository; score: number; index: number }> = [];
  for (let i = 0; i < all.length; i++) {
    const score = repositoryScore(all[i], q);
    if (score < Infinity) scored.push({ repo: all[i], score, index: i });
  }
  scored.sort((a, b) => a.score - b.score || a.index - b.index);
  return scored.slice(0, limit).map((s) => s.repo);
}

/** Context payload for a picked repository: carries display name, full path, and canonical URL without local checkouts. */
export function repositoryContext(r: MentionRepository): Omit<ContextRef, "id"> {
  const title = repositoryTitle(r);
  const lines = [
    t("chat-composerpanel.repo_context_title", { title }),
    t("chat-composerpanel.repo_context_path", { path: r.path }),
    t("chat-composerpanel.repo_context_url", { url: r.canonical_url }),
  ];
  if (r.purpose?.trim()) lines.push(t("chat-composerpanel.repo_context_purpose", { purpose: r.purpose.trim() }));
  return {
    kind: "repository",
    title,
    url: r.canonical_url,
    text: lines.join("\n"),
    attrs: {
      repo_id: r.id,
      ...(r.provider ? { provider: r.provider } : {}),
      path: r.path,
    },
  };
}

export interface RepositoryMentionRow {
  kind: "repository";
  id: string;
  name: string;
  detail: string;
  repository: MentionRepository;
  context: Omit<ContextRef, "id">;
}

/** Fetches the assistant's bound repositories on demand, passing X-Persona-Manager when known. */
export async function loadMentionRepositories(manager: string | undefined, signal?: AbortSignal): Promise<MentionRepository[]> {
  const reply = await api<AssistantRepositoriesReply>("/assistant/repositories", {
    signal,
    ...(manager ? { headers: { [MANAGER_HEADER]: manager } } : {}),
  });
  return normalizeRepositories(reply, manager);
}

/** Builds MentionRow entries for matching repositories. */
export function repositoryMentionRows(repositories: readonly MentionRepository[], query: string): RepositoryMentionRow[] {
  return repositoryMatches(repositories, query).map((r) => ({
    kind: "repository",
    id: `repository:${r.id}`,
    name: repositoryTitle(r),
    detail: repositoryDetail(r),
    repository: r,
    context: repositoryContext(r),
  }));
}
