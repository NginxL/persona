package persona.module.voice

import persona.core.hub.HubError
import persona.core.hub.HubFailure
import java.io.File
import java.nio.file.Files
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.runTest
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import androidx.test.ext.junit.runners.AndroidJUnit4

/** Setting up a conversation: the model from the hub's configuration, the voice kept with the pairing. */
@OptIn(ExperimentalCoroutinesApi::class)
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class VoicePageTest {
    private val directory: File = Files.createTempDirectory("voice-page").toFile()
    private val prefs = VoicePrefs(File(directory, "voice.json"))
    private val extensions = FakeExtensions()
    private val audios = mutableListOf<FakeAudio>()

    @After fun clean() { directory.deleteRecursively() }

    private fun TestScope.page() = VoicePage(extensions, "voice", prefs, this, voiceText()) { FakeAudio().also { audios += it } }

    @Test fun anOlderHubTalksWithTheExtensionsOwnDefaultModel() = runTest {
        extensions.choice = null
        val page = page()
        page.begin()
        assertEquals(VoiceSetup.Loading, page.state.value.setup)
        advanceUntilIdle()
        assertEquals(VoiceSetup.Ready("deep"), page.state.value.setup)
        assertEquals(listOf<String?>("deep"), extensions.voiceRequests)
        extensions.extension.onOpen()
        assertEquals("""{"type":"start","model":"deep"}""", extensions.socket.texts.single())
        assertEquals("深 · 沉稳", page.state.value.subtitle)
        assertEquals("calm", page.state.value.checkedVoice)
        page.close()
    }

    @Test fun aModelThatCannotBeUsedIsSaidNotSwapped() = runTest {
        extensions.choice = VoiceModelChoice("fast", "读不到扩展模型列表")
        val page = page()
        page.begin()
        advanceUntilIdle()
        assertEquals(VoiceSetup.Unavailable("读不到扩展模型列表"), page.state.value.setup)
        assertTrue("No conversation with another model", extensions.sockets.isEmpty())
        assertNull(page.session.value)
        assertNull(page.state.value.subtitle)

        extensions.choice = VoiceModelChoice("retired", null)
        page.begin()
        advanceUntilIdle()
        assertEquals(VoiceSetup.Unavailable(voiceText().modelMissing), page.state.value.setup)

        extensions.choice = VoiceModelChoice("fast", "未验证")
        page.begin()
        advanceUntilIdle()
        assertEquals(VoiceSetup.Ready("fast"), page.state.value.setup)
        extensions.extension.onOpen()
        assertEquals("""{"type":"start","model":"fast"}""", extensions.socket.texts.single())
        page.close()
    }

    @Test fun aConfigurationThatCannotBeReadIsNotTakenForNone() = runTest {
        extensions.choiceError = HubFailure(HubError.Server, "upstream", 502)
        val page = page()
        page.begin()
        advanceUntilIdle()
        assertEquals(VoiceSetup.Failed("hub 没有给出结果：upstream"), page.state.value.setup)
        assertTrue(extensions.sockets.isEmpty())

        extensions.choiceError = null
        extensions.modelsError = HubFailure(HubError.Timeout)
        page.begin()
        advanceUntilIdle()
        assertEquals(VoiceSetup.Failed("连不上 hub，连上后点重试。"), page.state.value.setup)

        extensions.modelsError = null
        page.begin()
        advanceUntilIdle()
        assertEquals("重试 reads it again", VoiceSetup.Ready("deep"), page.state.value.setup)
        assertNotNull(page.session.value)
        page.close()
    }

    @Test fun thePickedVoiceIsKeptAndANewOneStartsAgain() = runTest {
        val page = page()
        page.begin()
        advanceUntilIdle()
        val first = extensions.socket
        page.pickVoice("calm")
        assertEquals("The voice it already speaks with: no new conversation", 1, extensions.sockets.size)
        assertEquals("calm", prefs.voice("voice"))
        page.pickVoice("soft")
        assertEquals(1000 to "bye", first.closed)
        assertEquals(2, extensions.sockets.size)
        extensions.extension.onOpen()
        assertEquals("""{"type":"start","model":"deep","voice":"soft"}""", extensions.socket.texts.single())
        assertEquals("soft", prefs.voice("voice"))
        assertEquals("深 · 温柔", page.state.value.subtitle)
        assertEquals("Kept on this phone for the next time", "soft", VoicePage(extensions, "voice", prefs, this, voiceText()) { FakeAudio() }.state.value.voice)
        page.close()
    }

    @Test fun aVoiceTheExtensionNoLongerHasIsNotSent() = runTest {
        prefs.setVoice("voice", "retired")
        val page = page()
        page.begin()
        advanceUntilIdle()
        extensions.extension.onOpen()
        assertEquals("""{"type":"start","model":"deep"}""", extensions.socket.texts.single())
        assertEquals("calm", page.state.value.checkedVoice)
        assertEquals("Another model may still have it", "retired", prefs.voice("voice"))
        page.close()
    }

    @Test fun voicesThatCannotBeReadDoNotStopTheConversation() = runTest {
        prefs.setVoice("voice", "soft")
        extensions.voicesError = HubFailure(HubError.Unreachable)
        val page = page()
        page.begin()
        advanceUntilIdle()
        assertEquals("连不上 hub", page.state.value.voicesError)
        extensions.extension.onOpen()
        assertEquals("""{"type":"start","model":"deep","voice":"soft"}""", extensions.socket.texts.single())
        assertEquals("深", page.state.value.subtitle)
        page.close()
    }

    @Test fun startingAgainReadsTheModelConfigurationAgain() = runTest {
        extensions.choice = VoiceModelChoice("fast", null)
        val page = page()
        page.begin()
        advanceUntilIdle()
        val first = extensions.socket
        extensions.choice = VoiceModelChoice("deep", null)
        page.begin()
        assertEquals(1000 to "bye", first.closed)
        advanceUntilIdle()
        extensions.extension.onOpen()
        assertEquals("""{"type":"start","model":"deep"}""", extensions.socket.texts.single())
        page.close()
    }

    @Test fun leavingTheAppEndsTheConversation() = runTest {
        val page = page()
        page.begin()
        page.pause()
        advanceUntilIdle()
        assertEquals("Stopped while setting up", VoiceSetup.Stopped, page.state.value.setup)
        assertTrue(extensions.sockets.isEmpty())

        page.begin()
        advanceUntilIdle()
        extensions.extension.onOpen()
        extensions.extension.onText(Ready)
        assertEquals(VoiceState.Phase.Live, page.session.value!!.state.value.phase)
        page.pause()
        assertEquals(VoiceState.Phase.Ended, page.session.value!!.state.value.phase)
        assertEquals(1, audios.last().stops)
        assertEquals("The setup stays: 重新开始 starts again", VoiceSetup.Ready("deep"), page.state.value.setup)
        page.close()
    }
}
