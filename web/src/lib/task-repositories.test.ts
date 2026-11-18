// @vitest-environment jsdom
import { expect, it } from "vitest";
import fixture from "../../../spec/fixtures/task-repositories/contract.json";
import { cleanRepositoryDraft, repositoryDraft, repositoryLinkKindLabel, repositoryLinkTitle, repositoryLinkView, repositoryProviderName, repositoryRelativePath, repositorySource, validRepositoryAlias, validRepositoryIdentity, validRepositorySet, validRequestedRef, validTaskRepositoryLink, type RequestedRef, type TaskRepository, type TaskRepositorySet } from "./task-repositories";

it("shares alias, layout and typed-ref fixtures with the manager", () => {
  for (const alias of fixture.aliases.valid) expect(validRepositoryAlias(alias), alias).toBe(true);
  for (const alias of fixture.aliases.invalid) expect(validRepositoryAlias(alias), alias).toBe(false);
  for (const ref of fixture.refs) expect(validRequestedRef(ref as RequestedRef)).toBe(true);
  expect(repositoryRelativePath("root", "", 1)).toBe(".");
  expect(repositoryRelativePath("root", "", 2)).toBeUndefined();
  expect(repositoryRelativePath("container", "web", 1)).toBe("web");
});

it("preserves typed refs and numeric string links when round-tripping a saved repository", () => {
  const row = { repo_id: "web", requested_ref: fixture.refs[3], links: [{ kind: "pull_request", number: "9007199254740993", canonical_url: "https://github.com/example/web/pull/9007199254740993" }] } as TaskRepository;
  expect(repositoryDraft([row])[0]).toMatchObject({ requested_ref: fixture.refs[3], links: [{ kind: "pull_request", number: "9007199254740993" }] });
  expect(cleanRepositoryDraft([{ repo_id: "web", links: [{ url: "  https://github.com/example/web/pull/7  " }, { url: "" }] }])[0].links).toEqual([{ url: "https://github.com/example/web/pull/7" }]);
});

const github = { provider: "github", instance_url: "https://github.com", path: "example/web", canonical_url: "https://github.com/example/web" };
const gitlab = { provider: "gitlab", instance_url: "https://gitlab.example.com", path: "team/api", canonical_url: "https://gitlab.example.com/team/api" };

it("reads a pasted issue or PR URL only when it belongs to the saved repository", () => {
  expect(repositoryLinkView(github, { url: "https://github.com/example/web/pull/7" })).toEqual({ kind: "pull_request", number: "7", url: "https://github.com/example/web/pull/7", label: "PR #7" });
  expect(repositoryLinkView(github, { url: "https://github.com/example/web/issues/12" })).toMatchObject({ kind: "issue", number: "12", label: "#12" });
  // Another repository, another host, another scheme, credentials, a query, a fragment, a deeper path or a non-number: all refused.
  const credentials = "https://user:pw" + "@github.com/example/web/pull/7";
  for (const url of ["https://github.com/example/docs/pull/7", "https://gitlab.com/example/web/pull/7", "http://github.com/example/web/pull/7",
    credentials, "https://github.com/example/web/pull/7?x=1", "https://github.com/example/web/pull/7#c",
    "https://github.com/example/web/pull/7/files", "https://github.com/example/web/pull/", "https://github.com/example/web/pull/07",
    "https://github.com/example/web/pull/0", "https://github.com/example/web", "not a url", ""]) {
    expect(repositoryLinkView(github, { url }), url).toBeNull();
  }
});

it("reads saved kind and number for GitHub and GitLab, keeping the number a string", () => {
  expect(repositoryLinkView(gitlab, { kind: "pull_request", number: "9" })).toEqual({ kind: "pull_request", number: "9", url: "https://gitlab.example.com/team/api/-/merge_requests/9", label: "MR !9" });
  expect(repositoryLinkView(gitlab, { kind: "issue", number: "9" })).toMatchObject({ url: "https://gitlab.example.com/team/api/-/issues/9", label: "#9" });
  expect(repositoryLinkView(gitlab, { url: "https://gitlab.example.com/team/api/-/merge_requests/9007199254740993" })).toMatchObject({ number: "9007199254740993" });
  expect(repositoryLinkView(github, { kind: "issue", number: "0" })).toBeNull();
  expect(repositoryLinkView({ ...github, provider: "bitbucket" }, { kind: "issue", number: "1" })).toBeNull();
});

