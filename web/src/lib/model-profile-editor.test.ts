import { beforeAll, describe, expect, it } from "vitest";
import { initI18n } from "../i18n";
import type { ModelProfile } from "./model-profiles";
import {
  draftToProfileInput,
  isProfileDraftDirty,
  profileDraftProblems,
  profileToDraft,
} from "./model-profile-editor";

beforeAll(async () => {
  await initI18n("en");
});

const baseProfile: ModelProfile = {
  id: "economy",
  name: "Economy",
  description: "Everyday models",
  purposes: {
    assistant: { provider: "launcher.gw", model: "sonnet-4", effort: "medium", permission_mode: "acceptEdits" },
    kb: {},
  },
  launchers: {
    gw: { model: "sonnet-4", small_model: "", effort: "low" },
  },
  tiers: {
    strong: { launcher: "gw", model: "opus-4", effort: "high", permission_mode: "plan", speed: "fast", description: "Hard work" },
  },
  changes: [],
};

describe("model-profile-editor", () => {
  it("round-trips a profile through draft and save input without emitting config_dir or secrets", () => {
    const draft = profileToDraft(baseProfile, ["assistant", "agent.new", "kb", "inbox.triage", "agent.delegate", "translate"]);
    expect(isProfileDraftDirty(draft, profileToDraft(baseProfile))).toBe(false);
    expect(profileDraftProblems(draft, { gw: "launcher" })).toEqual([]);

    const input = draftToProfileInput(draft, 5);
    expect(input).toEqual({
      revision: 5,
      name: "Economy",
      description: "Everyday models",
      purposes: {
        assistant: { provider: "launcher.gw", model: "sonnet-4", effort: "medium", permission_mode: "acceptEdits" },
        kb: {},
      },
      launchers: {
        gw: { model: "sonnet-4", small_model: "", effort: "low" },
      },
      tiers: {
        strong: { launcher: "gw", model: "opus-4", effort: "high", permission_mode: "plan", speed: "fast", description: "Hard work" },
      },
    });
    expect(JSON.stringify(input)).not.toContain("config_dir");
    expect(JSON.stringify(input)).not.toContain("api_key");
  });

  it("omits unchecked purposes and empty launcher entries", () => {
    const draft = profileToDraft(baseProfile);
    draft.purposes.kb.included = false;
    draft.launchers.gw = {};
    const input = draftToProfileInput(draft, 2);
    expect(input.purposes).toEqual({
      assistant: { provider: "launcher.gw", model: "sonnet-4", effort: "medium", permission_mode: "acceptEdits" },
    });
    expect(input.launchers).toBeUndefined();
    expect(isProfileDraftDirty(draft, profileToDraft(baseProfile))).toBe(true);
  });

  it("validates profile name, launcher API restrictions, and tier constraints", () => {
    const draft = profileToDraft(baseProfile);
    draft.name = "   ";
    draft.launchers.api_only = { model: "m-1", small_model: "m-2" };
    draft.tiers.push({
      key: "bad-1",
      name: "Invalid Name",
      launcher: "",
      model: "bad model id!",
      effort: "extreme",
      permission_mode: "bogusMode",
      speed: "warp",
      description: "line1\nline2",
    });
    const problems = profileDraftProblems(draft, { gw: "launcher", api_only: "api" });
    expect(problems.length).toBeGreaterThanOrEqual(7);
    expect(problems.some((p) => p.includes("Profile name"))).toBe(true);
    expect(problems.some((p) => p.includes("api_only"))).toBe(true);
    expect(problems.some((p) => p.includes("Invalid Name"))).toBe(true);
    expect(problems.some((p) => p.includes("bad model id!"))).toBe(true);
    expect(problems.some((p) => p.includes("permission_mode"))).toBe(true);
  });

  it("detects duplicate tier names and preserves untouched legacy tier model IDs", () => {
    const legacy: ModelProfile = {
      id: "legacy",
      name: "Legacy",
      tiers: {
        old: { launcher: "claude", model: "bad!legacy" },
      },
      changes: [],
    };
    const draft = profileToDraft(legacy);
    // Untouched legacy model ID does not block editing unrelated fields (mirrors server/internal/assistant/model_profiles_edit.go).
    expect(profileDraftProblems(draft)).toEqual([]);

    // Adding a duplicate tier name is flagged.
    draft.tiers.push({
      key: "dup",
      name: "old",
      launcher: "claude",
      model: "valid-model",
      effort: "",
      permission_mode: "",
      speed: "",
      description: "",
    });
    expect(profileDraftProblems(draft).some((p) => p.includes("Duplicate"))).toBe(true);
  });
});
