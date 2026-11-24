package persona.feature.inbox

import android.content.Context
import androidx.compose.runtime.Composable
import androidx.compose.ui.test.junit4.StateRestorationTester
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeDown
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.MailInboxItem
import persona.core.contracts.api.MailMessage
import persona.core.contracts.api.MailReference
import persona.core.contracts.api.MailReplyTargetChanged
import persona.core.contracts.api.MailTransport
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import persona.core.inbox.*

/** Which message a mail page opens, switches to and acknowledges. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp")
class MailScreenSelectionTest {
    @get:Rule val rule = createComposeRule()

    private val context: Context
        get() = ApplicationProvider.getApplicationContext()

    private class RecordingTransport : MailTransport {
        override val scope = "test"
        val messageCalls = mutableListOf<Long>()
        val seenCalls = mutableListOf<Pair<Long, Boolean>>()
        var messageGate: CompletableDeferred<Unit>? = null

        override suspend fun item(itemId: String) = MailInboxItem(
            itemId,
            MailReference(accountId = "account", uids = listOf(OLDER_UID, NEWEST_UID)),
        )

        override suspend fun message(ref: MailReference, uid: Long): MailMessage {
            messageCalls += uid
            messageGate?.await()
            return MailMessage(
                accountId = ref.accountId,
                mailbox = ref.mailbox,
                uid = uid,
                subject = "Message $uid",
                text = "Body $uid",
                seen = seenCalls.any { it.first == uid && it.second },
            )
        }

        override suspend fun setSeen(ref: MailReference, uid: Long, seen: Boolean) {
            seenCalls += uid to seen
        }

        override suspend fun refreshInbox() = Unit
    }

    /** A shared composer whose send reports that the reply now targets a newer message. */
    private class ConflictRepository(base: FakeInboxRepository, scope: CoroutineScope) : InboxRepository by base {
        override val replyComposers = MutableStateFlow<ReplyComposers?>(ReplyComposers(this, scope))
        override suspend fun readDraft(target: ReplyTarget) = ReplyDraft("Reply")
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest): Unit = throw MailReplyTargetChanged()
    }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun mailRepository() = FakeInboxRepository().apply {
        snapshot.value = InboxSnapshot(records = listOf(InboxRecord(
            InboxItem(ITEM_ID, kind = "mail", title = "Mail item"),
            source = "mail",
            mail = InboxMailRef("account", uids = listOf(OLDER_UID, NEWEST_UID)),
        )))
    }

    private fun mailContent(
        transport: RecordingTransport,
        routeJson: String,
        repository: InboxRepository = mailRepository(),
    ): @Composable () -> Unit {
        // Decode too: this is how a back-stack route reaches the page after restoration.
        val route = JsonCodec.decodeFromString(MailRoute.serializer(), routeJson)
        return {
            PersonaTheme(reduceMotion = true) {
                MailScreen(repository, transport, route, navigator)
            }
        }
    }

    private fun awaitMessages(transport: RecordingTransport, count: Int) {
        rule.waitUntil(5_000) { transport.messageCalls.size >= count }
        rule.waitForIdle()
    }

    @Test fun freshSelectionUsesOnlyAListedUidAndOtherwiseNewest() {
        val uids = listOf(OLDER_UID, NEWEST_UID)
        assertEquals(OLDER_UID, mailUidToLoad(OLDER_UID, uids))
        assertEquals(NEWEST_UID, mailUidToLoad(4, uids))
        assertEquals(NEWEST_UID, mailUidToLoad(null, uids))
        assertEquals(NEWEST_UID, mailUidToLoad(NEWEST_UID, uids))
    }

    @Test fun aFreshPageLoadsAndMarksOnlyTheRouteUid() {
        val transport = RecordingTransport()
        rule.setContent(mailContent(transport, """{"itemId":"$ITEM_ID","uid":$OLDER_UID}"""))

        awaitMessages(transport, 1)

        assertEquals(listOf(OLDER_UID), transport.messageCalls)
        assertEquals(listOf(OLDER_UID to true), transport.seenCalls)
    }

    @Test fun aRouteUidTheItemNoLongerListsFallsBackToNewest() {
        val transport = RecordingTransport()
        rule.setContent(mailContent(transport, """{"itemId":"$ITEM_ID","uid":4}"""))

        awaitMessages(transport, 1)

        assertEquals(listOf(NEWEST_UID), transport.messageCalls)
        assertEquals(listOf(NEWEST_UID to true), transport.seenCalls)
    }

    @Test fun aStaleRouteUidKeepsTheLoadedMessageDuringRefresh() {
        val transport = RecordingTransport()
        rule.setContent(mailContent(transport, """{"itemId":"$ITEM_ID","uid":4}"""))
        awaitMessages(transport, 1)
        rule.onNodeWithText("Body $NEWEST_UID").assertExists()

        transport.messageGate = CompletableDeferred()
        rule.onRoot().performTouchInput { swipeDown(startY = centerY, endY = bottom - 20, durationMillis = 500) }
        rule.waitUntil(5_000) { transport.messageCalls.size == 2 }
        rule.waitForIdle()

        rule.onNodeWithText("Body $NEWEST_UID").assertExists()
        transport.messageGate?.complete(Unit)
        rule.waitForIdle()
        assertEquals(listOf(NEWEST_UID, NEWEST_UID), transport.messageCalls)
    }

    @Test fun aReplyTargetConflictResetsTheRouteSelectionToNewest() {
        val transport = RecordingTransport()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)
        try {
            val repository = ConflictRepository(mailRepository(), scope)
            rule.setContent(mailContent(transport, """{"itemId":"$ITEM_ID","uid":$OLDER_UID}""", repository))
            awaitMessages(transport, 1)
            val composer = requireNotNull(repository.replyComposers.value).get(ReplyTarget.Inbox(ITEM_ID))
            rule.waitUntil(5_000) { !composer.state.value.loading }

            composer.send()
            rule.waitForIdle()
            assertEquals("Reply state: ${composer.state.value}", true, composer.state.value.targetChanged)
            awaitMessages(transport, 2)

            assertEquals(listOf(OLDER_UID, NEWEST_UID), transport.messageCalls)
            assertEquals(listOf(OLDER_UID to true, NEWEST_UID to true), transport.seenCalls)
        } finally {
            scope.cancel()
        }
    }

    @Test fun aDirectOpenStillLoadsAndMarksNewest() {
        val transport = RecordingTransport()
        rule.setContent(mailContent(transport, """{"itemId":"$ITEM_ID"}"""))

        awaitMessages(transport, 1)

        assertEquals(listOf(NEWEST_UID), transport.messageCalls)
        assertEquals(listOf(NEWEST_UID to true), transport.seenCalls)
    }

    @Test fun theRouteUidSurvivesSavedStateRestoration() {
        val transport = RecordingTransport()
        val restoration = StateRestorationTester(rule)
        restoration.setContent(mailContent(transport, """{"itemId":"$ITEM_ID","uid":$OLDER_UID}"""))
        awaitMessages(transport, 1)

        restoration.emulateSavedInstanceStateRestore()
        awaitMessages(transport, 2)

        assertEquals(listOf(OLDER_UID, OLDER_UID), transport.messageCalls)
        assertEquals(listOf(OLDER_UID to true), transport.seenCalls)
    }

    @Test fun aUsersMessageChoiceWinsOverTheRouteAndSurvivesRestoration() {
        val transport = RecordingTransport()
        val restoration = StateRestorationTester(rule)
        restoration.setContent(mailContent(transport, """{"itemId":"$ITEM_ID","uid":$OLDER_UID}"""))
        awaitMessages(transport, 1)

        rule.onNodeWithText(context.getString(R.string.inbox_mail_newest)).performClick()
        rule.waitForIdle()
        assertEquals(listOf(OLDER_UID, NEWEST_UID), transport.messageCalls)

        restoration.emulateSavedInstanceStateRestore()
        awaitMessages(transport, 3)

        assertEquals(listOf(OLDER_UID, NEWEST_UID, NEWEST_UID), transport.messageCalls)
        assertEquals(listOf(OLDER_UID to true, NEWEST_UID to true), transport.seenCalls)
    }

    private companion object {
        const val ITEM_ID = "mail-item"
        const val OLDER_UID = 3L
        const val NEWEST_UID = 5L
    }
}
