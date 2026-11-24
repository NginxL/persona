// A call's two AudioContexts (audio.ts): the capture context at the device's rate, the player at the voice's output rate.
// A browser lets audio start only from a user's gesture, so both are made and resumed inside the click that starts the
// call, before anything is awaited (docs/lessons/voice.md, "Two AudioContexts"). They live apart from audio.ts so the
// first call of a page can make them in that click while its code (calls.ts, call-stack.ts) is still loading.

export interface VoiceContexts {
  capture: AudioContext;
  player: AudioContext;
}

/** Makes and resumes both contexts: call it inside the click. */
export function openContexts(outputRate: number): VoiceContexts {
  const capture = new AudioContext();
  let player: AudioContext;
  try {
    player = new AudioContext({ sampleRate: outputRate });
  } catch {
    // an older browser that cannot run a context at that rate: buffers carry their own rate, the context resamples
    player = new AudioContext();
  }
  void capture.resume().catch(() => {});
  void player.resume().catch(() => {});
  return { capture, player };
}

/** Closes contexts no call took (the page left, or the call's code would not load). */
export function closeContexts(c: VoiceContexts): void {
  void c.capture.close().catch(() => {});
  void c.player.close().catch(() => {});
}