it("carries a full source title on the exact saved link, never onto another repository or number", () => {
  const link = { kind: "pull_request" as const, number: "7", canonical_url: "https://github.com/example/web/pull/7", title: "  Fix the login redirect  " };
  expect(repositoryLinkView(github, link)).toEqual({ kind: "pull_request", number: "7", url: "https://github.com/example/web/pull/7", label: "PR #7", title: "Fix the login redirect" });
  const elsewhere = { ...link, canonical_url: "https://github.com/example/docs/pull/7" };
  expect(repositoryLinkView(github, elsewhere)).toBeNull();
  expect(repositoryLinkView(github, { ...link, title: "two\nlines" })).toEqual({ kind: "pull_request", number: "7", url: "https://github.com/example/web/pull/7", label: "PR #7" });
});

it("accepts the same title bounds as the server and keeps titles through a draft round trip", () => {
  expect(repositoryLinkTitle("  A title  ")).toBe("A title");
  expect(repositoryLinkTitle("x".repeat(1024))).toHaveLength(1024);
  expect(repositoryLinkTitle("x".repeat(1025))).toBeUndefined();
  expect(repositoryLinkTitle("é".repeat(512))).toHaveLength(512);
  expect(repositoryLinkTitle("é".repeat(513))).toBeUndefined();
  expect(repositoryLinkTitle("a\u0000b")).toBeUndefined();
  expect(repositoryLinkTitle("   ")).toBeUndefined();
  const row = { repo_id: "web", requested_ref: { kind: "default" }, links: [
    { kind: "pull_request", number: "7", canonical_url: "https://github.com/example/web/pull/7", title: "Fix the redirect" },
    { kind: "issue", number: "8", canonical_url: "https://github.com/example/web/issues/8", title: "Reported crash" },
  ] } as TaskRepository;
  expect(repositoryDraft([row])[0].links).toEqual([
    { kind: "pull_request", number: "7", title: "Fix the redirect" },
    { kind: "issue", number: "8", title: "Reported crash" },
  ]);
  expect(cleanRepositoryDraft([{ repo_id: "web", links: [
    { kind: "pull_request", number: "7", title: "  Fix the redirect  " },
    { url: "https://github.com/example/web/issues/8", title: "Reported crash" },
    { kind: "issue", number: "9", title: "x".repeat(1025) },
  ] }])[0].links).toEqual([
    { kind: "pull_request", number: "7", title: "Fix the redirect" },
    { url: "https://github.com/example/web/issues/8", title: "Reported crash" },
    { kind: "issue", number: "9" },
  ]);
});

it("validates a saved response's link against the repository it claims", () => {
  const link = { kind: "pull_request" as const, number: "7", canonical_url: "https://github.com/example/web/pull/7", title: "Fix the redirect" };
  const envelope = (links: TaskRepositorySet["repositories"][number]["links"]): TaskRepositorySet => ({ schema_version: 1, manager: "assistant", todo_id: "todo", revision: 1,
    repositories: [{ repo_id: "web", requested_ref: { kind: "default" }, source_snapshot: { identity: github, clone_url: github.canonical_url, transport_origin: "canonical_default", catalog_revision: 1 }, links }] });
  expect(validTaskRepositoryLink(github, link)).toBe(true);
  expect(validRepositorySet(envelope([link]), "assistant", "todo")).toBe(true);
  expect(validTaskRepositoryLink(github, { ...link, canonical_url: "https://github.com/example/docs/pull/7" })).toBe(false);
  expect(validRepositorySet(envelope([{ ...link, canonical_url: "https://github.com/example/docs/pull/7" }]), "assistant", "todo")).toBe(false);
  expect(validRepositorySet(envelope([{ ...link, title: "bad\u0000title" }]), "assistant", "todo")).toBe(false);
  expect(validRepositorySet(envelope([{ ...link, title: " padded " }]), "assistant", "todo")).toBe(false);
});

// An extension's repository (docs/extensions/repository-providers.md) on a placeholder host.
const acme = { provider: "ext:acme-code/acme", instance_url: "https://code.acme.test", path: "acme-code/acme", canonical_url: "https://code.acme.test/acme-code/acme" };
const acmeLink = { kind: "pull_request" as const, number: "12", canonical_url: "https://code.acme.test/acme-code/acme/reviews/12", title: "Fix the build" };
const acmeEnvelope = (row: Omit<Partial<TaskRepository>, "source_snapshot"> & { source_snapshot?: Partial<TaskRepository["source_snapshot"]> }): TaskRepositorySet => ({ schema_version: 1, manager: "assistant", todo_id: "todo", revision: 1,
  repositories: [{ repo_id: "acme", requested_ref: { kind: "default" }, links: [acmeLink], ...row,
    source_snapshot: { identity: acme, clone_url: acme.canonical_url, transport_origin: "canonical_default", catalog_revision: 1, ...row.source_snapshot } }] });

