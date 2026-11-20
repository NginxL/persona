import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const LANGS = ["en", "zh-Hans", "ja"] as const;
const MODES = ["default", "acceptEdits", "plan", "auto", "bypassPermissions"] as const;

type Language = (typeof LANGS)[number];
type Mode = (typeof MODES)[number];
type Names = Record<Mode, Record<Language, string>>;
type Key = [namespace: string, key: string];

// Header, composer and mode-change lines use lib-session-actions. Answered-mode and planned-task
// labels are standalone aliases. Mode-switch confirmations must contain the same Full access name;
// approval action phrases, launcher-default and unset/default-behavior variants are not aliases.
const EXACT: Record<Mode, Key[]> = {
  default: [["lib-session-actions", "mode_default"], ["lib-answered", "mode_default"], ["assistant-tasks", "perm_default"]],
  acceptEdits: [["lib-session-actions", "mode_accept_edits"], ["lib-answered", "mode_accept_edits"], ["assistant-tasks", "perm_accept_edits"]],
  plan: [["lib-session-actions", "mode_plan"], ["assistant-tasks", "perm_plan"]],
  auto: [["lib-session-actions", "mode_auto"], ["assistant-tasks", "perm_auto"]],
  bypassPermissions: [["lib-session-actions", "mode_bypass_permissions"], ["lib-answered", "mode_bypass"], ["assistant-tasks", "perm_bypass"]],
};

const CONTAINS: Record<Mode, Key[]> = {
  default: [],
  acceptEdits: [],
  plan: [],
  auto: [],
  bypassPermissions: [
    ["chat-sessiondialogs", "bypass_confirm"],
    ["chat-sessiondialogs", "leave_bypass_detail"],
    ["chat-sessiondialogs", "bypass_pending"],
    ["lib-session-actions", "bypass_warning"],
    ["assistant-taskeditor", "bypass_title"],
    ["assistant-taskeditor", "bypass_confirm"],
    ["assistant-taskeditor", "bypass_on"],
  ],
};

function json<T>(url: URL): T {
  return JSON.parse(readFileSync(url, "utf8")) as T;
}

describe("permission mode names (#10363)", () => {
  it("uses the shared table in every web locale, including approvals and confirmations", () => {
    const fixture = json<{ names: Names }>(new URL("../../../spec/fixtures/permission-modes/names.json", import.meta.url));
    expect(Object.keys(fixture.names).sort()).toEqual([...MODES].sort());

    for (const language of LANGS) {
      const catalogs = new Map<string, Record<string, string>>();
      const value = ([namespace, key]: Key) => {
        if (!catalogs.has(namespace)) {
          catalogs.set(namespace, json(new URL(`./locales/${language}/${namespace}.json`, import.meta.url)));
        }
        return catalogs.get(namespace)![key];
      };

      for (const mode of MODES) {
        const expected = fixture.names[mode][language];
        for (const key of EXACT[mode]) expect(value(key), `${language} ${key.join(".")}`).toBe(expected);
        for (const key of CONTAINS[mode]) expect(value(key), `${language} ${key.join(".")}`).toContain(expected);
      }
    }
  });
});
