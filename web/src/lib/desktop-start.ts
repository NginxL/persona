// New agent with a launcher that starts the session in a desktop app (#5480, #5726): Claude's desktop app (`claude-desktop`,
// docs/integrations/claude-integration.md "New sessions in the desktop app") or the Codex App (`codex-app`,
// docs/integrations/codex.md). Its folder may be left empty (a temporary session: the computer picks the folder), browsing
// starts in the folder that holds the app's projects, and a new folder may be named there (the computer makes it when the
// session starts). The same rules as iOS's DesktopStart.swift (Android has none of this yet).
import type { Launcher, Session } from "./types";
import { directory, directoryKey } from "./workspace-directory";

/** `Launcher.source` of the launcher that starts the session in Claude's desktop app. */
export const CLAUDE_DESKTOP_SOURCE = "claude_desktop";
/** `Launcher.source` of the launcher that starts the chat in the Codex App. */
export const CODEX_APP_SOURCE = "codex_app";

/** The launcher starts the session in a desktop app, not in a terminal. */
export const startsInDesktopApp = (l: Pick<Launcher, "source"> | undefined): boolean =>
  l?.source === CLAUDE_DESKTOP_SOURCE || l?.source === CODEX_APP_SOURCE;

/** A folder the Codex App made for a chat in no project, `<home>/Documents/Codex/<yyyy-mm-dd>/<name>` (iOS
 *  AppGroups.isCodexChatFolder): no project of its own. */
export function isCodexChatFolder(path: string): boolean {
  const canonical = directoryKey(path);
  const windows = canonical !== null && /^(?:[a-z]:\/|\/\/)/i.test(canonical);
  const parts = (windows ? canonical : path).split(/[/\\]/).filter(Boolean);
  if (parts.length < 4) return false;
  const n = parts.length;
  return parts[n - 4] === (windows ? "documents" : "Documents") && parts[n - 3] === (windows ? "codex" : "Codex") && /^\d{4}-\d{2}-\d{2}$/.test(parts[n - 2]);
}

/** The folder that holds most of the folders of a desktop app's sessions among `sessions` (one computer's): Claude's, as
 *  the computer places the scratch folder, or with `codex` the Codex App's projects (its chats in no project left out).
 *  Ties go to the first by name. undefined when there is none. */
export function projectsFolder(sessions: readonly Session[], codex = false): string | undefined {
  const count = new Map<string, { path: string; count: number }>();
  for (const s of sessions) {
    if (s.agent_surface !== "desktop") continue;
    const cwd = directory(s.cwd ?? "");
    if (!cwd) continue;
    if (codex ? s.agent !== "codex" || isCodexChatFolder(s.cwd) : s.agent !== "claude") continue;
    const cut = cwd.replace(/\/+$/, "").lastIndexOf("/");
    const parent = cut > 0 ? cwd.slice(0, cut) : "";
    const key = directoryKey(parent);
    if (!key || /^\/\/[^/]+\/[^/]+$/.test(key)) continue;
    const existing = count.get(key);
    count.set(key, { path: existing && existing.path < parent ? existing.path : parent, count: (existing?.count ?? 0) + 1 });
  }
  let best: string | undefined;
  let most = 0;
  for (const { path: folder, count: n } of count.values()) {
    if (n > most || (n === most && best !== undefined && folder < best)) {
      best = folder;
      most = n;
    }
  }
  return best;
}

/** The path of a new folder `name` in `folder`; undefined when the name is not one folder's name. */
export function newFolderPath(name: string, folder: string): string | undefined {
  const n = name.trim();
  const parent = directory(folder);
  if (!n || n === "." || n === ".." || n.includes("/") || n.includes("\0") || parent === null) return undefined;
  const windows = /^(?:[a-z]:\/|\/\/)/i.test(parent);
  if (windows && (/[\x00-\x1f\\<>:"|?*]/.test(n) || /[. ]$/.test(n) || /^(?:con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)/i.test(n))) return undefined;
  return `${parent.replace(/\/+$/, "")}/${n}`;
}
