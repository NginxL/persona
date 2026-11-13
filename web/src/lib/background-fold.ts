import type { AssistantInfo } from "./types";

/**
 * Whether a chat folds its background rounds (#4314, #6595): the main conversation and the leader conversations
 * (`AssistantInfo.leaders`, docs/assistant/unified-entry.md "Leader conversations") while the hub's switch is on.
 * The hub omitting `main_background_folded` means on. Other side conversations keep showing every round.
 */
export function foldsBackgroundRounds(
  sessionId: string,
  assistant: Pick<AssistantInfo, "main" | "leaders" | "main_background_folded"> | null | undefined,
): boolean {
  if (!assistant) return false;
  if (assistant.main_background_folded === false) return false;
  if (assistant.main && sessionId === assistant.main) return true;
  return assistant.leaders?.includes(sessionId) ?? false;
}
