package persona.android.dev

import android.app.Application
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import persona.android.ShareTargetRow
import persona.core.contracts.SessionKey
import persona.core.designsystem.PersonaTheme

@RunWith(RobolectricTestRunner::class)
@Config(application = Application::class, sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class DiagnosticsShareSheetTest {
    @get:Rule val rule = createComposeRule()
    private val target = SessionKey("machine", "session")

    @Test fun archiveHasAnAccessibleNameAndOpenChatUsesTheChosenTarget() {
        var opened: SessionKey? = null
        mount(busy = false, onOpen = { opened = it })

        rule.onNodeWithContentDescription("Diagnostics archive").assertExists()
        rule.onNodeWithText("Please inspect").assertExists()
        rule.onNodeWithText("Investigate chat").assertExists()
        rule.onNodeWithText("Open chat").assertIsEnabled().performClick()
        assertEquals(target, opened)
    }

    @Test fun busyPreventsOpeningAnotherDraft() {
        mount(busy = true, onOpen = { error("must not open while busy") })
        rule.onNodeWithText("Open chat").assertIsNotEnabled()
    }

    private fun mount(busy: Boolean, onOpen: (SessionKey?) -> Unit) {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                DiagnosticsShareSheet(
                    fileName = "persona-diagnostics-android-20261117-1200.zip",
                    note = "Please inspect", onNote = {},
                    rows = listOf(ShareTargetRow(target, "Investigate chat", "Computer")),
                    selected = target, onSelect = {}, busy = busy, loading = false, paired = true, error = null,
                    onOpen = onOpen, onCancel = {}, onOpenPersona = {},
                )
            }
        }
    }
}
