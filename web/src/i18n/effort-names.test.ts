import { describe, expect, it } from "vitest";
import { initI18n, t } from ".";
import { optionText } from "../lib/agent-settings";
import { CODEX_EFFORTS, EFFORTS, effortTitle } from "../lib/session-actions";

const tiers = ["Minimal", "Low", "Medium", "High", "XHigh", "Max", "Ultra"];
const langs = ["en", "zh-Hans", "ja"] as const;

describe("reasoning effort names", () => {
  it.each(langs)("uses canonical names across the catalog and settings in %s", async (lang) => {
    await initI18n(lang);
    expect(EFFORTS.map((level) => level.title)).toEqual(tiers.slice(1, -1));
    expect(CODEX_EFFORTS.map((level) => level.title)).toEqual(tiers);
    for (const title of tiers) {
      const id = title.toLowerCase();
      expect(t(`lib-session-actions.effort_${id}`)).toBe(title);
      expect(effortTitle(id)).toBe(title);
      expect(optionText("effortLevel", id)).toBe(title);
      expect(optionText("model_reasoning_effort", id)).toBe(title);
      expect(optionText("defaultThinkingLevel", id)).toBe(title);
    }
  });

  it("keeps an explanation in each language for every tier", async () => {
    for (const [prefix, levels] of [["effort", EFFORTS], ["codex_effort", CODEX_EFFORTS]] as const) {
      const details = [];
      for (const lang of langs) {
        await initI18n(lang);
        details.push(levels.map(({ id }) => t(`lib-session-actions.${prefix}_${id}_detail`)));
      }
      for (const [index, level] of levels.entries()) {
        const descriptions = details.map((locale) => locale[index]);
        expect(descriptions.every((text) => text && !text.startsWith("lib-session-actions.")), level.id).toBe(true);
        expect(new Set(descriptions).size, level.id).toBe(langs.length);
      }
    }
  });
});
