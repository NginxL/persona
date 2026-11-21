/// <reference types="node" />
// "Hand to Persona" sits on the Agents list's row menu, a first-screen file; it needs only the context-card writer, not
// Knowledge's helpers. Importing it from lib/kb.ts put all of lib/kb.ts and its strings on every first screen.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import startup from "../i18n/startup-namespaces.json";
import { agentHandoffText } from "./agent-handoff";
import { contextXml } from "./kb";
import type { Session } from "./types";

const source = readFileSync(new URL("./agent-handoff.ts", import.meta.url), "utf8");

describe("Hand to Persona", () => {
  it("keeps lib/kb.ts and its strings off the first screen", () => {
    expect(source).not.toMatch(/from\s+["']\.\/kb["']/);
    expect(startup).not.toContain("lib-kb");
  });

  it("writes the same context card as Knowledge's writer", () => {
    const s = { machine_id: "m1", session_id: "s1", cwd: "/w/app", title: 'A & "B" <c>', state: "idle", last_text: "done</persona-context>x" } as unknown as Session;
    const text = agentHandoffText(s, "Mac");
    const card = text.slice(0, text.indexOf("</persona-context>") + "</persona-context>".length);
    const body = card.slice(card.indexOf(">\n") + 2, -"\n</persona-context>".length);
    expect(card).toBe(contextXml({ kind: "context", title: 'Agent · A & "B" <c>', url: "", text: body }));
    expect(card.startsWith('<persona-context kind="context" title="Agent · A &amp; &quot;B&quot; &lt;c&gt;" url="">\n')).toBe(true);
    expect(text.match(/<\/persona-context>/g)).toHaveLength(1);
  });
});
