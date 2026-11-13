import type { ClientModel } from './ClientModel';
import { ResourceRow } from './Wire';
import { NativeRecording } from '../platform/NativeRecording';
import type { NativeRecordingStop } from '../platform/NativeRecording';

const MAX_TRANSCRIPT_BYTES: number = 4 * 1024 * 1024 - 4096;
const MAX_DURATION_SECONDS: number = 60 * 60;

export interface KnowledgeRecordingSegment {
  start: number;
  end: number;
  text: string;
}

export interface KnowledgeRecordingUpload {
  revision: number;
  started_at: string;
  ended_at: string;
  duration: number;
  language: string;
  device: string;
  title: string;
  bookmarks: Object[];
  segments: KnowledgeRecordingSegment[];
  complete: boolean;
  kind: string;
}

/** The owned local draft and the acknowledgement journal for one idempotent Hub recording PUT. */
export interface KnowledgeRecordingEntry {
  id: string;
  profileId: string;
  hubId: string;
  managerId: string;
  startedAt: string;
  endedAt: string;
  duration: number;
  audioBytes: number;
  language: string;
  title: string;
  transcript: string;
  revision: number;
  captureState: string;
  transcriptionState: string;
  uploadState: string;
  uploadBody: KnowledgeRecordingUpload | undefined;
  serverStatus: string;
  serverRevision: number;
  interrupted: boolean;
  error: string;
  uploadError: string;
}

export interface RecordingTransport {
  request(path: string, method?: string, body?: string, operationId?: string): Promise<string>;
}

export interface RecordingRepository { transport: RecordingTransport; }

interface RecordingProfile {
  id: string;
  hubId: string;
  manager: string;
  baseUrl: string;
  pin: string;
}

interface RecordingClient {
  connected: boolean;
  demo: boolean;
  profile: RecordingProfile;
  scope(): string;
  connectionGeneration(): number;
  owns(generation: number, scope: string): boolean;
  viewFor(row: ResourceRow): Promise<ClientModel>;
  repo(): RecordingRepository;
}

interface RecordingLocalSnapshot {
  root: ClientModel;
  revision: number;
  generation: number;
  scope: string;
  profileId: string;
  hubId: string;
  rootManager: string;
  ownerManager: string;
  forgetEpoch: number;
}

interface RecordingRemoteSnapshot extends RecordingLocalSnapshot {
  localSnapshot: RecordingLocalSnapshot;
  owner: ClientModel;
  ownerGeneration: number;
  ownerScope: string;
  ownerProfile: RecordingProfile;
  repository: RecordingRepository;
}

function utf8Length(value: string): number {
  let bytes = 0;
  for (let index = 0; index < value.length; index++) {
    const code = value.charCodeAt(index);
    if (code < 0x80) bytes++;
    else if (code < 0x800) bytes += 2;
    else if (code >= 0xd800 && code <= 0xdbff && index + 1 < value.length) {
      const next = value.charCodeAt(index + 1);
      if (next >= 0xdc00 && next <= 0xdfff) { bytes += 4; index++; }
      else bytes += 3;
    } else bytes += 3;
    if (bytes > MAX_TRANSCRIPT_BYTES) return bytes;
  }
  return bytes;
}

function id(): string {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (value: string): string => {
    const random = Math.floor(Math.random() * 16);
    return (value === 'x' ? random : (random & 0x3) | 0x8).toString(16);
  });
}

function profileCopy(profile: RecordingProfile): RecordingProfile {
  return { id: profile.id, hubId: profile.hubId, manager: profile.manager, baseUrl: profile.baseUrl, pin: profile.pin };
}

function copyEntry(entry: KnowledgeRecordingEntry): KnowledgeRecordingEntry {
  const copied: KnowledgeRecordingEntry = {
    id: entry.id,
    profileId: entry.profileId,
    hubId: entry.hubId,
    managerId: entry.managerId,
    startedAt: entry.startedAt,
    endedAt: entry.endedAt,
    duration: entry.duration,
    audioBytes: entry.audioBytes,
    language: entry.language,
    title: entry.title,
    transcript: entry.transcript,
    revision: entry.revision,
    captureState: entry.captureState,
    transcriptionState: entry.transcriptionState,
    uploadState: entry.uploadState,
    uploadBody: copyUploadBody(entry.uploadBody),
    serverStatus: entry.serverStatus,
    serverRevision: entry.serverRevision,
    interrupted: entry.interrupted,
    error: entry.error,
    uploadError: entry.uploadError
  };
  return copied;
}

