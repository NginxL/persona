package persona.feature.chat

import android.content.Context
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.width
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.assertHasNoClickAction
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import coil3.ImageLoader
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.ModeChange
import persona.core.contracts.SessionKey
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.lines.ChatLine

/** A mode-change line is a readable separator, not a control; long wording wraps instead of ellipsizing. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-mdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ModeChangeRenderingTest {
    @get:Rule val rule = createComposeRule()
    private val context get() = ApplicationProvider.getApplicationContext<Context>()
    private val at = "2026-11-18T09:01:00Z"

    private fun set(device: String? = "Phone") = ModeChange(
        kind = "set", from = "default", to = "acceptEdits", applied = "now",
        actorKind = "device", deviceName = device, at = at,
    )

    @androidx.compose.runtime.Composable
    private fun line(change: ModeChange, id: String) {
        ChatLineContent(
            line = ChatLine.ModeChange(id, change), session = SessionKey("machine", "session"),
            loader = ImageLoader.Builder(context).build(), recordedDuration = null,
            onToggle = {}, onFold = {}, folding = emptySet(), foldErrors = emptyMap(), onFullText = {},
        )
    }

    @Test fun shortAndLongLabelsAreFullyAccessibleNoninteractiveAndWrap() {
        val short = ModeChange(kind = "restart_done", to = "bypassPermissions", at = at)
        val long = set(device = "Phone in the design studio upstairs, used by the whole Android release qualification team")
        val shortWords = ModeChangeWords.label(short, context.resources)
        val longWords = ModeChangeWords.label(long, context.resources)

        rule.setContent {
            PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                Column {
                    Box(Modifier.width(280.dp)) { line(short, "mode-short") }
                    Box(Modifier.width(280.dp)) { line(long, "mode-long") }
                }
            }
        }

        val shortNode = rule.onNodeWithContentDescription(shortWords)
        val longNode = rule.onNodeWithContentDescription(longWords)
        shortNode.assertIsDisplayed().assertHasNoClickAction()
        longNode.assertIsDisplayed().assertHasNoClickAction()
        assertNull(shortNode.fetchSemanticsNode().config.getOrNull(SemanticsActions.OnClick))
        assertNull(longNode.fetchSemanticsNode().config.getOrNull(SemanticsActions.OnClick))
        assertEquals(0, rule.onAllNodes(hasClickAction()).fetchSemanticsNodes().size)
        val shortHeight = shortNode.fetchSemanticsNode().size.height
        val longHeight = longNode.fetchSemanticsNode().size.height
        assertTrue("the long label must add lines, not truncate: short=$shortHeight long=$longHeight",
            longHeight > shortHeight)
    }
}
