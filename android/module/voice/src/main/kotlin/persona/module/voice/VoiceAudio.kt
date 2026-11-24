package persona.module.voice

import android.content.Context
import android.media.AudioAttributes
import android.media.AudioDeviceInfo
import android.media.AudioFocusRequest
import android.media.AudioFormat
import android.media.AudioManager
import android.media.AudioRecord
import android.media.AudioTrack
import android.media.MediaRecorder
import android.media.audiofx.AcousticEchoCanceler
import android.media.audiofx.AudioEffect
import android.media.audiofx.NoiseSuppressor
import android.os.Build
import android.os.SystemClock
import android.util.Log
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit

/**
 * The phone's side of a voice conversation: the microphone going up and the extension's voice coming down. Behind an
 * interface so the conversation can be tested without audio, and an emulator without a microphone can show the page
 * (debug builds, `debug.voiceAudio fake`).
 */
internal interface VoiceAudio {
    interface Listener {
        /** One 20 ms frame of PCM16 mono: [length] bytes of [pcm] (reused for the next frame) and its loudness 0…1. */
        fun onFrame(pcm: ByteArray, length: Int, level: Float)

        /** Whether the extension's voice is playing (what has arrived is still being played). */
        fun onSpeaking(speaking: Boolean)

        /** The microphone stopped delivering: another app took it, or the recorder died. */
        fun onMicrophoneLost()
    }

    /** Opens the microphone at [inputRate] and the player at [outputRate]; false when the microphone cannot be opened. */
    fun start(inputRate: Int, outputRate: Int, listener: Listener): Boolean

    /** Plays [pcm] (PCM16 mono at the output rate) after what is already queued. */
    fun play(pcm: ByteArray)

    /** Drops what has not been played yet. */
    fun clear()

    /** Releases everything and puts the phone's audio back as it was. Safe to call more than once, or before [start]. */
    fun stop()
}

/** Debug builds can stand in for the phone's audio (`debug.voiceAudio fake`); release never sets it. */
internal object VoiceAudioOverride {
    @Volatile var factory: (() -> VoiceAudio)? = null
}

/** Headsets, in the order a call would use them: wired, Bluetooth, USB, LE audio, hearing aids. */
private val Headsets = listOf(
    AudioDeviceInfo.TYPE_WIRED_HEADSET, AudioDeviceInfo.TYPE_WIRED_HEADPHONES, AudioDeviceInfo.TYPE_BLUETOOTH_SCO,
    AudioDeviceInfo.TYPE_USB_HEADSET, AudioDeviceInfo.TYPE_BLE_HEADSET, AudioDeviceInfo.TYPE_HEARING_AID,
)

/**
 * Which of the communication devices of [types] (AudioDeviceInfo types) a conversation goes to: a headset when one is
 * connected, else the loudspeaker; never the earpiece, a phone held away from the face would be inaudible. null: none.
 */
internal fun preferredRoute(types: List<Int>): Int? =
    types.indexOfFirst { it in Headsets }.takeIf { it >= 0 } ?: types.indexOf(AudioDeviceInfo.TYPE_BUILTIN_SPEAKER).takeIf { it >= 0 }

/**
 * Before API 31 there is no communication device to pick: whether the loudspeaker is on, given the output devices of
 * [types]. Off when [preferredRoute] picks a headset, on otherwise, so the conversation never lands on the earpiece.
 */
internal fun legacySpeakerphone(types: List<Int>): Boolean =
    preferredRoute(types)?.let { types[it] !in Headsets } ?: true

/**
 * The real thing: `VOICE_COMMUNICATION` recording with the system's echo canceller and noise suppressor (on the
 * loudspeaker the model must not hear itself), communication mode routed to a headset or the loudspeaker, audio focus
 * for the call, and a stream player for what the extension says. Everything is put back in [stop].
 */
