import { isPersonaStateDir } from "./compatibility";
import { normalizePath } from "./files";
import type { Session } from "./types";

export function isSidebarAgent(session: Session): boolean {
  if (session.assistant || session.space === "health") return false;
  // Older hubs may report the health directory without a space marker. Match the state directory and manager name
  // used by identity.InHealthSpace, including paths from Windows nodes.
  const cwd = session.cwd ?? "";
  const path = normalizePath(/^(?:[a-z]:|\\\\)/i.test(cwd) ? cwd.replaceAll("\\", "/") : cwd);
  const parts = path.split("/");
  return !parts.some((part, index) => isPersonaStateDir(part) && /^health(?:-m-[0-9a-f]+)?$/.test(parts[index + 1] ?? ""));
}
