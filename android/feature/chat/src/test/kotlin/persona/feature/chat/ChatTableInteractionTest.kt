package persona.feature.chat

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.requiredWidth
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeLeft
import androidx.compose.ui.test.swipeRight
import androidx.compose.ui.test.swipeUp
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w360dp-h900dp-mdpi")
class ChatTableInteractionTest {
    @get:Rule val rule = createComposeRule()

    @Test fun horizontalDragScrollsTheTableNotTheMessageAndAnEdgeDragDoesNotMoveItBackward() {
        var quoted = ""
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                val menu = remember { MessageMenuState().apply { quote = { quoted = it.text } } }
                val actions = remember(menu) {
                    ChatRowActions(SessionKey("box", "s1"), { "" }, null, photos = null, pictures = { emptyList() },
                        openImages = { _, _ -> }, menu = menu, retryLineId = null, onRetry = {})
                }
                CompositionLocalProvider(LocalChatRowActions provides actions) {
                    Box(Modifier.testTag("message").width(360.dp).background(PersonaTheme.palette.background)) {
                        MessageContent(Item("wide", role = "assistant", kind = "text", text = wide), user = false)
                    }
                }
            }
        }
        waitForTable()
        val range = horizontalRange()
        assertTrue(range.maxValue() > 0)
        rule.onNodeWithTag("message").performTouchInput { swipeLeft() }
        rule.waitForIdle()
        assertTrue("a sideways drag scrolls the table", range.value() > 0)
        assertEquals("the message does not swipe to quote", "", quoted)

        rule.onAllNodes(horizontal)[0].performSemanticsAction(SemanticsActions.ScrollBy) { val _ = it(-100_000f, 0f) }
        rule.waitForIdle()
        rule.onNodeWithTag("message").performTouchInput { swipeRight(startX = left + 2f) }
        rule.waitForIdle()
        assertEquals("a table at its start cannot move backward", 0f, range.value())
        assertEquals("", quoted)
    }

    @Test fun verticalDragThatStartsOnTheTableScrollsTheChat() {
        lateinit var list: LazyListState
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                list = rememberLazyListState()
                LazyColumn(state = list, modifier = Modifier.testTag("chat").width(360.dp).height(420.dp)) {
                    item("table") {
                        Box(Modifier.testTag("table")) {
                            MessageContent(Item("wide", role = "assistant", kind = "text", text = wide), user = false)
                        }
                    }
                    items(20) { index ->
                        Text("History $index. Previous conversation text that gives the list room to scroll.")
                    }
                }
            }
        }
        waitForTable()
        val range = horizontalRange()
        rule.onNodeWithTag("table").performTouchInput { swipeUp() }
        rule.waitForIdle()
        assertTrue("the vertical drag reaches the chat", list.firstVisibleItemIndex > 0 || list.firstVisibleItemScrollOffset > 0)
        assertEquals("the table itself did not scroll sideways", 0f, range.value())
    }

    @Test fun streamingChunksRetainTheHorizontalScrollPosition() {
        var text by mutableStateOf(streamStart)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.width(360.dp)) { ChatMarkdown(text, id = "stream", selectable = false, streaming = true) }
            }
        }
        waitForTable()
        rule.onAllNodes(horizontal)[0].performSemanticsAction(SemanticsActions.ScrollBy) { val _ = it(120f, 0f) }
        rule.waitForIdle()
        assertEquals(120f, horizontalRange().value())

        text += "\n| Late row | A newly streamed sentence adds another row without replacing the scroll identity. | 新增的一行中文内容。 | Keep |"
        rule.waitUntil(10_000) { rule.onAllNodesWithText("Late row", substring = true).fetchSemanticsNodes().isNotEmpty() }
        rule.waitForIdle()
        assertEquals("a new AST does not reset the table's scroll state", 120f, horizontalRange().value())

        text += "\n| Final row | The final chunk also keeps the reader at the same horizontal position. | 最后一块内容。 | Done |"
        rule.waitUntil(10_000) { rule.onAllNodesWithText("Final row", substring = true).fetchSemanticsNodes().isNotEmpty() }
        assertEquals(120f, horizontalRange().value())
    }

    @Test fun sharePictureKeepsUpstreamFixedColumnsAndNoInteractiveScroll() {
        // At 2x, the interactive minimum overflows 640 dp while upstream's four fixed 160 dp columns fit exactly.
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                val density = LocalDensity.current
                CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale = 2f)) {
                    Column(Modifier.requiredWidth(640.dp)) {
                        Box(Modifier.testTag("normal")) {
                            ChatMarkdown(wide, id = "normal", selectable = false)
                        }
                        Box(Modifier.testTag("picture")) {
                            CompositionLocalProvider(LocalInPicture provides true) {
                                ChatMarkdown(wide, id = "picture", selectable = false)
                            }
                        }
                    }
                }
            }
        }
        rule.waitUntil(10_000) { tableCells("normal").size == 12 && tableCells("picture").size == 12 }
        assertTrue("the interactive table has real horizontal scrolling",
            horizontalRanges("normal").any { it.maxValue() > 0f })
        val pictureRanges = horizontalRanges("picture")
        assertTrue("the share picture has no interactive horizontal scroll range: ${pictureRanges.map { it.maxValue() }}",
            pictureRanges.none { it.maxValue() > 0f })

        val interactiveWidths = tableCells("normal").map { it.boundsInRoot.width }
        assertTrue("interactive columns are content-derived: $interactiveWidths", interactiveWidths.distinct().size > 1)
        val pictureWidths = tableCells("picture").map { it.boundsInRoot.width }
        pictureWidths.forEach { width ->
            assertEquals("share picture keeps the library's equal fixed columns", pictureWidths.first(), width, 1f)
        }
    }

    private fun tableCells(tag: String) = rule.onAllNodes(
        SemanticsMatcher.keyIsDefined(SemanticsProperties.CollectionItemInfo) and hasAnyAncestor(hasTestTag(tag)),
        useUnmergedTree = true,
    ).fetchSemanticsNodes()

    private fun horizontalRanges(tag: String) = rule.onAllNodes(
        horizontal and hasAnyAncestor(hasTestTag(tag)), useUnmergedTree = true,
    ).fetchSemanticsNodes().map { it.config[SemanticsProperties.HorizontalScrollAxisRange] }

    private fun waitForTable() {
        rule.waitUntil(10_000) {
            rule.onAllNodes(horizontal).fetchSemanticsNodes().singleOrNull()?.let { node ->
                node.config.getOrNull(SemanticsProperties.HorizontalScrollAxisRange)?.maxValue()?.let { it > 0f }
            } == true
        }
    }

    private fun horizontalRange() = rule.onAllNodes(horizontal).fetchSemanticsNodes().single()
        .config[SemanticsProperties.HorizontalScrollAxisRange]

    private companion object {
        val horizontal = SemanticsMatcher.keyIsDefined(SemanticsProperties.HorizontalScrollAxisRange)
        val streamStart = """
            | Area | Finding | 中文说明 | Next step |
            | --- | --- | --- | --- |
            | Network retry | The request waits for a connection timeout before trying the next available route. | 网络请求会先等待连接超时，然后再尝试下一条可用路线。 | Retry |
            | Rendering | Explanatory sentences need readable columns instead of becoming narrow strips. | 表格中的完整句子需要合适的列宽。 | Read |
        """.trimIndent()
        val wide = streamStart
    }
}
