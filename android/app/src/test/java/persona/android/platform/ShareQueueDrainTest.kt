package persona.android.platform

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import persona.core.contracts.JsonCodec
import java.io.File

class ShareQueueDrainTest {
    @get:Rule val folder = TemporaryFolder()

    private fun queued(id: String): File = File(folder.root, "shares/$id/entry.json").also {
        writePlatformFile(it, JsonCodec.encodeToString(QueuedShare.serializer(), QueuedShare(id, "title", "text", queued = true)))
    }

    // #4818: an unselected group's main resolver cancels with "Pairing changed"; that share waits, the rest still go.
    @Test fun cancelledMainResolverKeepsEntryAndTriesTheRest() = runTest {
        val entries = listOf(queued("a"), queued("b"))
        val sent = mutableListOf<String>()
        val remaining = drainQueuedShares(entries, active = {}) { file ->
            deliverQueuedShare(file, connected = true, untitled = "untitled", prepareMain = { _ -> null },
                upload = { _, _, _, _ -> error("No attachments") },
                main = { _, id, via ->
                    assertEquals(persona.core.contracts.api.MessageVia.Share, via)
                    if (id == "a") throw CancellationException("Pairing changed") else sent += id
                },
                agent = { _, _, _, _ -> error("No agent target") })
        }
        assertTrue(remaining)
        assertEquals(listOf("b"), sent)
        assertTrue(JsonCodec.decodeFromString<QueuedShare>(entries[0].readText()).queued)
        assertFalse(entries[1].parentFile!!.exists())
    }

    @Test fun anExistingAgentSessionReceivesShareProvenance() = runTest {
        val file = File(folder.root, "shares/agent/entry.json").also {
            writePlatformFile(it, JsonCodec.encodeToString(QueuedShare.serializer(),
                QueuedShare("agent-share", "title", "text", machine = "build", session = "release", queued = true)))
        }
        val calls = mutableListOf<String>()
        assertFalse(deliverQueuedShare(file, connected = true, untitled = "untitled",
            prepareMain = { _ -> error("An existing Agent session needs no main creation") },
            upload = { _, _, _, _ -> error("No attachments") },
            main = { _, _, _ -> error("Wrong destination") },
            agent = { target, text, id, via ->
                assertEquals(persona.core.contracts.SessionKey("build", "release"), target)
                assertTrue(text.contains("text"))
                calls += "$id:$via"
            }))
        assertEquals(listOf("agent-share:share"), calls)
    }

    // One group's long upload holds only its own folder; another group's share sheet can still stage.
    @Test fun scopesDoNotShareALock() {
        val locks = ShareLocks()
        assertTrue(locks["groups/a"].tryLock())
        assertFalse(locks["groups/a"].tryLock())
        assertTrue(locks["groups/b"].tryLock())
    }
}
