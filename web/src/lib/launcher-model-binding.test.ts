import { describe, expect, it } from "vitest";
import { agyModels, claudeModels, codexModels, neon } from "./launcher-model-binding.fixture";
import { foldLauncherModels, launcherBindingState } from "./launcher-model-binding";

describe("launcher model and effort bindings", () => {
  it("chooses the listed Medium variant by default and preserves a compatible user's choice", () => {
    const [row] = foldLauncherModels(agyModels.models);
    expect(row).toMatchObject({ name: "Gemini 4 Neon", id: `${neon}-medium`, resolved: neon });
    expect(launcherBindingState(agyModels, {}).pickModel(row.id)).toEqual({ model: `${neon}-medium`, effort: "" });
    expect(launcherBindingState(agyModels, { effort: "high" }).pickModel(row.id)).toEqual({ model: `${neon}-high`, effort: "" });
    expect(launcherBindingState(agyModels, { effort: "max" }).pickModel(row.id)).toEqual({ model: `${neon}-medium`, effort: "" });
    const withoutMedium = agyModels.models.filter((m) => m.default_effort !== "medium");
    expect(foldLauncherModels(withoutMedium)[0].id).toBe(`${neon}-low`);
  });

  it("uses the saved variant's tier and shows exactly the levels with siblings", () => {
    const list = { ...agyModels, models: agyModels.models.filter((m) => m.default_effort !== "high").reverse() };
    const state = launcherBindingState(list, { model: `${neon}-xhigh` });
    expect(state.value).toBe("xhigh");
    expect(state.choices).toEqual(["low", "medium", "xhigh"]);
    expect(state.pickEffort("")).toEqual({ model: `${neon}-medium`, effort: "" });
    expect(state.pickEffort("low")).toEqual({ model: `${neon}-low`, effort: "" });
    expect(state.pickEffort("max")).toEqual({ model: `${neon}-xhigh`, effort: "" });
  });

  it.each(["high", "medium", "max"])("clears the saved standalone %s after resolving the variant", (effort) => {
    const level = effort === "max" ? "medium" : effort;
    const state = launcherBindingState(agyModels, { model: `${neon}-medium`, effort });
    expect(state.value).toBe(level);
    expect(state.model).toBe(`${neon}-${level}`);
    expect(state.legacy).toEqual({ model: `${neon}-${level}`, effort: "" });
  });

  it("keeps inherited variants dynamic until an effort level is explicitly chosen", () => {
    const followed = { model: `${neon}-medium` };
    const state = launcherBindingState(agyModels, {}, followed);
    expect(state.value).toBe("");
    expect(state.defaultEffort).toBe("medium");
    expect(state.pickEffort("")).toEqual({ effort: "" });
    expect(state.pickEffort("high")).toEqual({ model: `${neon}-high`, effort: "" });
    expect(launcherBindingState(agyModels, { effort: "high" }, followed).legacy).toEqual({ model: `${neon}-high`, effort: "" });
  });

  it("leaves singleton variants, nonmatching suffixes and remembered custom IDs separate", () => {
    const models = [
      { id: `${neon}-low`, name: "Gemini 4 Neon (Low)", default_effort: "low", efforts: ["low"] },
      { id: `${neon}-medium`, name: "Custom medium", default_effort: "medium", custom: true },
      { id: `${neon}-high`, name: "Other model", default_effort: "low" },
    ];
    const list = { agent: "agy", models };
    expect(foldLauncherModels(models)).toEqual(models);
    const state = launcherBindingState(list, { model: `${neon}-low`, effort: "low" });
    expect(state.legacy).toBeUndefined();
    expect(state.pickEffort("low")).toEqual({ effort: "low" });
    expect(state.pickModel("provider/custom-id")).toEqual({ model: "provider/custom-id" });
    expect(launcherBindingState(list, { model: "provider/custom-id" }).choices).toContain("high");
  });

  it.each([claudeModels, codexModels])("keeps $agent model and effort independent", (list) => {
    const model = list.models[0].id;
    const state = launcherBindingState(list, { model, effort: "high" });
    expect(state.legacy).toBeUndefined();
    expect(state.pickModel(model)).toEqual({ model });
    expect(state.pickEffort("medium")).toEqual({ effort: "medium" });
  });
});
