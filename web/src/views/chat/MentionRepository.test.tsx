// @vitest-environment jsdom
// @ bound repositories in the Persona assistant's composer (#12531): fetches GET /api/v1/assistant/repositories on
// demand when the @ panel opens (with X-Persona-Manager), caps empty queries to EMPTY_PER_GROUP (3), filters by name,
// aliases, path, canonical_url, and purpose, attaches a removable repository chip, and sends <persona-context kind="repository">
// without fabricating local checkout paths.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { loadPages } from "../../lib/lazy-page";
import { getContexts, removeContext } from "../../lib/links";
import { Composer, type Outgoing } from "./Composer";
import type { MentionSource } from "./ComposerPanel";

const REPOSITORIES = [
  {
    id: "repo-1",
    provider: "github",
    instance_url: "https://github.com",
    path: "nwverse/persona",
    canonical_url: "https://github.com/nwverse/persona",
    name: "Persona",
    aliases: ["hub", "main-repo"],
    purpose: "Main monorepo",
    verified_at: "2026-11-20T00:00:00Z",
  },
  {
    id: "repo-2",
    provider: "gitlab",
    instance_url: "https://gitlab.example.com",
    path: "team/billing-api",
    canonical_url: "https://gitlab.example.com/team/billing-api",
    aliases: ["invoice"],
    purpose: "Handles subscription billing",
    verified_at: "2026-11-20T00:00:00Z",
  },
  {
    id: "repo-3",
    provider: "github",
    instance_url: "https://github.com",
    path: "nwverse/persona-mobile",
    canonical_url: "https://github.com/nwverse/persona-mobile",
    name: "Mobile App",
    purpose: "iOS and Android clients",
    verified_at: "2026-11-20T00:00:00Z",
  },
  {
    id: "repo-4",
    provider: "ext:corp/forge",
    instance_url: "https://forge.example.com",
    path: "infra/deploy-kit",
    canonical_url: "https://forge.example.com/infra/deploy-kit",
    name: "Deploy Kit",
    aliases: ["release-bot"],
    purpose: "Cluster rollout tooling",
    provider_name: "Corp Forge",
    verified_at: "2026-11-20T00:00:00Z",
  },
];

let repoReply: () => Promise<unknown> = async () => ({ manager: "assistant", catalog_revision: 1, repositories: REPOSITORIES });
const api = vi.fn(async (path: string, _init?: unknown): Promise<unknown> => {
  if (path === "/assistant/repositories") return repoReply();
  if (path === "/saved-prompts") return { prompts: [] };
  if (path.startsWith("/machines/")) throw new Error("404");
  throw new Error(`unexpected ${path}`);
});
const repoCalls = () => api.mock.calls.filter(([path]) => path === "/assistant/repositories");
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init?: unknown) => api(path, init),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeAll(() => loadPages());

