import type { LauncherModels } from "./launcher-models";
import { effortTitle } from "./session-actions";

export const neon = "gemini-4-neon";
export const agyModels: LauncherModels = {
  agent: "agy",
  models: [
    ...["low", "medium", "high", "xhigh"].map((level) => ({
      id: `${neon}-${level}`,
      name: `Gemini 4 Neon (${effortTitle(level)})`,
      default_effort: level,
      efforts: ["low", "medium", "high", "xhigh"],
    })),
    { id: "gemini-4-flash", name: "Gemini 4 Flash" },
  ],
};

export const codexModels: LauncherModels = {
  agent: "codex",
  models: [{ id: "gpt-5.5", name: "GPT-5.5", efforts: ["minimal", "medium", "xhigh", "ultra"] }],
};
export const claudeModels: LauncherModels = {
  agent: "claude",
  models: [{ id: "sonnet", name: "Sonnet", efforts: ["low", "high"] }],
};
