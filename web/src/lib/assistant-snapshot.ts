// The last complete assistant response, restored while the hub reads it again on a full reload.
// It is a display snapshot, never an authority: refreshAssistant applies the fresh response through
// the same request ordering as before. The feed cache's scope partitions it by hub and credential.
import { availableConversation } from "./conversation-deleted";
import { feedCacheScope, fingerprint, scopedKey } from "./feed-cache";
import type { AssistantInfo } from "./types";

const VERSION = 1;
const PREFIX = "assistant-view:";

export interface AssistantSnapshot {
  /** The actual manager, including the default whose request used no manager header. */
  manager: string;
  assistant: AssistantInfo;
}

const key = (page: string) => scopedKey(feedCacheScope(), `${PREFIX}${fingerprint(page)}`);
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
const strings = (value: unknown) => value === undefined || (Array.isArray(value) && value.every((item) => typeof item === "string"));

/** page is the explicit manager, or "" for the default whose roster identity may not be known yet. */
export function readAssistantSnapshot(page: string): AssistantSnapshot | null {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(key(page) ?? "") ?? "null");
    if (!object(value) || value.version !== VERSION || typeof value.manager !== "string" || !object(value.assistant)) return null;
    if (page && value.manager !== page) return null;
    const assistant = value.assistant;
    if (typeof assistant.machine_id !== "string" || (assistant.main !== undefined && typeof assistant.main !== "string")
      || !Array.isArray(assistant.conversations) || !strings(assistant.leaders) || !strings(assistant.conversation_order)) return null;
    if (!assistant.conversations.every((row) => object(row) && typeof row.machine_id === "string" && typeof row.session_id === "string")) return null;
    return {
      manager: value.manager,
      assistant: { ...assistant, conversations: assistant.conversations.filter(availableConversation) } as unknown as AssistantInfo,
    };
  } catch {
    return null; // unavailable storage, malformed JSON, signed out, or another credential
  }
}

export function writeAssistantSnapshot(page: string, manager: string, assistant: AssistantInfo): void {
  try {
    const storageKey = key(page);
    if (storageKey) localStorage.setItem(storageKey, JSON.stringify({ version: VERSION, manager, assistant }));
  } catch {
    // Full or disabled storage only means the next reload waits for the hub, as before.
  }
}

export function clearAssistantSnapshot(page: string): void {
  try {
    const storageKey = key(page);
    if (storageKey) localStorage.removeItem(storageKey);
  } catch {
    // Storage can be unavailable while a roster change is being applied.
  }
}
