package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.inbox.InboxItem
import java.io.File
import persona.core.inbox.*

/**
 * Settings › Translation in the inbox drawn on the JVM with Robolectric's native graphics, in every style, light and dark: a mail row
 * with the hub's translation (and one without), the bar above a translated mail, and the grey line under a Feishu
 * message. PNGs go to `build/translation-shots/` (or `-Dpersona.shots=<dir>`), docs/android/android.md "Translation".
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class InboxTranslationShotsTest {
    @get:Rule val rule = createComposeRule()

    private val hub = object : TranslationTransport {
        override suspend fun translate(texts: List<String>, target: String) = texts.map { "【译】$it" }
        override suspend fun translateMail(account: String, uid: Long, mailbox: String, target: String) = MailTranslation("季度回顾", "正文")
    }
    private val prefs = MutableStateFlow(TranslationPrefs(enabled = true, chats = listOf(TranslateChat("f2", "设计评审"))))
    private val translator = Translator(prefs, { hub }, CoroutineScope(SupervisorJob() + Dispatchers.Unconfined), batchDelayMillis = 0)

    private val translatedMail = InboxRecord(
        InboxItem("m504", kind = "mail", title = "Q3 review: numbers and next steps", sender = "Alex Example",
            preview = "Hi team, please find the Q3 numbers attached…"),
        mail = InboxMailRef("acc", "INBOX", listOf(504)),
        translations = mapOf("zh-Hans" to ItemTranslation("第三季度回顾：数据和下一步", "大家好，第三季度的数据见附件……")),
    )
    private val plainMail = InboxRecord(InboxItem("m505", kind = "mail", title = "周会纪要", sender = "示例用户", preview = "本周进展……"),
        mail = InboxMailRef("acc", "INBOX", listOf(505)))
    private val foreign = LarkMessage("1", "u1", "Tokyo Office", parts = listOf(LarkPart.Text("Could we move the review to Thursday afternoon?")))

    @Composable private fun Row(record: InboxRecord) {
        val t = rememberItemTranslation(record)
        MessageItem(record.item.sender.orEmpty(), t?.subject ?: record.item.title, onClick = {}, source = MessageSource.Mail,
            sourceLabel = "邮件", time = "10:24", preview = t?.preview ?: record.item.preview,
            titleIcon = t?.let { PhIcons.Translate }, titleIconLabel = t?.let { "已翻译" })
    }

    private fun out() = File(System.getProperty("persona.shots") ?: "build/translation-shots").also { it.mkdirs() }

    @Test fun drawsTheTranslationsInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalTranslator provides translator) {
                    Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l),
                        verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                        Row(translatedMail)
                        Row(plainMail)
                        MailTranslationBar("zh-Hans", translating = false, translated = true, showingOriginal = false, error = null, onToggle = {})
                        MailTranslationBar("zh-Hans", translating = true, translated = false, showingOriginal = false, error = null, onToggle = {})
                        MailTranslationBar("zh-Hans", translating = false, translated = false, showingOriginal = false, error = "hub 暂时翻译不了，显示的是原文", onToggle = {})
                        Text(foreign.parts.filterIsInstance<LarkPart.Text>().single().text, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink)
                        LarkTranslationLine(foreign, "f2")
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "inbox-translate-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
        rule.onNodeWithText("第三季度回顾：数据和下一步").assertExists()
        rule.onNodeWithText("【译】Could we move the review to Thursday afternoon?").assertExists()
    }

    @Test fun nothingIsTranslatedWhenOffOrOutsideThePickedChats() {
        prefs.value = TranslationPrefs(enabled = true, mail = false, chats = listOf(TranslateChat("f2", "设计评审")))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalTranslator provides translator) {
                    Column {
                        Row(translatedMail)
                        LarkTranslationLine(foreign, "f9") // not a picked chat
                        LarkTranslationLine(foreign.copy(id = "2", mine = true), "f2") // mine
                        LarkTranslationLine(LarkMessage("3", "u2", "示例用户", parts = listOf(LarkPart.Text("好的，周四下午可以"))), "f2") // already Chinese
                        LarkTranslationLine(foreign.copy(id = "4", translatable = false), "f2") // recalled or encrypted
                    }
                }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("Q3 review: numbers and next steps").assertExists()
        assertEquals(0, rule.onAllNodesWithText("【译】", substring = true).fetchSemanticsNodes().size)
    }

    @Test fun theBarSwitchesBetweenTranslationAndOriginal() {
        var original by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                MailTranslationBar("en", translating = false, translated = true, showingOriginal = original, error = null, onToggle = { original = !original })
            }
        }
        rule.onNodeWithText("已翻译成English").assertExists()
        rule.onNodeWithText("显示原文").performClick()
        assertTrue(original)
        rule.onNodeWithText("显示译文").assertExists()
    }
}
