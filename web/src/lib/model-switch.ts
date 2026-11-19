// Switching a running Agent's model (issue #2434, now through the composer's model pill and panel, #5032): which sessions
// can, the launcher whose list the panel shows, which row is the one it runs, and the small event bus that lets the
// composer open the panel when `/model` is entered. Applying sends POST …/modes {model} and then {effort}
// (lib/session-panel.ts pickPlan), and the node types `/model <id>` into Claude Code through the same guards as a
// message (server/internal/node/model_switch.go).
import { t } from "../i18n";
import { agentId, sessionAgent } from "./agent-kind";
import { sessionCan, stateOf } from "./format";
import type { AgentModel } from "./launcher-models";
import type { Session } from "./types";

/** Explicit runtime capability takes precedence. Older sources retain the Claude Code terminal heuristic.
 * Persona conversations use their own model controls. */
export function canSwitchModel(s: Session | undefined): boolean {
  if (!s || s.assistant) return false;
  if (s.capabilities?.model !== undefined) return s.capabilities.model && sessionCan(s, "send") && stateOf(s) !== "offline";
  if (s.source_kind) return false;
  if (s.agent && s.agent !== "claude") return false;
  return sessionCan(s, "send") && stateOf(s) !== "offline";
}

/** Why the model cannot be switched right now, "" when it can (the node refuses the same cases). */
export function modelBusy(s: Session): string {
  const st = stateOf(s);
  return st === "working" || st === "needs_you" ? t("composer.busy") : "";
}

/** The node's name for the Codex App's launcher (#5726, server/internal/node/codexapp_new.go): its list is empty and
 *  `locked`, since the App picks a chat's model. */
export const CODEX_APP_LAUNCHER = "codex-app";

/** The launcher whose list the panel shows first: the session's own; without one, its engine's: the Codex App's for one
 *  of the App's chats (`space_kind` codex_app), plain codex for another Codex session, plain claude otherwise. A Codex
 *  session used to read Claude Code's list (#6501): its pill named Claude's default entry, "Default (recommended)", when
 *  the session reported no model, and the panel offered Claude's models as its own. */
export const sessionLauncher = (s: Partial<Pick<Session, "launcher" | "agent" | "agent_label" | "source_kind" | "agent_surface" | "space_kind">>): string => {
  if (s.launcher) return s.launcher;
  if (agentId(sessionAgent(s).agent) !== "codex") return "claude";
  return s.space_kind === "codex_app" ? CODEX_APP_LAUNCHER : "codex";
};

/** The id without its 1M marker, lower case: the model both spellings name. */
const bareModel = (v: string): string => v.trim().toLowerCase().replace(/\[1m\]$/, "");

/** Whether two ids name one model: equal after trimming, ignoring case and the 1M marker, or one is the other's "/"
 *  suffix at a boundary (a gateway routing the model under a prefix, or a launcher's vendored id against the bare
 *  echo). Empty ids never match. Mirrors the hub's rule (proto.SameLauncherModel). */
export function sameLauncherModel(a: string | undefined, b: string | undefined): boolean {
  const x = bareModel(a ?? "");
  const y = bareModel(b ?? "");
  if (!x || !y) return false;
  if (x === y) return true;
  return (x.includes("/") && x.endsWith("/" + y)) || (y.includes("/") && y.endsWith("/" + x));
}

/** Whether `m` is the model the session runs now: `current` is what the transcript shows (a concrete model id), or
 *  the id the app switched to; the list has the alias (`opus`), what it stands for (`resolved`) and the spellings the hub
 *  folded into the row (`aliases`, #10365), any case. A gateway may echo a routed name or the other 1M spelling, which
 *  sameLauncherModel folds. The model panel matches its rows and saved setups by the same rule. */
export function isCurrentModel(m: Pick<AgentModel, "id" | "resolved" | "aliases"> & { name?: string }, current: string | undefined): boolean {
  if (!current) return false;
  const c = current.toLowerCase();
  return [m.id, m.resolved, ...(m.aliases ?? [])].some((v) => v && (v.toLowerCase() === c || sameLauncherModel(current, v)));
}

/** `/model` alone (what the slash menu fills in, with or without the space) in the box. */
export const isModelCommand = (draft: string) => draft.trim() === "/model";

// ── opening the panel from the composer ──────────────────────────────────────

type Opener = () => void;
const openers = new Map<string, Opener>();

/** The model pill of the conversation `key` answers `/model` Enter with its panel; returns the unsubscribe. */
export function onModelPicker(key: string, open: Opener): () => void {
  openers.set(key, open);
  return () => {
    if (openers.get(key) === open) openers.delete(key);
  };
}

/** Opens the conversation's model panel; false when it has none (the agent has no model switch): the composer then sends
 *  the text as it always did. */
export function requestModelPicker(key: string): boolean {
  const open = openers.get(key);
  if (!open) return false;
  open();
  return true;
}