function copyUploadBody(body: KnowledgeRecordingUpload | undefined): KnowledgeRecordingUpload | undefined {
  if (!body) return undefined;
  const copied: KnowledgeRecordingUpload = {
    revision: body.revision,
    started_at: body.started_at,
    ended_at: body.ended_at,
    duration: body.duration,
    language: body.language,
    device: body.device,
    title: body.title,
    bookmarks: body.bookmarks.slice(),
    segments: body.segments.map((item: KnowledgeRecordingSegment): KnowledgeRecordingSegment => {
      const segment: KnowledgeRecordingSegment = { start: item.start, end: item.end, text: item.text };
      return segment;
    }),
    complete: body.complete,
    kind: body.kind
  };
  return copied;
}

function errorCode(error: Error): string {
  const value = error as Object as Record<string, Object>;
  const status = typeof value['status'] === 'number' ? value['status'] as number : 0;
  if (['knowledge_recording_microphone_denied', 'knowledge_recording_storage_full', 'knowledge_recording_too_short',
    'knowledge_recording_chinese_only', 'knowledge_recording_transcribe_failed', 'offline', 'request_updated'].includes(error.message)) return error.message;
  if (error.message === 'storage_unavailable') return 'knowledge_storage_unavailable';
  if (error.message === 'recording_limit') return 'knowledge_recording_limit';
  if (error.message === 'recording_write_failed') return 'knowledge_recording_write_failed';
  if (status === 401 || status === 403) return 'permission_denied';
  if (status === 410) return 'knowledge_recording_deleted';
  return 'knowledge_recording_transcribe_failed';
}

/** Capture → stop → explicit on-device transcription → idempotent transcript-only sync. */
@ObservedV2
export class KnowledgeRecording {
  private static readonly MAX_LIVE_MODELS: number = 32;
  private static readonly liveModels: Set<KnowledgeRecording> = new Set<KnowledgeRecording>();

  @Trace entries: KnowledgeRecordingEntry[] = [];
  @Trace entry: KnowledgeRecordingEntry | undefined = undefined;
  @Trace captureState: string = 'idle';
  @Trace transcriptionState: string = 'idle';
  @Trace uploadState: string = 'idle';
  @Trace error: string = '';
  @Trace transcript: string = '';

  private root: ClientModel | undefined;
  private platform: NativeRecording;
  private context: Object | undefined;
  private ownerManager: string = '';
  private openRevision: number = 0;
  private local: RecordingLocalSnapshot | undefined;
  private activeTask: number = 0;
  private pendingStartTask: number = 0;
  private localWrite: Promise<void> = Promise.resolve();
  private forgetEpochs: Map<string, number> = new Map<string, number>();
  private locallyDeletedIds: Set<string> = new Set<string>();
  private activeEntryTasks: Set<string> = new Set<string>();
  private activeTranscriptionId: string = '';

  constructor(platform: NativeRecording) {
    if (KnowledgeRecording.liveModels.size >= KnowledgeRecording.MAX_LIVE_MODELS) throw new Error('recording_registry_full');
    this.platform = platform;
    KnowledgeRecording.liveModels.add(this);
  }

  async dispose(): Promise<void> {
    try { await this.stopForBackground(); } catch (_) { }
    this.openRevision++; this.activeTask++;
    this.local = undefined; this.root = undefined; this.context = undefined; this.ownerManager = '';
    this.activeTranscriptionId = '';
    this.entries = []; this.entry = undefined; this.transcript = '';
    this.captureState = 'idle'; this.transcriptionState = 'idle'; this.uploadState = 'idle'; this.error = '';
    KnowledgeRecording.liveModels.delete(this);
  }

  /** Stops capture and transcription even when their views remain mounted in the background. */
  static async backgroundAll(): Promise<void> {
    await Promise.all(Array.from(KnowledgeRecording.liveModels).map(async (model: KnowledgeRecording): Promise<void> => {
      try { await model.stopForBackground(); } catch (_) { }
    }));
  }

  /** Retires a forgotten Hub profile before waiting, then removes only that profile's local recordings. */
  static async forgetProfile(profileId: string, context: Object): Promise<void> {
    if (!profileId) return;
    const models = Array.from(KnowledgeRecording.liveModels), platforms = new Set<NativeRecording>();
    const retirements: Promise<void>[] = [];
    for (const model of models) {
      platforms.add(model.platform);
      retirements.push(model.retireProfile(profileId));
    }
    await Promise.all(retirements);
    await Promise.all(Array.from(platforms).map(async (platform: NativeRecording): Promise<void> => {
      await platform.forgetProfile(profileId);
    }));
    // The context is required even when no recording model is live this launch:
    // a fresh platform instance must scan and purge recordings left by prior sessions.
    await NativeRecording.purgeProfile(context, profileId);
  }

