package persona.module.voice

import java.io.File
import java.nio.file.Files
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Test

/** The picked voices live in the pairing's own directory and go with it. */
class VoicePrefsTest {
    private val root: File = Files.createTempDirectory("voice-prefs").toFile()

    @After fun clean() { root.deleteRecursively() }

    @Test fun eachExtensionKeepsItsVoiceInThePairingsDirectory() {
        File(root, "hub-data/hub-1/gen-1").mkdirs()
        val prefs = VoicePrefs.of(root, "hub-1/gen-1")
        assertNull(prefs.voice("voice"))
        prefs.setVoice("voice", "soft")
        prefs.setVoice("radio", "calm")
        prefs.setVoice("voice", "bright")
        assertEquals("bright", prefs.voice("voice"))
        assertEquals("calm", VoicePrefs.of(root, "hub-1/gen-1").voice("radio"))
        assertEquals("Another pairing has its own", null, VoicePrefs.of(root, "hub-1/gen-2").voice("voice"))
        assertFalse("No temporary file left", File(root, "hub-data/hub-1/gen-1/voice.json.tmp").exists())
    }

    @Test fun anErasedPairingIsNeverRecreated() {
        val prefs = VoicePrefs.of(root, "hub-1/gone")
        prefs.setVoice("voice", "soft")
        assertFalse(File(root, "hub-data/hub-1/gone").exists())
        assertNull(prefs.voice("voice"))
    }

    @Test fun aDamagedFileReadsAsNothingPicked() {
        val directory = File(root, "hub-data/hub-1/gen-1").apply { mkdirs() }
        File(directory, "voice.json").writeText("{not json")
        val prefs = VoicePrefs.of(root, "hub-1/gen-1")
        assertNull(prefs.voice("voice"))
        prefs.setVoice("voice", "soft")
        assertEquals("soft", prefs.voice("voice"))
        File(directory, "voice.json").writeText("""{"voice":7,"other":null,"radio":"calm"}""")
        assertEquals("7", prefs.voice("voice"))
        assertNull(prefs.voice("other"))
        assertEquals("calm", prefs.voice("radio"))
    }
}
