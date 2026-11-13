import { util } from '@kit.ArkTS';
import { sessionRevisionEntry, sessionRevisionFingerprint, sessionRevisionHydrationMatches, sessionRevisionSnapshot, sessionRevisionTarget } from '../core/SessionRevision';
import type { SessionRevisionRestore, SessionRevisionTarget as RevisionTarget } from '../core/SessionRevision';
import type { TranscriptItem } from '../core/Domain';
import type { ClientModel } from './ClientModel';
import type { HubRepository } from './HubRepository';
import { segment } from './HubRepository';
import { ResourceRow, Wire } from './Wire';
import { HttpFailure } from '../platform/HubTransport';

const FORK_PATH: string = '/fork';
const REWIND_PATH: string = '/rewind';

export class SessionRevisionOutcome {
  state: string = 'failed';
  error: string = '';
  prompt: string = '';
  restore: string = '';
  inputCleared: boolean = false;
  codeRestored: boolean = false;
  capturedDraft: string = '';
  capturedDraftRevision: string = '';
  draftUnchanged: boolean = false;
  canFillDraft: boolean = false;
}

function cloneRow(row: ResourceRow): ResourceRow {
  const copy = new ResourceRow(row.id, row.title, row.kind);
  copy.detail = row.detail;
  copy.state = row.state;
  copy.group = row.group; copy.manager = row.manager;
  copy.machine = row.machine;
  copy.session = row.session;
  copy.path = row.path;
  copy.raw = row.raw;
  return copy;
}

function isConversation(row: ResourceRow): boolean {
  return row.kind === 'conversation' || row.kind === 'session';
}

function sessionId(row: ResourceRow): string {
  return row.session || row.id;
}

function imageHasToolUse(item: TranscriptItem): boolean {
  return (item.tool_use_id !== undefined && item.tool_use_id !== null) ||
    (item.toolUseId !== undefined && item.toolUseId !== null);
}

@ObservedV2
export class SessionRevision {
  @Trace forkState: string = 'idle';
  @Trace forkError: string = '';
  @Trace rewindState: string = 'idle';
  @Trace rewindError: string = '';

  private model: ClientModel | undefined;
  private row: ResourceRow | undefined;
  private repository: HubRepository | undefined;
  private ownerScope: string = '';
  private ownerTarget: string = '';
  private ownerGeneration: number = 0;
  private ownerRowKey: string = '';
  private ownerMachine: string = '';
  private ownerSession: string = '';
  private items: TranscriptItem[] = [];
  private visible: boolean = false;
  private epoch: number = 0;
  private forkInFlight: boolean = false;
  private rewindInFlight: boolean = false;

  /** Bind one visible conversation to an immutable copy of its raw transcript. This starts no reads or streams. */
  open(model: ClientModel, row: ResourceRow, items: readonly TranscriptItem[], visible: boolean = true): void {
    this.hide();
    this.epoch++;
    this.model = model;
    this.row = cloneRow(row);
    this.ownerScope = model.scope();
    this.ownerTarget = model.target(row);
    this.ownerGeneration = model.connectionGeneration();
    this.ownerRowKey = row.key();
    this.ownerMachine = row.machine;
    this.ownerSession = sessionId(row);
    this.items = sessionRevisionSnapshot(items);
    this.visible = visible;
    try { this.repository = model.repo(); } catch (_) { this.repository = undefined; }
    this.forkState = 'idle'; this.forkError = '';
    this.rewindState = 'idle'; this.rewindError = '';
    this.forkInFlight = false; this.rewindInFlight = false;
  }

  /** Replace the retained raw transcript snapshot. Rendered message strings are never accepted here. */
  updateItems(items: readonly TranscriptItem[]): void {
    if (!this.visible) return;
    this.items = sessionRevisionSnapshot(items);
  }

