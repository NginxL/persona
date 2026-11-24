package persona.module.voice

import android.content.Context
import android.media.AudioManager
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config

/** Routing a conversation on the oldest Android the app installs on (API 30) and on one with communication devices. */
@RunWith(AndroidJUnit4::class)
class VoiceAudioRouteTest {
    private val context: Context = ApplicationProvider.getApplicationContext()
    private val manager = context.getSystemService(AudioManager::class.java)

    private val listener = object : VoiceAudio.Listener {
        override fun onFrame(pcm: ByteArray, length: Int, level: Float) {}
        override fun onSpeaking(speaking: Boolean) {}
        override fun onMicrophoneLost() {}
    }

    @Config(sdk = [30])
    @Test fun onApi30TheCallGoesToTheLoudspeakerAndEverythingIsPutBack() {
        @Suppress("DEPRECATION")
        assertFalse(manager.isSpeakerphoneOn)
        val audio = AndroidVoiceAudio(context)
        audio.start(16_000, 24_000, listener)
        assertEquals(AudioManager.MODE_IN_COMMUNICATION, manager.mode)
        @Suppress("DEPRECATION")
        assertTrue(manager.isSpeakerphoneOn)
        audio.stop()
        assertEquals(AudioManager.MODE_NORMAL, manager.mode)
        @Suppress("DEPRECATION")
        assertFalse(manager.isSpeakerphoneOn)
    }

    @Config(sdk = [31])
    @Test fun fromApi31TheModeIsPutBackAfterTheCall() {
        val audio = AndroidVoiceAudio(context)
        audio.start(16_000, 24_000, listener)
        assertEquals(AudioManager.MODE_IN_COMMUNICATION, manager.mode)
        audio.stop()
        assertEquals(AudioManager.MODE_NORMAL, manager.mode)
    }
}