  private retireProfile(profileId: string): Promise<void> {
    const writes = this.localWrite;
    this.advanceForgetEpoch(profileId);
    const matches = this.local?.profileId === profileId;
    const capturing = matches && ['requesting', 'recording'].includes(this.captureState);
    const transcribing = matches && (!!this.activeTranscriptionId || this.transcriptionState === 'transcribing');
    if (matches) {
      this.openRevision++; this.activeTask++;
      this.local = undefined; this.root = undefined; this.context = undefined; this.ownerManager = '';
      this.entries = []; this.entry = undefined; this.transcript = '';
      this.captureState = 'idle'; this.transcriptionState = 'idle'; this.uploadState = 'idle'; this.error = '';
      this.activeTranscriptionId = '';
    }
    return (async (): Promise<void> => {
      if (transcribing) { try { await this.platform.cancelTranscription(); } catch (_) { } }
      if (capturing) { try { await this.platform.stop(); } catch (_) { } }
      await writes.catch(() => undefined);
    })();
  }

  private forgetEpoch(profileId: string): number { return this.forgetEpochs.get(profileId) || 0; }
  private advanceForgetEpoch(profileId: string): void { this.forgetEpochs.set(profileId, this.forgetEpoch(profileId) + 1); }
  private profileEpochCurrent(snapshot: RecordingLocalSnapshot): boolean {
    return this.forgetEpoch(snapshot.profileId) === snapshot.forgetEpoch;
  }

  private async saveLocal(current: KnowledgeRecordingEntry, snapshot: RecordingLocalSnapshot): Promise<void> {
    const operation = this.localWrite.then(async (): Promise<void> => {
      if (!this.profileEpochCurrent(snapshot) || this.locallyDeletedIds.has(current.id)) return;
      await this.platform.save(current);
      // A write already in flight when Forget began is removed after it finishes.
      if (!this.profileEpochCurrent(snapshot) || this.locallyDeletedIds.has(current.id)) await this.platform.remove(current.id);
    });
    this.localWrite = operation.catch(() => undefined);
    await operation;
  }

  private async removeLocal(recordingId: string): Promise<void> {
    const operation = this.localWrite.then(async (): Promise<void> => { await this.platform.remove(recordingId); });
    this.localWrite = operation.catch(() => undefined);
    await operation;
  }

  private async withEntryTask(recordingId: string, action: () => Promise<boolean>): Promise<boolean> {
    if (this.activeEntryTasks.has(recordingId)) return false;
    this.activeEntryTasks.add(recordingId);
    try { return await action(); }
    finally {
      this.activeEntryTasks.delete(recordingId);
      if (this.locallyDeletedIds.has(recordingId) && !this.entries.some((item: KnowledgeRecordingEntry) => item.id === recordingId))
        this.locallyDeletedIds.delete(recordingId);
    }
  }

  async open(client: ClientModel, ownerManager: string, context: Object): Promise<void> {
    if (this.captureState === 'recording') await this.stop().catch(() => false);
    if (this.captureState === 'requesting') {
      this.activeTask++;
      await this.platform.stop().catch(() => undefined);
    }
    if (this.activeTranscriptionId || this.transcriptionState === 'transcribing') await this.cancelTranscription().catch(() => undefined);
    const revision = ++this.openRevision;
    this.root = client; this.context = context; this.ownerManager = ownerManager || client.profile.manager;
    this.local = undefined;
    this.entries = []; this.entry = undefined; this.transcript = '';
    this.captureState = 'idle'; this.transcriptionState = 'idle'; this.uploadState = 'idle'; this.error = '';
    let snapshot: RecordingLocalSnapshot | undefined;
    try {
      snapshot = this.captureLocal(client, this.ownerManager, revision);
      this.local = snapshot;
      await this.platform.initialize(context);
      if (!this.isLocalCurrent(snapshot)) return;
      const loaded = await this.platform.load(snapshot.profileId, snapshot.hubId, snapshot.ownerManager);
      if (!this.isLocalCurrent(snapshot)) return;
      const recovered: KnowledgeRecordingEntry[] = [];
      for (const item of loaded) {
        let changed = false;
        if (item.transcriptionState === 'transcribing') {
          item.transcriptionState = 'failed'; item.error = 'knowledge_recording_transcribe_failed'; changed = true;
        }
        if (item.uploadState === 'uploading') {
          // A process can exit after PUT dispatch and before its acknowledgement is journaled.
          // The endpoint replaces by recording id/revision, so retry the saved body as unknown.
          item.uploadState = 'unknown'; item.uploadError = 'knowledge_recording_upload_unknown';
          item.serverStatus = ''; item.serverRevision = 0; changed = true;
        }
        if (changed) recovered.push(item);
      }
      this.entries = loaded;
      const selected = loaded.find((item: KnowledgeRecordingEntry) => item.uploadState === 'unknown' || item.uploadState === 'failed') || loaded[0];
      if (selected) this.selectEntry(selected.id);
      for (const item of recovered) {
        await this.saveLocal(item, snapshot);
        if (!this.isLocalCurrent(snapshot)) return;
      }
    } catch (failure) {
      if (revision === this.openRevision && this.root === client) this.error = errorCode(failure as Error);
    }
  }

