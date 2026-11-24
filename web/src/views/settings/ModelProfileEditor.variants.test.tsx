// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { initI18n, t } from "../../i18n";
import { agyModels, claudeModels, codexModels, neon } from "../../lib/launcher-model-binding.fixture";
import type { ModelProvider, ModelPurpose } from "../../lib/model-config";
import type { ModelProfile } from "../../lib/model-profiles";
import { ModelPicker } from "./ModelBindingPickers";
import { ModelProfileEditor } from "./ModelProfileEditor";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const catalogs = { agy: agyModels, claude: claudeModels, codex: codexModels };
const providers: ModelProvider[] = Object.entries(catalogs).map(([agent, catalog]) => ({
  id: `launcher.${agent}`, name: agent, agent, kind: "launcher", model: agent === "agy" ? `${neon}-medium` : catalog.models[0].id,
}));
const purpose: ModelPurpose = {
  id: "assistant", name: "Main chat", group: "Assistant", kind: "launcher", revision: 1,
  override: {}, effective: { provider: "launcher.agy", model: `${neon}-medium` }, fields: ["provider", "model", "effort"],
  provider_ids: providers.map((provider) => provider.id),
};

let host: HTMLDivElement;
let root: Root;
const saved = vi.fn(async () => {});
const card = (id: string) => host.querySelector<HTMLElement>(`[data-testid="profile-${id}"]`)!;
const effort = (scope: ParentNode, label = t("settings-modelconfiguration.effort")) =>
  [...scope.querySelectorAll("select")].find((node) => node.closest("label")?.querySelector("span")?.textContent === label)!;
const launcherEffort = (name: string) => effort(card(`launcher-${name}`), t("settings-modelprofiles.field_effort"));
const modelPicker = (scope: ParentNode) =>
  [...scope.querySelectorAll<HTMLButtonElement>("button[aria-expanded]")].filter((node) => !node.closest("details")).at(-1)!;

async function render(profile: Partial<ModelProfile>) {
  await act(async () => root.render(<ModelProfileEditor manager="assistant" revision={2}
    profile={{ id: "test-profile", name: "Test profile", changes: [], ...profile }}
    purposes={[purpose]} providers={providers} busy={false} error="" conflict={false}
    onSave={saved} onReload={async () => {}} onCancel={() => {}} />));
}

