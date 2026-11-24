package persona.feature.chat

import android.content.Context
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.io.IOException
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.withTimeoutOrNull
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.SHELL_WAITING
import persona.core.contracts.START_CONTINUE_FEATURE
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.ChatActionResult
import persona.core.contracts.api.ChatActions
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ChatStartingNoteTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources

    @Test fun startingAndFailedStartsAreSaidAboveTheComposer() {
        assertNull(startingNote(res, null))
        assertNull(startingNote(res, Session(machineId = "mac", sessionId = "s")))
        assertEquals(StartingNote("正在启动 Claude，消息会在它就绪后发出", failed = false),
            startingNote(res, Session(machineId = "mac", sessionId = "s", status = "idle", starting = true)))
        assertEquals("正在启动 Codex，消息会在它就绪后发出",
            startingNote(res, Session(machineId = "mac", sessionId = "s", starting = true, agent = "codex"))?.text)
        // A failure wins over a stale starting flag, and the legacy wording remains byte-for-byte unchanged.
        assertEquals(StartingNote("没有启动起来：claude did not become ready", failed = true),
            startingNote(res, Session(machineId = "mac", sessionId = "s", status = "offline", starting = true,
                startError = " claude did not become ready ")))
    }

    @Test fun codedFailureKeepsFullHubTextWithoutLegacyCap() {
        val fullText = "The hub could not start the kept shell. " +
            "Every detail from the diagnosis remains available to the owner. ".repeat(8).trim()
        val note = startingNote(res, Session(machineId = "mac", sessionId = "s", status = "offline",
            startError = "  $fullText  ", startErrorCode = SHELL_WAITING))

        assertEquals(StartingNote(fullText, failed = true, coded = true), note)
        assertEquals(fullText.length, note?.text?.length)
        assertFalse(note?.text.orEmpty().startsWith(res.getString(R.string.chat_starting_failed, "")))
        // A blank code is still the old, uncoded failure, including its prefix.
        assertEquals(StartingNote("没有启动起来：plain failure", failed = true),
            startingNote(res, Session(machineId = "mac", sessionId = "s", startError = "plain failure", startErrorCode = " ")))
    }

    @Test fun confirmedStartAnswerAdoptsClearedSessionThenRetriesAndRefreshes() = runTest {
        val original = waitingSession()
        val returned = original.copy(title = "The kept tab", rev = 42, status = "offline", starting = false)
        val calls = StartCallRecorder(
            answer = { _, choice ->
                assertEquals("y", choice)
                returned
            },
            refreshFailure = IOException("Synthetic refresh outage"),
        )

        // A confirmed answer is durable even when the best-effort refresh fails.
        continueSessionStart(original, "y", calls.actions, calls.sender, calls.sessions)

        assertEquals(1, calls.continueCalls)
        assertEquals(listOf("continue mac/s y", "adopt mac/s", "retry mac/s $SHELL_WAITING", "refresh"), calls.events)
        assertEquals(returned.copy(
            starting = true,
            status = "idle",
            startError = null,
            startErrorCode = null,
            startErrorArgs = null,
        ), calls.adopted.single())
    }

    @Test fun failedStartConfirmationDoesNotAdoptRefreshOrResend() = runTest {
        val calls = StartCallRecorder(answer = { _, _ -> throw IOException("Synthetic answer refusal") })

        val failure = runCatching {
            continueSessionStart(waitingSession(), "y", calls.actions, calls.sender, calls.sessions)
        }.exceptionOrNull()

        assertTrue(failure is IOException)
        assertEquals(1, calls.continueCalls)
        assertEquals(listOf("continue mac/s y"), calls.events)
        assertTrue(calls.adopted.isEmpty())
    }

    @Test fun timedOutStartConfirmationDoesNotAdoptRefreshOrResend() = runTest {
        val calls = StartCallRecorder(answer = { _, _ -> awaitCancellation() })

        val completed = withTimeoutOrNull(1_000) {
            continueSessionStart(waitingSession(), "y", calls.actions, calls.sender, calls.sessions)
        }

        assertNull("The unconfirmed answer times out", completed)
        assertEquals(1, calls.continueCalls)
        assertEquals(listOf("continue mac/s y"), calls.events)
        assertTrue(calls.adopted.isEmpty())
    }
}

/** One recorded continuation, with no transport or repository work before the hub confirms it. */
private class StartCallRecorder(
    private val answer: suspend (Session, String) -> Session = { session, _ -> session },
    private val refreshFailure: Exception? = null,
) {
    val events = mutableListOf<String>()
    val adopted = mutableListOf<Session>()
    var continueCalls = 0

    val actions = object : ChatActions {
        override suspend fun isMuted(session: Session): Boolean = error("Not used")
        override suspend fun execute(session: Session, action: ChatAction): ChatActionResult = error("Not used")
        override suspend fun verify(session: Session, action: ChatAction): ChatActionResult = error("Not used")
        override suspend fun continueStart(session: Session, choice: String): Session {
            continueCalls++
            events += "continue ${session.key.path} $choice"
            return answer(session, choice)
        }
    }
    val sender = object : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) =
            error("Not used")
        override suspend fun retryRefused(session: SessionKey, code: String): Int {
            events += "retry ${session.path} $code"
            return 2
        }
    }
    val sessions = object : SessionsRepository {
        override val machines = MutableStateFlow(emptyList<Machine>())
        override val sessions = MutableStateFlow(emptyList<Session>())
        override suspend fun adopt(session: Session) {
            adopted += session
            events += "adopt ${session.key.path}"
        }
        override suspend fun refresh() {
            events += "refresh"
            refreshFailure?.let { throw it }
        }
    }
}

