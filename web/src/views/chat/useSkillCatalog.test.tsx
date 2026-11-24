// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import type { MentionSkill, SkillCatalog, SkillSource } from "../../lib/skill-mentions";
import { SkillCatalogStatus } from "./SkillCatalogStatus";
import { useSkillCatalog } from "./useSkillCatalog";

const api = vi.hoisted(() => vi.fn());
vi.mock("../../lib/api", async (orig) => ({ ...(await orig<typeof import("../../lib/api")>()), api }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const A: SkillSource = { manager: "assistant", environment: "environment-a", launcher: "claude" };
const B: SkillSource = { manager: "assistant", environment: "environment-b", launcher: "codex" };
const skill = (id: string, name: string): MentionSkill => ({ id, name, description: `${name} description`, source: "project" });
const answer = (version: number, complete: boolean, skills: MentionSkill[]): SkillCatalog => ({ catalog_version: version, complete, skills });
let pending: { resolve: (catalog: SkillCatalog) => void }[];
let host: HTMLDivElement;
let root: Root;

function Catalog({ source, enabled = true }: { source: SkillSource; enabled?: boolean }) {
  const current = useSkillCatalog(source, "computer", "/example", undefined, enabled);
  return <>
    <span data-catalog-state>{current.status ?? "ready"}</span>
    {current.catalog?.skills.map((s) => <span data-skill key={s.id}>{s.name}</span>)}
    <SkillCatalogStatus status={current.status} />
  </>;
}

function render(source: SkillSource, enabled = true) {
  act(() => root.render(<Catalog source={source} enabled={enabled} />));
}

async function settle(index: number, catalog: SkillCatalog) {
  await act(async () => {
    pending[index].resolve(catalog);
    await Promise.resolve();
  });
}

const names = () => [...host.querySelectorAll("[data-skill]")].map((row) => row.textContent);
const state = () => host.querySelector("[data-catalog-state]")!.textContent;

beforeEach(() => {
  localStorage.clear();
  pending = [];
  api.mockReset();
  // Deliberately ignore the AbortSignal: the test controls when an abandoned request answers.
  api.mockImplementation(() => new Promise((resolve) => { pending.push({ resolve }); }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => { act(() => root.unmount()); host.remove(); });

it("rejects a newer catalog version instead of listing skills as available", async () => {
  render(A);
  await settle(0, answer(2, true, [skill("new", "unsupported")]));
  expect(names()).toEqual([]);
  expect(state()).toBe("error");
  expect(host.textContent).toContain(t("chat-composerpanel.skills_error"));
});

it("shows loading and none of environment A's rows while environment B is pending", async () => {
  render(A);
  await settle(0, answer(1, true, [skill("a", "from-a")]));
  expect(names()).toEqual(["from-a"]);
  render(B);
  expect(names()).toEqual([]);
  expect(state()).toBe("loading");
  expect(host.textContent).toContain(t("chat-composerpanel.skills_loading"));
  await settle(1, answer(1, true, [skill("b", "from-b")]));
  expect(names()).toEqual(["from-b"]);
});

it("distinguishes a partial inventory from a complete one", async () => {
  render(A);
  await settle(0, answer(1, false, [skill("a", "verified-local")]));
  expect(names()).toEqual(["verified-local"]);
  expect(host.textContent).toContain(t("chat-composerpanel.skills_partial"));
  render(B);
  await settle(1, answer(1, true, [skill("b", "complete")]));
  expect(names()).toEqual(["complete"]);
  expect(host.textContent).not.toContain(t("chat-composerpanel.skills_partial"));
  expect(host.querySelector("[role=status]")).toBeNull();
});

it("ignores an abandoned request that answers after the newer environment", async () => {
  render(A);
  render(B);
  await settle(1, answer(1, true, [skill("b", "newer-environment")]));
  expect(names()).toEqual(["newer-environment"]);
  await settle(0, answer(1, true, [skill("a", "abandoned-late")]));
  expect(names()).toEqual(["newer-environment"]);
  expect(host.textContent).not.toContain("abandoned-late");
  expect(state()).toBe("ready");
});

it("does not request the catalog or report an error while disabled, then reads once when enabled", async () => {
  render(A, false);
  expect(api).not.toHaveBeenCalled();
  expect(names()).toEqual([]);
  expect(host.querySelector("[role=status]")).toBeNull();
  render(A, true);
  expect(api).toHaveBeenCalledTimes(1);
  expect(state()).toBe("loading");
  await settle(0, answer(1, true, [skill("a", "persona-office")]));
  expect(names()).toEqual(["persona-office"]);
  expect(state()).toBe("ready");
});
