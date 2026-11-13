/** Immutable native-menu context for a Conversation SessionRevision action. */
export class ConversationRevisionLease {
  readonly model: Object;
  readonly generation: number;
  readonly scope: string;
  readonly target: string;
  readonly visibilityRevision: number;
  readonly itemId: string;
  readonly fingerprint: string;
  readonly messageOwnerTarget: string;

  constructor(model: Object, generation: number, scope: string, target: string, visibilityRevision: number,
    itemId: string, fingerprint: string, messageOwnerTarget: string) {
    this.model = model;
    this.generation = generation;
    this.scope = scope;
    this.target = target;
    this.visibilityRevision = visibilityRevision;
    this.itemId = itemId;
    this.fingerprint = fingerprint;
    this.messageOwnerTarget = messageOwnerTarget;
    Object.freeze(this);
  }

  owns(model: Object, active: boolean, connected: boolean, demo: boolean, generation: number, scope: string, target: string,
    visibilityRevision: number, itemId: string, messageOwnerTarget: string, currentSession: boolean): boolean {
    return active && connected && !demo && currentSession && this.model === model && this.generation === generation &&
      this.scope === scope && this.target === target && this.target === messageOwnerTarget &&
      this.visibilityRevision === visibilityRevision && this.itemId === itemId && this.messageOwnerTarget === messageOwnerTarget;
  }

  matches(model: Object, active: boolean, connected: boolean, demo: boolean, generation: number, scope: string, target: string,
    visibilityRevision: number, itemId: string, fingerprint: string, messageOwnerTarget: string, currentSession: boolean): boolean {
    return this.owns(model, active, connected, demo, generation, scope, target, visibilityRevision, itemId, messageOwnerTarget, currentSession) &&
      this.fingerprint === fingerprint;
  }
}

export interface SessionRevisionDraftOutcome {
  inputCleared: boolean;
  prompt: string;
  capturedDraft: string;
  capturedDraftRevision: string;
}

export interface SessionRevisionDraftModel {
  draft: string;
  draftRevision: string;
  updateDraft(text: string): void;
  persistDraft(): Promise<void>;
}

export function sessionRevisionDraftStillCaptured(outcome: SessionRevisionDraftOutcome, model: SessionRevisionDraftModel): boolean {
  return model.draft === outcome.capturedDraft && model.draftRevision === outcome.capturedDraftRevision;
}

/** Restore only the draft that was captured by SessionRevision; persistence failure is reported separately from its ACK. */
export async function restoreAcknowledgedSessionDraft(
  model: SessionRevisionDraftModel,
  outcome: SessionRevisionDraftOutcome,
  stillCurrent: () => boolean,
  onCacheFailure: () => void,
): Promise<boolean> {
  if (!outcome.inputCleared || !outcome.prompt || !stillCurrent() || !sessionRevisionDraftStillCaptured(outcome, model)) return false;
  model.updateDraft(outcome.prompt);
  if (model.draft !== outcome.prompt) return false;
  const restoredRevision = model.draftRevision;
  try {
    await model.persistDraft();
  } catch (_) {
    if (stillCurrent() && model.draftRevision === restoredRevision) onCacheFailure();
    return false;
  }
  return true;
}
