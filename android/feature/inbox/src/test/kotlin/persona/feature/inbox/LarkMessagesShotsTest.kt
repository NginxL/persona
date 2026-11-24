package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.time.Instant
import java.time.ZoneId
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.ConnectionState
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.UiText
import persona.core.inbox.*

/** Empty, populated, loading and failed Messages pages in every style/theme and at 100%/200% text. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1100dp-xhdpi")
class LarkMessagesShotsTest {
    @get:Rule val rule = createComposeRule()

    private val populated = listOf(
        LarkChatSummary(
            id = "direct",
            name = "Alex Example",
            preview = "I will bring the notes and a concise status update.",
            previewMine = true,
            updatedAt = "2026-01-02T03:04:05Z",
        ),
        LarkChatSummary(
            id = "group",
            name = "Weekend planning with a deliberately long group name",
            isGroup = true,
            unread = 4,
            muted = true,
            preview = "The latest message is intentionally long so the largest text still has to ellipsize cleanly.",
            previewSender = "Sam",
            updatedAt = "2026-01-01T03:04:05Z",
            readPosition = "41".toLong(),
            mentionPosition = 52,
        ),
        LarkChatSummary(
            id = "marked",
            name = "Design review",
            isGroup = true,
            markedUnread = true,
            preview = "A marked chat keeps its unread affordance without inventing a message count.",
            updatedAt = "2025-06-15T12:00:00Z",
        ),
    )

    @Test fun everyStateStyleThemeAndTextSize() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var scale by mutableStateOf(1f)
        var fixture by mutableStateOf("populated")
        var run by mutableIntStateOf(0)
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, scale)) {
                PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                    key(run) {
                        Box(Modifier.testTag("shot").width(400.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                            LarkMessagesContent(
                                state = MutableStateFlow(state(fixture)),
                                connection = ConnectionState.Online,
                                onRefresh = {},
                                onLoadMore = {},
                                avatar = { null },
                                onOpen = {},
                            )
                        }
                    }
                }
            }
        }

        for (state in listOf("empty", "populated", "loading", "error")) {
            for (appearance in AppStyle.entries) for (night in listOf(false, true)) {
                for ((textSize, fontScale) in listOf("default" to 1f, "text-200" to 2f)) {
                    rule.runOnIdle {
                        fixture = state
                        style = appearance
                        dark = night
                        scale = fontScale
                        run++
                    }
                    rule.waitForIdle()
                    when (state) {
                        "empty" -> {
                            rule.onNodeWithText("No chats yet").assertExists()
                            rule.onNodeWithText("Loading Lark chats").assertDoesNotExist()
                        }
                        "populated" -> {
                            rule.onNodeWithText("Alex Example").assertExists()
                            rule.onNodeWithText("Weekend planning with a deliberately long group name", substring = true).assertExists()
                            rule.onNodeWithText("4").assertExists()
                            rule.onNodeWithContentDescription("Notifications muted").assertExists()
                            rule.onAllNodesWithContentDescription("Notifications muted", useUnmergedTree = true).assertCountEquals(1)
                            if (fontScale >= 2f) {
                                assertVisiblePrefix("Weekend planning with a deliberately long group name", characters = 6)
                            }
                        }
                        "loading" -> {
                            rule.onNodeWithText("Loading Lark chats").assertExists()
                            rule.onNodeWithText("No chats yet").assertDoesNotExist()
                        }
                        "error" -> {
                            rule.onNodeWithText("Couldn't load Lark chats: hub unavailable").assertExists()
                            rule.onNodeWithText("Retry").assertExists()
                            rule.onNodeWithText("No chats yet").assertDoesNotExist()
                        }
                    }
                    capture("lark-messages-$state-${appearance.name.lowercase()}-${if (night) "dark" else "light"}-$textSize")
                }
            }
        }
    }

    @Test fun longNameUnreadAndOldDateKeepTheNameReadableAtLargestText() {
        val longName = "Design review with a deliberately long chat name"
        val row = LarkChatSummary(
            id = "long-name",
            name = longName,
            isGroup = true,
            markedUnread = true,
            preview = "An old marked chat keeps its name ahead of its unread tag and full date.",
            updatedAt = "2025-06-15T12:00:00Z",
        )
        val expectedDate = Instant.parse(row.updatedAt).atZone(ZoneId.systemDefault()).toLocalDate().toString()
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var run by mutableIntStateOf(0)
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, 2f)) {
                PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                    key(run) {
                        Box(Modifier.testTag("shot").width(400.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                            LarkMessagesContent(
                                state = MutableStateFlow(LarkMessagesState(rows = listOf(row), loaded = true, loading = false)),
                                connection = ConnectionState.Online,
                                onRefresh = {},
                                onLoadMore = {},
                                avatar = { null },
                                onOpen = {},
                            )
                        }
                    }
                }
            }
        }
        for (appearance in AppStyle.entries) for (night in listOf(false, true)) {
            rule.runOnIdle {
                style = appearance
                dark = night
                run++
            }
            rule.waitForIdle()
            capture("lark-messages-long-name-unread-old-date-${appearance.name.lowercase()}-${if (night) "dark" else "light"}-text-200")
            rule.onNodeWithText("Unread", useUnmergedTree = true).assertExists()
            rule.onNodeWithText(expectedDate, useUnmergedTree = true).assertExists()
            assertVisiblePrefix(longName, characters = 6)
        }
    }

    @Test fun anEmptyFirstPageKeepsContinuationLoadingFailureAndRetryVisible() {
        val state = MutableStateFlow(LarkMessagesState(loaded = true, loading = false, nextCursor = "two"))
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                LarkMessagesContent(
                    state = state,
                    connection = ConnectionState.Online,
                    onRefresh = {},
                    onLoadMore = {},
                    avatar = { null },
                    onOpen = {},
                )
            }
        }
        rule.onNodeWithText("Loading Lark chats").assertExists()
        rule.onNodeWithText("No chats yet").assertDoesNotExist()

        rule.runOnIdle {
            state.value = state.value.copy(moreFailure = UiText.Plain("tail unavailable"))
        }
        rule.onNodeWithText("Couldn't load Lark chats: tail unavailable").assertExists()
        rule.onNodeWithText("Retry").assertExists()
        rule.onNodeWithText("No chats yet").assertDoesNotExist()
    }

    @Test fun refreshCompletionStartsNearEndPaginationWithAnUnchangedWindow() {
        val row = LarkChatSummary(id = "chat", name = "Chat")
        val state = MutableStateFlow(
            LarkMessagesState(rows = listOf(row), nextCursor = "two", loaded = true, loading = true),
        )
        var loads = 0
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                LarkMessagesContent(
                    state = state,
                    connection = ConnectionState.Online,
                    onRefresh = {},
                    onLoadMore = { loads++ },
                    avatar = { null },
                    onOpen = {},
                )
            }
        }
        rule.waitForIdle()
        org.junit.Assert.assertEquals(0, loads)

        rule.runOnIdle { state.value = state.value.copy(loading = false) }
        rule.waitUntil(5_000) { loads == 1 }
    }

    private fun state(name: String): LarkMessagesState = when (name) {
        "empty" -> LarkMessagesState(loaded = true, loading = false)
        "populated" -> LarkMessagesState(rows = populated, loaded = true, loading = false)
        "loading" -> LarkMessagesState(loaded = false, loading = true)
        "error" -> LarkMessagesState(loaded = false, loading = false, failure = UiText.Plain("hub unavailable"))
        else -> error("Unknown fixture: $name")
    }

    /** Semantics retains the full string even when pixels show only an ellipsis; inspect the actual glyph layout instead. */
    private fun assertVisiblePrefix(text: String, characters: Int) {
        val node = rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode()
        val layouts = mutableListOf<TextLayoutResult>()
        val _ = node.config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        val layout = layouts.single()
        val lastRequiredGlyph = layout.getBoundingBox(characters - 1)
        assertTrue(
            "$text must show at least $characters characters, not an ellipsis alone: " +
                "layout=${layout.size}, glyph=$lastRequiredGlyph",
            lastRequiredGlyph.width > 0f && lastRequiredGlyph.left >= 0f &&
                lastRequiredGlyph.right <= layout.size.width && lastRequiredGlyph.top >= 0f &&
                lastRequiredGlyph.bottom <= layout.size.height,
        )
    }

    private fun capture(name: String) {
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) {
            if (bitmap.getPixel(x, y) != background) other++
        }
        assertTrue("$name drew nothing", other > 100)
        val out = File(System.getProperty("persona.shots") ?: "build/lark-messages-shots").also { it.mkdirs() }
        File(out, "$name.png").outputStream().use {
            assertTrue("$name could not be written", bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
        }
    }
}
