import { agentId } from "./agent-kind";
import { managedProvider } from "./compatibility";
import type { Launcher, Session } from "./types";

/** A runtime connection cannot resume a native transcript of the same engine,
 * nor another runtime's session. Different connection identity needs staging. */
export function assistantSwitchNeeded(session: Session, target: Launcher, engine: string): boolean {
  if (session.launcher === target.name) return false;
  if (agentId(session.agent || "claude") !== agentId(engine)) return true;
  if (session.launcher?.startsWith("persona_ext_") || target.name.startsWith("persona_ext_")) return true;
  const runtimeTarget = !managedProvider(target.source) && !["", "zshrc", "path", "builtin"].includes(target.source ?? "");
  if (session.source_kind === "runtime") return !runtimeTarget || session.source_instance_id !== target.source;
  return runtimeTarget;
}
