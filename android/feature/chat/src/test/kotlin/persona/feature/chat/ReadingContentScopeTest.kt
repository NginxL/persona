package persona.feature.chat

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.unit.Density
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Item
import persona.core.contracts.MessageSource
import persona.core.contracts.SessionKey
import persona.core.designsystem.AppStyle
import persona.core.designsystem.ComposerGrowLines
import persona.core.designsystem.ContentTokens
import persona.core.designsystem.ExpandedComposer
import persona.core.designsystem.InterfaceFontScaleCap
import persona.core.designsystem.JTopBar
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.ReadingContent
import persona.core.designsystem.Tokens

/** The production reply and full-screen reader match independent text at their semantic density. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-mdpi")
class ReadingContentScopeTest {
    @get:Rule val rule = createComposeRule()

    @Test fun replyBodyKeepsGrowingAtTheSystemMaximum() {
        var scale by mutableStateOf(1f)
        rule.setContent {
            val current = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(current.density, scale)) {
                PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                    Column {
                        ReplyMarkdown(ReplyWords, "reply/reading", ownReply = false)
                        CompositionLocalProvider(LocalDensity provides Density(current.density, scale)) {
                            Text(ReferenceReplyWords, style = PersonaTheme.type.body)
                        }
                    }
                }
            }
        }
        rule.waitUntil(10_000) { rule.onAllNodesWithText(ReplyWords).fetchSemanticsNodes().isNotEmpty() }
        for (chosenScale in listOf(1f, InterfaceFontScaleCap, 2f)) {
            rule.runOnIdle { scale = chosenScale }
            rule.waitForIdle()
            assertEquals("reply at $chosenScale", height(ReferenceReplyWords), height(ReplyWords), 0f)
        }
    }

    @Test fun collapsedSourcePreviewStaysInterfaceAtTheSystemMaximum() {
        rule.setContent {
            val current = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(current.density, 2f)) {
                PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                    Column {
                        SourceMessage(Item("source", text = SourcePreview), MessageSource("conversation", "conversation", "Source"))
                        CompositionLocalProvider(LocalDensity provides Density(current.density, InterfaceFontScaleCap)) {
                            Text(ReferenceSourcePreview, style = PersonaTheme.type.callout,
                                maxLines = ContentTokens.sourcePreviewLines, overflow = TextOverflow.Ellipsis)
                        }
                    }
                }
            }
        }
        rule.waitForIdle()
        assertEquals("collapsed source preview", layoutHeight(ReferenceSourcePreview), layoutHeight(SourcePreview))
    }

    @Test fun historicalPlanToolDetailStaysInterfaceAtTheSystemMaximum() {
        val item = Item("plan", kind = "tool_use", toolName = "ExitPlanMode",
            toolInput = buildJsonObject { put("plan", HistoricalPlan) })
        rule.setContent {
            val current = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(current.density, 2f)) {
                PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                    Column {
                        ToolInputDetail(item)
                        CompositionLocalProvider(LocalDensity provides Density(current.density, InterfaceFontScaleCap)) {
                            ChatMarkdown(ReferenceHistoricalPlan, "reference-plan", PersonaTheme.type.callout)
                        }
                    }
                }
            }
        }
        rule.waitForIdle()
        assertEquals("historical plan detail", layoutHeight(ReferenceHistoricalPlan), layoutHeight(HistoricalPlan))
    }

    @Test fun directChatComposerUsesTheChosenScaleForItsTypedText() {
        var scale by mutableStateOf(1f)
        val state = ChatComposerState().apply { value = TextFieldValue(TypedWords) }
        rule.setContent {
            val current = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(current.density, scale)) {
                PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                    ChatComposer(SessionKey("computer", "session"), state, working = false, sending = false, canClear = false,
                        service = null, onSend = {}, onCommand = {}, onNotice = {}, dictate = null)
                    CompositionLocalProvider(LocalDensity provides Density(current.density, scale)) {
                        BasicTextField(ReferenceTypedWords, {}, Modifier.testTag(EditorReferenceTag),
                            maxLines = ComposerGrowLines, textStyle = PersonaTheme.type.body)
                    }
                }
            }
        }
        for (chosenScale in listOf(1f, InterfaceFontScaleCap, 2f)) {
            rule.runOnIdle { scale = chosenScale }
            rule.waitForIdle()
            assertEquals("direct composer at $chosenScale", tagLayoutHeight(EditorReferenceTag), layoutHeight(TypedWords))
        }
    }

    @Test fun expandedChatComposerUsesTheChosenScaleAcrossItsDialog() {
        var scale by mutableStateOf(1f)
        rule.setContent {
            val current = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(current.density, scale)) {
                PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                    ExpandedComposer(TextFieldValue(TypedWords), {}, "Placeholder", onCollapse = {}) {}
                    CompositionLocalProvider(LocalDensity provides Density(current.density, scale)) {
                        BasicTextField(ReferenceTypedWords, {}, Modifier.testTag(EditorReferenceTag),
                            maxLines = ComposerGrowLines, textStyle = PersonaTheme.type.body)
                    }
                }
            }
        }
        var defaultHeight = -1
        for (chosenScale in listOf(1f, InterfaceFontScaleCap, 2f)) {
            rule.runOnIdle { scale = chosenScale }
            rule.waitForIdle()
            val actual = layoutHeight(TypedWords)
            if (chosenScale == 1f) {
                defaultHeight = actual
                assertEquals("expanded composer default", tagLayoutHeight(EditorReferenceTag), actual)
            } else if (chosenScale == InterfaceFontScaleCap) {
                assertEquals("expanded composer keeps legacy cap rendering", defaultHeight, actual)
            } else {
                assertEquals("expanded composer above the cap", tagLayoutHeight(EditorReferenceTag), actual)
            }
        }
    }

    @Test fun readerBodyGrowsAcrossTheDialogWhileItsTitleStaysCapped() {
        var scale by mutableStateOf(1f)
        var readerOpen = ""
        rule.setContent {
            readerOpen = stringResource(R.string.chat_reader_open)
            val current = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(current.density, scale)) {
                PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                    ReadingContent { ReaderEntry(ReaderWords, ReaderTitle, always = true) }
                    CompositionLocalProvider(LocalDensity provides Density(current.density, minOf(scale, InterfaceFontScaleCap))) {
                        JTopBar(ReferenceReaderTitle)
                    }
                    CompositionLocalProvider(LocalDensity provides Density(current.density, scale)) {
                        Text(ReferenceReaderWords, Modifier.padding(Tokens.Space.m), style = PersonaTheme.type.code)
                    }
                }
            }
        }
        rule.onNodeWithText(readerOpen).performClick()
        rule.waitUntil(10_000) { rule.onAllNodesWithText(ReaderWords).fetchSemanticsNodes().isNotEmpty() }
        var defaultBodyHeight = -1f
        var defaultTitleHeight = -1f
        for (chosenScale in listOf(1f, InterfaceFontScaleCap, 2f)) {
            rule.runOnIdle { scale = chosenScale }
            rule.waitForIdle()
            val bodyHeight = height(ReaderWords)
            val titleHeight = height(ReaderTitle)
            if (chosenScale == 1f) {
                defaultBodyHeight = bodyHeight
                defaultTitleHeight = titleHeight
                assertEquals("reader body default", height(ReferenceReaderWords), bodyHeight, 0f)
            } else if (chosenScale == InterfaceFontScaleCap) {
                assertEquals("reader body keeps legacy cap rendering", defaultBodyHeight, bodyHeight, 0f)
                assertEquals("reader title keeps legacy cap rendering", defaultTitleHeight, titleHeight, 0f)
            } else {
                assertEquals("reader body above the cap", height(ReferenceReaderWords), bodyHeight, 0f)
                assertEquals("reader title above the cap", height(ReferenceReaderTitle), titleHeight, 0f)
            }
        }
    }

    private fun height(text: String): Float = rule.onNodeWithText(text).getUnclippedBoundsInRoot().let {
        (it.bottom - it.top).value
    }

    private fun layoutHeight(text: String): Int = rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode().let { node ->
        val layouts = mutableListOf<TextLayoutResult>()
        node.config[SemanticsActions.GetTextLayoutResult].action!!.invoke(layouts)
        layouts.single().size.height
    }

    private fun tagLayoutHeight(tag: String): Int = rule.onNodeWithTag(tag).fetchSemanticsNode().let { node ->
        val layouts = mutableListOf<TextLayoutResult>()
        node.config[SemanticsActions.GetTextLayoutResult].action!!.invoke(layouts)
        layouts.single().size.height
    }

    private companion object {
        const val ReplyWords = "Reply words"
        const val ReferenceReplyWords = "Reference reply"
        const val SourcePreview = "First source line\nSecond source line\nThird source line\nFourth source line\nFifth source line"
        const val ReferenceSourcePreview = "Other first line\nOther second line\nOther third line\nOther fourth line\nOther fifth line"
        const val HistoricalPlan = "Historical plan"
        const val ReferenceHistoricalPlan = "Reference plan"
        const val TypedWords = "First\nSecond\nThird\nFourth"
        const val ReferenceTypedWords = "One\nTwo\nThree\nFour"
        const val EditorReferenceTag = "editor-reference"
        const val ReaderWords = "Reader body"
        const val ReferenceReaderWords = "Reference reader"
        const val ReaderTitle = "Reader title"
        const val ReferenceReaderTitle = "Reference reader title"
    }
}
