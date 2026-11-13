import { describe, it, expect } from '@ohos/hypium';
import { util } from '@kit.ArkTS';
import { NativeRecording } from '../../../main/ets/platform/NativeRecording';
import { nativeTestContext } from './NativeContext';
import { fixtureArgument } from './NativeTransport.test';

export default function nativeRecordingTests(): void {
  describe('NativeRecording', (): void => {
    it('microphone_capture_stops_with_durable_bounded_pcm', 0, async (): Promise<void> => {
      const platform = new NativeRecording(), context = nativeTestContext(), id = util.generateRandomUUID();
      await platform.initialize(context);
      try {
        await platform.ensureMicrophonePermission(context);
        await platform.start(context, id, (): void => {});
        await new Promise<void>((resolve) => setTimeout(resolve, 1200));
        const stopped = await platform.stop();
        expect(stopped.bytes > 0).assertTrue(); expect(stopped.bytes <= 115200000).assertTrue();
        expect(stopped.duration > 0).assertTrue(); expect(stopped.path.endsWith(id + '.pcm')).assertTrue();
      } finally { await platform.stop(); await platform.remove(id); }
    });
    it('offline_mandarin_recognizes_an_owned_long_pcm_fixture', 0, async (): Promise<void> => {
      const id = fixtureArgument('recordingFixtureId');
      if (!/^f0432130-[0-9a-f-]{27}$/.test(id)) throw new Error('invalid_recording_fixture');
      const platform = new NativeRecording(); await platform.initialize(nativeTestContext());
      try {
        const text = await platform.transcribe(id, 'zh-CN');
        expect(text.length > 0).assertTrue(); expect(text.includes('你好') || text.includes('录音')).assertTrue();
      } finally { await platform.cancelTranscription(); await platform.remove(id); }
    });
  });
}
