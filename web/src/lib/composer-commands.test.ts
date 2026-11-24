import { describe, expect, it } from "vitest";
import { agentCommands, hasSlashSkillQuery, paneClaude, rankCommands, rankSlashSkills, SLASH_LIMIT } from "./composer-commands";
import { t } from "../i18n";
import { WORKFLOWS_COMMAND } from "./background-panel";
import { LOOP_TEXT } from "./loop-command";
import type { Session } from "./types";
import { PERMISSIONS_COMMAND } from "./permissions-command";

const session = (o: Partial<Session>): Session => ({ machine_id: "m", session_id: "s", cwd: "/x", status: "idle", state: "idle", pane_id: "p1", ...o }) as Session;
const ALL = [{ text: "继续" }, { text: "/compact" }, { text: "/model " }, { text: LOOP_TEXT }, { text: "/clear" }];
const words = (s?: Session) => agentCommands(ALL, s).map((c) => c.text.trim());

// The "/" list by what the session can do (iOS canPickModel / canClear, Android canTypeClaudeCommands).
describe("agentCommands", () => {
  it("offers /model and /clear to an idle Claude in a pane", () => {
    expect(words(session({}))).toEqual(["继续", "/compact", "/model", LOOP_TEXT.trim(), "/clear"]);
  });
  it("keeps /clear away while it works or waits for you", () => {
    expect(words(session({ state: "working", status: "working" }))).toEqual(["继续", "/compact", "/model", LOOP_TEXT.trim()]);
    expect(words(session({ state: "needs_you", status: "blocked" }))).toEqual(["继续", "/compact", "/model", LOOP_TEXT.trim()]);
  });
  it("has neither for Codex, TRAE CLI or a runtime that cannot switch models", () => {
    for (const s of [session({ agent: "codex" }), session({ agent: "traex" }), session({ source_kind: "codex_app_server", capabilities: { send: true, terminal: true } })]) {
      const w = words(s);
      expect(w).not.toContain("/model");
      expect(w).not.toContain("/clear");
    }
  });
  it("offers /model wherever the model pill switches models, /clear only in a pane", () => {
    const w = words(session({ pane_id: undefined }));
    expect(w).toContain("/model");
    expect(w).not.toContain("/clear");
  });
  it("drops /loop where the agent has none", () => {
    expect(words(session({ agent: "codex" }))).toEqual(["继续", "/compact"]);
  });
  it("has neither /model nor /clear for an offline Claude", () => {
    expect(words(session({ state: "offline", status: "offline" }))).not.toContain("/model");
  });
});

// A runtime's session (#11237): /model where it says the model can switch; Claude Code's typed commands never.
describe("agentCommands on a runtime's session", () => {
  const LIST = [{ text: "继续" }, { text: "/compact" }, { text: "/context" }, { text: "/model " }];
  const runtime = (o: Partial<Session> = {}) => session({ source_kind: "antigravity", pane_id: undefined, agent: "antigravity", capabilities: { send: true, model: true }, ...o });
  const list = (s?: Session) => agentCommands(LIST, s).map((c) => c.text.trim());
  it("lists /model where capabilities.model says so, and no /compact or /context", () => {
    expect(list(runtime())).toEqual(["继续", "/model"]);
    expect(list(runtime({ capabilities: { send: true, model: false } }))).toEqual(["继续"]);
  });
  it("keeps /context to Claude Code's terminal and /compact to terminal agents", () => {
    expect(list(session({}))).toEqual(["继续", "/compact", "/context", "/model"]);
    expect(list(session({ agent: "codex" }))).toEqual(["继续", "/compact"]);
    expect(list(session({ assistant: true }))).toEqual(["继续"]);
  });
});

// /workflows opens the Agent's Background tasks panel (#5421, cindy's): Claude Code Agents only.
describe("agentCommands' /workflows", () => {
  const LIST = [{ text: "继续" }, { text: WORKFLOWS_COMMAND }];
  const has = (s?: Session) => agentCommands(LIST, s).some((c) => c.text === WORKFLOWS_COMMAND);
  it("is listed for a Claude Code Agent, working or not, in a pane or not", () => {
    expect(has(session({}))).toBe(true);
    expect(has(session({ agent: "claude", state: "working", status: "working" }))).toBe(true);
    expect(has(session({ pane_id: undefined }))).toBe(true);
  });
  it("is not listed for the assistant, Codex, another runtime or no session", () => {
    for (const s of [session({ assistant: true }), session({ agent: "codex" }), session({ source_kind: "codex_app_server" }), session({ agent: "opencode" }), undefined]) expect(has(s)).toBe(false);
  });
  it("is Persona's own row: no Agent named as its source", () => {
    expect(agentCommands(LIST, session({})).find((c) => c.text === WORKFLOWS_COMMAND)).toEqual({ text: WORKFLOWS_COMMAND });
  });
});

describe("paneClaude", () => {
  it("is Claude in a herdr pane that takes messages", () => {
    expect(paneClaude(session({}))).toBe(true);
    expect(paneClaude(session({ agent: "claude" }))).toBe(true);
    expect(paneClaude(session({ assistant: true }))).toBe(false);
    expect(paneClaude(undefined)).toBe(false);
  });
});