it("accepts an extension's identity only as an https instance with a well-formed source id", () => {
  expect(validRepositoryIdentity(acme)).toBe(true);
  expect(validRepositoryIdentity({ ...acme, instance_url: "https://code.acme.test:8443", canonical_url: "https://code.acme.test:8443/acme-code/acme" })).toBe(true);
  for (const provider of ["ext:Acme/acme", "ext:acme", "ext:acme/", "ext:-acme/acme", "ext:acme/acme/extra", `ext:${"a".repeat(33)}/acme`, "bitbucket"]) {
    expect(validRepositoryIdentity({ ...acme, provider }), provider).toBe(false);
  }
  expect(validRepositoryIdentity({ ...acme, instance_url: "http://code.acme.test", canonical_url: "http://code.acme.test/acme-code/acme" })).toBe(false);
  expect(validRepositoryIdentity({ ...acme, canonical_url: "https://code.acme.test/acme-code/other" })).toBe(false);
  // GitHub and GitLab keep today's rule, self-hosted http included.
  expect(validRepositoryIdentity({ ...gitlab, instance_url: "http://gitlab.example.com", canonical_url: "http://gitlab.example.com/team/api" })).toBe(true);
});

it("reads an extension's saved link by its stored address only, strictly under the repository's", () => {
  expect(validTaskRepositoryLink(acme, acmeLink)).toBe(true);
  expect(repositoryLinkView(acme, acmeLink)).toEqual({ kind: "pull_request", number: "12", url: acmeLink.canonical_url, label: "#12", title: "Fix the build" });
  const credentials = "https://user:pw" + "@code.acme.test/acme-code/acme/reviews/12";
  for (const canonical_url of ["https://code.other.test/acme-code/acme/reviews/12", "http://code.acme.test/acme-code/acme/reviews/12", credentials,
    "https://code.acme.test/acme-code/acme/reviews/12?x=1", "https://code.acme.test/acme-code/acme/reviews/12#c", "https://code.acme.test/acme-code/acme-other/reviews/12",
    "https://code.acme.test/acme-code/acme", "https://code.acme.test/acme-code/acme/", "https://code.acme.test/acme-code/acme/../other/12", "https://code.acme.test/acme-code/acme/a%2Fb"]) {
    expect(validTaskRepositoryLink(acme, { ...acmeLink, canonical_url }), canonical_url).toBe(false);
    expect(repositoryLinkView(acme, { ...acmeLink, canonical_url }), canonical_url).toBeNull();
  }
  for (const number of ["0", "012", "1".repeat(21), "12a"]) expect(validTaskRepositoryLink(acme, { ...acmeLink, number }), number).toBe(false);
  expect(validTaskRepositoryLink(acme, { ...acmeLink, kind: "merge_request" as "pull_request" })).toBe(false);
  // GitHub's layout never applies to an extension's repository, nor an extension's address to GitHub's.
  expect(validTaskRepositoryLink(acme, { ...acmeLink, canonical_url: "https://code.acme.test/acme-code/acme/pull/12" })).toBe(true);
  expect(validTaskRepositoryLink(github, { ...acmeLink, canonical_url: "https://github.com/example/web/reviews/12" })).toBe(false);
});

it("keeps a pasted address under an extension's repository pending, and draws a draft's kind and number from its saved link", () => {
  expect(repositoryLinkView(acme, { url: " https://code.acme.test/acme-code/acme/reviews/13 " }, [acmeLink])).toEqual({ pending: true, url: "https://code.acme.test/acme-code/acme/reviews/13", label: "https://code.acme.test/acme-code/acme/reviews/13" });
  // The address of a link the to-do has is that link (the manager keeps it without asking the source).
  expect(repositoryLinkView(acme, { url: acmeLink.canonical_url }, [acmeLink])).toMatchObject({ kind: "pull_request", number: "12", url: acmeLink.canonical_url });
  for (const url of ["https://code.other.test/acme-code/acme/reviews/13", "http://code.acme.test/acme-code/acme/reviews/13", "https://code.acme.test/acme-code/acme/reviews/13?x=1",
    "https://code.acme.test/acme-code/acme/reviews/13#c", "https://code.acme.test/acme-code/sibling/reviews/13", "https://code.acme.test/acme-code/acme", "https://u" + "@code.acme.test/acme-code/acme/1", "not a url", ""]) {
    expect(repositoryLinkView(acme, { url }, [acmeLink]), url).toBeNull();
  }
  // A draft keeps kind + number only (repositoryDraft); its address and title come from the saved link it names.
  expect(repositoryLinkView(acme, { kind: "pull_request", number: "12" }, [acmeLink])).toEqual({ kind: "pull_request", number: "12", url: acmeLink.canonical_url, label: "#12", title: "Fix the build" });
  expect(repositoryLinkView(acme, { kind: "issue", number: "12" }, [acmeLink])).toBeNull();
  expect(repositoryLinkView(acme, { kind: "pull_request", number: "12" })).toBeNull();
  expect(repositoryDraft(acmeEnvelope({}).repositories)[0].links).toEqual([{ kind: "pull_request", number: "12", title: "Fix the build" }]);
});

