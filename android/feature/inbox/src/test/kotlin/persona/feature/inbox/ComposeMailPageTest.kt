package persona.feature.inbox

import androidx.activity.ComponentActivity
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.MailAccount
import persona.core.contracts.api.MailInboxItem
import persona.core.contracts.api.MailMessage
import persona.core.contracts.api.MailReference
import persona.core.contracts.api.MailTransport
import persona.core.contracts.api.SendMailRequest
import persona.core.designsystem.PersonaTheme
import persona.core.inbox.*

/** Leaving the compose page while a send is in flight must neither close it nor cancel the send (#5706). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en")
class ComposeMailPageTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private class SlowSend : MailTransport {
        override val scope = "test"
        val release = CompletableDeferred<Unit>()
        var started = false
        var cancelled = false
        var finished = false
        override suspend fun item(itemId: String) = MailInboxItem(itemId)
        override suspend fun message(ref: MailReference, uid: Long) = MailMessage(accountId = "a1", uid = uid)
        override suspend fun setSeen(ref: MailReference, uid: Long, seen: Boolean) = Unit
        override suspend fun refreshInbox() = Unit
        override suspend fun accounts() = listOf(MailAccount(id = "a1", email = "me@example.com"))
        override suspend fun sendMail(request: SendMailRequest) {
            started = true
            try { release.await() } catch (e: CancellationException) { cancelled = true; throw e }
            finished = true
        }
    }

    private val draft = MailDraft(accountId = "a1", to = "alex@example.com", subject = "Re: Q4 planning",
        body = "Looks good.", inReplyTo = "<m8@example.com>")

    @Test fun leavingWhileSendingWaitsForTheSend() {
        val transport = SlowSend()
        var done = 0
        var leave by mutableStateOf(false)
        var open by mutableStateOf(true)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                // Like MailScreen: onDone takes the page out of composition, which would cancel its scope.
                if (open) ComposeMailPage(transport, draft, onDone = { done++; open = false }, leaveRequest = leave,
                    onLeaveHandled = { leave = false })
            }
        }
        rule.onNodeWithText("Send").performClick()
        rule.waitForIdle()
        assertTrue(transport.started)

        // The dialog's close (onDismiss) and the system back both ask to leave mid-send.
        leave = true
        rule.waitForIdle()
        rule.runOnUiThread { rule.activity.onBackPressedDispatcher.onBackPressed() }
        rule.waitForIdle()
        assertEquals("the page closed mid-send", 0, done)
        assertFalse("the send was cancelled", transport.cancelled)

        transport.release.complete(Unit)
        rule.waitForIdle()
        assertTrue(transport.finished)
        assertFalse(transport.cancelled)
        assertEquals(1, done)
    }

    /** A flag or read-state reply that lands after the mail page moved to another message leaves that message alone. */
    @Test fun lateFlagUpdateSkipsAnotherMessage() {
        val shown = MailMessage(accountId = "a1", uid = 8)
        assertEquals(true, shown.updatedIf(8) { it.copy(flagged = true) }?.flagged)
        assertEquals(shown, shown.updatedIf(7) { it.copy(flagged = true) })
        assertEquals(null, (null as MailMessage?).updatedIf(8) { it.copy(seen = true) })
    }
}