  /** Revoke pending admissions and release the snapshot. No automatic work is stopped or started. */
  hide(): void {
    this.visible = false;
    this.epoch++;
    this.items = [];
    this.model = undefined;
    this.row = undefined;
    this.repository = undefined;
    this.ownerScope = ''; this.ownerTarget = ''; this.ownerGeneration = 0;
    this.ownerRowKey = ''; this.ownerMachine = ''; this.ownerSession = '';
  }

  isCurrent(): boolean {
    return this.currentOwner(this.epoch);
  }

  /** The native confirmation can retain this value and pass it back to fork/rewind. */
  fingerprint(itemId: string): string {
    if (!this.isCurrent()) return '';
    return sessionRevisionFingerprint(this.items, itemId);
  }

  /** Fork support is authoritative only when this row explicitly carries can_fork=true. */
  canFork(itemId?: string): boolean {
    if (this.forkGate(itemId, true) !== '') return false;
    return true;
  }

  /** Returns a stable reason key when rewind is unavailable, or an empty string when it can be confirmed. */
  rewindBlock(itemId: string): string {
    return this.rewindGate(itemId, true);
  }

  canRewind(itemId: string): boolean {
    return this.rewindBlock(itemId) === '';
  }

  /** Create an offline branch from a transcript reply after native confirmation. */
  async fork(itemId: string, expectedFingerprint: string): Promise<ResourceRow | undefined> {
    const gate = this.forkGate(itemId, true);
    if (gate !== '') { this.forkState = 'failed'; this.forkError = gate; return undefined; }
    if (!expectedFingerprint || this.fingerprint(itemId) !== expectedFingerprint) {
      this.forkState = 'failed'; this.forkError = 'session_revision_target_changed'; return undefined;
    }
    if (this.forkInFlight) return undefined;
    const model = this.model, row = this.row, repo = this.repository, epoch = this.epoch;
    if (!model || !row || !repo) { this.forkState = 'failed'; this.forkError = 'offline'; return undefined; }
    const sourceSession = this.ownerSession;
    const path = this.path(FORK_PATH);
    if (model.operationBlocked(path)) { this.forkState = 'pending'; this.forkError = 'pending_action'; return undefined; }
    const fingerprint = expectedFingerprint;
    const op = util.generateRandomUUID();
    this.forkInFlight = true; this.forkState = 'branching'; this.forkError = '';
    try {
      const result = await repo.call(path, 'POST', Wire.payload(['at_item_id'], [itemId]), op,
        (): boolean => this.forkAdmitted(model, row, repo, epoch, itemId, fingerprint));
      const forkedSession = Wire.text(result, 'session_id');
      if (!forkedSession || forkedSession === sourceSession) {
        if (this.epoch === epoch) { this.forkState = 'unknown'; this.forkError = 'result_unknown'; }
        return undefined;
      }
      if (this.epoch === epoch) { this.forkState = 'done'; this.forkError = ''; }
      return this.forkedRow(row, forkedSession);
    } catch (error) {
      const key = this.errorKey(error as Error, false);
      if (this.epoch === epoch) {
        this.forkError = key;
        this.forkState = error instanceof HttpFailure && error.resultPending ? 'pending' :
          error instanceof HttpFailure && error.resultUnknown ? 'unknown' : 'failed';
      }
      return undefined;
    } finally { if (this.epoch === epoch) this.forkInFlight = false; }
  }

