package persona.feature.inbox

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.SemanticsNodeInteraction
import androidx.compose.ui.test.click
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.isFocused
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.runBlocking
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.inbox.*

/**
 * #4093: a tap on the blank space of the inbox reply, connector reply and document comment boxes puts the caret in the
 * field (the chat composer's fix, #894), in every style; a box that cannot be edited ignores it.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class InboxComposerTapTest {
    @get:Rule val rule = createComposeRule()

    private var style by mutableStateOf(AppStyle.Glass)
    private var shown by mutableStateOf(true)

    private fun setBox(content: @Composable () -> Unit) {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                Column { if (shown) Box(Modifier.testTag("composer")) { content() } }
            }
        }
    }

    // 20dp from the left edge: inside the frame, outside the text line, in all three styles.
    private fun SemanticsNodeInteraction.tapBlank() = performTouchInput { click(Offset(20.dp.toPx(), centerY)) }

    private fun assertBlankTapFocusesTheField() {
        for (each in AppStyle.entries) {
            // Take the box away first, so the focus leaves with it (under Robolectric a focus whose field is replaced
            // moves straight to the new field), then draw it again in the next style, unfocused.
            shown = false
            rule.waitForIdle()
            style = each
            shown = true
            rule.waitForIdle()
            rule.onNode(hasSetTextAction() and isFocused()).assertDoesNotExist()
            rule.onNodeWithTag("composer").tapBlank()
            rule.waitForIdle()
            rule.onNode(hasSetTextAction() and isFocused()).assertExists("blank tap focuses the field in $each")
        }
    }

    @Test fun replyBoxBlankTapFocusesTheField() {
        setBox {
            ReplyComposerField(ReplyComposerState(loading = false), {}, {}, "回复")
        }
        assertBlankTapFocusesTheField()
    }

    @Test fun replyBoxThatCannotBeEditedIgnoresTheTap() {
        setBox { ReplyComposerField(ReplyComposerState(loading = true), {}, {}, "回复") }
        rule.onNodeWithTag("composer").tapBlank()
        rule.waitForIdle()
        rule.onNode(hasSetTextAction() and isFocused()).assertDoesNotExist()
    }

    @Test fun connectorBoxBlankTapFocusesTheField() {
        setBox {
            ConnectorComposer("", {}, null, {}, ConnectorThreadState(loaded = true), null) {}
        }
        assertBlankTapFocusesTheField()
    }

    @Test fun documentCommentBoxBlankTapFocusesTheField() {
        val model = LarkDocCommentsModel("https://example.feishu.cn/docx/D1", LarkDocCommentsTest.FakeApi())
        runBlocking { model.load() }
        setBox {
            LarkDocCommentComposer(model) {}
        }
        assertBlankTapFocusesTheField()
    }
}
