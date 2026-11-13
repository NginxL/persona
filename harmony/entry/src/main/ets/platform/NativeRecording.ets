import { abilityAccessCtrl, common, Permissions } from '@kit.AbilityKit';
import { audio } from '@kit.AudioKit';
import { speechRecognizer } from '@kit.CoreSpeechKit';
import { fileIo } from '@kit.CoreFileKit';
import { util } from '@kit.ArkTS';
import type { KnowledgeRecordingEntry } from '../models/KnowledgeRecording';

const MICROPHONE: string = 'ohos.permission.MICROPHONE';
const SAMPLE_RATE: number = 16000;
const BYTES_PER_SECOND: number = SAMPLE_RATE * 2;
const MAX_RECORDING_SECONDS: number = 60 * 60;
const MAX_RECORDING_BYTES: number = BYTES_PER_SECOND * MAX_RECORDING_SECONDS;
const MAX_LOCAL_BYTES: number = 350 * 1024 * 1024;
const MAX_LOCAL_ENTRIES: number = 10;
const PCM_CHUNK_BYTES: number = 1280;

function utf8Length(value: string): number {
  let bytes = 0;
  for (let index = 0; index < value.length; index++) {
    const code = value.charCodeAt(index);
    if (code < 0x80) bytes++;
    else if (code < 0x800) bytes += 2;
    else if (code >= 0xd800 && code <= 0xdbff && index + 1 < value.length && value.charCodeAt(index + 1) >= 0xdc00 && value.charCodeAt(index + 1) <= 0xdfff) {
      bytes += 4; index++;
    } else bytes += 3;
  }
  return bytes;
}

export interface NativeRecordingStop {
  path: string;
  bytes: number;
  duration: number;
}

/** App-private bounded PCM storage and foreground-only native audio / offline ASR. */
export class NativeRecording {
  private context: common.UIAbilityContext | undefined;
  private directory: string = '';
  private capturer: audio.AudioCapturer | undefined;
  private readListener: ((data: ArrayBuffer) => void) | undefined;
  private fileDescriptor: number = -1;
  private activeId: string = '';
  private activePath: string = '';
  private activeBytes: number = 0;
  private readFailed: boolean = false;
  private generation: number = 0;
  private stopping: Promise<NativeRecordingStop> | undefined;
  private asr: speechRecognizer.SpeechRecognitionEngine | undefined;
  private asrSession: string = '';
  private cancelAsr: boolean = false;
  private asrCompletion: ((error?: Error) => void) | undefined;
  private asrPending: Promise<string> | undefined;

  /** Purges a forgotten profile even when no Knowledge recording model was opened this launch. */
  static async purgeProfile(context: Object, profileId: string): Promise<void> {
    const platform = new NativeRecording();
    await platform.initialize(context);
    await platform.forgetProfile(profileId);
  }

  async initialize(context: Object): Promise<void> {
    const host = context as common.UIAbilityContext;
    if (!host.filesDir) throw new Error('storage_unavailable');
    this.context = host;
    this.directory = host.filesDir + '/knowledge-recordings';
    try { fileIo.statSync(this.directory); } catch (_) { fileIo.mkdirSync(this.directory, true); }
  }

  async ensureMicrophonePermission(context: Object): Promise<void> {
    const host = context as common.UIAbilityContext;
    const access = abilityAccessCtrl.createAtManager();
    let status = access.getSelfPermissionStatus(MICROPHONE as Permissions);
    if (status === abilityAccessCtrl.PermissionStatus.NOT_DETERMINED) {
      await access.requestPermissionsFromUser(host, [MICROPHONE] as Permissions[]);
      status = access.getSelfPermissionStatus(MICROPHONE as Permissions);
    }
    if (status === abilityAccessCtrl.PermissionStatus.DENIED || status === abilityAccessCtrl.PermissionStatus.RESTRICTED) {
      throw new Error('knowledge_recording_microphone_denied');
    }
    if (status !== abilityAccessCtrl.PermissionStatus.GRANTED) throw new Error('knowledge_recording_microphone_denied');
  }

