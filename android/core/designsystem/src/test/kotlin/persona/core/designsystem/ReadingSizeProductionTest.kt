package persona.core.designsystem

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
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

/** Real message and composer components match independent text at their semantic density. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-mdpi")
class ReadingSizeProductionTest {
    @get:Rule val rule = createComposeRule()

    @Test fun userMessageAndTypedComposerGrowWhilePlaceholderStopsAtTheCap() {
        var scale by mutableStateOf(1f)
        rule.setContent {
            val current = LocalDensity.current
            val interfaceScale = minOf(scale, InterfaceFontScaleCap)
            CompositionLocalProvider(LocalDensity provides Density(current.density, scale)) {
                PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                    Column {
                        UserBubble(UserWords)
                        ComposerTextField(TypedWords, {}, "Typed placeholder")
                        ComposerTextField("", {}, Placeholder)
                        // These controls never use ReadingContent: their explicitly supplied density is the expectation.
                        CompositionLocalProvider(LocalDensity provides Density(current.density, scale)) {
                            Column {
                                Text(ReferenceUserWords, style = PersonaTheme.type.body)
                                BasicTextField(ReferenceTypedWords, {}, Modifier.fillMaxWidth().testTag(TypedReferenceTag),
                                    maxLines = ComposerGrowLines, textStyle = PersonaTheme.type.body)
                            }
                        }
                        CompositionLocalProvider(LocalDensity provides Density(current.density, interfaceScale)) {
                            Text(ReferencePlaceholder, style = PersonaTheme.type.body)
                        }
                    }
                }
            }
        }

        for (chosenScale in listOf(1f, InterfaceFontScaleCap, 2f)) {
            rule.runOnIdle { scale = chosenScale }
            rule.waitForIdle()
            assertEquals("user message at $chosenScale", layoutHeight(ReferenceUserWords), layoutHeight(UserWords))
            assertEquals("typed composer at $chosenScale", tagLayoutHeight(TypedReferenceTag), layoutHeight(TypedWords))
            assertEquals("placeholder at $chosenScale", layoutHeight(ReferencePlaceholder), layoutHeight(Placeholder))
        }
    }

    private fun layoutHeight(text: String): Int = layout(rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode().let { node ->
        mutableListOf<TextLayoutResult>().also { layouts ->
            val _ = node.config[SemanticsActions.GetTextLayoutResult].action!!.invoke(layouts)
        }
    })

    private fun tagLayoutHeight(tag: String): Int = layout(rule.onNodeWithTag(tag).fetchSemanticsNode().let { node ->
        mutableListOf<TextLayoutResult>().also { layouts ->
            val _ = node.config[SemanticsActions.GetTextLayoutResult].action!!.invoke(layouts)
        }
    })

    private fun layout(results: List<TextLayoutResult>): Int = results.single().size.height

    private companion object {
        const val UserWords = "User words"
        const val ReferenceUserWords = "Reference user"
        const val TypedWords = "First\nSecond\nThird\nFourth"
        const val ReferenceTypedWords = "One\nTwo\nThree\nFour"
        const val TypedReferenceTag = "typed-reference"
        const val Placeholder = "Interface placeholder"
        const val ReferencePlaceholder = "Reference placeholder"
    }
}