  /** Rewind one user message after the native UI has confirmed its chosen restore mode. */
  async rewind(itemId: string, restore: SessionRevisionRestore, expectedFingerprint: string): Promise<SessionRevisionOutcome> {
    const outcome = new SessionRevisionOutcome();
    outcome.restore = restore;
    outcome.capturedDraft = this.model?.draft || '';
    outcome.capturedDraftRevision = this.model?.draftRevision || '';
    const gate = this.rewindGate(itemId, true);
    if (gate !== '') { outcome.error = gate; this.rewindState = 'failed'; this.rewindError = gate; return outcome; }
    if (restore !== 'conversation' && restore !== 'code_and_conversation') {
      outcome.error = 'unsupported'; this.rewindState = 'failed'; this.rewindError = outcome.error; return outcome;
    }
    if (!expectedFingerprint || this.fingerprint(itemId) !== expectedFingerprint) {
      outcome.error = 'session_revision_target_changed'; this.rewindState = 'failed'; this.rewindError = outcome.error; return outcome;
    }
    if (this.rewindInFlight) {
      outcome.error = 'pending_action'; this.rewindState = 'pending'; this.rewindError = outcome.error; return outcome;
    }
    const model = this.model, row = this.row, repo = this.repository, epoch = this.epoch;
    if (!model || !row || !repo) {
      outcome.error = 'offline'; this.rewindState = 'failed'; this.rewindError = outcome.error; return outcome;
    }
    const path = this.path(REWIND_PATH);
    if (model.operationBlocked(path)) {
      outcome.error = 'pending_action'; this.rewindState = 'pending'; this.rewindError = outcome.error; return outcome;
    }
    const fingerprint = expectedFingerprint;
    const snapshot = sessionRevisionSnapshot(this.items);
    let target: RevisionTarget | undefined = sessionRevisionTarget(snapshot, itemId);
    let hydratedItemId: string = '';
    let hydratedText: string | undefined = undefined;
    if (!target) {
      outcome.error = 'session_revision_no_text'; this.rewindState = 'failed'; this.rewindError = outcome.error; return outcome;
    }
    this.rewindInFlight = true; this.rewindState = 'rewinding'; this.rewindError = '';
    try {
      if (target.cutItemId) {
        hydratedItemId = target.cutItemId;
        const fullPath = this.path('/items/' + segment(hydratedItemId) + '/full');
        const full = await repo.call(fullPath, 'GET', {}, '',
          (): boolean => this.rewindAdmitted(model, row, repo, epoch, itemId, fingerprint));
        hydratedText = Wire.text(full, 'text');
        if (!this.rewindHydratedAdmitted(model, row, repo, epoch, snapshot, itemId, hydratedText, hydratedItemId)) {
          outcome.error = 'session_revision_target_changed'; this.setRewindState(epoch, 'failed', outcome.error); return outcome;
        }
        target = sessionRevisionTarget(snapshot, itemId, hydratedText);
        if (!target) {
          outcome.error = 'session_revision_no_text'; this.setRewindState(epoch, 'failed', outcome.error); return outcome;
        }
      }
      const admitted = hydratedText === undefined ? this.rewindAdmitted(model, row, repo, epoch, itemId, fingerprint) :
        this.rewindHydratedAdmitted(model, row, repo, epoch, snapshot, itemId, hydratedText, hydratedItemId);
      if (!admitted) {
        outcome.error = 'session_revision_target_changed'; this.setRewindState(epoch, 'failed', outcome.error); return outcome;
      }
      const body = Wire.payload(['prompt', 'occurrence', 'prompts_after', 'restore'],
        [target.prompt, target.occurrence, target.promptsAfter, restore]);
      const result = await repo.call(path, 'POST', body, util.generateRandomUUID(),
        (): boolean => hydratedText === undefined ? this.rewindAdmitted(model, row, repo, epoch, itemId, fingerprint) :
          this.rewindHydratedAdmitted(model, row, repo, epoch, snapshot, itemId, hydratedText, hydratedItemId));
      outcome.prompt = Wire.text(result, 'prompt', target.prompt) || target.prompt;
      outcome.restore = Wire.text(result, 'restore', restore);
      outcome.inputCleared = Wire.bool(result, 'input_cleared');
      outcome.codeRestored = Wire.bool(result, 'code_restored');
      outcome.state = 'done';
      outcome.draftUnchanged = this.currentOwner(epoch) && model.draft === outcome.capturedDraft &&
        model.draftRevision === outcome.capturedDraftRevision;
      outcome.canFillDraft = outcome.inputCleared && outcome.capturedDraft.trim().length === 0 && outcome.draftUnchanged;
      if (this.epoch === epoch) { this.rewindState = 'done'; this.rewindError = ''; }
      return outcome;
    } catch (error) {
      const key = this.errorKey(error as Error, true);
      outcome.error = key;
      outcome.state = error instanceof HttpFailure && error.resultPending ? 'pending' :
        error instanceof HttpFailure && error.resultUnknown ? 'unknown' : 'failed';
      if (this.epoch === epoch) { this.rewindState = outcome.state; this.rewindError = key; }
      return outcome;
    } finally { if (this.epoch === epoch) this.rewindInFlight = false; }
  }