it("validates an extension row's saved name, source state and link words on reads", () => {
  expect(validRepositorySet(acmeEnvelope({ provider_state: "missing", link_kinds: [{ kind: "pull_request", label: "MR" }], source_snapshot: { provider_name: "Acme Code" } }), "assistant", "todo")).toBe(true);
  expect(validRepositorySet(acmeEnvelope({ source_snapshot: { provider_name: "x".repeat(80) } }), "assistant", "todo")).toBe(true);
  expect(validRepositorySet(acmeEnvelope({ source_snapshot: { provider_name: "x".repeat(81) } }), "assistant", "todo")).toBe(false);
  expect(validRepositorySet(acmeEnvelope({ provider_state: "starting" as "off" }), "assistant", "todo")).toBe(false);
  for (const link_kinds of [[{ kind: "merge_request", label: "MR" }], [{ kind: "pull_request", label: "" }], [{ kind: "pull_request", label: "x".repeat(25) }], "MR"]) {
    expect(validRepositorySet(acmeEnvelope({ link_kinds: link_kinds as TaskRepository["link_kinds"] }), "assistant", "todo"), JSON.stringify(link_kinds)).toBe(false);
  }
  expect(validRepositorySet(acmeEnvelope({ links: [{ ...acmeLink, canonical_url: "https://code.acme.test/acme-code/other/reviews/12" }] }), "assistant", "todo")).toBe(false);
  // A row cloned from the address its source answered says so; only an extension's row can.
  expect(validRepositorySet(acmeEnvelope({ source_snapshot: { transport_origin: "provider", clone_url: "ssh://git@code.acme.test/acme-code/acme.git" } }), "assistant", "todo")).toBe(true);
  expect(validRepositorySet(acmeEnvelope({ source_snapshot: { transport_origin: "elsewhere" as "provider" } }), "assistant", "todo")).toBe(false);
});

it("finds a saved repository's source under its own id, else through its extension's rename by contribution and host", () => {
  const renamed = { id: "ext:acme-code-new/acme", name: "Acme Code", builtin: false, hosts: ["code.acme.test"], state: "available" as const };
  const builtin = { id: "gitlab", name: "GitLab", builtin: true, hosts: ["gitlab.com"], state: "available" as const };
  expect(repositorySource(acme, [builtin, { ...renamed, id: acme.provider }])?.id).toBe(acme.provider);
  expect(repositorySource(acme, [builtin, renamed])).toBe(renamed);
  // Another contribution, another host or no list at all is not it.
  expect(repositorySource(acme, [{ ...renamed, id: "ext:acme-code-new/other" }])).toBeUndefined();
  expect(repositorySource(acme, [{ ...renamed, hosts: ["code.other.test"] }])).toBeUndefined();
  expect(repositorySource(acme, undefined)).toBeUndefined();
  expect(repositorySource({ provider: "gitlab", instance_url: "https://gitlab.com" }, [builtin])).toBe(builtin);
  expect(repositorySource({ provider: "github", instance_url: "https://github.com" }, [builtin])).toBeUndefined();
});

it("names a source by its live name, else its saved name, never by its raw id", () => {
  const live = [{ id: acme.provider, name: "Acme Code", builtin: false, hosts: ["code.acme.test"], state: "available" as const }];
  expect(repositoryProviderName(acme.provider, live, "Old name")).toBe("Acme Code");
  expect(repositoryProviderName(acme.provider, [], "Old name")).toBe("Old name");
  expect(repositoryProviderName(acme.provider)).toBeUndefined();
  expect(repositoryProviderName("github")).toBe("GitHub");
  expect(repositoryLinkKindLabel("pull_request", [{ kind: "pull_request", label: "MR" }])).toBe("MR");
  expect(repositoryLinkKindLabel("issue", [{ kind: "pull_request", label: "MR" }])).toBeUndefined();
});