  canSelectEntry(): boolean {
    return !this.pendingStartTask && this.activeEntryTasks.size === 0 &&
      !['requesting', 'recording'].includes(this.captureState) && this.transcriptionState !== 'transcribing' && this.uploadState !== 'uploading';
  }

  selectEntry(recordingId: string): void {
    if (!this.canSelectEntry()) return;
    const selected = this.entries.find((item: KnowledgeRecordingEntry) => item.id === recordingId);
    if (!selected) return;
    this.entry = selected; this.transcript = selected.transcript; this.captureState = selected.captureState;
    this.transcriptionState = selected.transcriptionState; this.uploadState = selected.uploadState;
    this.error = selected.error || selected.uploadError;
  }

  canStart(): boolean { return !!this.local && !!this.context && this.pendingStartTask === 0 && this.activeEntryTasks.size === 0 &&
    this.uploadState !== 'uploading' && !['requesting', 'recording'].includes(this.captureState) && this.transcriptionState !== 'transcribing'; }
  canStop(): boolean { return this.captureState === 'recording'; }
  canTranscribe(): boolean { return !!this.entry && this.entry.captureState === 'stopped' && this.entry.transcriptionState !== 'complete' && this.entry.transcriptionState !== 'transcribing'; }
  canRetryUpload(): boolean { return !!this.entry && !!this.entry.uploadBody && ['unknown', 'failed'].includes(this.entry.uploadState); }
  canDeleteLocal(): boolean { return !!this.entry && this.entry.captureState !== 'recording' && this.entry.transcriptionState !== 'transcribing' && this.entry.uploadState !== 'uploading'; }

  async start(): Promise<boolean> {
    const snapshot = this.local, context = this.context, client = this.root;
    if (!snapshot || !context || !client || !this.canStart() || !this.isLocalCurrent(snapshot)) return false;
    const task = ++this.activeTask;
    this.pendingStartTask = task;
    this.error = ''; this.captureState = 'requesting';
    try {
      await this.platform.ensureMicrophonePermission(context);
      if (task !== this.activeTask || !this.isLocalCurrent(snapshot)) {
        if (task === this.activeTask && this.captureState === 'requesting') this.captureState = 'idle';
        return false;
      }
      const startedAt = new Date().toISOString(), recordingId = id().toLowerCase();
      const entry: KnowledgeRecordingEntry = {
        id: recordingId, profileId: snapshot.profileId, hubId: snapshot.hubId, managerId: snapshot.ownerManager,
        startedAt, endedAt: '', duration: 0, audioBytes: 0, language: 'zh-CN', title: '', transcript: '', revision: 0,
        captureState: 'recording', transcriptionState: 'idle', uploadState: 'idle', uploadBody: undefined,
        serverStatus: '', serverRevision: 0, interrupted: false, error: '', uploadError: ''
      };
      await this.saveLocal(entry, snapshot);
      if (task !== this.activeTask || !this.isLocalCurrent(snapshot)) {
        await this.platform.remove(entry.id);
        if (task === this.activeTask && this.captureState === 'requesting') this.captureState = 'idle';
        return false;
      }
      this.entry = entry; this.entries = [entry, ...this.entries]; this.transcript = '';
      await this.platform.start(context, recordingId, (reason: string) => { this.stopForCaptureFailure(reason).catch(() => undefined); });
      if (task !== this.activeTask || !this.isLocalCurrent(snapshot)) { await this.stopEntry(entry, true); return false; }
      this.captureState = 'recording'; return true;
    } catch (failure) {
      const current = this.entry, failureFields = failure as Object as Record<string, Object>;
      let stopped = failureFields['recordingStop'] ? failureFields['recordingStop'] as Object as NativeRecordingStop : undefined;
      if (current?.captureState === 'recording' && !stopped) {
        try { stopped = await this.platform.stop(); } catch (_) { }
      }
      if (!this.isLocalCurrent(snapshot)) {
        if (current?.captureState === 'recording') await this.stopEntry(current, true, stopped).catch(() => false);
        return false;
      }
      const code = errorCode(failure as Error);
      if (current?.captureState === 'recording') {
        const hasAudio = await this.stopEntry(current, false, stopped);
        const finished = this.entry;
        if (hasAudio && finished?.id === current.id) {
          const kept = copyEntry(finished); kept.error = code;
          await this.persistLocal(kept, snapshot); this.error = code;
        } else if (!hasAudio && finished?.id === current.id) {
          this.entries = this.entries.filter((item: KnowledgeRecordingEntry) => item.id !== current.id);
          this.entry = undefined; this.captureState = 'idle'; this.error = code;
          await this.platform.remove(current.id).catch(() => undefined);
        }
      } else {
        if (task === this.activeTask && this.captureState === 'requesting') this.captureState = 'idle';
        this.error = task === this.activeTask ? code : '';
      }
      return false;
    } finally {
      if (this.pendingStartTask === task) this.pendingStartTask = 0;
    }
  }

