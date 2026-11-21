// Kept separate from lib/model-config.ts so the first screen (AssistantChat) does not pull model-config helpers
// into the initial bundle (src/startup-preload.ts FIRST_SCREEN_BUDGET).
import { t } from "../i18n";

/** The main chat's note when it asked for the Agent SDK and runs on the CLI (the session's `runtime_fallback`). */
export function runtimeFallbackText(reason: string): string {
  return t("lib-model-config.runtime_fallback", { reason: reason.trim() });
}