internal class AndroidVoiceAudio(context: Context) : VoiceAudio {
    private val manager = context.applicationContext.getSystemService(AudioManager::class.java)
    private val lock = Any()
    @Volatile private var closed = false
    private var savedMode: Int? = null
    private var savedSpeakerphone: Boolean? = null
    private var startedSco = false
    private var focus: AudioFocusRequest? = null
    private var effects = emptyList<AudioEffect>()
    private var track: AudioTrack? = null
    private var recorder: AudioRecord? = null
    private var mic: Thread? = null
    private var player: Thread? = null
    private val queue = LinkedBlockingQueue<ByteArray>()
    @Volatile private var lastAudioAt = 0L

    private val attributes = AudioAttributes.Builder()
        .setUsage(AudioAttributes.USAGE_VOICE_COMMUNICATION).setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build()

    override fun start(inputRate: Int, outputRate: Int, listener: VoiceAudio.Listener): Boolean = synchronized(lock) {
        if (closed || recorder != null) return false
        try {
            route()
            val output = startPlayer(outputRate, listener)
            val input = openMicrophone(inputRate) ?: return false
            startMicrophone(input, inputRate, listener)
            track = output
            true
        } catch (error: Exception) {
            // Unsupported rates, a busy device, a policy that refuses capture: the page says the microphone would not open.
            Log.w(Tag, "voice audio did not start", error)
            false
        }
    }

