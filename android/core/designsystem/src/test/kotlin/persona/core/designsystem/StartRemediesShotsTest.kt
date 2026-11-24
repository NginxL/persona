package persona.core.designsystem

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
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
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.time.Instant
import kotlinx.coroutines.CompletableDeferred
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.ShellQuestion

/** Native JVM evidence for the startup question in every style, appearance and supported interface text size. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
class StartRemediesShotsTest {
    @get:Rule val rule = createComposeRule()
    private val now = Instant.parse("2026-11-20T12:00:00Z")
    private val question = ShellQuestion(
        line = "[oh-my-zsh] Would you like to update? [Y/n]",
        choices = listOf("y", "n", "enter"),
        kind = "update",
        default = "y",
        screen = "Last login: Fri Nov 20 11:59:00\n[oh-my-zsh] Would you like to update? [Y/n]",
        pane = "window-1:pane-2",
        keptUntil = "2026-11-20T12:30:00Z",
    )

    private fun matrix(large: Boolean) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, if (large) 2f else 1f)) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Column(
                        Modifier.testTag("shell-shot").width(400.dp).background(PersonaTheme.palette.background)
                            .padding(Tokens.Space.l),
                        verticalArrangement = Arrangement.spacedBy(Tokens.Space.m),
                    ) {
                        ShellQuestionCard(question, answer = { true }, fixedNow = now)
                        ShellQuestionCard(
                            ShellQuestion("Password:", choices = listOf("y", "cancel"), kind = "secret", secret = true),
                            answer = { true }, fixedNow = now,
                        )
                    }
                }
            }
        }
        rule.onNodeWithTag("shell-choice-y").assertExists()
        rule.onNodeWithTag("shell-choice-cancel").assertExists()
        rule.onAllNodes(androidx.compose.ui.test.hasTestTag("shell-choice-y")).let {
            assertEquals("the secret prompt does not offer yes", 1, it.fetchSemanticsNodes().size)
        }
        rule.onNodeWithText("window-1:pane-2", substring = true).assertDoesNotExist()
        rule.onNodeWithText("Last login", substring = true).assertDoesNotExist()
        val out = File(System.getProperty("persona.shots") ?: "build/start-remedy-shots").also { it.mkdirs() }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shell-shot").captureToImage().asAndroidBitmap()
            File(out, "shell-question-${if (large) "large" else "default"}-${s.name.lowercase()}-${if (d) "dark" else "light"}.png")
                .outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    @Test fun questionInEveryStyleAndAppearance() = matrix(large = false)

    @Test fun questionAtLargestTextInEveryStyleAndAppearance() = matrix(large = true)

    @Test fun orderedChoicesShareOneBusyAnswerAndSuccessIsReportedOnce() {
        val answers = mutableListOf<String>()
        val answered = mutableListOf<String>()
        val gate = CompletableDeferred<Boolean>()
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                ShellQuestionCard(question, answer = { choice -> answers += choice; gate.await() }, fixedNow = now, onAnswered = { answered += it })
            }
        }
        rule.onNodeWithTag("shell-choice-y").performClick()
        rule.onNodeWithTag("shell-choice-n").assertIsNotEnabled()
        rule.onNodeWithTag("shell-choice-enter").assertIsNotEnabled()
        assertEquals(listOf("y"), answers)
        assertTrue(answered.isEmpty())
        rule.runOnIdle { gate.complete(true) }
        rule.waitForIdle()
        assertEquals(listOf("y"), answered)
    }

    @Test fun refusalKeepsTheQuestionAndTerminalDetailsStayOptIn() {
        var calls = 0
        var answered = 0
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                ShellQuestionCard(question, answer = {
                    calls++
                    if (calls == 1) throw IllegalStateException("synthetic refusal")
                    false
                }, fixedNow = now, onAnswered = { answered++ })
            }
        }
        rule.onNodeWithTag("shell-choice-n").performClick()
        rule.waitForIdle()
        assertEquals(0, answered)
        rule.onNodeWithText("Not now (n)").assertExists()
        rule.onNodeWithTag("shell-question-details").performClick()
        rule.onNodeWithText("Last login", substring = true).assertExists()
        rule.onNodeWithText("window-1:pane-2", substring = true).assertDoesNotExist()
        rule.onNodeWithTag("shell-choice-n").performClick()
        assertEquals(2, calls)
        assertEquals(0, answered)
    }

    @Test fun expiryAndRestrictedChoicesAreExact() {
        assertEquals(listOf("cancel"), answerableShellChoices(ShellQuestion("Password", listOf("y", "cancel"), secret = true)))
        assertEquals(listOf("n", "cancel"), answerableShellChoices(
            ShellQuestion("Trust this host?", listOf("y", "n", "yes", "cancel"), kind = "ssh_host_key")))
        assertEquals(listOf("y", "n", "enter"), answerableShellChoices(question))
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Column {
                    ShellQuestionCard(question.copy(keptUntil = now.toString()), answer = { true }, fixedNow = now)
                    ShellQuestionCard(question.copy(keptUntil = "not-a-time"), answer = { true }, fixedNow = now)
                }
            }
        }
        rule.onAllNodes(androidx.compose.ui.test.hasTestTag("shell-question")).let {
            assertEquals("only the malformed expiry remains open", 1, it.fetchSemanticsNodes().size)
        }
        assertFalse(question.copy(keptUntil = now.minusSeconds(1).toString()).isOpenAt(now))
        assertTrue(question.copy(keptUntil = now.plusSeconds(1).toString()).isOpenAt(now))
    }
}