  async stop(): Promise<boolean> {
    const current = this.entry;
    if (!current || current.captureState !== 'recording') return false;
    return this.stopEntry(current, false);
  }

  async stopForBackground(): Promise<void> {
    if (this.captureState === 'recording' || this.captureState === 'requesting') {
      this.activeTask++;
      if (this.captureState === 'recording') await this.stop();
      else {
        const current = this.entry;
        let stopped: NativeRecordingStop | undefined;
        try { stopped = await this.platform.stop(); } catch (_) { }
        if (current?.captureState === 'recording') await this.stopEntry(current, false, stopped);
      }
    }
    if (this.activeTranscriptionId || this.transcriptionState === 'transcribing') await this.cancelTranscription();
  }

  async stopForCaptureFailure(reason: string): Promise<void> {
    const current = this.entry;
    if (!current || current.captureState !== 'recording') return;
    await this.stopEntry(current, false);
    if (this.entry?.id === current.id) {
      const stopped = copyEntry(this.entry); stopped.error = reason;
      await this.persistLocal(stopped, this.local);
      if (this.entry?.id === stopped.id) this.error = reason;
    }
  }

  private async stopEntry(current: KnowledgeRecordingEntry, stale: boolean, stoppedResult?: NativeRecordingStop): Promise<boolean> {
    const snapshot = this.local;
    const stopped = copyEntry(current);
    let result: NativeRecordingStop = stoppedResult || { path: '', bytes: stopped.audioBytes, duration: stopped.duration };
    if (!stoppedResult) { try { result = await this.platform.stop(); } catch (_) { } }
    stopped.audioBytes = Math.max(0, Math.min(result.bytes, 115200000));
    stopped.duration = Math.max(0, Math.min(result.duration, MAX_DURATION_SECONDS));
    stopped.endedAt = new Date().toISOString(); stopped.captureState = 'stopped'; stopped.interrupted = stale;
    if (stopped.audioBytes === 0) stopped.error = 'knowledge_recording_too_short';
    let saved = true;
    try { await this.persistLocal(stopped, snapshot); } catch (_) { saved = false; }
    if (!stale && snapshot && this.isLocalCurrent(snapshot) && this.entry?.id === stopped.id) {
      if (!saved) {
        this.entries = this.entries.map((item: KnowledgeRecordingEntry) => item.id === stopped.id ? stopped : item);
        this.entry = stopped;
      }
      this.captureState = 'stopped'; this.transcriptionState = stopped.transcriptionState; this.uploadState = stopped.uploadState;
      this.error = saved ? stopped.error : 'knowledge_storage_unavailable';
    }
    return stopped.audioBytes > 0 && saved;
  }

  /** Transcription starts only from this explicit action. The same stable id/revision is then uploaded as full state. */
  async transcribeAndUpload(): Promise<boolean> {
    const current = this.entry, snapshot = this.local;
    if (!current || !snapshot || !this.canTranscribe() || !this.isLocalCurrent(snapshot) || this.activeEntryTasks.has(current.id)) return false;
    if (current.language !== 'zh-CN') { this.error = 'knowledge_recording_chinese_only'; return false; }
    if (current.duration <= 0 || current.duration > MAX_DURATION_SECONDS || current.audioBytes <= 0) { this.error = 'knowledge_recording_too_short'; return false; }
    return this.withEntryTask(current.id, async (): Promise<boolean> => {
      this.activeTranscriptionId = current.id;
      try { return await this.transcribeAndUploadCurrent(current, snapshot); }
      finally { if (this.activeTranscriptionId === current.id) this.activeTranscriptionId = ''; }
    });
  }

