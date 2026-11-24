package persona.feature.inbox

import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.performClick
import androidx.navigation3.runtime.NavKey
import androidx.navigation3.runtime.entryProvider
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.currentCoroutineContext
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.MailAttachment
import persona.core.contracts.api.MailInboxItem
import persona.core.contracts.api.MailMessage
import persona.core.contracts.api.MailReference
import persona.core.contracts.api.ManagerPins
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File
import persona.core.inbox.*

/** A stamped mail item's page can still download its attachments, as the item's manager. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1500dp")
class MailRouteAttachmentsTest {
    @get:Rule val rule = createComposeRule()
    @get:Rule val temporary = TemporaryFolder()

    @Before fun choose() = AssistantManagerChoice.choose(null, null, "assistant")
    @After fun reset() = AssistantManagerChoice.reset()

    private val hubId = "mail:a1:8"
    private val attachmentPath = listOf("mail", "messages", "a1", "8", "attachments", "1")

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    /** One mail with one attachment; each download records the pins it ran under. */
    private class Files(private val directory: File) : MailAttachmentTransport {
        val downloads = mutableListOf<ManagerPins?>()
        override val scope = "pairing"
        override suspend fun item(itemId: String) = MailInboxItem(itemId, MailReference("a1", uids = listOf(8)))
        override suspend fun message(ref: MailReference, uid: Long) = MailMessage("a1", uid = uid, subject = "Plan", seen = true,
            attachments = listOf(MailAttachment("plan.pdf", "application/pdf", 10, "1")))
        override suspend fun setSeen(ref: MailReference, uid: Long, seen: Boolean) = Unit
        override suspend fun refreshInbox() = Unit
        override suspend fun downloadAttachmentFile(ref: MailReference, uid: Long, attachment: MailAttachment,
            progress: (Long, Long?) -> Unit): MailAttachmentFile {
            downloads += currentCoroutineContext()[ManagerPins]
            val saved = File(directory, "plan.pdf").apply { writeBytes(byteArrayOf(1)) }
            return object : MailAttachmentFile { override val file = saved; override fun close() = Unit }
        }
    }

    /** Opens the mail route of item [itemId] stamped [manager] and downloads its attachment; the pins the download ran under. */
    private fun download(itemId: String, manager: String?): ManagerPins? {
        val repository = FakeInboxRepository()
        repository.snapshot.value = InboxSnapshot(records = listOf(InboxRecord(InboxItem(itemId, kind = "mail", title = "Plan"),
            source = "mail", mail = InboxMailRef("a1", uids = listOf(8)), manager = manager)))
        val transport = Files(temporary.newFolder())
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                entryProvider<NavKey> { inboxEntries(navigator, repository, mailTransport = transport) }(MailRoute(itemId)).Content()
            }
        }
        val label = ApplicationProvider.getApplicationContext<android.content.Context>().getString(R.string.inbox_mail_download_open)
        rule.waitUntil(5_000) { rule.onAllNodes(hasContentDescription(label)).fetchSemanticsNodes().isNotEmpty() }
        rule.onNode(hasContentDescription(label)).assertIsEnabled().performClick()
        rule.waitUntil(5_000) { transport.downloads.isNotEmpty() }
        return transport.downloads.single()
    }

    @Test fun anotherManagersMailDownloadsAsThatManager() {
        assertEquals("m-3", download(InboxItemKey.of(hubId, "m-3"), "m-3")?.manager(attachmentPath))
    }

    @Test fun theDefaultsStampedMailDownloadsNamingNoManager() {
        // The default's stamp pins the page, but names no manager on the wire.
        assertEquals("", download(hubId, "assistant")?.manager(attachmentPath))
    }

    @Test fun anUnstampedMailDownloadsWithoutPins() {
        assertNull(download(hubId, null))
    }
}
