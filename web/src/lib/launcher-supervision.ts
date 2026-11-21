// Descriptive picker eligibility; the hub re-attests before execution.
import { managedProvider } from "./compatibility";
import type { Launcher } from "./types";

export function launcherSupervisionPolicy(target: Launcher): boolean {
  if (!target.name || target.name.startsWith("persona_ext_")) return false;
  const native = !target.source || ["zshrc", "path", "builtin"].includes(target.source) || managedProvider(target.source);
  const agent = target.agent_type || target.agent;
  if (native && (!agent || agent === "claude" || agent === "codex")) return true;
  return Boolean(target.assistant && target.supervision_policy);
}
