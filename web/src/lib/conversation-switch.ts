// Explicit logical-conversation switching. Preparing never sends a model input;
// a retry keeps the exact operation/epoch after an uncertain response.
import { t } from "../i18n";
import { api } from "./api";
import { uuid } from "./format";
import type { ModelPick, Session } from "./types";

export const ASSISTANT_SEGMENTS_FEATURE = "assistant_segments";
export interface ConversationSwitchRequest {
  operation_id: string;
  expected_epoch: number;
  source_machine_id: string;
  machine_id: string;
  launcher: string;
  model?: string;
  effort?: string;
  permission_mode?: string;
}
const path = (session: Pick<Session, "machine_id" | "session_id">) => `/assistant/conversations/${encodeURIComponent(session.session_id)}`;
export interface ConversationSwitchState { enabled: boolean; epoch: number; supervision_required?: boolean }
export const conversationSwitchState = (session: Pick<Session, "machine_id" | "session_id">): Promise<ConversationSwitchState> => api(`${path(session)}/segments?machine_id=${encodeURIComponent(session.machine_id)}`);
export async function prepareConversationSwitch(session: Session, pick: ModelPick): Promise<ConversationSwitchRequest> {
  const state = await conversationSwitchState(session);
  if (!state.enabled || !Number.isSafeInteger(state.epoch) || state.epoch < 1) throw new Error(t("chat-modelpanel.switch_unavailable"));
  return { ...pick, source_machine_id: session.machine_id, machine_id: session.machine_id,
    operation_id: uuid(), expected_epoch: state.epoch, permission_mode: session.permission_mode };
}
export async function submitConversationSwitch(session: Session, request: ConversationSwitchRequest): Promise<void> {
  await api(`${path(session)}/switch`, { method: "POST", json: request });
}

export { assistantSwitchNeeded } from "./assistant-execution";
