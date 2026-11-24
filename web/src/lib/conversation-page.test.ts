import { describe, expect, it } from "vitest";
import { conversationRoute, spacePage } from "./conversation-page";
import type { Session } from "./types";

describe("a space conversation's page", () => {
  it("is its space's page, where its plan cards and questions show, not the bare agent chat", () => {
    const row = (space?: string): Record<string, Session[]> => ({ m1: [{ session_id: "c1", machine_id: "m1", assistant: true, space } as Session] });
    expect(conversationRoute("m1", "c1", row("zz-notes"), null)).toEqual({ name: "track", slug: "zz-notes", sid: "c1" });
    expect(conversationRoute("m1", "c1", row("health"), null)).toEqual({ name: "track", slug: "health", sid: "c1" });
    expect(spacePage("zz-notes", "c1")).toEqual({ name: "track", slug: "zz-notes", sid: "c1" });
    // an agent's conversation stays on /s/
    expect(conversationRoute("m1", "a1", { m1: [{ session_id: "a1", machine_id: "m1" } as Session] }, null)).toEqual({ name: "session", mid: "m1", sid: "a1" });
  });
});
