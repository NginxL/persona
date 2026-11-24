// @vitest-environment jsdom
// The health space stays the default manager's whatever assistant the Persona page shows (docs/assistant/managers.md
// "Clients", docs/assistant/health.md): its conversation list and a new conversation name no manager. Moved here from
// lib/manager-choice.test.ts so the core's tests run without this module (#13932).
import { afterEach, describe, expect, it } from "vitest";
import { chooseManager, resetManagerChoice } from "../../lib/manager-choice";
import { startHealthConversation } from "./reports";

afterEach(() => resetManagerChoice());

describe("health conversations and the shown manager", () => {
  it("name no manager after another is picked: the list and a new conversation", async () => {
    const { api } = await import("../../lib/api");
    const seen: string[] = [];
    const real = globalThis.fetch;
    globalThis.fetch = (async (url: string, init: RequestInit) => {
      seen.push(`${(init.method ?? "GET")} ${url.replace(/^.*\/api\/v1/, "")} ${new Headers(init.headers).get("X-Persona-Manager")}`);
      return new Response("{}", { status: 200 });
    }) as typeof fetch;
    try {
      chooseManager("m-1");
      seen.length = 0;
      await api("/assistant?space=health");
      await startHealthConversation({ prompt: "x" }).catch(() => {});
      expect(seen.filter((x) => x.includes("space=health"))).toEqual(["GET /assistant?space=health null", "POST /assistant/conversations?space=health null"]);
    } finally {
      globalThis.fetch = real;
    }
  });
});