  private currentOwner(epoch: number): boolean {
    const model = this.model, row = this.row, repo = this.repository;
    if (!this.visible || epoch !== this.epoch || !model || !row || !repo || !this.ownerMachine || !this.ownerSession ||
      !model.connected || model.demo) return false;
    if (model.connectionGeneration() !== this.ownerGeneration || model.scope() !== this.ownerScope || !model.owns(this.ownerGeneration, this.ownerScope)) return false;
    if (model.target() !== this.ownerTarget || model.target(row) !== this.ownerTarget || model.selected.key() !== this.ownerRowKey) return false;
    if (model.selected.manager !== row.manager || model.selected.machine !== this.ownerMachine || sessionId(model.selected) !== this.ownerSession) return false;
    try { if (model.repo() !== repo) return false; } catch (_) { return false; }
    return true;
  }

  private forkGate(itemId: string | undefined, includeBusy: boolean): string {
    if (!this.currentOwner(this.epoch)) return 'request_updated';
    const model = this.model;
    if (!model) return 'request_updated';
    const row = model.selected;
    if (!isConversation(row) || Wire.bool(row.data(), 'can_fork') !== true) return 'session_revision_unsupported';
    if (includeBusy) {
      if (this.forkInFlight || this.forkState === 'pending' || this.forkState === 'unknown') return 'pending_action';
      if (model.operationBlocked(this.path(FORK_PATH))) return 'pending_action';
    }
    if (itemId !== undefined) {
      const item = this.items.find((candidate: TranscriptItem) => candidate.id === itemId);
      if (!item || item.role !== 'assistant' || sessionRevisionEntry(itemId) === undefined) return 'session_revision_target_invalid';
    }
    return '';
  }

  private rewindGate(itemId: string, includeBusy: boolean): string {
    if (!this.currentOwner(this.epoch)) return 'request_updated';
    const model = this.model, row = model?.selected;
    if (!row || !model || !isConversation(row)) return 'session_revision_unsupported';
    const data = row.data();
    const sourceKind = Wire.text(data, 'source_kind');
    const agent = Wire.text(data, 'agent');
    const sid = sessionId(row);
    if (sourceKind || Wire.bool(data, 'assistant') || sid.includes('@') || (agent && agent !== 'claude')) return 'session_revision_unsupported';
    if (Wire.bool(data, 'starting') || row.state === 'offline' || Wire.text(data, 'state') === 'offline' || Wire.text(data, 'status') === 'offline')
      return 'session_revision_not_running';
    const state = Wire.text(data, 'state', row.state), status = Wire.text(data, 'status');
    if (state === 'working' || status === 'working') return 'session_revision_working';
    if (Wire.number(data, 'pending_interactions') > 0 || ['needs_you', 'blocked'].includes(state) || ['needs_you', 'blocked'].includes(status))
      return 'session_revision_needs_you';
    const decision = model.decision;
    if (decision && decision.machine === this.ownerMachine && decision.session === this.ownerSession &&
      ['open', 'pending', 'unknown', 'answering'].includes(decision.status)) return 'session_revision_open_request';
    const item = this.items.find((candidate: TranscriptItem) => candidate.id === itemId);
    if (!item || item.role !== 'user' || queuedItem(item) || sessionRevisionEntry(itemId) === undefined)
      return 'session_revision_target_invalid';
    if (item.kind !== 'text' && item.kind !== 'image' || item.kind === 'image' && imageHasToolUse(item)) return 'session_revision_target_invalid';
    if (includeBusy && (this.rewindInFlight || this.rewindState === 'pending' || this.rewindState === 'unknown' ||
      model.operationBlocked(this.path(REWIND_PATH)))) return 'pending_action';
    return '';
  }