  private async transcribeAndUploadCurrent(current: KnowledgeRecordingEntry, snapshot: RecordingLocalSnapshot): Promise<boolean> {
    const task = ++this.activeTask;
    const progress = copyEntry(current);
    progress.transcriptionState = 'transcribing'; progress.error = '';
    this.transcriptionState = 'transcribing'; this.error = '';
    const result = copyEntry(progress);
    try {
      await this.persistLocal(progress, snapshot);
      if (task !== this.activeTask || !this.isLocalCurrent(snapshot)) {
        if (task !== this.activeTask && this.isLocalCurrent(snapshot)) {
          result.transcriptionState = result.transcript ? 'complete' : 'idle'; result.error = '';
          try { await this.persistLocal(result, snapshot); }
          catch (_) {
            if (this.isLocalCurrent(snapshot) && this.entry?.id === result.id) {
              this.entries = this.entries.map((item: KnowledgeRecordingEntry) => item.id === result.id ? result : item);
              this.entry = result; this.transcriptionState = result.transcriptionState;
              this.error = 'knowledge_storage_unavailable';
            }
          }
        }
        return false;
      }
      const text = (await this.platform.transcribe(result.id, result.language)).trim();
      if (task !== this.activeTask || text.length === 0 || utf8Length(text) > MAX_TRANSCRIPT_BYTES) throw new Error('knowledge_recording_transcribe_failed');
      const changed = text !== result.transcript || !result.uploadBody;
      result.transcript = text; result.transcriptionState = 'complete'; result.error = '';
      if (changed) {
        result.revision = Math.max(Date.now(), result.revision + 1);
        result.uploadBody = this.makeUpload(result);
        result.uploadState = 'idle'; result.serverStatus = ''; result.serverRevision = 0; result.uploadError = '';
      }
      await this.persistLocal(result, snapshot);
      if (!this.isLocalCurrent(snapshot)) return false;
      return await this.uploadEntry(result, snapshot);
    } catch (failure) {
      if (task !== this.activeTask) return false;
      result.transcriptionState = result.transcript ? 'complete' : (failure as Error).message === 'transcription_cancelled' ? 'idle' : 'failed';
      result.error = result.transcript ? '' : errorCode(failure as Error);
      try { await this.persistLocal(result, snapshot); }
      catch (_) {
        if (this.isLocalCurrent(snapshot) && this.entry?.id === result.id) {
          this.entries = this.entries.map((item: KnowledgeRecordingEntry) => item.id === result.id ? result : item);
          this.entry = result; this.transcriptionState = result.transcriptionState;
          this.error = 'knowledge_storage_unavailable';
        }
      }
      return false;
    }
  }

  async retryUpload(): Promise<boolean> {
    const current = this.entry, snapshot = this.local;
    if (!current || !snapshot || !this.canRetryUpload() || !this.isLocalCurrent(snapshot) || this.activeEntryTasks.has(current.id)) return false;
    return this.withEntryTask(current.id, async (): Promise<boolean> => await this.uploadEntry(current, snapshot));
  }

  async cancelTranscription(): Promise<void> {
    this.activeTask++;
    const snapshot = this.local;
    const activeId = this.activeTranscriptionId;
    await this.platform.cancelTranscription();
    const current = this.entries.find((item: KnowledgeRecordingEntry) => item.id === activeId) || this.entry;
    if (current?.transcriptionState === 'transcribing') {
      const cancelled = copyEntry(current);
      cancelled.transcriptionState = cancelled.transcript ? 'complete' : 'idle';
      await this.persistLocal(cancelled, snapshot);
      if (snapshot && this.isLocalCurrent(snapshot) && this.entry?.id === current.id) {
        this.transcriptionState = cancelled.transcriptionState;
        this.error = '';
      }
    }
  }

  async deleteLocal(): Promise<boolean> {
    const current = this.entry, snapshot = this.local;
    if (!current || !snapshot || !this.isLocalCurrent(snapshot) || !this.canDeleteLocal() || current.captureState === 'recording') return false;
    this.locallyDeletedIds.add(current.id);
    try {
      await this.removeLocal(current.id);
      if (!this.activeEntryTasks.has(current.id)) this.locallyDeletedIds.delete(current.id);
      if (!this.isLocalCurrent(snapshot) || this.entry?.id !== current.id) return true;
      this.entries = this.entries.filter((item: KnowledgeRecordingEntry) => item.id !== current.id);
      this.entry = this.entries[0]; this.transcript = this.entry?.transcript || '';
      this.captureState = this.entry?.captureState || 'idle'; this.transcriptionState = this.entry?.transcriptionState || 'idle';
      this.uploadState = this.entry?.uploadState || 'idle'; this.error = '';
      return true;
    } catch (_) { if (this.isLocalCurrent(snapshot) && this.entry?.id === current.id) this.error = 'storage_unavailable'; return false; }
  }

