package persona.module.voice

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update

/**
 * One voice conversation with the hub's voice extension (docs/extensions/extensions.md "Voice"). Full duplex: once the extension is
 * ready the microphone goes up all the time, silence and muted frames too (the model decides when a turn ends), the
 * extension's voice comes down and plays at once, and a `clear` drops what has not been played. [start] once, [stop]
 * when the page goes away; everything after [stop] is ignored.
 *
 * The socket and the audio call in on their own threads; the state is safe to read from any.
 */
internal class VoiceSession(
    private val extensions: VoiceExtensions,
    private val extensionId: String,
    private val voice: String?,
    private val model: String?,
    private val audio: VoiceAudio,
    private val text: VoiceText,
) {
    private val mutable = MutableStateFlow(VoiceState())
    val state: StateFlow<VoiceState> = mutable.asStateFlow()
    private val heard = MutableStateFlow(0f)

    /** The microphone's loudness 0…1, smoothed; apart from [state] because it changes 50 times a second. */
    val level: StateFlow<Float> = heard.asStateFlow()

    private val lock = Any()
    @Volatile private var socket: VoiceSocket? = null
    private var opened = false
    private var started = false
    private var audible = false
    @Volatile private var stopped = false
    @Volatile private var muted = false
    private var tools = OpenTools()

    private val listener = object : VoiceSocketListener {
        override fun onOpen() {
            synchronized(lock) { opened = true }
            sendStart()
        }

        override fun onText(text: String) {
            if (!stopped) VoiceEvent.parse(text)?.let(::handle)
        }

        override fun onBinary(bytes: ByteArray) {
            if (!stopped && synchronized(lock) { audible }) audio.play(bytes)
        }

        override fun onClosed(code: Int, reason: String) = end(closeMessage(code, reason, live(), text))

        override fun onFailure(error: Throwable, status: Int?) = end(connectFailure(error, status, live(), text))
    }

    private val microphone = object : VoiceAudio.Listener {
        override fun onFrame(pcm: ByteArray, length: Int, level: Float) {
            if (stopped) return
            // Muted still sends: silence, so the extension's turn taking keeps running.
            if (muted) pcm.fill(0, 0, length)
            socket?.send(pcm, 0, length)
            heard.value = smoothLevel(heard.value, if (muted) 0f else level)
        }

        override fun onSpeaking(speaking: Boolean) {
            if (!stopped) mutable.update { if (it.speaking == speaking) it else it.copy(speaking = speaking) }
        }

        override fun onMicrophoneLost() = end(text.microphone)
    }

    fun start() {
        val connected = try {
            extensions.connect(extensionId, listener)
        } catch (error: Exception) {
            // No route or a pairing that just changed: the same as a socket that would not open.
            end(connectFailure(error, null, live = false, text = text))
            return
        }
        val late = synchronized(lock) {
            if (stopped) true else { socket = connected; false }
        }
        if (late) connected.close(1000, "bye") else sendStart()
    }

    /** The start frame goes first, once the socket is open; the socket can open before [start] has it in hand. */
    private fun sendStart() {
        val target = synchronized(lock) {
            val open = socket
            if (stopped || !opened || started || open == null) return
            started = true
            open
        }
        target.send(startFrame(voice, model))
    }

    private fun live() = state.value.phase == VoiceState.Phase.Live

    private fun handle(event: VoiceEvent) {
        when (event) {
            is VoiceEvent.Ready -> begin(event)
            VoiceEvent.Clear -> if (synchronized(lock) { audible }) audio.clear()
            is VoiceEvent.Transcript -> mutable.update { it.copy(lines = it.lines.with(event)) }
            is VoiceEvent.Tool -> {
                tools = tools.with(event, text.working)
                val current = tools.current
                mutable.update { it.copy(tool = current) }
            }
            is VoiceEvent.Error -> {
                val message = event.message ?: text.error
                mutable.update { it.copy(error = message) }
                if (event.fatal) end(message)
            }
        }
    }

    /** The extension is ready: only now do the microphone and the player start. */
    private fun begin(ready: VoiceEvent.Ready) {
        synchronized(lock) {
            if (stopped || audible) return
            audible = true
        }
        val ok = try { audio.start(ready.inputRate, ready.outputRate, microphone) } catch (_: Exception) { false }
        if (!ok) {
            end(text.microphone)
            return
        }
        mutable.update { if (it.phase == VoiceState.Phase.Connecting && !stopped) it.copy(phase = VoiceState.Phase.Live) else it }
    }

    fun setMuted(on: Boolean) {
        if (stopped) return
        muted = on
        mutable.update { it.copy(muted = on) }
    }

    /** Stops the answer being said (the same as talking over it, without having to). */
    fun interrupt() {
        if (stopped) return
        if (synchronized(lock) { audible }) audio.clear()
        socket?.send(InterruptFrame)
    }

    fun stop() {
        val (target, playing) = synchronized(lock) {
            if (stopped) return
            stopped = true
            socket to audible
        }
        target?.close(1000, "bye")
        if (playing) audio.stop()
        heard.value = 0f
        mutable.update { it.copy(phase = VoiceState.Phase.Ended, speaking = false) }
    }

    /** Ends the conversation; [error] says why, unless it had already ended or something said why before. */
    private fun end(error: String?) {
        if (!stopped && error != null) mutable.update { it.copy(error = error) }
        stop()
    }
}