    private fun route() {
        val previous = manager.mode
        manager.mode = AudioManager.MODE_IN_COMMUNICATION
        try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                val devices = manager.availableCommunicationDevices
                preferredRoute(devices.map { it.type })?.let { manager.setCommunicationDevice(devices[it]) }
            } else {
                routeLegacy()
            }
        } catch (error: Exception) {
            // Put the mode back here: stop() only restores what route() finished setting up.
            restoreLegacyRoute()
            manager.mode = previous
            throw error
        }
        savedMode = previous
        focus = AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT).setAudioAttributes(attributes).build()
            .also { manager.requestAudioFocus(it) }
    }

    /** API 30: the loudspeaker for a call without a headset, Bluetooth SCO for a Bluetooth headset. */
    @Suppress("DEPRECATION")
    private fun routeLegacy() {
        val types = manager.getDevices(AudioManager.GET_DEVICES_OUTPUTS).map { it.type }
        savedSpeakerphone = manager.isSpeakerphoneOn
        manager.isSpeakerphoneOn = legacySpeakerphone(types)
        // Without SCO a Bluetooth headset is not used for a call, and the voice would come out of the earpiece.
        if (preferredRoute(types)?.let { types[it] } == AudioDeviceInfo.TYPE_BLUETOOTH_SCO) {
            startedSco = true
            manager.startBluetoothSco()
            manager.isBluetoothScoOn = true
        }
    }

    @Suppress("DEPRECATION")
    private fun restoreLegacyRoute() {
        if (startedSco) {
            startedSco = false
            manager.isBluetoothScoOn = false
            manager.stopBluetoothSco()
        }
        savedSpeakerphone?.let { manager.isSpeakerphoneOn = it }
        savedSpeakerphone = null
    }

    private fun startPlayer(rate: Int, listener: VoiceAudio.Listener): AudioTrack {
        val minimum = AudioTrack.getMinBufferSize(rate, AudioFormat.CHANNEL_OUT_MONO, AudioFormat.ENCODING_PCM_16BIT)
        val output = AudioTrack.Builder().setAudioAttributes(attributes)
            .setAudioFormat(AudioFormat.Builder().setSampleRate(rate).setEncoding(AudioFormat.ENCODING_PCM_16BIT).setChannelMask(AudioFormat.CHANNEL_OUT_MONO).build())
            .setBufferSizeInBytes(maxOf(minimum, rate / 5 * 2)) // about 200 ms
            .setTransferMode(AudioTrack.MODE_STREAM).build()
        output.play()
        player = Thread({
            var speaking = false
            try {
                while (!closed) {
                    val chunk = try { queue.poll(100, TimeUnit.MILLISECONDS) } catch (_: InterruptedException) { break }
                    // Speaking while chunks keep coming, and a moment after the last: writes block at the speed of playback.
                    val playing = chunk != null || SystemClock.elapsedRealtime() - lastAudioAt < SpeakingTailMillis
                    if (playing != speaking) { speaking = playing; listener.onSpeaking(playing) }
                    if (chunk != null) output.write(chunk, 0, chunk.size)
                }
            } finally {
                val _ = runCatching { output.pause(); output.flush(); output.stop() }
                output.release()
                if (speaking) listener.onSpeaking(false)
            }
        }, "voice-play").apply { start() }
        return output
    }

    private fun openMicrophone(rate: Int): AudioRecord? {
        val frame = rate / 50
        val minimum = AudioRecord.getMinBufferSize(rate, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT)
        @Suppress("MissingPermission") // the page asks for RECORD_AUDIO before a conversation starts
        val input = AudioRecord(MediaRecorder.AudioSource.VOICE_COMMUNICATION, rate, AudioFormat.CHANNEL_IN_MONO,
            AudioFormat.ENCODING_PCM_16BIT, maxOf(minimum, frame * 2 * 4))
        if (input.state != AudioRecord.STATE_INITIALIZED) { input.release(); return null }
        effects = listOfNotNull(
            if (AcousticEchoCanceler.isAvailable()) AcousticEchoCanceler.create(input.audioSessionId) else null,
            if (NoiseSuppressor.isAvailable()) NoiseSuppressor.create(input.audioSessionId) else null,
        ).onEach { effect -> val _ = runCatching { effect.enabled = true } }
        input.startRecording()
        if (input.recordingState != AudioRecord.RECORDSTATE_RECORDING) { input.release(); return null }
        recorder = input
        return input
    }

    private fun startMicrophone(input: AudioRecord, rate: Int, listener: VoiceAudio.Listener) {
        mic = Thread({
            val frame = rate / 50 // 20 ms
            val samples = ShortArray(frame)
            val bytes = ByteArray(frame * 2)
            var failures = 0
            try {
                while (!closed) {
                    val n = input.read(samples, 0, frame)
                    if (n < 0) {
                        // A dead recorder never comes back; other errors get a moment before giving up.
                        if (n == AudioRecord.ERROR_DEAD_OBJECT || ++failures > 50) { if (!closed) listener.onMicrophoneLost(); break }
                        Thread.sleep(20)
                        continue
                    }
                    failures = 0
                    if (n == 0) continue
                    for (i in 0 until n) {
                        val v = samples[i].toInt()
                        bytes[2 * i] = (v and 0xff).toByte()
                        bytes[2 * i + 1] = (v shr 8 and 0xff).toByte()
                    }
                    listener.onFrame(bytes, n * 2, pcmLevel(samples, n))
                }
            } catch (_: InterruptedException) {
            } catch (error: Exception) {
                if (!closed) { Log.w(Tag, "microphone stopped", error); listener.onMicrophoneLost() }
            } finally {
                val _ = runCatching { input.stop() }
                input.release()
            }
        }, "voice-mic").apply { start() }
    }

    override fun play(pcm: ByteArray) {
        if (closed) return
        lastAudioAt = SystemClock.elapsedRealtime()
        queue.offer(pcm)
    }

    override fun clear() {
        queue.clear()
        lastAudioAt = 0
        // Pausing cuts a write in progress short; what it had not written is dropped with the queue.
        track?.let { runCatching { it.pause(); it.flush(); it.play() } }
    }

    override fun stop() {
        val (threads, held) = synchronized(lock) {
            if (closed) return
            closed = true
            listOfNotNull(mic, player) to effects
        }
        queue.clear()
        threads.forEach { it.interrupt() }
        held.forEach { val _ = runCatching { it.release() } }
        // The player and the microphone release their own device when their threads end; one never started is ours.
        if (player == null) track?.release()
        if (mic == null) recorder?.release()
        focus?.let { manager.abandonAudioFocusRequest(it) }
        savedMode?.let { mode ->
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) manager.clearCommunicationDevice() else restoreLegacyRoute()
            manager.mode = mode
        }
    }

    private companion object {
        const val Tag = "PersonaVoice"
        const val SpeakingTailMillis = 300L
    }
}