async function choose(select: HTMLSelectElement, value: string) {
  await act(async () => {
    select.value = value;
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

async function pickNeon(scope: ParentNode) {
  if (modelPicker(scope).getAttribute("aria-expanded") !== "true") await act(async () => modelPicker(scope).click());
  const options = [...scope.querySelectorAll<HTMLButtonElement>('[role="option"]')].filter((node) => node.textContent?.startsWith("Gemini 4 Neon"));
  expect(options).toHaveLength(1);
  await act(async () => options[0].click());
}

async function save() {
  const button = [...host.querySelectorAll("button")].find((node) => node.textContent === t("settings-modelprofiles.save"))!;
  expect(button.disabled).toBe(false);
  await act(async () => button.click());
}

beforeEach(async () => {
  await initI18n("en");
  await ModelPicker.preload();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  saved.mockClear();
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input);
    if (path.endsWith("/launchers")) return Response.json({ launchers: providers.map((provider) => ({
      name: provider.agent, source: "persona", kind: "launcher", agent: provider.agent, model: provider.model,
    })) });
    const agent = path.match(/launcher\.(\w+)\/models/)?.[1] as keyof typeof catalogs | undefined;
    if (agent && catalogs[agent]) return Response.json(catalogs[agent]);
    throw new Error(`Unexpected request ${path}`);
  }));
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

it("hydrates purpose, managed launcher, and preset variants and cleans legacy fields on Save", async () => {
  const legacy = { model: `${neon}-medium`, effort: "high" };
  await render({
    purposes: { assistant: { provider: "launcher.agy", ...legacy } },
    launchers: { agy: legacy },
    tiers: { review: { launcher: "agy", model: `${neon}-medium` } },
  });
  const purposeCard = card("purpose-assistant");
  const launcherCard = card("launcher-agy");
  const presetCard = card("tier-edit-review");
  expect(modelPicker(purposeCard).textContent).toBe(`Gemini 4 Neon${neon}-high`);
  expect(modelPicker(launcherCard).textContent).toBe(`Gemini 4 Neon${neon}-high`);
  expect(modelPicker(presetCard).textContent).toBe(`Gemini 4 Neon${neon}-medium`);
  expect([effort(purposeCard).value, launcherEffort("agy").value, effort(presetCard).value]).toEqual(["high", "high", "medium"]);
  await save();
  expect(saved).toHaveBeenLastCalledWith(expect.objectContaining({
    purposes: { assistant: { provider: "launcher.agy", model: `${neon}-high` } },
    launchers: { agy: { model: `${neon}-high`, effort: "" } },
    tiers: { review: { launcher: "agy", model: `${neon}-medium` } },
  }));

  for (const select of [effort(purposeCard), launcherEffort("agy"), effort(presetCard)]) await choose(select, "xhigh");
  await save();
  expect(saved).toHaveBeenLastCalledWith(expect.objectContaining({
    purposes: { assistant: { provider: "launcher.agy", model: `${neon}-xhigh` } },
    launchers: { agy: { model: `${neon}-xhigh`, effort: "" } },
    tiers: { review: { launcher: "agy", model: `${neon}-xhigh` } },
  }));
});

it("maps inherited effort edits and defaults a newly picked preset to Medium", async () => {
  await render({ purposes: { assistant: {} }, tiers: { review: { launcher: "agy" } } });
  const scope = card("purpose-assistant");
  expect(effort(scope).value).toBe("");
  expect(effort(scope).options[0].textContent).toContain("Medium");
  await choose(effort(scope), "high");
  const preset = card("tier-edit-review");
  await pickNeon(preset);
  expect(effort(preset).value).toBe("medium");
  await choose(effort(preset), "xhigh");
  await choose(effort(preset), "");
  expect(effort(preset).value).toBe("medium");
  await save();
  expect(saved).toHaveBeenLastCalledWith(expect.objectContaining({
    purposes: { assistant: { model: `${neon}-high` } },
    tiers: { review: { launcher: "agy", model: `${neon}-medium` } },
  }));
});

it("preserves leave unchanged, clear, and explicit launcher fields and independent Claude and Codex effort", async () => {
  await render({
    launchers: { agy: { small_model: "" }, claude: { model: "sonnet", effort: "low" } },
    tiers: { review: { launcher: "codex", model: "gpt-5.5", effort: "medium" } },
  });
  expect(launcherEffort("agy").selectedOptions[0].textContent).toBe(t("settings-modelprofiles.leave_unchanged"));
  await choose(launcherEffort("agy"), "");
  await choose(launcherEffort("claude"), "high");
  await choose(effort(card("tier-edit-review")), "xhigh");
  await save();
  expect(saved).toHaveBeenLastCalledWith(expect.objectContaining({
    launchers: { agy: { small_model: "", effort: "" }, claude: { model: "sonnet", effort: "high" } },
    tiers: { review: { launcher: "codex", model: "gpt-5.5", effort: "xhigh" } },
  }));

  await choose(launcherEffort("agy"), "__unset__");
  await choose(launcherEffort("claude"), "__unset__");
  await save();
  expect(saved).toHaveBeenLastCalledWith(expect.objectContaining({
    launchers: { agy: { small_model: "" }, claude: { model: "sonnet" } },
  }));

  await choose(launcherEffort("agy"), "high");
  await save();
  expect(saved).toHaveBeenLastCalledWith(expect.objectContaining({
    launchers: { agy: { model: `${neon}-high`, small_model: "", effort: "" }, claude: { model: "sonnet" } },
  }));
});
