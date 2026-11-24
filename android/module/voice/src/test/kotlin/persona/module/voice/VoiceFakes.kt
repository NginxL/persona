package persona.module.voice

import persona.core.contracts.api.HubExtension

/** A socket that records what the page sends. */
internal class FakeSocket : VoiceSocket {
    val texts = mutableListOf<String>()
    val frames = mutableListOf<ByteArray>()
    var closed: Pair<Int, String?>? = null

    override fun send(text: String): Boolean { texts += text; return closed == null }
    override fun send(bytes: ByteArray, offset: Int, length: Int): Boolean { frames += bytes.copyOfRange(offset, offset + length); return closed == null }
    override fun close(code: Int, reason: String?) { closed = code to reason }
}

/** A hub with one voice extension; each connect is a new [FakeSocket], its listener kept to play the extension's part. */
internal class FakeExtensions : VoiceExtensions {
    var models = VoiceModelOptions(listOf(VoiceModelOption("fast", "快"), VoiceModelOption("deep", "深")), default = "deep")
    var choice: VoiceModelChoice? = null
    var modelsError: Exception? = null
    var choiceError: Exception? = null
    var voicesError: Exception? = null
    var connectError: Exception? = null
    /** Opens the socket before connect returns (a fast transport). */
    var openAtOnce = false
    val voices = VoiceOptions(listOf(VoiceOption("soft", "温柔", "female", listOf("zh")), VoiceOption("calm", "沉稳", "male", listOf("zh", "en"))), default = "calm")
    val voiceRequests = mutableListOf<String?>()
    val sockets = mutableListOf<Pair<FakeSocket, VoiceSocketListener>>()
    val socket: FakeSocket get() = sockets.last().first
    val extension: VoiceSocketListener get() = sockets.last().second

    override suspend fun voiceExtension() = HubExtension("voice", "语音", "voice", "running")
    override suspend fun voices(extensionId: String, model: String?): VoiceOptions {
        voiceRequests += model
        voicesError?.let { throw it }
        return voices
    }
    override suspend fun models(extensionId: String): VoiceModelOptions = modelsError?.let { throw it } ?: models
    override suspend fun modelChoice(extensionId: String): VoiceModelChoice? = choiceError?.let { throw it } ?: choice
    override fun connect(extensionId: String, listener: VoiceSocketListener): VoiceSocket {
        connectError?.let { throw it }
        val socket = FakeSocket()
        sockets += socket to listener
        if (openAtOnce) listener.onOpen()
        return socket
    }
}

/** Audio without a device: records what the session asks of it and lets a test speak for the microphone. */
internal class FakeAudio : VoiceAudio {
    var opens = true
    var listener: VoiceAudio.Listener? = null
    var rates: Pair<Int, Int>? = null
    val played = mutableListOf<ByteArray>()
    var clears = 0
    var stops = 0

    override fun start(inputRate: Int, outputRate: Int, listener: VoiceAudio.Listener): Boolean {
        rates = inputRate to outputRate
        this.listener = listener
        return opens
    }
    override fun play(pcm: ByteArray) { played += pcm }
    override fun clear() { clears++ }
    override fun stop() { stops++ }
}

internal const val Ready = """{"type":"ready","input_rate":16000,"output_rate":24000}"""

/** The page's words as the app shows them in the test's language (Simplified Chinese unless the test says otherwise). */
internal fun voiceText() = VoiceText(androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>().resources)