beforeEach(() => {
  Element.prototype.scrollIntoView ??= () => {};
  repoReply = async () => ({ manager: "assistant", catalog_revision: 1, repositories: REPOSITORIES });
  api.mockClear();
  localStorage.clear();
  getContexts("persona-new").forEach((c) => removeContext("persona-new", c.id));
  getContexts("m/s").forEach((c) => removeContext("m/s", c.id));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const render = (
  mention: MentionSource,
  onSend: (m: Outgoing) => Promise<boolean> = async () => true,
  draftKey = "persona-new",
  machineId?: string,
) => act(() => root.render(<Composer draftKey={draftKey} placeholder="" machineId={machineId} mention={mention} onSend={onSend} />));

const box = () => host.querySelector("textarea")!;

async function type(text: string) {
  const el = box();
  await act(async () => {
    el.focus();
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, text);
    el.setSelectionRange(text.length, text.length);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
}

const key = (k: string) => act(() => void box().dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));
const options = () => [...host.querySelectorAll<HTMLButtonElement>('[role="option"]:not([data-panel-action])')];
const optionTexts = () => options().map((o) => o.textContent ?? "");
const waitForOptions = (count: number) =>
  vi.waitFor(async () => {
    await act(async () => {});
    expect(options()).toHaveLength(count);
  });

describe("@ assistant-bound repositories (#12531)", () => {
  it("loads on demand with X-Persona-Manager only when @ opens and caps empty queries to EMPTY_PER_GROUP (3)", async () => {
    render({ self: "persona-new", files: () => [], repositories: "assistant" });
    expect(repoCalls()).toHaveLength(0);

    await type("@");
    await waitForOptions(3);
    expect(repoCalls()).toHaveLength(1);
    expect(repoCalls()[0]).toEqual([
      "/assistant/repositories",
      expect.objectContaining({ headers: { "X-Persona-Manager": "assistant" } }),
    ]);
    expect(host.textContent).toContain(t("chat-composerpanel.repositories"));
    const rows = optionTexts();
    expect(rows).toHaveLength(3);
    expect(rows[0]).toContain("Persona");
    expect(rows[0]).toContain("nwverse/persona · GitHub · Main monorepo");
    expect(rows[1]).toContain("team/billing-api");
    expect(rows[1]).toContain("GitLab · Handles subscription billing");
    expect(rows[2]).toContain("Mobile App");
  });

  it("filters across name, alias, full path (even with cwd set), URL, and purpose without refetching", async () => {
    render({ cwd: "/home/me/.persona/assistant", self: "persona-new", files: () => [], repositories: "assistant" }, async () => true, "persona-new", "box");

    // 4th repository beyond empty cap found by alias
    await type("@release-bot");
    await waitForOptions(1);
    expect(optionTexts()[0]).toContain("Deploy Kit");
    expect(optionTexts()[0]).toContain("infra/deploy-kit · Corp Forge · Cluster rollout tooling");

    // Full path containing a slash still matches repositories when cwd is set
    await type("@nwverse/persona-mobile");
    await waitForOptions(1);
    expect(optionTexts()[0]).toContain("Mobile App");

    // Purpose match
    await type("@subscription");
    await waitForOptions(1);
    expect(optionTexts()[0]).toContain("team/billing-api");

    // URL match
    await type("@forge.example.com");
    await waitForOptions(1);
    expect(optionTexts()[0]).toContain("Deploy Kit");

    // Only fetched repositories once while panel remained open
    expect(api.mock.calls.filter(([path]) => path === "/assistant/repositories")).toHaveLength(1);
  });

  it("picks via keyboard Enter into a removable chip and sends clean repository context without local checkout paths", async () => {
    const onSend = vi.fn(async (_m: Outgoing) => true);
    render({ self: "persona-new", files: () => [], repositories: "assistant" }, onSend);

    await type("@");
    await waitForOptions(3);
    // Move past "Add images…" action row to the first repository row ("Persona")
    key("ArrowDown");
    key("Enter");
    expect(options()).toHaveLength(0);
    expect(box().value).toBe("");
    expect(host.textContent).toContain(t("link-contextchips.repository"));
    expect(host.textContent).toContain("Persona");

    await type("Check open issues");
    await act(async () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-composer.send")}"]`)!.click());

    expect(onSend).toHaveBeenCalledOnce();
    const text = onSend.mock.calls[0][0].text;
    expect(text).toMatch(
      /^<persona-context kind="repository" title="Persona" url="https:\/\/github\.com\/nwverse\/persona" repo_id="repo-1" provider="github" path="nwverse\/persona">\n/,
    );
    expect(text).toContain("nwverse/persona");
    expect(text).toContain("https://github.com/nwverse/persona");
    expect(text).not.toContain('cwd="');
    expect(text).not.toContain('machine="');
    expect(text.endsWith("</persona-context>\n\nCheck open issues")).toBe(true);
  });

  it("supports Tab selection, pointer click selection, chip removal, and Escape dismissal", async () => {
    const onSend = vi.fn(async (_m: Outgoing) => true);
    render({ self: "persona-new", files: () => [], repositories: "assistant" }, onSend);

    // Tab picks filtered repository
    await type("@invoice");
    await waitForOptions(1);
    key("Tab");
    expect(box().value).toBe("");
    expect(getContexts("persona-new")).toHaveLength(1);
    expect(getContexts("persona-new")[0]).toMatchObject({
      kind: "repository",
      title: "team/billing-api",
      url: "https://gitlab.example.com/team/billing-api",
    });

    // Click picks a second repository
    await type("@release-bot");
    await waitForOptions(1);
    await act(async () => options()[0].click());
    expect(getContexts("persona-new")).toHaveLength(2);

    // Remove the first chip
    await act(async () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("link-contextchips.remove_label")}"]`)!.click());
    expect(getContexts("persona-new")).toHaveLength(1);
    expect(getContexts("persona-new")[0].title).toBe("Deploy Kit");

    // Escape closes the menu without selecting
    await type("@persona");
    await waitForOptions(2);
    key("Escape");
    expect(options()).toHaveLength(0);
  });

  it("is not offered in non-assistant composers", async () => {
    render({ self: "m/s", files: () => [] }, async () => true, "m/s");
    await type("@persona");
    expect(api).not.toHaveBeenCalledWith("/assistant/repositories", expect.anything());
    expect(host.textContent).not.toContain(t("chat-composerpanel.repositories"));
    expect(options()).toHaveLength(0);
  });

  it("degrades cleanly on empty lists, endpoint failures, and mismatched managers", async () => {
    // 1. Endpoint error
    repoReply = async () => {
      throw new Error("503 unavailable");
    };
    const onSend = vi.fn(async (_m: Outgoing) => true);
    render({ self: "persona-new", files: () => [], repositories: "assistant" }, onSend);
    await type("@");
    await vi.waitFor(() => expect(repoCalls()).toHaveLength(1));
    await act(async () => {});
    expect(options()).toHaveLength(0);
    expect(host.textContent).not.toContain(t("chat-composerpanel.repositories"));
    await type("Ordinary message still works");
    await act(async () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-composer.send")}"]`)!.click());
    expect(onSend).toHaveBeenCalledWith({ text: "Ordinary message still works", images: [] });

    // 2. Empty repository list
    repoReply = async () => ({ manager: "assistant", catalog_revision: 0, repositories: [] });
    await type("@");
    await vi.waitFor(() => expect(repoCalls()).toHaveLength(2));
    await act(async () => {});
    expect(options()).toHaveLength(0);

    // 3. Manager mismatch ignored
    repoReply = async () => ({ manager: "other-manager", catalog_revision: 1, repositories: REPOSITORIES });
    key("Escape");
    await type("x");
    await type("@");
    await vi.waitFor(() => expect(repoCalls()).toHaveLength(3));
    await act(async () => {});
    expect(options()).toHaveLength(0);
  });
});
