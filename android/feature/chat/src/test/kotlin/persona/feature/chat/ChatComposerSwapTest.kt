package persona.feature.chat

import android.content.Context
import android.content.Intent
import android.content.pm.ActivityInfo
import android.content.pm.ResolveInfo
import android.speech.RecognizerIntent
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.click
import androidx.compose.ui.test.getBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatComposerService
import persona.core.contracts.api.ComposerDelivery
import persona.core.designsystem.AppStyle
import persona.core.designsystem.HomeChrome
import persona.core.designsystem.LocalHomeChrome
import persona.core.designsystem.NavItem
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.icons.PhIcons
import java.io.File

/**
 * The composer's round button in each state it can be in (#4210 T3, iOS `ShellComposerBar`): send once there is something to
 * send, else stop while the Agent works, nothing else while the box is empty (the mic stays); and on Glass the swap between
 * them is iOS's, in the plain composer an Agent's chat has and in the home one Persona's conversation and New chat have, with
 * the button on its way out taking no tap. Brief, Board and animations off swap at once. The clock is the test's.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class ChatComposerSwapTest {
    @get:Rule val rule = createComposeRule()

    private val key = SessionKey("box", "s1")
    private val state = ChatComposerState()
    private var working by mutableStateOf(false)
    private var sending by mutableStateOf(false)
    private var canInterrupt by mutableStateOf(true)
    private var blocked by mutableStateOf<String?>(null)
    private var sends = 0
    private val interrupts = mutableListOf<SessionKey>()
    private val service = object : ChatComposerService {
        override val deliveries = MutableStateFlow(emptyMap<String, ComposerDelivery>())
        override suspend fun upload(session: SessionKey, name: String, file: File) = name
        override suspend fun interrupt(session: SessionKey) { interrupts += session }
        override suspend fun retryRefused(clientMessageId: String, clearDraft: Boolean): ComposerDelivery = error("not here")
    }

    /** The phone has a voice input screen, so the composer offers its mic (Dictation.kt asks the package manager). */
    private fun withMic() {
        val info = ResolveInfo().apply { activityInfo = ActivityInfo().apply { packageName = "com.example.voice"; name = "Voice" } }
        shadowOf(ApplicationProvider.getApplicationContext<Context>().packageManager)
            .addResolveInfoForIntent(Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH), info)
    }

    private fun mount(style: AppStyle = AppStyle.Glass, reduceMotion: Boolean = true, home: Boolean = false) {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = reduceMotion, style = style) {
                val chrome = if (home) HomeChrome(listOf(NavItem("home", "Home", PhIcons.House)), "home", onSelect = {}, leading = {}) else null
                CompositionLocalProvider(LocalHomeChrome provides chrome) {
                    Column(Modifier.fillMaxSize().background(PersonaTheme.palette.background)) {
                        Spacer(Modifier.weight(1f))
                        ChatComposer(key, state, working, sending, canClear = false, service = service, onSend = { sends++ },
                            onCommand = {}, onNotice = {}, canInterrupt = canInterrupt, blockedLabel = blocked, onBlocked = {})
                    }
                }
            }
        }
    }

    private fun typed(text: String) = change { state.value = TextFieldValue(text, TextRange(text.length)) }
    private fun change(write: () -> Unit) { write(); Snapshot.sendApplyNotifications() }
    private fun shown(description: String) = rule.onAllNodesWithContentDescription(description).fetchSemanticsNodes().isNotEmpty()
    private fun width(description: String): Float = rule.onNodeWithContentDescription(description).getBoundsInRoot().let { (it.right - it.left).value }
    private fun frames(ms: Long) = rule.mainClock.advanceTimeBy(ms)

    // --- Which button shows -----------------------------------------------------------------------------------------------

    @Test fun anEmptyBoxShowsTheMicAndNeitherSendNorStop() {
        withMic(); mount()
        rule.waitForIdle()
        assertTrue(shown("Dictate")); assertFalse(shown("Send")); assertFalse(shown("Stop"))
    }

    @Test fun somethingTypedShowsSendBesideTheMicAndSendWinsOverStop() {
        withMic(); mount()
        typed("Fix the login page"); rule.waitForIdle()
        assertTrue(shown("Dictate")); assertTrue(shown("Send")); assertFalse(shown("Stop"))
        rule.onNodeWithContentDescription("Send").assertIsEnabled()
        change { working = true }; rule.waitForIdle()
        assertTrue("typing while the Agent works still sends (it queues)", shown("Send")); assertFalse(shown("Stop"))
    }

    @Test fun anAgentAtWorkWithAnEmptyBoxShowsStop() {
        withMic(); mount()
        change { working = true }; rule.waitForIdle()
        assertTrue(shown("Stop")); assertFalse(shown("Send"))
        rule.onNodeWithContentDescription("Stop").assertIsEnabled().performClick()
        rule.waitForIdle()
        assertEquals(listOf(key), interrupts)
        // A session that cannot be interrupted from the phone offers no stop.
        change { canInterrupt = false }; rule.waitForIdle()
        assertFalse(shown("Stop")); assertFalse(shown("Send"))
    }

    @Test fun sendingMakesSendALoadingButtonThatTakesNoTap() {
        withMic(); mount()
        typed("hello"); rule.waitForIdle()
        change { sending = true }; rule.waitForIdle()
        rule.onNodeWithContentDescription("Send").assertIsNotEnabled()
    }

    @Test fun withoutAMicTheEmptyBoxKeepsASendThatCannotBePressed() {
        mount()
        rule.waitForIdle()
        assertFalse(shown("Dictate"))
        rule.onNodeWithContentDescription("Send").assertIsNotEnabled()
        typed("hi"); rule.waitForIdle()
        rule.onNodeWithContentDescription("Send").assertIsEnabled().performClick()
        assertEquals(1, sends)
    }

    @Test fun whatMustBeDoneFirstTakesTheButtonsPlace() {
        withMic(); mount()
        change { blocked = "Connect a model first" }; rule.waitForIdle()
        rule.onNodeWithText("Connect a model first").assertExists()
        typed("hello"); rule.waitForIdle()
        assertFalse(shown("Send"))
    }

    // --- The motion in the real composer ------------------------------------------------------------------------------------

    private fun typingGrowsSend(home: Boolean) {
        withMic()
        rule.mainClock.autoAdvance = false
        mount(reduceMotion = false, home = home)
        rule.mainClock.advanceTimeByFrame()
        assertFalse(shown("Send"))
        typed("Fix the login page")
        frames(16)
        val first = width("Send")
        frames(64)
        val later = width("Send")
        assertTrue("send is $first dp in its first frame and $later dp 64 ms on", first < 5f && later > first && later < 45f)
        frames(500)
        assertEquals(48f, width("Send"), 0.01f)
        // Emptying the box takes it away the same way.
        typed("")
        frames(64)
        assertTrue("send is leaving", shown("Send") && width("Send") < 47f)
        frames(500)
        assertFalse(shown("Send"))
    }

    @Test fun typingGrowsSendInAnAgentsComposer() = typingGrowsSend(home = false)
    @Test fun typingGrowsSendInPersonasHomeComposer() = typingGrowsSend(home = true)

    @Test fun stopTurnsIntoSendAsTextIsTypedAndTheStopThatLeavesTakesNoTap() {
        withMic()
        change { working = true }
        rule.mainClock.autoAdvance = false
        mount(reduceMotion = false)
        rule.mainClock.advanceTimeByFrame()
        rule.onNodeWithContentDescription("Stop").assertIsEnabled()
        typed("go on")
        frames(48)
        assertTrue(shown("Stop") && shown("Send"))
        rule.onNodeWithContentDescription("Stop").assertIsNotEnabled()
            .assert(SemanticsMatcher.keyIsDefined(SemanticsProperties.HideFromAccessibility))
        rule.onNodeWithContentDescription("Stop").performTouchInput { click(center) }
        rule.mainClock.advanceTimeBy(100)
        assertEquals("the stop on its way out did not interrupt", emptyList<SessionKey>(), interrupts)
        frames(500)
        assertFalse(shown("Stop"))
        rule.onNodeWithContentDescription("Send").assertIsEnabled()
    }

    @Test fun focusAndAFirstWordArrivingTogetherStillGrowSend() {
        // The composer opens on focus and moves its buttons to the second row; send comes in the same frame (a picked quick
        // message does both). It grows there instead of standing complete.
        withMic()
        rule.mainClock.autoAdvance = false
        mount(reduceMotion = false)
        rule.mainClock.advanceTimeByFrame()
        change { state.focused = true; state.value = TextFieldValue("/continue", TextRange(9)) }
        frames(16)
        val first = width("Send")
        assertTrue("send is $first dp in its first frame", first < 5f)
        frames(500)
        assertEquals(48f, width("Send"), 0.01f)
    }

    @Test fun briefSwapsAtOnceInTheRealComposer() = swapsAtOnce(AppStyle.Brief, reduceMotion = false)
    @Test fun boardSwapsAtOnceInTheRealComposer() = swapsAtOnce(AppStyle.Board, reduceMotion = false)
    @Test fun glassWithAnimationsOffSwapsAtOnceInTheRealComposer() = swapsAtOnce(AppStyle.Glass, reduceMotion = true)

    private fun swapsAtOnce(style: AppStyle, reduceMotion: Boolean) {
        withMic()
        change { working = true }
        rule.mainClock.autoAdvance = false
        mount(style, reduceMotion)
        rule.mainClock.advanceTimeByFrame()
        assertTrue(shown("Stop"))
        typed("go on")
        rule.mainClock.advanceTimeByFrame()
        assertFalse("$style: the stop is gone in the first frame", shown("Stop"))
        assertEquals("$style", 48f, width("Send"), 0.01f)
        rule.onNodeWithContentDescription("Send").assertIsEnabled()
    }
}
