package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.MailAccount
import persona.core.contracts.api.MailAddress
import persona.core.contracts.api.MailAttachment
import persona.core.contracts.api.MailInboxItem
import persona.core.contracts.api.MailMessage
import persona.core.contracts.api.MailReference
import persona.core.contracts.api.MailTransport
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File
import persona.core.inbox.*

/**
 * The mail page (with an AI twin's draft, a thread of two and attachments) and writing a reply, in each of the app's
 * languages, Glass and the other styles, light and dark (docs/protocol/i18n.md). Written as PNGs to `build/i18n-shots/` (or
 * `-Dpersona.shots=<dir>`) to look at.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class MailI18nShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1500dp-xhdpi")
    @Test fun simplifiedChinese() = shots("zh", reply = "回复全部", compose = "回复")

    @Config(sdk = [35], qualifiers = "en-w400dp-h1500dp-xhdpi")
    @Test fun english() = shots("en", reply = "Reply all", compose = "Reply")

    @Config(sdk = [35], qualifiers = "ja-w400dp-h1500dp-xhdpi")
    @Test fun japanese() = shots("ja", reply = "全員に返信", compose = "返信")

    private val mail = MailMessage(
        accountId = "a1", uid = 8, subject = "Q4 planning", from = MailAddress("Alex Example", "alex@example.com"),
        to = listOf(MailAddress("Me", "me@example.com"), MailAddress("", "team@example.com")),
        cc = listOf(MailAddress("Sam Example", "sam@example.com")), date = "2026-11-04T10:00:00Z", flagged = true, seen = true,
        text = "Hi,\n\nThe Q4 plan is attached. Can you review it by Friday?\n\nAlex",
        attachments = listOf(MailAttachment("q4-plan.pdf", "application/pdf", 182_400, "1"), MailAttachment("", "", 812, "2")),
        messageId = "<m8@example.com>",
    )

    private class OneMail(private val mail: MailMessage) : MailTransport {
        override val scope = "shots"
        override suspend fun item(itemId: String) = MailInboxItem(itemId, MailReference("a1", uids = listOf(7, 8)))
        override suspend fun message(ref: MailReference, uid: Long) = mail.copy(uid = uid)
        override suspend fun setSeen(ref: MailReference, uid: Long, seen: Boolean) = Unit
        override suspend fun refreshInbox() = Unit
        override suspend fun accounts() = listOf(MailAccount(id = "a1", email = "me@example.com"))
    }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun shots(language: String, reply: String, compose: String) {
        val repository = FakeInboxRepository()
        repository.snapshot.value = InboxSnapshot(records = listOf(InboxRecord(
            InboxItem("mail:a1:8", kind = "mail", title = mail.subject, sender = mail.from.display(),
                summary = "Alex asks for a review of the Q4 plan by Friday.", tier = "action"),
            source = "mail", mail = InboxMailRef("a1", uids = listOf(7, 8)),
            agent = InboxAgent("drafted", draft = "Thanks Alex, I'll review it by Thursday.", basis = "Alex asked for a date."),
        )))
        val transport = OneMail(mail)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var page by mutableStateOf("mail")
        var run by mutableIntStateOf(0)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1500.dp).background(PersonaTheme.palette.background)) {
                        if (page == "mail") MailScreen(repository, transport, MailRoute("mail:a1:8"), navigator)
                        else ComposeMailPage(transport,
                            MailCompose.reply(LocalContext.current.resources, mail, "a1", me = "me@example.com", all = true,
                                zone = java.time.ZoneId.of("Asia/Shanghai")), onDone = {})
                    }
                }
            }
        }
        for (p in listOf("mail", "compose")) for (s in AppStyle.entries) for (d in listOf(false, true)) {
            if (s != AppStyle.Glass && d) continue
            page = p; style = s; dark = d; run++
            rule.waitForIdle()
            rule.mainClock.advanceTimeBy(1_000)
            rule.waitForIdle()
            rule.onNodeWithText(if (p == "mail") reply else compose).assertExists()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            val name = "mail-$p-$language-${s.name.lowercase()}-${if (d) "dark" else "light"}.png"
            File(out(), name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }
}
