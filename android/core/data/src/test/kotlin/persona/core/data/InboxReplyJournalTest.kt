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

    @Test fun readsPre14075AndCoreInboxReplyTargetDiscriminators() = runBlocking {
        val root = Files.createTempDirectory("inbox-compat-test").toFile()
        try {
            val scope = HubScope("hub", "gen")
            val dir = root.resolve(scope.path).apply { mkdirs() }
            val file = dir.resolve("replies.json")

            // Persisted by pre-#14075 builds where ReplyTarget lived in persona.feature.inbox.
            file.writeText(
                """{"outbox":[""" +
                    """{"target":{"type":"persona.feature.inbox.ReplyTarget.Lark","feedId":"feed-1"},""" +
                    """"request":{"text":"hi","client_id":"c-1"},"state":"sent"},""" +
                    """{"target":{"type":"persona.feature.inbox.ReplyTarget.Inbox","itemId":"item-1"},""" +
                    """"request":{"text":"yo","client_id":"c-2"},"state":"waiting"}]}""",
            )
            val pre = InboxReplyJournal(dir, scope.path).read(scope.path)
            assertEquals(listOf(ReplyTarget.Lark("feed-1"), ReplyTarget.Inbox("item-1")), pre.outbox.map { it.target })

            // Persisted by #14075 builds before the @SerialName fix was restored.
            file.writeText(
                """{"outbox":[""" +
                    """{"target":{"type":"persona.core.inbox.ReplyTarget.Lark","feedId":"feed-2"},""" +
                    """"request":{"text":"mention persona.core.inbox.ReplyTarget.Lark in body","client_id":"c-3"},"state":"sent"},""" +
                    """{"target":{"type":"persona.core.inbox.ReplyTarget.Inbox","itemId":"item-2"},""" +
                    """"request":{"text":"ok","client_id":"c-4"},"state":"waiting"}]}""",
            )
            val mid = InboxReplyJournal(dir, scope.path).read(scope.path)
            assertEquals(listOf(ReplyTarget.Lark("feed-2"), ReplyTarget.Inbox("item-2")), mid.outbox.map { it.target })
            assertEquals("mention persona.core.inbox.ReplyTarget.Lark in body", mid.outbox[0].request.text)

            // Re-writing persists the stable persona.feature.inbox.ReplyTarget.* serial name.
            InboxReplyJournal(dir, scope.path).update(scope.path) { it }
            val written = file.readText()
            assertTrue(written.contains(""""type":"persona.feature.inbox.ReplyTarget.Lark""""))
            assertTrue(written.contains(""""type":"persona.feature.inbox.ReplyTarget.Inbox""""))
        } finally { root.deleteRecursively() }
    }
}
