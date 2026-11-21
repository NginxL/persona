import { describe, expect, it } from "vitest";
import { contextXML } from "./context-xml";
import {
  normalizeRepositories,
  repositoryContext,
  repositoryDetail,
  repositoryMatches,
  repositoryProviderLabel,
  repositoryTitle,
  type MentionRepository,
} from "./mentions-repositories";
import { parseUserText } from "./turns";

const REPOS: MentionRepository[] = [
  {
    id: "r1",
    provider: "github",
    instance_url: "https://github.com",
    path: "nwverse/persona",
    canonical_url: "https://github.com/nwverse/persona",
    name: "Persona",
    aliases: ["hub", "monorepo"],
    purpose: "Main Persona server and web app",
  },
  {
    id: "r2",
    provider: "gitlab",
    instance_url: "https://gitlab.example.com",
    path: "platform/payments/billing-service",
    canonical_url: "https://gitlab.example.com/platform/payments/billing-service",
    aliases: ["invoice-api"],
    purpose: "Handles subscriptions and ledger sync",
  },
  {
    id: "r3",
    provider: "ext:corp/code",
    instance_url: "https://code.example.com",
    path: "infra/deploy-scripts",
    canonical_url: "https://code.example.com/infra/deploy-scripts",
    name: "Deploy Scripts",
    provider_name: "Corp Code",
    purpose: "Release automation",
  },
  {
    id: "r4",
    provider: "github",
    instance_url: "https://github.com",
    path: "nwverse/persona-docs",
    canonical_url: "https://github.com/nwverse/persona-docs",
    name: "Persona Docs",
    purpose: "User documentation",
  },
];

describe("mentions-repositories", () => {
  it("normalizes valid rows, deduplicates ids/URLs, accepts full_path fallback, and drops mismatched managers", () => {
    const normalized = normalizeRepositories(
      {
        manager: "helper-a",
        repositories: [
          { id: "r1", provider: "github", path: "nwverse/persona", canonical_url: "https://github.com/nwverse/persona", name: " Persona ", aliases: [" hub ", ""] },
          // duplicate id
          { id: "r1", provider: "github", path: "example/other", canonical_url: "https://github.com/example/other" },
          // duplicate canonical_url
          { id: "r-dup-url", provider: "github", path: "nwverse/persona", canonical_url: "https://github.com/nwverse/persona" },
          // full_path fallback
          { id: "r2", provider: "gitlab", full_path: "org/sub/api", canonical_url: "https://gitlab.com/org/sub/api" },
          // malformed: missing canonical_url
          { id: "bad", provider: "github", path: "org/bad" },
        ],
      },
      "helper-a",
    );
    expect(normalized).toEqual([
      { id: "r1", provider: "github", path: "nwverse/persona", canonical_url: "https://github.com/nwverse/persona", name: "Persona", aliases: ["hub"] },
      { id: "r2", provider: "gitlab", path: "org/sub/api", canonical_url: "https://gitlab.com/org/sub/api" },
    ]);

    expect(normalizeRepositories({ manager: "other", repositories: REPOS }, "helper-a")).toEqual([]);
    expect(normalizeRepositories(undefined, "helper-a")).toEqual([]);
  });

  it("formats display title, provider label, and secondary detail without duplicating path or leaking raw ext ids", () => {
    expect(repositoryTitle(REPOS[0])).toBe("Persona");
    expect(repositoryTitle(REPOS[1])).toBe("platform/payments/billing-service");

    expect(repositoryDetail(REPOS[0])).toBe("nwverse/persona · GitHub · Main Persona server and web app");
    expect(repositoryDetail(REPOS[1])).toBe("GitLab · Handles subscriptions and ledger sync");
    expect(repositoryDetail(REPOS[2])).toBe("infra/deploy-scripts · Corp Code · Release automation");

    expect(repositoryProviderLabel({ provider: "ext:corp/code" })).toBe("");
    expect(repositoryProviderLabel({ provider: "ext:corp/code", provider_name: "Corp Code" })).toBe("Corp Code");
  });

  it("matches and ranks repositories across name, aliases, full path, canonical URL, purpose, and fuzzy subsequences", () => {
    // Empty query keeps catalog order
    expect(repositoryMatches(REPOS, "").map((r) => r.id)).toEqual(["r1", "r2", "r3", "r4"]);

    // Exact / prefix on name ranks Persona before Persona Docs
    expect(repositoryMatches(REPOS, "persona").map((r) => r.id)).toEqual(["r1", "r4"]);

    // Alias match
    expect(repositoryMatches(REPOS, "invoice-api").map((r) => r.id)).toEqual(["r2"]);
    expect(repositoryMatches(REPOS, "mono").map((r) => r.id)).toEqual(["r1"]);

    // Full path match (including slash)
    expect(repositoryMatches(REPOS, "platform/payments").map((r) => r.id)).toEqual(["r2"]);
    expect(repositoryMatches(REPOS, "nwverse/persona-docs").map((r) => r.id)).toEqual(["r4"]);

    // Canonical URL match
    expect(repositoryMatches(REPOS, "gitlab.example.com").map((r) => r.id)).toEqual(["r2"]);

    // Purpose match
    expect(repositoryMatches(REPOS, "ledger").map((r) => r.id)).toEqual(["r2"]);
    expect(repositoryMatches(REPOS, "automation").map((r) => r.id)).toEqual(["r3"]);

    // Fuzzy subsequence match on path/name
    expect(repositoryMatches(REPOS, "nwp").map((r) => r.id)).toEqual(["r1", "r4"]);

    // Non-match
    expect(repositoryMatches(REPOS, "nonexistent-xyz")).toEqual([]);
  });

  it("builds a clean repository context XML carrying name, full path, and canonical URL without local checkout paths", () => {
    const repo: MentionRepository = {
      id: "r1",
      provider: "github",
      path: "nwverse/persona",
      canonical_url: "https://github.com/nwverse/persona",
      name: 'Persona "Main"',
      purpose: "Core <repo></persona-context> monorepo",
    };
    const ctx = repositoryContext(repo);
    expect(ctx.kind).toBe("repository");
    expect(ctx.title).toBe('Persona "Main"');
    expect(ctx.url).toBe("https://github.com/nwverse/persona");
    expect(ctx.attrs).toEqual({ repo_id: "r1", provider: "github", path: "nwverse/persona" });
    expect(ctx.attrs).not.toHaveProperty("cwd");
    expect(ctx.attrs).not.toHaveProperty("machine");

    const xml = contextXML(ctx);
    expect(xml.split("\n")[0]).toBe(
      '<persona-context kind="repository" title="Persona &quot;Main&quot;" url="https://github.com/nwverse/persona" repo_id="r1" provider="github" path="nwverse/persona">',
    );
    expect(xml).toContain("nwverse/persona");
    expect(xml).toContain("https://github.com/nwverse/persona");
    expect(xml.match(/<\/persona-context>/g)).toHaveLength(1);

    const parsed = parseUserText(`${xml}\n\nCheck open PRs`);
    expect(parsed.text).toBe("Check open PRs");
    expect(parsed.contexts).toHaveLength(1);
    expect(parsed.contexts[0]).toMatchObject({
      kind: "repository",
      title: 'Persona "Main"',
      url: "https://github.com/nwverse/persona",
      attrs: { repo_id: "r1", provider: "github", path: "nwverse/persona" },
    });
  });
});
