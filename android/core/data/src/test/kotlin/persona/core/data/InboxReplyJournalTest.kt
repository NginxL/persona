package persona.core.data

import persona.core.contracts.HubScope
import persona.core.inbox.ReplyRequest
import persona.core.inbox.ReplyTarget
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import java.nio.file.Files

class InboxReplyJournalTest {
    @Test fun pairingGenerationIsRequiredAndClosedJournalCannotRecreateErasedFiles() = runBlocking {
        val root = Files.createTempDirectory("inbox-scope-test").toFile()
        try {
            val first = HubScope("hub", "first")
            val next = HubScope("hub", "next")
            val directory = root.resolve(first.path)
            val journal = InboxReplyJournal(directory, first.path)
            val target = ReplyTarget.Lark("feed")
            val request = ReplyRequest("reply", "client-id")
            journal.update(first.path) {
                it.copy(drafts = mapOf(target.storageKey() to InboxStoredDraft(request.text, request.clientId)))
                    .enqueue(InboxPendingReply(target, request, "feed", "waiting"))
            }
            assertEquals(request, InboxReplyJournal(directory, first.path).read(first.path).outbox.single().request)
            try { journal.read(next.path); fail("Expected pairing isolation") } catch (_: IllegalArgumentException) { }
            try { InboxReplyJournal(root, "a".repeat(64)); fail("Expected generation requirement") } catch (_: IllegalArgumentException) { }

            // ScopedHubData closes the journal before HubDataStore erases its entire directory.
            journal.close()
            assertTrue(directory.deleteRecursively())
            try { journal.update(first.path) { it }; fail("Expected closed journal") } catch (_: IllegalStateException) { }
            assertFalse(directory.exists())
            val fresh = InboxReplyJournal(root.resolve(next.path), next.path)
            assertEquals(InboxReplyDocument(), fresh.read(next.path))
        } finally { root.deleteRecursively() }
    }
}