  async start(context: Object, id: string, onLimit: (reason: string) => void): Promise<string> {
    if (this.capturer || this.fileDescriptor >= 0 || !this.directory) throw new Error('recording_busy');
    if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(id)) throw new Error('invalid_recording_id');
    const generation = ++this.generation;
    this.readFailed = false;
    await this.ensureMicrophonePermission(context);
    if (generation !== this.generation) throw new Error('request_updated');
    this.ensureCapacity();
    const path = this.audioPath(id);
    const output = fileIo.openSync(path, fileIo.OpenMode.CREATE | fileIo.OpenMode.WRITE_ONLY | fileIo.OpenMode.TRUNC);
    this.fileDescriptor = output.fd; this.activeId = id.toLowerCase(); this.activePath = path; this.activeBytes = 0;
    try {
      const capturer = await audio.createAudioCapturer({
        streamInfo: {
          samplingRate: SAMPLE_RATE,
          channels: audio.AudioChannel.CHANNEL_1,
          sampleFormat: audio.AudioSampleFormat.SAMPLE_FORMAT_S16LE,
          encodingType: audio.AudioEncodingType.ENCODING_TYPE_RAW
        },
        capturerInfo: { source: audio.SourceType.SOURCE_TYPE_MIC, capturerFlags: 0 }
      });
      if (generation !== this.generation) { await capturer.release(); throw new Error('request_updated'); }
      this.capturer = capturer;
      const listener = (data: ArrayBuffer): void => {
        if (generation !== this.generation || this.fileDescriptor < 0 || this.readFailed) return;
        const bytes = data.byteLength;
        if (!bytes || bytes % 2 !== 0) { this.readFailed = true; onLimit('recording_write_failed'); return; }
        if (this.activeBytes + bytes > MAX_RECORDING_BYTES) { this.readFailed = true; onLimit('recording_limit'); return; }
        try {
          const written = fileIo.writeSync(this.fileDescriptor, data);
          if (written !== bytes) { this.readFailed = true; onLimit('recording_write_failed'); return; }
          this.activeBytes += written;
        } catch (_) { this.readFailed = true; onLimit('recording_write_failed'); }
      };
      this.readListener = listener;
      capturer.on('readData', listener);
      await capturer.start();
      if (generation !== this.generation) {
        const stopped = await this.stop();
        const failure = new Error('request_updated');
        (failure as Object as Record<string, Object>)['recordingStop'] = stopped as Object;
        throw failure;
      }
      return path;
    } catch (error) {
      const failure = error instanceof Error ? error : new Error('recording_start_failed');
      const fields = failure as Object as Record<string, Object>;
      let stopped = fields['recordingStop'] ? fields['recordingStop'] as Object as NativeRecordingStop : undefined;
      if (!stopped) {
        try { stopped = await this.stop(); }
        catch (_) { stopped = { path, bytes: this.activeBytes, duration: this.activeBytes / BYTES_PER_SECOND }; }
      }
      fields['recordingStop'] = stopped as Object;
      throw failure;
    }
  }

  async stop(): Promise<NativeRecordingStop> {
    if (this.stopping) return this.stopping;
    const path = this.activePath, fd = this.fileDescriptor, bytes = this.activeBytes;
    const capturer = this.capturer, listener = this.readListener;
    this.generation++;
    this.capturer = undefined; this.readListener = undefined; this.fileDescriptor = -1;
    this.activeId = ''; this.activePath = ''; this.activeBytes = 0;
    const result: NativeRecordingStop = { path, bytes, duration: bytes / BYTES_PER_SECOND };
    this.stopping = (async (): Promise<NativeRecordingStop> => {
      if (capturer) {
        if (listener) capturer.off('readData', listener);
        try { if (capturer.state === audio.AudioState.STATE_RUNNING) await capturer.stop(); } catch (_) { }
        try { await capturer.release(); } catch (_) { }
      }
      if (fd >= 0) {
        try { fileIo.fsyncSync(fd); } catch (_) { }
        try { fileIo.closeSync(fd); } catch (_) { }
      }
      return result;
    })();
    try { return await this.stopping; } finally { this.stopping = undefined; }
  }

  async save(entry: KnowledgeRecordingEntry): Promise<void> {
    this.requireReady();
    if (!this.validId(entry.id) || JSON.stringify(entry).length > 5 * 1024 * 1024) throw new Error('storage_unavailable');
    const destination = this.manifestPath(entry.id), temporary = destination + '.tmp';
    const file = fileIo.openSync(temporary, fileIo.OpenMode.CREATE | fileIo.OpenMode.WRITE_ONLY | fileIo.OpenMode.TRUNC);
    let open = true;
    try {
      const content = JSON.stringify(entry);
      if (fileIo.writeSync(file.fd, content) !== utf8Length(content)) throw new Error('storage_unavailable');
      fileIo.fsyncSync(file.fd);
      fileIo.closeSync(file.fd); open = false;
      fileIo.renameSync(temporary, destination);
    } catch (error) {
      if (open) { try { fileIo.closeSync(file.fd); } catch (_) { } }
      try { fileIo.unlinkSync(temporary); } catch (_) { }
      throw error as Error;
    }
  }

  async load(profileId: string, hubId: string, managerId: string): Promise<KnowledgeRecordingEntry[]> {
    this.requireReady();
    const entries: KnowledgeRecordingEntry[] = [];
    for (const name of fileIo.listFileSync(this.directory)) {
      if (!/^[0-9a-f-]{36}\.json$/i.test(name)) continue;
      let entry: KnowledgeRecordingEntry;
      try { entry = JSON.parse(fileIo.readTextSync(this.directory + '/' + name)) as KnowledgeRecordingEntry; }
      catch (_) { continue; }
      if (!this.validId(entry.id) || entry.profileId !== profileId || entry.hubId !== hubId || entry.managerId !== managerId) continue;
      if (entry.captureState === 'recording') {
        const path = this.audioPath(entry.id);
        let bytes = 0;
        try { bytes = fileIo.statSync(path).size; } catch (_) { }
        if (bytes > 0 && bytes <= MAX_RECORDING_BYTES) {
          entry.audioBytes = bytes; entry.duration = bytes / BYTES_PER_SECOND; entry.captureState = 'stopped';
          entry.endedAt = new Date().toISOString(); entry.interrupted = true;
          await this.save(entry);
        } else {
          try { fileIo.unlinkSync(path); } catch (_) { }
          try { fileIo.unlinkSync(this.manifestPath(entry.id)); } catch (_) { }
          continue;
        }
      }
      entries.push(entry);
    }
    return entries.sort((left: KnowledgeRecordingEntry, right: KnowledgeRecordingEntry) => right.startedAt.localeCompare(left.startedAt));
  }

  async transcribe(id: string, language: string): Promise<string> {
    this.requireReady();
    if (language !== 'zh-CN') throw new Error('knowledge_recording_chinese_only');
    const path = this.audioPath(id);
    let size = 0;
    try { size = fileIo.statSync(path).size; } catch (_) { throw new Error('recording_file_missing'); }
    if (size < 16000) throw new Error('knowledge_recording_too_short');
    if (size > MAX_RECORDING_BYTES || size % 2 !== 0) throw new Error('recording_invalid_size');
    if (this.asrPending) throw new Error('transcription_busy');
    this.cancelAsr = false;
    const operation = this.runTranscription(path, size);
    this.asrPending = operation;
    try { return await operation; } finally { if (this.asrPending === operation) this.asrPending = undefined; }
  }

  async cancelTranscription(): Promise<void> {
    this.cancelAsr = true;
    const engine = this.asr, session = this.asrSession;
    if (engine && session) { try { engine.cancel(session); } catch (_) { } }
    this.asrCompletion?.(new Error('transcription_cancelled'));
    if (engine) await this.asrPending?.catch(() => undefined);
  }

  async remove(id: string): Promise<void> {
    this.requireReady();
    if (!this.validId(id) || id === this.activeId) throw new Error('recording_busy');
    for (const path of [this.audioPath(id), this.manifestPath(id)]) {
      try { fileIo.unlinkSync(path); } catch (_) { }
    }
  }

  /** Removes only local recordings whose manifest belongs to this Hub profile. */
  async forgetProfile(profileId: string): Promise<void> {
    if (!profileId || !this.directory || !this.context) return;
    for (const name of fileIo.listFileSync(this.directory)) {
      const temporary = name.endsWith('.json.tmp');
      const manifestName = temporary ? name.slice(0, -4) : name;
      if (!/^[0-9a-f-]{36}\.json$/i.test(manifestName)) continue;
      const path = this.directory + '/' + name;
      let entry: KnowledgeRecordingEntry;
      try { entry = JSON.parse(fileIo.readTextSync(path)) as KnowledgeRecordingEntry; } catch (_) { continue; }
      const id = manifestName.slice(0, -5);
      if (!this.validId(id) || !entry || typeof entry.id !== 'string' || entry.id.toLowerCase() !== id.toLowerCase() || entry.profileId !== profileId) continue;
      if (this.activeId.toLowerCase() === id.toLowerCase()) await this.stop();
      for (const ownedPath of [this.audioPath(id), this.manifestPath(id), this.manifestPath(id) + '.tmp']) {
        try { fileIo.unlinkSync(ownedPath); } catch (_) { }
      }
    }
  }

  private async runTranscription(path: string, size: number): Promise<string> {
    const engine = await speechRecognizer.createEngine({
      language: 'zh-CN', online: 1, extraParams: { locate: 'CN', recognizerMode: 'long' }
    });
    const session = util.generateRandomUUID(); this.asr = engine; this.asrSession = session;
    let finished = false, failed: Error | undefined, latest = '', finals: string[] = [];
    let started = false, notifyStarted: () => void = (): void => {};
    const startReady = new Promise<void>((resolve) => { notifyStarted = resolve; });
    let complete: () => void = (): void => {};
    const completion = new Promise<void>((resolve) => { complete = resolve; });
    const settle = (error?: Error): void => { if (finished) return; finished = true; failed = error; complete(); };
    this.asrCompletion = settle;
    engine.setListener({
      onStart: (_session: string, _message: string): void => {
        if (_session !== session || this.asr !== engine || this.asrSession !== session || finished || this.cancelAsr) return;
        started = true; notifyStarted();
      },
      onEvent: (_session: string, _code: number, _message: string): void => {},
      onResult: (_session: string, result: speechRecognizer.SpeechRecognitionResult): void => {
        if (_session !== session || this.asr !== engine || this.asrSession !== session || finished || this.cancelAsr) return;
        const text = result.result.trim();
        if (!text) return;
        latest = text;
        if (result.isFinal) finals.push(text);
      },
      onComplete: (_session: string, _message: string): void => { if (_session === session && this.asr === engine && this.asrSession === session) settle(); },
      onError: (_session: string, code: number, _message: string): void => {
        if (_session !== session || this.asr !== engine || this.asrSession !== session || finished) return;
        failed = new Error(code === 1002200012 ? 'knowledge_recording_microphone_denied' : 'knowledge_recording_transcribe_failed');
        settle(failed);
      }
    });
    try {
      if (this.cancelAsr) throw new Error('transcription_cancelled');
      engine.startListening({ sessionId: session, audioInfo: { audioType: 'pcm', sampleRate: SAMPLE_RATE, soundChannel: 1, sampleBit: 16 },
        // The engine rejects the frame reaching its limit. Allow one padded PCM
        // frame beyond the file so explicit finish, rather than the limit, ends it.
        extraParams: { recognitionMode: 1, maxAudioDuration: Math.max(20000, Math.ceil(size / BYTES_PER_SECOND * 1000) + 40) } });
      let startTimer: number = -1;
      try {
        await Promise.race([startReady, completion, new Promise<void>((_, reject): void => {
          startTimer = setTimeout((): void => reject(new Error('knowledge_recording_transcribe_failed')), 10000);
        })]);
      } finally { if (startTimer !== -1) clearTimeout(startTimer); }
      if (this.cancelAsr) throw new Error('transcription_cancelled');
      if (failed) throw failed as Error;
      if (!started || finished) throw new Error('knowledge_recording_transcribe_failed');
      const input = fileIo.openSync(path, fileIo.OpenMode.READ_ONLY);
      try {
        let offset = 0;
        while (offset < size) {
          if (this.cancelAsr) throw new Error('transcription_cancelled');
          if (failed) throw failed as Error;
          if (finished) throw new Error('knowledge_recording_transcribe_failed');
          const length = Math.min(PCM_CHUNK_BYTES, size - offset);
          const source = new ArrayBuffer(length);
          const read = fileIo.readSync(input.fd, source, { length: length });
          if (read !== length) throw new Error('recording_file_read_failed');
          let send = new Uint8Array(source);
          if (send.length < 640) { const padded = new Uint8Array(640); padded.set(send); send = padded; }
          else if (send.length !== 640 && send.length !== 1280) { const padded = new Uint8Array(1280); padded.set(send); send = padded; }
          engine.writeAudio(session, send);
          offset += read;
          if (failed) throw failed as Error;
          if (finished && offset < size) throw new Error('knowledge_recording_transcribe_failed');
          if (offset < size) await new Promise<void>((resolve) => setTimeout(resolve, send.length === 640 ? 20 : 40));
        }
      } finally { fileIo.closeSync(input.fd); }
      if (this.cancelAsr) throw new Error('transcription_cancelled');
      if (failed) throw failed as Error;
      if (!finished) engine.finish(session);
      let timer: number = -1;
      try {
        await Promise.race([completion, new Promise<void>((_, reject): void => {
          timer = setTimeout((): void => reject(new Error('knowledge_recording_transcribe_failed')), 30000);
        })]);
      } finally { if (timer !== -1) clearTimeout(timer); }
      if (this.cancelAsr) throw new Error('transcription_cancelled');
      if (failed) throw failed as Error;
      const text = (finals.length ? finals.join('') : latest).trim();
      if (!text) throw new Error('knowledge_recording_transcribe_failed');
      return text;
    } catch (error) {
      if (error instanceof Error && error.message === 'transcription_cancelled') {
        try { engine.cancel(session); } catch (_) { }
        throw error;
      }
      try { engine.cancel(session); } catch (_) { }
      throw failed || error as Error;
    } finally {
      settle(failed || new Error('transcription_cancelled'));
      try { engine.shutdown(); } catch (_) { }
      if (this.asr === engine) this.asr = undefined;
      if (this.asrSession === session) this.asrSession = '';
      if (this.asrCompletion === settle) this.asrCompletion = undefined;
    }
  }

  private ensureCapacity(): void {
    let entries = 0, bytes = 0;
    for (const name of fileIo.listFileSync(this.directory)) {
      if (/^[0-9a-f-]{36}\.json$/i.test(name)) entries++;
      if (/^[0-9a-f-]{36}\.pcm$/i.test(name)) {
        try { bytes += fileIo.statSync(this.directory + '/' + name).size; } catch (_) { }
      }
    }
    if (entries > MAX_LOCAL_ENTRIES || bytes + MAX_RECORDING_BYTES > MAX_LOCAL_BYTES) throw new Error('knowledge_recording_storage_full');
  }

  private requireReady(): void { if (!this.directory || !this.context) throw new Error('storage_unavailable'); }
  private validId(id: string): boolean { return /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(id); }
  private audioPath(id: string): string { this.requireReady(); if (!this.validId(id)) throw new Error('invalid_recording_id'); return this.directory + '/' + id.toLowerCase() + '.pcm'; }
  private manifestPath(id: string): string { this.requireReady(); if (!this.validId(id)) throw new Error('invalid_recording_id'); return this.directory + '/' + id.toLowerCase() + '.json'; }
}
