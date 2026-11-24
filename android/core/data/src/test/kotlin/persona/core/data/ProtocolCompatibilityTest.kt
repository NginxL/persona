package persona.core.data

import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.inbox.ReplyTarget

/** Golden UUIDs from the reply-receipt algorithm at baseline 39a706a338. */
class ProtocolCompatibilityTest {
    @Test fun receiptIdsDoNotChangeWhenTheProductNameChanges() {
        val id = "00000000-0000-4000-8000-000000000001"
        assertEquals("98a0a4fa-bda2-8166-b476-7da1d22651b7", inboxReplyReceiptId(ReplyTarget.Inbox("item"), id))
        assertEquals("f21c4c8b-6929-8c3f-af98-12e95c4453d8", inboxReplyReceiptId(ReplyTarget.Lark("chat"), id))
        assertEquals("bdab814e-669a-8b4b-9490-0e66b9de4da1", inboxReplyReceiptId(ReplyTarget.Lark("thread:root"), id))
    }
}