  private forkAdmitted(model: ClientModel, row: ResourceRow, repo: HubRepository, epoch: number, itemId: string, fingerprint: string): boolean {
    return this.currentOwner(epoch) && this.model === model && this.row?.key() === row.key() && this.repository === repo &&
      this.forkGate(itemId, false) === '' && sessionRevisionFingerprint(this.items, itemId) === fingerprint;
  }

  private rewindAdmitted(model: ClientModel, row: ResourceRow, repo: HubRepository, epoch: number, itemId: string, fingerprint: string): boolean {
    return this.currentOwner(epoch) && this.model === model && this.row?.key() === row.key() && this.repository === repo &&
      this.rewindGate(itemId, false) === '' && sessionRevisionFingerprint(this.items, itemId) === fingerprint;
  }

  private rewindHydratedAdmitted(
    model: ClientModel,
    row: ResourceRow,
    repo: HubRepository,
    epoch: number,
    snapshot: readonly TranscriptItem[],
    itemId: string,
    full: string,
    hydratedItemId: string,
  ): boolean {
    return this.currentOwner(epoch) && this.model === model && this.row?.key() === row.key() && this.repository === repo &&
      this.rewindGate(itemId, false) === '' && sessionRevisionHydrationMatches(snapshot, this.items, itemId, full, hydratedItemId);
  }

  private path(suffix: string): string {
    return '/api/v1/sessions/' + segment(this.ownerMachine) + '/' + segment(this.ownerSession) + suffix;
  }

  private forkedRow(source: ResourceRow, id: string): ResourceRow {
    const data = Wire.payload(['id', 'session_id', 'machine_id', 'manager', 'title', 'cwd', 'status', 'state', 'can_fork'],
      [id, id, source.machine, source.manager, source.title, source.path, 'offline', 'offline', Wire.bool(source.data(), 'can_fork')]);
    const row = ResourceRow.from(data, source.kind);
    row.manager = source.manager; row.machine = source.machine; row.session = id; row.path = source.path; row.state = 'offline';
    return row;
  }

  private setRewindState(epoch: number, state: string, error: string): void {
    if (this.epoch !== epoch) return;
    this.rewindState = state;
    this.rewindError = error;
  }

  private errorKey(error: Error, rewind: boolean): string {
    if (error instanceof HttpFailure) {
      if (error.resultPending) return 'pending';
      if (error.resultUnknown) return 'result_unknown';
      if (rewind && (error.status === 404 || error.status === 405) && error.code !== 'gone') return 'session_revision_hub_outdated';
      if (rewind && error.status === 403) return 'session_revision_full_access';
      if (error.code.includes('timeout')) return 'session_revision_timeout';
      if (rewind) {
        if (error.code === 'rewind_busy') return 'session_revision_working';
        if (error.code === 'dialog_open') return 'session_revision_dialog_open';
        if (error.code === 'input_draft') return 'session_revision_input_draft';
        if (error.code === 'rewind_unsupported' || error.code === 'node_outdated') return 'session_revision_unsupported';
        if (error.code === 'rewind_nothing') return 'session_revision_nothing';
        if (error.code === 'rewind_mismatch') return 'session_revision_mismatch';
        if (error.code === 'rewind_failed') return 'session_revision_failed';
      } else {
        if (error.code === 'fork_unsupported') return 'session_revision_unsupported';
        if (error.code === 'fork_turn_running') return 'session_revision_fork_turn_running';
        if (error.code === 'fork_item_missing') return 'session_revision_target_changed';
      }
      return error.code || 'failed';
    }
    if (['offline', 'pending_action', 'request_updated', 'permission_denied', 'storage_unavailable'].includes(error.message)) return error.message;
    if (error.message.includes('timeout')) return 'session_revision_timeout';
    return 'failed';
  }
}

function queuedItem(item: TranscriptItem): boolean {
  return item['queued'] === true;
}
