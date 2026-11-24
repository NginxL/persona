package persona.module.voice

import android.os.SystemClock
import kotlin.math.PI
import kotlin.math.sin

/**
 * Debug builds only: `--es debug.voiceAudio fake` makes the voice page use [FakeVoiceAudio] instead of the phone's
 * audio, so an emulator started with `-no-audio` (no microphone to open) still shows "Listening" and "Speaking" for captures.
 * Release has neither this nor the fake (docs/android/android.md "Voice chat").
 */
object VoiceDebug {
    fun useFakeAudio() {
        VoiceAudioOverride.factory = { FakeVoiceAudio() }
    }
}

/**
 * Silence going up, nothing played, and a slowly swelling level for the orb: the conversation as it looks with a
 * microphone, without one. Persona counts as speaking while audio keeps arriving, like the real player.
 */
internal class FakeVoiceAudio : VoiceAudio {
    @Volatile private var running = false
    @Volatile private var lastAudioAt = 0L
    private var thread: Thread? = null

    @Synchronized
    override fun start(inputRate: Int, outputRate: Int, listener: VoiceAudio.Listener): Boolean {
        if (running) return false
        running = true
        val frame = ByteArray(inputRate / 50 * 2)
        thread = Thread({
            val began = SystemClock.elapsedRealtime()
            var speaking = false
            while (running) {
                val now = SystemClock.elapsedRealtime()
                val level = (0.32 + 0.26 * sin((now - began) / 1000.0 * 2 * PI / 3.2)).toFloat()
                frame.fill(0)
                listener.onFrame(frame, frame.size, level)
                val playing = now - lastAudioAt < 300
                if (playing != speaking) { speaking = playing; listener.onSpeaking(playing) }
                try { Thread.sleep(20) } catch (_: InterruptedException) { break }
            }
        }, "voice-fake").apply { isDaemon = true; start() }
        return true
    }

    override fun play(pcm: ByteArray) { lastAudioAt = SystemClock.elapsedRealtime() }

    override fun clear() { lastAudioAt = 0 }

    @Synchronized
    override fun stop() {
        running = false
        thread?.interrupt()
    }
}