  private async uploadEntry(current: KnowledgeRecordingEntry, local: RecordingLocalSnapshot): Promise<boolean> {
    if (!this.entries.some((item: KnowledgeRecordingEntry) => item.id === current.id) || !current.uploadBody || !current.transcript ||
      this.locallyDeletedIds.has(current.id) || current.uploadState === 'uploading' ||
      ['stored', 'stale', 'expired'].includes(current.serverStatus)) return false;
    let owner: RecordingRemoteSnapshot;
    try { owner = await this.captureRemote(local); }
    catch (failure) {
      const failed = copyEntry(current); failed.uploadState = 'failed'; failed.uploadError = errorCode(failure as Error);
      await this.persistLocal(failed, local); return false;
    }
    if (!this.isRemoteCurrent(owner) || !this.recordMatches(current, owner)) {
      const failed = copyEntry(current); failed.uploadState = 'failed'; failed.uploadError = 'request_updated';
      await this.persistLocal(failed, local); return false;
    }
    const pending = copyEntry(current), body = pending.uploadBody;
    if (!body || !this.isRemoteCurrent(owner)) return false;
    pending.uploadState = 'uploading'; pending.uploadError = '';
    await this.persistLocal(pending, local);
    if (this.locallyDeletedIds.has(current.id) || !this.entries.some((item: KnowledgeRecordingEntry) => item.id === current.id) || !this.isRemoteCurrent(owner)) {
      const failed = copyEntry(pending); failed.uploadState = 'failed'; failed.uploadError = 'request_updated';
      await this.persistLocal(failed, local); return false;
    }
    const attempted = copyEntry(pending);
    const path = '/api/v1/recordings/' + current.id.toLowerCase();
    try {
      // Recording PUT is documented as an idempotent full-state write, not a client_ops receipt route.
      // The manifest journals this exact body/id/revision before dispatch and records the server status after reply.
      const raw = await owner.repository.transport.request(path, 'PUT', JSON.stringify(body));
      const answer = JSON.parse(raw) as Record<string, Object>;
      const ackId = typeof answer['id'] === 'string' ? answer['id'] as string : '';
      const ackRevision = typeof answer['revision'] === 'number' ? answer['revision'] as number : 0;
      const status = typeof answer['status'] === 'string' ? answer['status'] as string : '';
      if (ackId.toLowerCase() !== current.id.toLowerCase() || !Number.isInteger(ackRevision) || ackRevision <= 0 ||
        !['stored', 'stale', 'expired'].includes(status)) throw new Error('result_unknown');
      attempted.serverStatus = status; attempted.serverRevision = ackRevision; attempted.uploadState = status;
      attempted.uploadError = '';
      await this.persistLocal(attempted, local);
      const stillCurrent = this.isRemoteCurrent(owner);
      return stillCurrent && status === 'stored' && ackRevision >= body.revision;
    } catch (failure) {
      const fields = failure as Object as Record<string, Object>, status = typeof fields['status'] === 'number' ? fields['status'] as number : 0;
      const unknown = fields['resultUnknown'] === true || fields['resultPending'] === true || status === 0 || status >= 500 || (failure as Error).message === 'result_unknown';
      attempted.uploadState = unknown ? 'unknown' : status === 410 ? 'deleted' : 'failed';
      attempted.uploadError = status === 410 ? 'knowledge_recording_deleted' : unknown ? 'knowledge_recording_upload_unknown' : 'knowledge_recording_upload_failed';
      await this.persistLocal(attempted, local);
      return false;
    }
  }

  private makeUpload(current: KnowledgeRecordingEntry): KnowledgeRecordingUpload {
    const segment: KnowledgeRecordingSegment = { start: 0, end: current.duration, text: current.transcript };
    return {
      revision: current.revision, started_at: current.startedAt, ended_at: current.endedAt, duration: current.duration,
      language: current.language, device: 'phone', title: current.title, bookmarks: [], segments: [segment], complete: true, kind: 'note'
    };
  }

