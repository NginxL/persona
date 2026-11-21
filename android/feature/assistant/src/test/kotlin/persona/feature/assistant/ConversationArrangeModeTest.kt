package persona.feature.assistant

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Session
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalHomeDrawer
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1200dp-xhdpi")
class ConversationArrangeModeTest {
    @get:Rule val rule = createComposeRule()

    private val chats = listOf(
        Session(machineId = "hub", sessionId = "a", title = "甲", updatedAt = "2026-11-21T10:00:00Z"),
        Session(machineId = "hub", sessionId = "b", title = "乙", updatedAt = "2026-11-21T09:00:00Z"),
    )

    private fun drawer(controller: ConversationArranger?, start: Boolean = false, writes: MutableList<List<String>> = mutableListOf(), dark: () -> Boolean = { false }, style: () -> AppStyle = { AppStyle.Board }, fontScale: () -> Float = { 1f }) {
        val arranger = controller ?: ConversationArranger(prepare = { {} }, write = { writes += it; it })
        rule.setContent {
            val density = LocalDensity.current
            val configuration = Configuration(LocalConfiguration.current).apply {
                this.fontScale = fontScale()
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark()) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale()), LocalConfiguration provides configuration) {
                PersonaTheme(darkTheme = dark(), reduceMotion = true, style = style()) {
                    CompositionLocalProvider(LocalHomeDrawer provides true) {
                        val scope = rememberCoroutineScope()
                        if (start) LaunchedEffect(arranger) { arranger.start(listOf("a", "b", "unknown")) }
                        Box(Modifier.testTag("conversation-arrange-shot").width(380.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                            AssistantDrawer("", {}, ConversationGroups(main = null, others = chats, recent = chats),
                                active = null, hasMain = false, onEnd = null, onMain = {}, onOpen = {}, onInfo = {},
                                onNew = {}, onSettings = {}, arranger = arranger, arrangeScope = scope)
                        }
                    }
                }
            }
        }
    }

    @Test fun handlesAreAbsentByDefault() {
        drawer(controller = null)
        rule.onNodeWithTag("conversation-arrange-handle-a").assertDoesNotExist()
        rule.onNodeWithTag("conversation-arrange-done").assertDoesNotExist()
    }

    @Test fun arrangeMovePreservesUnknownIdsAndDoneHidesHandles() {
        val writes = mutableListOf<List<String>>()
        val controller = ConversationArranger(prepare = { {} }, write = { writes += it; it })
        drawer(controller = controller, start = true, writes = writes)
        rule.onNodeWithTag("conversation-arrange-handle-a").assertExists()
        rule.onNodeWithTag("conversation-arrange-up-a").assertExists()
        rule.onNodeWithTag("conversation-arrange-down-a").performClick()
        rule.waitForIdle()
        assertEquals(listOf(listOf("b", "a", "unknown")), writes)
        rule.onNodeWithTag("conversation-arrange-done").performClick()
        rule.onNodeWithTag("conversation-arrange-handle-a").assertDoesNotExist()
    }

    @Test fun arrangeModeLightAndDarkInEveryStyle() {
        var dark by mutableStateOf(false)
        var style by mutableStateOf(AppStyle.Board)
        var fontScale by mutableFloatStateOf(1f)
        drawer(controller = ConversationArranger(prepare = { {} }, write = { it }), start = true, dark = { dark }, style = { style }, fontScale = { fontScale })
        rule.onNodeWithTag("conversation-arrange-handle-a").assertExists()

        val standard = mutableMapOf<Pair<AppStyle, Boolean>, Bitmap>()
        for (s in AppStyle.entries) for (d in listOf(false, true)) for (scale in listOf(1f, 2f)) {
            style = s
            dark = d
            fontScale = scale
            Snapshot.sendApplyNotifications()
            rule.waitForIdle()
            rule.onNodeWithTag("conversation-arrange-handle-a").assertExists()
            val bitmap = rule.onNodeWithTag("conversation-arrange-shot").captureToImage().asAndroidBitmap()
            if (scale == 1f) {
                if (d) assertFalse("dark mode must change the rendered pixels", standard.getValue(s to false).sameAs(bitmap))
                standard[s to d] = bitmap
            } else assertFalse("large text must change the rendered pixels", standard.getValue(s to d).sameAs(bitmap))
            val suffix = if (scale > 1f) "-large" else ""
            historyShot(bitmap, "android-conversation-arrange-${s.name.lowercase()}-${if (d) "dark" else "light"}$suffix")
        }
    }
}
