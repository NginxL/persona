import { audio } from '@kit.AudioKit';

const PLAYBACK_BUFFER_SECONDS: number = 3;
const DEFAULT_OUTPUT_RATE: number = 24000;

/** Foreground-only PCM16 mono capture and playback for the voice extension. */
export class VoiceAudioBridge {
  private capturer: audio.AudioCapturer | undefined;
  private renderer: audio.AudioRenderer | undefined;
  private readListener: ((data: ArrayBuffer) => void) | undefined;
  private writeListener: audio.AudioRendererWriteDataCallback | undefined;
  private sender: ((payload: ArrayBuffer) => void) | undefined;
  private muted: boolean = false;
  private generation: number = 0;
  private playback: Uint8Array[] = [];
  private playbackOffset: number = 0;
  private playbackBytes: number = 0;
  private maxPlaybackBytes: number = DEFAULT_OUTPUT_RATE * 2 * PLAYBACK_BUFFER_SECONDS;

  /** Bounds the short window in which the extension may speak before the renderer has finished starting. */
  setPlaybackRate(outputRate: number): void {
    if (!Number.isInteger(outputRate) || outputRate < 8000 || outputRate > 96000) return;
    this.maxPlaybackBytes = outputRate * 2 * PLAYBACK_BUFFER_SECONDS;
    this.trimPlayback();
  }

  async start(inputRate: number, outputRate: number, send: (payload: ArrayBuffer) => void): Promise<void> {
    const generation = ++this.generation;
    this.setPlaybackRate(outputRate);
    await this.releaseAudio();
    if (generation !== this.generation) return;
    this.sender = send;
    try {
      const output = await audio.createAudioRenderer({
        streamInfo: {
          samplingRate: outputRate,
          channels: audio.AudioChannel.CHANNEL_1,
          sampleFormat: audio.AudioSampleFormat.SAMPLE_FORMAT_S16LE,
          encodingType: audio.AudioEncodingType.ENCODING_TYPE_RAW
        },
        rendererInfo: {
          usage: audio.StreamUsage.STREAM_USAGE_VOICE_COMMUNICATION,
          rendererFlags: 0
        }
      });
      if (generation !== this.generation) { await output.release(); return; }
      this.renderer = output;
      const write = (buffer: ArrayBuffer): audio.AudioDataCallbackResult => this.writePlayback(buffer);
      this.writeListener = write;
      output.on('writeData', write);
      await output.start();
      if (generation !== this.generation) return;

      const input = await audio.createAudioCapturer({
        streamInfo: {
          samplingRate: inputRate,
          channels: audio.AudioChannel.CHANNEL_1,
          sampleFormat: audio.AudioSampleFormat.SAMPLE_FORMAT_S16LE,
          encodingType: audio.AudioEncodingType.ENCODING_TYPE_RAW
        },
        capturerInfo: {
          source: audio.SourceType.SOURCE_TYPE_VOICE_COMMUNICATION,
          capturerFlags: 0
        }
      });
      if (generation !== this.generation) { await input.release(); return; }
      this.capturer = input;
      const read = (buffer: ArrayBuffer): void => {
        if (generation !== this.generation || !this.sender) return;
        const source = new Uint8Array(buffer);
        const audioData = new ArrayBuffer(source.length);
        const captured = new Uint8Array(audioData);
        captured.set(source);
        if (captured.length % 2 !== 0) return;
        if (this.muted) captured.fill(0);
        this.sender(audioData);
      };
      this.readListener = read;
      input.on('readData', read);
      await input.start();
      if (generation !== this.generation) return;
    } catch (_) {
      if (generation === this.generation) await this.stop();
      throw new Error('voice_audio_unavailable');
    }
  }

  setMuted(muted: boolean): void { this.muted = muted; }

  enqueue(payload: ArrayBuffer): void {
    if (payload.byteLength === 0 || payload.byteLength % 2 !== 0) return;
    let frame = new Uint8Array(payload).slice();
    if (frame.length > this.maxPlaybackBytes) frame = frame.slice(frame.length - this.maxPlaybackBytes);
    const overflow = this.playbackBytes + frame.length - this.maxPlaybackBytes;
    if (overflow > 0) this.discardPlaybackBytes(overflow);
    this.playback.push(frame);
    this.playbackBytes += frame.length;
  }

  clear(): void {
    this.playback = [];
    this.playbackOffset = 0;
    this.playbackBytes = 0;
    this.renderer?.flush().catch(() => undefined);
  }

  async stop(): Promise<void> {
    this.generation++;
    await this.releaseAudio();
  }

  private async releaseAudio(): Promise<void> {
    this.sender = undefined;
    this.playback = [];
    this.playbackOffset = 0;
    this.playbackBytes = 0;
    const input = this.capturer;
    const output = this.renderer;
    const read = this.readListener, write = this.writeListener;
    this.capturer = undefined;
    this.renderer = undefined;
    this.readListener = undefined; this.writeListener = undefined;
    if (input && read) input.off('readData', read);
    if (output && write) output.off('writeData', write);
    if (input) {
      try { if (input.state === audio.AudioState.STATE_RUNNING) await input.stop(); } catch (_) { }
      try { await input.release(); } catch (_) { }
    }
    if (output) {
      try { if (output.state === audio.AudioState.STATE_RUNNING) await output.stop(); } catch (_) { }
      try { await output.release(); } catch (_) { }
    }
  }

  private trimPlayback(): void {
    const overflow = this.playbackBytes - this.maxPlaybackBytes;
    if (overflow > 0) this.discardPlaybackBytes(overflow);
  }

  private discardPlaybackBytes(count: number): void {
    let remaining = count;
    while (remaining > 0 && this.playback.length > 0) {
      const head = this.playback[0];
      const available = head.length - this.playbackOffset;
      const drop = Math.min(available, remaining) & ~1;
      if (drop <= 0) break;
      this.playbackBytes -= drop;
      remaining -= drop;
      this.playbackOffset += drop;
      if (this.playbackOffset >= head.length) {
        this.playback.shift();
        this.playbackOffset = 0;
      }
    }
  }

  private writePlayback(buffer: ArrayBuffer): audio.AudioDataCallbackResult {
    const output = new Uint8Array(buffer);
    output.fill(0);
    let written = 0;
    while (written + 1 < output.length && this.playback.length > 0) {
      const head = this.playback[0];
      const available = head.length - this.playbackOffset;
      const count = Math.min(output.length - written, available) & ~1;
      if (count <= 0) break;
      output.set(head.subarray(this.playbackOffset, this.playbackOffset + count), written);
      written += count;
      this.playbackOffset += count;
      this.playbackBytes -= count;
      if (this.playbackOffset >= head.length) {
        this.playback.shift();
        this.playbackOffset = 0;
      }
    }
    return audio.AudioDataCallbackResult.VALID;
  }
}
