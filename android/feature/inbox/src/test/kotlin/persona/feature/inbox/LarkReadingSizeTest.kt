package persona.feature.inbox

import androidx.compose.foundation.layout.Column
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.withStyle
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
import persona.core.designsystem.ReadingContent
import persona.core.inbox.*

/** Forwarded bodies read at the system scale while sender and code-header chrome stops at the interface cap. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-mdpi")
class LarkReadingSizeTest {
    @get:Rule val rule = createComposeRule()

    @Test fun forwardedBodyReadsAcrossTheSheetWhileSenderStaysCapped() {
        val part = LarkPart.MergeForward("History", listOf(LarkForwardedMessage(
            id = "message", senderName = Sender, text = ForwardedBody, parts = listOf(LarkPart.Text(ForwardedBody)))))
        rule.setContent {
            val current = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(current.density, 2f)) {
                PersonaTheme(reduceMotion = true) {
                    LarkForwardedSheet(part, onlineSince = null, onDismiss = {})
                    Column {
                        CompositionLocalProvider(LocalDensity provides Density(current.density, 2f)) {
                            Text(ReferenceForwardedBody, style = PersonaTheme.type.body)
                        }
                        CompositionLocalProvider(LocalDensity provides Density(current.density, InterfaceFontScaleCap)) {
                            Text(ReferenceSender, style = PersonaTheme.type.caption)
                        }
                    }
                }
            }
        }
        rule.waitForIdle()
        assertEquals("forwarded body", textHeight(ReferenceForwardedBody), textHeight(ForwardedBody))
        assertEquals("forwarded sender", textHeight(ReferenceSender), textHeight(Sender))
    }

    @Test fun codeGrowsButItsLanguageAndCopyControlStayCapped() {
        rule.setContent {
            val current = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(current.density, 2f)) {
                PersonaTheme(reduceMotion = true) {
                    Column {
                        ReadingContent { LarkCodeBlock(CodeBody, Language) }
                        CompositionLocalProvider(LocalDensity provides Density(current.density, 2f)) {
                            Text(buildAnnotatedString {
                                withStyle(SpanStyle(fontFamily = FontFamily.Monospace, fontSize = PersonaTheme.type.code.fontSize)) {
                                    append(ReferenceCodeBody)
                                }
                            }, style = PersonaTheme.type.body)
                        }
                        CompositionLocalProvider(LocalDensity provides Density(current.density, InterfaceFontScaleCap)) {
                            Text(ReferenceLanguage, style = PersonaTheme.type.micro)
                            Text(ReferenceCopy, style = PersonaTheme.type.micro)
                        }
                    }
                }
            }
        }
        rule.waitForIdle()
        assertEquals("code body", textHeight(ReferenceCodeBody), textHeight(CodeBody))
        assertEquals("code language", textHeight(ReferenceLanguage), textHeight(Language))
        assertEquals("copy control", textHeight(ReferenceCopy), textHeight(Copy))
    }

    private fun textHeight(text: String): Int = rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode().let { node ->
        val layouts = mutableListOf<TextLayoutResult>()
        val _ = node.config[SemanticsActions.GetTextLayoutResult].action!!.invoke(layouts)
        layouts.single().size.height
    }

    private companion object {
        const val Sender = "Sender"
        const val ReferenceSender = "Reference sender"
        const val ForwardedBody = "Body"
        const val ReferenceForwardedBody = "Words"
        const val CodeBody = "return 1"
        const val ReferenceCodeBody = "return 2"
        const val Language = "kotlin"
        const val ReferenceLanguage = "reference"
        const val Copy = "Copy"
        const val ReferenceCopy = "Reference copy"
    }
}
