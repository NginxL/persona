package persona.feature.chat

import android.content.ClipboardManager
import android.content.Context
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.graphics.compositeOver
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onLast
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.em
import com.mikepenz.markdown.annotator.DefaultAnnotatorSettings
import com.mikepenz.markdown.annotator.buildMarkdownAnnotatedString
import com.mikepenz.markdown.model.State as MarkdownResult
import com.mikepenz.markdown.model.parseMarkdown
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Item
import persona.core.designsystem.*
import java.io.File

/**
 * A reply's code blocks, inline code and quotes as Telegram draws them (#2984): a fence's language and Copy in a bar on
 * top of it, box-free inline code in a quieter ink, and quote cards, a long one folded. In every style, light and
 * dark; PNGs in `build/reply-block-shots/` (`-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1400dp-xhdpi")
class ReplyBlocksShotsTest {
    @get:Rule val rule = createComposeRule()

    private val reply = "先跑`npm run build`后，打开 [`the guide`](https://example.com/guide)，再清掉 `.cache` 目录：\n\n```ts\nexport async function clear(page: string) {\n" +
        "  await caches.delete(`page:\${page}`)\n  return reload(page)\n}\n```\n\n不带语言的也一样：\n\n```\nrm -rf .cache && npm run build\n```\n\n" +
        "> 改了`cache-key`以后，旧缓存要等一天才过期。\n\n" +
        "> 上线前请确认三件事：一是首页的请求已经合并，跑过一遍回归测试；二是价格页换成了新的缓存键，旧键在 CDN 上的副本会在一天内过期；" +
        "三是文档页最后做，它的请求最多，改动的风险最低。如果中途发现首屏时间反而变长，先回滚价格页，再看是不是缓存没命中。\n\n有问题再找我。"

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/reply-block-shots").also { it.mkdirs() }

    @Test fun shotsInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var renderedPalette: PersonaPalette? = null
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val palette = PersonaTheme.palette
                SideEffect { renderedPalette = palette }
                Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l),
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.l)) {
                    MessageContent(Item("a1", role = "assistant", kind = "text", text = reply), user = false)
                }
            }
        }
        // Compose idleness does not wait for the Markdown cache's background parser.
        rule.waitUntil(10_000) { rule.onAllNodesWithText("复制").fetchSemanticsNodes().size == 2 }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            Thread.sleep(200)
            rule.waitForIdle()
            rule.runOnIdle {
                val palette = checkNotNull(renderedPalette)
                val ink = palette.replyCodeInk
                assertTrue("$s dark=$d must use a quieter ink", ink != palette.ink && ink != palette.ink2)
                for (surface in listOf(palette.background, palette.surface, palette.surfaceOpaque, palette.surfaceStrong,
                    palette.sunken, palette.accentSoft)) {
                    assertTrue("$s dark=$d has insufficient code contrast on $surface",
                        ExtensionThemeColors.contrast(ink, surface.compositeOver(palette.background)) >= 4.5)
                }
            }
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "reply-blocks-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    @Test fun copyTakesTheWholeBlockAndALongQuoteFolds() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                CompositionLocalProvider(LocalReplyStreaming provides false) {
                    Column(Modifier.width(400.dp).padding(Tokens.Space.l)) {
                        MessageContent(Item("a1", role = "assistant", kind = "text", text = reply), user = false)
                    }
                }
            }
        }
        rule.waitForIdle()
        // Two code blocks, each with Copy in its bar; the first copies its code, without the fence.
        val copies = rule.onAllNodesWithText("复制")
        rule.waitUntil(10_000) { copies.fetchSemanticsNodes().size == 2 }
        assertEquals(2, copies.fetchSemanticsNodes().size)
        copies[0].performClick()
        val clipboard = ApplicationProvider.getApplicationContext<Context>().getSystemService(ClipboardManager::class.java)
        assertEquals("export async function clear(page: string) {\n  await caches.delete(`page:\${page}`)\n  return reload(page)\n}",
            clipboard.primaryClip?.getItemAt(0)?.text?.toString())
        // The long quote is folded (the folded words open it too); its chevron opens it and folds it again.
        rule.onAllNodesWithContentDescription("展开全部").onLast().performClick()
        rule.onNodeWithContentDescription("收起").assertExists().performClick()
        rule.onAllNodesWithContentDescription("展开全部").onLast().assertExists()
    }

    @Test fun inlineCodeWordsAreTheSpans() {
        assertEquals("npm run build", codeSpanWords("`npm run build`"))
        assertEquals("a `b` c", codeSpanWords("`` a `b` c ``"))
        assertEquals("  ", codeSpanWords("`  `"))
    }

    @Test fun inlineCodeAddsGapsOnlyNextToCjkLettersAndCopiesOnlyTheWords() {
        val code = "`cmd`"
        val copied = mutableListOf<String>()
        val ink = Color.DarkGray
        val source = "漢${code}字、カ${code}な、한${code}글、ㄅ${code}ㄆ、𠀀${code}字、x${code}y、 $code 。"
        val result = annotatedCode(source, ink) { copied += it }
        assertEquals("漢\u2009cmd\u2009字、カ\u2009cmd\u2009な、한\u2009cmd\u2009글、ㄅ\u2009cmd\u2009ㄆ、𠀀\u2009cmd\u2009字、xcmdy、 cmd 。", result.text)
        val spans = result.spanStyles.filter { it.item.fontFamily == FontFamily.Monospace }
        assertEquals(7, spans.size)
        for (span in spans) {
            assertEquals("cmd", result.text.substring(span.start, span.end))
            assertEquals(SpanStyle(fontFamily = FontFamily.Monospace, fontSize = 0.94.em, color = ink), span.item)
        }
        val links = result.getLinkAnnotations(0, result.length)
        assertEquals(7, links.size)
        val copy = links.first().item as LinkAnnotation.Clickable
        copy.linkInteractionListener!!.onClick(copy)
        assertEquals(listOf("cmd"), copied)
        assertEquals("漢cmd字", pickedWords(result.text.substringBefore('、')))
    }

    @Test fun linkedCodeKeepsTheOuterLinkAndSelectionDisablesCopy() {
        val code = "`docs/guide.md`"
        val source = PathLinks.markdown("看${code}吧，另见 [${code}](https://example.com/guide)。", setOf("docs/guide.md"))
        val result = annotatedCode(source, Color.DarkGray) {
            error("Linked code must open its target")
        }
        assertEquals("看\u2009docs/guide.md\u2009吧，另见 docs/guide.md。", result.text)
        val urls = result.getLinkAnnotations(0, result.length).map { it.item as LinkAnnotation.Url }
        assertEquals(listOf("docs/guide.md"), urls.mapNotNull { PathLinks.pathOf(it.url) })
        assertEquals("https://example.com/guide", urls.last().url)
        assertTrue(urls.all { it.styles?.style?.color == Color.Blue })
        assertEquals(listOf(Color.Unspecified, Color.Unspecified),
            result.spanStyles.filter { it.item.fontFamily == FontFamily.Monospace }.map { it.item.color })
        val selected = annotatedCode(code, Color.Unspecified)
        assertEquals("docs/guide.md", selected.text)
        assertTrue(selected.getLinkAnnotations(0, selected.length).isEmpty())
    }

    private fun annotatedCode(source: String, ink: Color, copy: ((String) -> Unit)? = null): AnnotatedString {
        val parsed = parseMarkdown(source) as MarkdownResult.Success
        val settings = DefaultAnnotatorSettings(
            linkTextSpanStyle = TextLinkStyles(SpanStyle(color = Color.Blue)),
            codeSpanStyle = SpanStyle(background = Color.Yellow),
            annotator = copyingCode(parsed.node, ink, copy),
            highlightSpanStyle = SpanStyle(),
        )
        return source.buildMarkdownAnnotatedString(parsed.node.children.first(), TextStyle.Default, settings)
    }
}