  private async captureRemote(local: RecordingLocalSnapshot): Promise<RecordingRemoteSnapshot> {
    if (!this.isLocalCurrent(local)) throw new Error('request_updated');
    const root = local.root as RecordingClient;
    if (!root.connected || root.demo) throw new Error(root.connected ? 'request_updated' : 'offline');
    let owner = root;
    if (local.ownerManager !== root.profile.manager) {
      const ref = new ResourceRow();
      ref.group = root.profile.id;
      ref.manager = local.ownerManager;
      owner = await root.viewFor(ref) as RecordingClient;
      if (!this.isLocalCurrent(local) || !owner.connected || owner.profile.id !== local.profileId || owner.profile.hubId !== local.hubId ||
        owner.profile.manager !== local.ownerManager) throw new Error('request_updated');
    }
    const ownerGeneration = owner.connectionGeneration(), ownerScope = owner.scope(), ownerProfile = profileCopy(owner.profile);
    const repository = owner.repo();
    const snapshot: RecordingRemoteSnapshot = {
      root: local.root,
      revision: local.revision,
      generation: local.generation,
      scope: local.scope,
      profileId: local.profileId,
      hubId: local.hubId,
      rootManager: local.rootManager,
      ownerManager: local.ownerManager,
      forgetEpoch: local.forgetEpoch,
      localSnapshot: local,
      owner: owner as ClientModel,
      ownerGeneration,
      ownerScope,
      ownerProfile,
      repository
    };
    if (!this.isRemoteCurrent(snapshot)) throw new Error('request_updated');
    return snapshot;
  }

  private captureLocal(client: ClientModel, manager: string, revision: number): RecordingLocalSnapshot {
    const root = client as RecordingClient, generation = client.connectionGeneration(), scope = client.scope();
    const snapshot: RecordingLocalSnapshot = {
      root: client, revision, generation, scope, profileId: root.profile.id, hubId: root.profile.hubId,
      rootManager: root.profile.manager, ownerManager: manager, forgetEpoch: this.forgetEpoch(root.profile.id)
    };
    if (!snapshot.profileId || root.demo || !root.owns(generation, scope) || root.profile.id !== snapshot.profileId ||
      root.profile.hubId !== snapshot.hubId || root.profile.manager !== snapshot.rootManager) throw new Error('request_updated');
    return snapshot;
  }

  private isLocalCurrent(snapshot: RecordingLocalSnapshot): boolean {
    if (this.openRevision !== snapshot.revision || this.root !== snapshot.root || this.local !== snapshot) return false;
    const root = snapshot.root as RecordingClient;
    return this.profileEpochCurrent(snapshot) && root.owns(snapshot.generation, snapshot.scope) && root.profile.id === snapshot.profileId && root.profile.hubId === snapshot.hubId &&
      root.profile.manager === snapshot.rootManager && this.ownerManager === snapshot.ownerManager;
  }

  private isRemoteCurrent(snapshot: RecordingRemoteSnapshot): boolean {
    const profile = snapshot.owner.profile;
    const expected = snapshot.ownerProfile;
    if (!this.isLocalCurrent(snapshot.localSnapshot) || !snapshot.owner.connected || !snapshot.owner.owns(snapshot.ownerGeneration, snapshot.ownerScope) ||
      profile.id !== expected.id || profile.hubId !== expected.hubId || profile.manager !== expected.manager ||
      profile.baseUrl !== expected.baseUrl || profile.pin !== expected.pin || profile.id !== snapshot.profileId ||
      profile.hubId !== snapshot.hubId || profile.manager !== snapshot.ownerManager) return false;
    try { return snapshot.owner.repo() === snapshot.repository; } catch (_) { return false; }
  }

  private recordMatches(current: KnowledgeRecordingEntry, owner: RecordingRemoteSnapshot): boolean {
    return current.profileId === owner.profileId && current.hubId === owner.hubId && current.managerId === owner.ownerManager;
  }

  private async persistLocal(current: KnowledgeRecordingEntry, snapshot: RecordingLocalSnapshot | undefined = this.local): Promise<void> {
    if (!snapshot) return;
    await this.saveLocal(current, snapshot);
    if (!snapshot || !this.isLocalCurrent(snapshot) || this.entry?.id !== current.id) return;
    const index = this.entries.findIndex((item: KnowledgeRecordingEntry) => item.id === current.id);
    if (index < 0) this.entries = [current, ...this.entries];
    else { const next = this.entries.slice(); next[index] = current; this.entries = next; }
    if (this.entry?.id === current.id) {
      this.entry = current; this.captureState = current.captureState; this.transcriptionState = current.transcriptionState;
      this.uploadState = current.uploadState; this.transcript = current.transcript;
    }
  }

}
