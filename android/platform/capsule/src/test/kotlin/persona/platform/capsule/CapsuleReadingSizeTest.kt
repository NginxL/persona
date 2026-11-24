package persona.platform.capsule

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.InterfaceFontScaleCap
import persona.core.designsystem.PersonaTheme

/** Capsule messages and drafts use the same reading scale as the conversation they represent. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-mdpi")
class CapsuleReadingSizeTest {
    @get:Rule val rule = createComposeRule()

    @Test fun userMessageKeepsTheChosenReadingScale() {
        var scale by mutableStateOf(1f)
        rule.setContent {
            val current = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(current.density, scale)) {
                PersonaTheme(reduceMotion = true) {
                    Column {
                        CapsuleMessageBubble(CapsuleMessage("user", UserWords, fromUser = true))
                        CompositionLocalProvider(LocalDensity provides Density(current.density, scale)) {
                            Text(ReferenceUserWords, style = PersonaTheme.type.callout)
                        }
                    }
                }
            }
        }
        for (chosenScale in listOf(1f, InterfaceFontScaleCap, 2f)) {
            rule.runOnIdle { scale = chosenScale }
            rule.waitForIdle()
            assertEquals("capsule message at $chosenScale", textHeight(ReferenceUserWords), textHeight(UserWords))
        }
    }

    @Test fun typedDraftKeepsTheChosenReadingScale() {
        var scale by mutableStateOf(1f)
        val entry = CapsuleEntry("group", "Conversation", "", CapsuleState.Done)
        val content = CapsuleContent(entries = listOf(entry), selectedId = entry.id, draftText = TypedWords)
        rule.setContent {
            val current = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(current.density, scale)) {
                PersonaTheme(reduceMotion = true) {
                    Box {
                        CapsuleSurface(content, true, {}, {}, {}, {}, {}, { _, _ -> }, { _, _ -> },
                            { _, _, _ -> true }, false, androidx.compose.runtime.remember { androidx.compose.runtime.mutableStateMapOf() })
                        CompositionLocalProvider(LocalDensity provides Density(current.density, scale)) {
                            BasicTextField(ReferenceTypedWords, {}, Modifier.testTag(ReferenceEditorTag),
                                maxLines = 2, textStyle = PersonaTheme.type.callout)
                        }
                    }
                }
            }
        }
        for (chosenScale in listOf(1f, InterfaceFontScaleCap, 2f)) {
            rule.runOnIdle { scale = chosenScale }
            rule.waitForIdle()
            assertEquals("capsule draft at $chosenScale", tagHeight(ReferenceEditorTag), tagHeight("capsule.input"))
        }
    }

    private fun textHeight(text: String): Int = layout(rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode().let { node ->
        mutableListOf<TextLayoutResult>().also { val _ = node.config[SemanticsActions.GetTextLayoutResult].action!!.invoke(it) }
    })

    private fun tagHeight(tag: String): Int = layout(rule.onNodeWithTag(tag).fetchSemanticsNode().let { node ->
        mutableListOf<TextLayoutResult>().also { val _ = node.config[SemanticsActions.GetTextLayoutResult].action!!.invoke(it) }
    })

    private fun layout(results: List<TextLayoutResult>): Int = results.single().size.height

    private companion object {
        const val UserWords = "Capsule user message"
        const val ReferenceUserWords = "Reference capsule message"
        const val TypedWords = "Typed capsule draft"
        const val ReferenceTypedWords = "Reference capsule draft"
        const val ReferenceEditorTag = "capsule-reference-editor"
    }
}
