package persona.core.moduleapi

/**
 * What may own the phone's audio, highest first: a call, a voice conversation, a meeting recording, playback (the order
 * #13431 gives Apple's `AudioSessionBroker`). A use outranks every later one.
 */
enum class AudioUse {
    Call, Conversation, Recording, Playback;

    fun outranks(other: AudioUse): Boolean = ordinal < other.ordinal
}

/**
 * The shell's audio broker, which modules ask before they open the microphone or start playing (#13430). Declared: the
 * Android shell does not serve one yet, and the voice module still takes Android's audio focus itself.
 */
interface AudioFocus {
    /**
     * Asks for the audio for [use] on behalf of module [owner]; null while a use that outranks it holds the audio. [onLost]
     * runs when such a use takes it later.
     */
    fun request(owner: String, use: AudioUse, onLost: () -> Unit): AudioGrant?
}

/** The audio, held until [release]. */
fun interface AudioGrant {
    fun release()
}
