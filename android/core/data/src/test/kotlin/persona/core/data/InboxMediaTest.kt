package persona.core.data

import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.RequestManager
import persona.core.inbox.LarkContentSource
import persona.core.inbox.LarkPart

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class InboxMediaTest {
    @Before fun choose() = AssistantManagerChoice.choose(null, null, "assistant")
    @After fun reset() = AssistantManagerChoice.reset()

    private class Transport : InboxAssistantTransport {
        override val scope = "hub/generation"
        /** Each file read with the manager its request names (RequestManager). */
        val reads = mutableListOf<Pair<String, String?>>()
        override suspend fun media(ref: String): ByteArray {
            reads += ref to currentCoroutineContext()[RequestManager]?.id
            return ref.toByteArray()
        }
        override suspend fun inbox() = error("unused")
        override suspend fun setDone(itemId: String, done: Boolean) = error("unused")
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = error("unused")
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = error("unused")
        override suspend fun assistant() = error("unused")
        override suspend fun createConversation(main: Boolean) = error("unused")
        override suspend fun pins() = error("unused")
        override suspend fun mutes() = error("unused")
    }

    @Test fun anotherManagersFileIsItsOwnEntryFetchedAsThatManager() = runBlocking {
        val media = InboxMedia(RuntimeEnvironment.getApplication(), uri = { it.path })
        val transport = Transport()
        val ref = "/api/v1/lark/media/file"
        fun file(manager: String?) = LarkPart.File(ref, "report.pdf", source = manager?.let { LarkContentSource(transport, it) })
        val default = media.download(transport, file(null))
        assertTrue("the default's file keeps its name", default.endsWith("/${cacheDigest(ref)}-report.pdf"))
        val other = media.download(transport, file("m-3"))
        assertNotEquals(default, other)
        assertEquals(listOf(ref to null, ref to "m-3"), transport.reads)
        assertEquals(other, media.download(transport, file("m-3")))
        // The default's stamp is the default's file.
        assertEquals(default, media.download(transport, file("assistant")))
        assertEquals(2, transport.reads.size)
    }
}
