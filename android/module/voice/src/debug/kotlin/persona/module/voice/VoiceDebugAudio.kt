package persona.module.voice

/** Debug builds: `debug.voiceAudio=fake` turns on [VoiceDebug.useFakeAudio]. */
internal fun voiceDebugAudio(value: String?) {
    if (value == "fake") VoiceDebug.useFakeAudio()
}
