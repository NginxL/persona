package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
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
import persona.core.designsystem.Tokens
import java.io.File

/**
 * A message while "Select" has its words selected where they are (#4210 T5), in every style, light and dark: the reply is
 * highlighted from its first word to its last, code block and list among them; the messages round it are as they are.
 * Robolectric draws neither the handles nor the system's menu (they are windows of their own), so only the highlight shows.
 * Glass again at the largest text size (`select-large-*`).
 * PNGs in `build/message-selection-shots/` (`-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class MessageSelectionShotsTest {
    @get:Rule val rule = createComposeRule()

    private val ask = "把登录页的重试逻辑改掉，再跑一遍测试。"
    private val reply = "改好了，重试现在最多三次，间隔逐次加倍：\n\n- 入口在 `LoginViewModel.retry()`\n- 超过次数后给出看得懂的错误\n\n" +
        "```kotlin\nfun retry(n: Int) = delay(250L shl n)\n```\n\n测试已经跑过，全部通过。"
    private val next = "同样的改法要用到注册页吗？"

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/message-selection-shots").also { it.mkdirs() }

    @Test fun shotsInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var fontScale by mutableStateOf(1f)
        val menu = MessageMenuState().apply { quote = {}; quoteWords = { _, _ -> } }
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale)) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    val actions = remember {
                        ChatRowActions(SessionKey("box", "s1"), { "" }, null, photos = null, pictures = { emptyList() },
                            openImages = { _, _ -> }, menu = menu, retryLineId = null, onRetry = {})
                    }
                    CompositionLocalProvider(LocalChatRowActions provides actions) {
                        Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l),
                            verticalArrangement = Arrangement.spacedBy(Tokens.Space.l)) {
                            MergedUserBubble(listOf(Item("u1", role = "user", kind = "text", text = ask)))
                            MessageContent(Item("a1", role = "assistant", kind = "text", text = reply), user = false)
                            MessageContent(Item("a2", role = "assistant", kind = "text", text = next), user = false)
                        }
                    }
                }
            }
        }
        // Compose idleness does not wait for the Markdown cache's background parser.
        rule.waitUntil(10_000) { rule.onAllNodesWithText("复制").fetchSemanticsNodes().size == 1 }
        fun shoot(name: String, styles: List<AppStyle> = AppStyle.entries) {
            for (s in styles) for (d in listOf(false, true)) {
                style = s; dark = d
                rule.waitForIdle()
                Thread.sleep(200)
                rule.waitForIdle()
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                File(out(), "$name-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                    .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            }
        }
        // At rest, then with the reply's words selected: nothing moves but the highlight.
        shoot("rest")
        rule.runOnIdle { menu.select(MessageTarget("a1", reply, user = false)) }
        rule.waitForIdle()
        rule.waitForIdle()
        assertEquals("a1", menu.selecting?.id)
        shoot("select")
        assertEquals("the selection survives a change of look", "a1", menu.selecting?.id)
        // The largest text size: the words wrap further, and the highlight follows them.
        val height = rule.onNodeWithTag("shot").fetchSemanticsNode().size.height
        fontScale = 2f
        rule.waitForIdle()
        assertTrue("text at the largest size is taller", rule.onNodeWithTag("shot").fetchSemanticsNode().size.height > height)
        shoot("select-large", listOf(AppStyle.Glass))
        assertEquals("the selection survives a change of text size", "a1", menu.selecting?.id)
    }
}
