// What an agent's composer offers and refuses, by what the session can do (iOS ChatView canPickModel / canClear,
// Android canTypeClaudeCommands): the "/" list and the cap on pictures.
import { agentName, sessionAgent } from "./agent-kind";
import { WORKFLOWS_COMMAND } from "./background-panel";
import { canSwitchModel } from "./model-switch";
import { hasLoop, LOOP_TEXT } from "./loop-command";
import { sessionCan, stateOf } from "./format";
import type { Session } from "./types";
import { canPickPermissions, PERMISSIONS_COMMAND } from "./permissions-command";

/** Pictures one message takes (iOS ChatView, MacComposerDrop.maxImages, Android ChatComposer). */
export const MAX_IMAGES = 4;

/** Rows the "/" palette lists at most (cindy's filterSlashCommands). */
export const SLASH_LIMIT = 25;

/** Claude Code in a herdr pane: it takes `/model` and `/clear` typed into its terminal (Codex, a compatible CLI, the assistant
 *  and the read-only sources have no such commands here). */
export function paneClaude(s: Session | undefined): boolean {
  if (!s || s.assistant || s.source_kind || !s.pane_id) return false;
  if (s.agent && s.agent !== "claude") return false;
  return sessionCan(s, "send") && sessionCan(s, "terminal");
}

/** Claude Code's own commands typed into its terminal (`/context`): not a runtime's session or the assistant, which take
 *  no typed commands, nor another agent's terminal. With no row yet, as before. */
export function claudeTerminal(s: Session | undefined): boolean {
  if (!s) return true;
  if (s.assistant || s.source_kind) return false;
  return !s.agent || s.agent === "claude";
}

/** An agent's own `/compact`, typed into its terminal: any terminal agent, never a runtime's session or the assistant. */
function takesTypedCommands(s: Session | undefined): boolean {
  return !s || (!s.assistant && !s.source_kind);
}

/** The agent commands the "/" list shows for `s`: /model wherever the model can switch (the same rule as the model pill,
 *  any runtime that says so included), /compact and /context only where something reads them as commands, /permissions
 *  where the permission picker is (lib/permissions-command.ts), /clear for pane Claude while it
 *  neither works nor waits for you, /loop and /workflows (its Background tasks panel, #5421) for Claude Code Agents. The
 *  agent's own slash commands (`agent`) are labelled with its name, the source the palette shows on their row. */
export function agentCommands<C extends { text: string; agent?: boolean; source?: string }>(all: C[], s: Session | undefined): C[] {
  const st = s ? stateOf(s) : "offline";
  const who = s ? sessionAgent(s) : undefined;
  const name = agentName(who?.agent, who?.label);
  return all
    .filter((c) => {
      const word = c.text.trim();
      if (word === "/model") return canSwitchModel(s);
      if (word === "/compact") return takesTypedCommands(s);
      if (word === "/context") return claudeTerminal(s);
      if (word === PERMISSIONS_COMMAND) return canPickPermissions(s);
      if (word === "/clear") return paneClaude(s) && st !== "working" && st !== "needs_you";
      if (c.text === LOOP_TEXT) return hasLoop(s);
      if (word === WORKFLOWS_COMMAND) return hasLoop(s) && !s?.assistant;
      return true;
    })
    .map((c) => (c.agent ? { ...c, source: name } : c));
}

/** The "/" palette's rows for `query` (what follows the "/"), ranked as cindy ranks them: the command's word (without its
 *  "/") equal to the query, then starting with it, then containing it, each kept in the list's order. Commands whose
 *  description contains the query come after every name hit, so a word in the user's language still finds a command
 *  without moving any name hit. An empty query lists them all. At most `limit`. */
export function rankCommands<C extends { word: string; desc: string }>(all: readonly C[], query: string, limit = SLASH_LIMIT): C[] {
  const q = query.trim().toLowerCase();
  const rank = (c: C) => {
    if (!q) return 0;
    const name = c.word.replace(/^\//, "").toLowerCase();
    if (name === q) return 0;
    if (name.startsWith(q)) return 1;
    if (name.includes(q)) return 2;
    return c.desc.toLowerCase().includes(q) ? 3 : -1;
  };
  return all
    .map((c, i) => ({ c, i, r: rank(c) }))
    .filter((e) => e.r >= 0)
    .sort((a, b) => a.r - b.r || a.i - b.i)
    .slice(0, limit)
    .map((e) => e.c);
}
