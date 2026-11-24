// What only a voice call runs (docs/web/web.md "Voice chat"): the call itself (conversation.ts, with its uplink and
// resampling) and the browser's side of it (audio.ts and the capture worklet's address, the cues, the pace check).
// calls.ts imports this with import() when the page's first call starts, so the first screen does not carry it; the
// first-screen modules import these only as types.
import { getToken } from "../../lib/api";
import { BASE } from "../../lib/base";
import { extRoot } from "../../lib/extension-api";
import { getSession } from "../../lib/store";
import { BrowserVoiceAudio } from "./audio";
import type { VoiceContexts } from "./audio-contexts";
import type { VoiceDeps } from "./conversation";
import { chosenVoice, resolveModel } from "./support";
import { voiceSocketURL } from "./frames";

export { endedByUser, VoiceConversation } from "./conversation";

/** The browser's side of a call: its socket on this page's origin with this page's credential, its audio, the hub.
 *  `contexts`: the ones the click that started the call made while this module was loading, for its first audio. */
export function browserDeps(mid: string, sid: string, extensionID: string, contexts?: VoiceContexts): VoiceDeps {
  let made = contexts;
  return {
    socketURL: () => {
      const token = getToken();
      return token ? voiceSocketURL(location, BASE, extRoot(), extensionID, token) : null;
    },
    openSocket: (url) => new WebSocket(url) as unknown as ReturnType<VoiceDeps["openSocket"]>,
    audio: (outputRate) => {
      // Continue and Retry make their own, inside their click
      const click = made;
      made = undefined;
      return new BrowserVoiceAudio(outputRate, click);
    },
    probe: async () => {
      try {
        // fetch, not api(): a 401 here is the call's to report, not a sign-out of the whole page
        const r = await fetch(`${BASE}/api/v1${extRoot()}/${encodeURIComponent(extensionID)}/info`, { headers: { Authorization: `Bearer ${getToken()}` } });
        return r.status;
      } catch {
        return null;
      }
    },
    resolveModel: () => resolveModel(extensionID),
    voice: () => chosenVoice(extensionID),
    session: () => getSession(mid, sid),
    now: () => Date.now(),
    setTimer: (fn, ms) => setTimeout(fn, ms),
    clearTimer: (id) => clearTimeout(id as ReturnType<typeof setTimeout>),
  };
}