private val startError = "The update question stopped the start; answer it to reopen the kept shell tab."
private const val questionLine = "[oh-my-zsh] Would you like to update? [Y/n]"

private fun waitingSession(
    code: String? = SHELL_WAITING,
    keptUntil: String = "2999-01-01T00:00:00Z",
) = Session(
    machineId = "mac",
    sessionId = "s",
    status = "offline",
    starting = false,
    startError = startError,
    startErrorCode = code,
    startErrorArgs = mapOf(
        "line" to questionLine,
        "choices" to "y,n,enter",
        "kind" to "update",
        "default" to "y",
        "kept_until" to keptUntil,
    ),
)

/** The real coded notice and terminal question, gated exactly as they are above an Agent chat's composer. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
class ChatStartFailureTest {
    @get:Rule val rule = createComposeRule()

    @Test fun startContinueFeatureAndExactShellWaitingCodeGateQuestion() {
        val calls = StartCallRecorder()
        var session by mutableStateOf(waitingSession(code = "dialog_open"))
        var features by mutableStateOf(setOf(START_CONTINUE_FEATURE))
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                ChatStartFailure(session, calls.actions, calls.sender, calls.sessions, features)
            }
        }

        // Valid arguments under another code are not a start question.
        rule.onNodeWithTag("shell-question").assertDoesNotExist()
        rule.onNodeWithText(startError).assertExists()
        rule.runOnIdle { session = waitingSession() }
        rule.onNodeWithTag("shell-question").assertExists()
        rule.onNodeWithTag("shell-choice-y").assertExists()

        // The same shell_waiting question stays hidden on a hub without start_continue.
        rule.runOnIdle { features = emptySet() }
        rule.onNodeWithTag("shell-question").assertDoesNotExist()
        rule.onNodeWithText(startError).assertExists()
        rule.runOnIdle { features = setOf(START_CONTINUE_FEATURE) }
        rule.onNodeWithTag("shell-question").assertExists()
        assertEquals(0, calls.continueCalls)
        assertTrue(calls.events.isEmpty())
    }

    @Test fun expiredQuestionIsHiddenWithoutHidingCodedFailure() {
        val calls = StartCallRecorder()
        var session by mutableStateOf(waitingSession(keptUntil = "2000-01-01T00:00:00Z"))
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                ChatStartFailure(session, calls.actions, calls.sender, calls.sessions, setOf(START_CONTINUE_FEATURE))
            }
        }

        rule.onNodeWithTag("shell-question").assertDoesNotExist()
        rule.onNodeWithText("Couldn't start").assertExists()
        rule.onNodeWithText(startError).assertExists()
        rule.runOnIdle { session = waitingSession() }
        rule.onNodeWithTag("shell-question").assertExists()
    }

    @Test fun codedNoteAndQuestionInEveryStyleAndAppearance() = captureMatrix(large = false)

    @Test fun codedNoteAndQuestionAt2xTextInEveryStyleAndAppearance() = captureMatrix(large = true)

    private fun captureMatrix(large: Boolean) {
        val calls = StartCallRecorder()
        val session = waitingSession()
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            val density = LocalDensity.current
            // Text scale is local to this scene; no process-global Robolectric state changes between captures.
            CompositionLocalProvider(LocalDensity provides Density(density.density, if (large) 2f else 1f)) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Column(
                        Modifier.testTag("session-start-shot").width(400.dp)
                            .background(PersonaTheme.palette.background).padding(Tokens.Space.m),
                    ) {
                        ChatStartFailure(session, calls.actions, calls.sender, calls.sessions,
                            setOf(START_CONTINUE_FEATURE))
                    }
                }
            }
        }

        val out = File(System.getProperty("persona.shots") ?: "build/chat-starting-note-shots").also { it.mkdirs() }
        for (appStyle in AppStyle.entries) for (isDark in listOf(false, true)) {
            rule.runOnIdle { style = appStyle; dark = isDark }
            rule.waitForIdle()
            rule.onNodeWithText("Couldn't start").assertExists()
            rule.onNodeWithText(startError).assertExists()
            rule.onNodeWithText("Didn't start:", substring = true).assertDoesNotExist()
            rule.onNodeWithTag("shell-question").assertExists()
            rule.onNodeWithTag("shell-choice-y").assertExists()
            rule.onNodeWithTag("shell-choice-n").assertExists()
            val image = rule.onNodeWithTag("session-start-shot").captureToImage().asAndroidBitmap()
            val name = "session-coded-note-question-${if (large) "2x" else "default"}-" +
                "${appStyle.name.lowercase()}-${if (isDark) "dark" else "light"}.png"
            File(out, name).outputStream().use { assertTrue(image.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        }
        assertTrue(calls.events.isEmpty())
    }
}
