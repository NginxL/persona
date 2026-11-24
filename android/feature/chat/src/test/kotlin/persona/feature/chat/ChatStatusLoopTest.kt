package persona.feature.chat

import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.text.TextLayoutResult
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.LoopInfo
import persona.core.contracts.Session
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.StatusLineState

// The status sheet is the only place that shows what a session's loop does: its prompt is shown in full, however long (#2708).
// Lines are measured with real fonts (NATIVE); a tall screen keeps the loop's row composed in the sheet's lazy list.
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "w400dp-h2400dp")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ChatStatusLoopTest {
    @get:Rule val rule = createComposeRule()

    @Test fun aLongLoopPromptIsShownInFull() {
        val prompt = "Every half hour, check the CI of the open pull requests, read the logs of any job that failed, " +
            "fix what is clearly ours with a new commit, leave a note on the pull request for anything that is not, " +
            "and when everything is green, post a short summary of what changed in the team chat with the links."
        val session = Session(machineId = "mac", sessionId = "s1", title = "Watch CI", status = "running",
            loop = LoopInfo(intervalSeconds = 1800, prompt = prompt))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                val resources = LocalResources.current
                val scope = rememberCoroutineScope()
                val recovery = remember { ChatStatusRecovery(resources, null, scope) {} }
                ChatStatusSheet(StatusLineState(), session, emptyList(), emptySet(), onAction = null, onDismiss = {},
                    replacement = null, onOpenReplacement = null, onConnectionDetails = null, terminal = null,
                    recordedDuration = null, notice = null, recovery = recovery, machine = null, draftSaveError = null,
                    readOnly = false, onOpenSession = null)
            }
        }
        val layouts = mutableListOf<TextLayoutResult>()
        val _ = rule.onNodeWithText(prompt, useUnmergedTree = true).fetchSemanticsNode()
            .config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        val layout = layouts.single()
        assertFalse("the prompt is cut after ${layout.lineCount} lines", layout.hasVisualOverflow)
        assertTrue("the prompt fits in ${layout.lineCount} lines: too short to test the cut", layout.lineCount > 3)
    }
}