// The palette's row names where an agent's own slash command comes from: the session's agent, not always Claude Code.
describe("agentCommands' source label", () => {
  const LIST: { text: string; agent?: boolean; source?: string }[] = [{ text: "继续" }, { text: "/compact", agent: true }];
  const sources = (s?: Session) => agentCommands(LIST, s).map((c) => c.source);
  it("is the agent's name on its own commands only", () => {
    expect(sources(session({}))).toEqual([undefined, t("agent-mark.claude")]);
    expect(sources(session({ agent: "codex" }))).toEqual([undefined, t("agent-mark.codex")]);
    expect(sources(undefined)).toEqual([undefined, t("agent-mark.claude")]);
  });
  it("leaves the list it was given alone", () => {
    agentCommands(LIST, session({}));
    expect(LIST[1]).toEqual({ text: "/compact", agent: true });
  });
});

// cindy's ranking (filterSlashCommands): exact, prefix, contains on the name, stable within each; descriptions after.
describe("rankCommands", () => {
  const cmd = (word: string, desc = "") => ({ word, desc });
  const words = (list: { word: string }[]) => list.map((c) => c.word);

  it("puts an exact name first, then names starting with it, then names containing it, each in list order", () => {
    const list = [cmd("/recompact"), cmd("/context"), cmd("/loop"), cmd("/compact"), cmd("/co"), cmd("/commit")];
    expect(words(rankCommands(list, "co"))).toEqual(["/co", "/context", "/compact", "/commit", "/recompact"]);
  });

  it("ignores case and the slash, and lists everything in order for a bare /", () => {
    const list = [cmd("/Model"), cmd("/loop"), cmd("/今天做了什么")];
    expect(words(rankCommands(list, "MOD"))).toEqual(["/Model"]);
    expect(words(rankCommands(list, "今天"))).toEqual(["/今天做了什么"]);
    expect(words(rankCommands(list, ""))).toEqual(["/Model", "/loop", "/今天做了什么"]);
  });

  it("matches descriptions only after every name hit, so they never reorder those", () => {
    const list = [cmd("/model", "Switch model"), cmd("/compact", "Compact the context"), cmd("/context", "Show the context"), cmd("/clear", "清空对话")];
    expect(words(rankCommands(list, "context"))).toEqual(["/context", "/compact"]);
    expect(words(rankCommands(list, "SWITCH"))).toEqual(["/model"]);
    expect(words(rankCommands(list, "对话"))).toEqual(["/clear"]);
  });

  it("finds nothing for a word nothing has", () => {
    expect(rankCommands([cmd("/model", "Switch model")], "zzz")).toEqual([]);
  });

  it("stops at 25 rows", () => {
    const list = Array.from({ length: 40 }, (_, i) => cmd(`/c${i}`));
    expect(SLASH_LIMIT).toBe(25);
    expect(rankCommands(list, "c")).toHaveLength(25);
    expect(rankCommands(list, "")).toHaveLength(25);
    // the exact hit is first even when it sits past the limit in the list
    expect(rankCommands(list, "c39")[0].word).toBe("/c39");
  });
});

describe("rankSlashSkills", () => {
  const skill = (id: string, name: string, description = "") => ({ id, name, description });

  it("keeps bare / and whitespace queries free of skills", () => {
    const list = [skill("1", "persona-office", "Generate Office documents"), skill("2", "grill-me", "Interview plan")];
    expect(hasSlashSkillQuery("")).toBe(false);
    expect(hasSlashSkillQuery("   ")).toBe(false);
    expect(hasSlashSkillQuery(null)).toBe(false);
    expect(hasSlashSkillQuery(undefined)).toBe(false);
    expect(hasSlashSkillQuery("persona")).toBe(true);
    expect(rankSlashSkills(list, "")).toEqual([]);
    expect(rankSlashSkills(list, "   ")).toEqual([]);
    expect(rankSlashSkills([], "persona")).toEqual([]);
  });

  it("ranks non-empty keyword matches by exact, prefix, contains and description without a 25-row cap", () => {
    const list = [
      skill("desc", "audit", "Review the change"),
      skill("contains", "preview", ""),
      skill("prefix", "review-later", ""),
      skill("exact", "/review", ""),
    ];
    expect(rankSlashSkills(list, "REVIEW").map((s) => s.id)).toEqual(["exact", "prefix", "contains", "desc"]);
    expect(rankSlashSkills(list, "zzz")).toEqual([]);

    const large = Array.from({ length: 30 }, (_, i) => skill(`s${i}`, `persona-skill-${i}`));
    expect(rankSlashSkills(large, "persona")).toHaveLength(30);
    expect(rankSlashSkills(large, "persona-skill-29")[0].id).toBe("s29");
  });
});

// /permissions (#6476) goes wherever the chat shows the permission picker: an agent with modes while it runs.
describe("agentCommands' /permissions", () => {
  const LIST = [{ text: "/compact" }, { text: PERMISSIONS_COMMAND }];
  const has = (s?: Session) => agentCommands(LIST, s).some((c) => c.text === PERMISSIONS_COMMAND);
  it("is listed for a running Claude Code Agent, idle or working, and Antigravity's runtime", () => {
    expect(has(session({}))).toBe(true);
    expect(has(session({ state: "working", status: "working" }))).toBe(true);
    expect(has(session({ agent: "agy", source_kind: "runtime" }))).toBe(true);
  });
  it("is not listed for agy or cursor in a terminal pane, whose mode is their start's", () => {
    expect(has(session({ agent: "agy" }))).toBe(false);
    expect(has(session({ agent: "cursor" }))).toBe(false);
  });
  it("is not listed for Codex, a compatible CLI, a read-only source, an offline Agent or no session", () => {
    for (const s of [session({ agent: "codex" }), session({ agent: "traex" }), session({ source_kind: "codex_app_server", capabilities: { send: true, terminal: true } }), session({ state: "offline", status: "offline" }), session({ capabilities: { modes: false } as Session["capabilities"] }), undefined]) {
      expect(has(s)).toBe(false);
    }
  });
});
