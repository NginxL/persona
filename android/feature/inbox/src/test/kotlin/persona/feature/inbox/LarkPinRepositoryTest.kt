package persona.feature.inbox

import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.LarkConversationTransport
import persona.core.contracts.api.LarkInvalidation
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import persona.core.inbox.*

/** "Pinned messages" through [LarkInboxRepository]: what each hub answer turns into (iOS `LarkChatPinsModel`). */
class LarkPinRepositoryTest {
    private class PinTransport : LarkConversationTransport {
        override val scope = "pins"
        var pinsAccount = "acct"
        var pinsFailure: Exception? = null
        var targetAccount = "acct"
        var changeBody: JsonObject = buildJsonObject { put("account_id", "acct"); put("pinned", true) }
        var changeFailure: Exception? = null
        val targets = mutableListOf<Boolean>()
        val changes = mutableListOf<String>()

        override suspend fun larkChatPins(feedId: String, accountId: String?, cursor: String?): JsonObject {
            pinsFailure?.let { throw it }
            return buildJsonObject {
                put("account_id", pinsAccount); put("total", 1)
                putJsonArray("items") { add(buildJsonObject { put("id", "p1"); put("message_id", "m1") }) }
            }
        }
        override suspend fun larkPinTarget(feedId: String, messageId: String, accountId: String, topic: Boolean): JsonObject {
            targets += topic
            return buildJsonObject {
                put("account_id", targetAccount)
                putJsonObject("target") {
                    putJsonObject("message") { put("id", messageId) }
                    putJsonObject("root") { put("id", "root") }
                }
            }
        }
        override suspend fun larkChangePin(feedId: String, messageId: String, pinId: String?, accountId: String): JsonObject {
            changes += accountId
            changeFailure?.let { throw it }
            return changeBody
        }
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = error("Unused")
        override suspend fun markRead(feedId: String, position: Long, badgeCount: Long?, messageIds: List<String>) = Unit
        override suspend fun inbox() = error("Unused")
        override suspend fun setDone(itemId: String, done: Boolean) = error("Unused")
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = error("Unused")
        override suspend fun assistant() = error("Unused")
        override suspend fun createConversation(main: Boolean) = error("Unused")
        override suspend fun pins() = error("Unused")
        override suspend fun mutes() = error("Unused")
        override suspend fun media(ref: String) = error("Unused")
    }

    private object NoCache : LarkWindowCache {
        override suspend fun read(scope: String, feed: String, around: Long?): JsonObject? = null
        override suspend fun write(scope: String, feed: String, around: Long?, page: JsonObject) = Unit
    }

    private fun repository(t: PinTransport) = LarkInboxRepository(FakeInboxRepository(), MutableStateFlow(t),
        MutableStateFlow(ConnectionState.Online), MutableSharedFlow<LarkInvalidation>(), NoCache)

    private suspend fun kindOf(block: suspend () -> Unit): LarkPinFailure.Kind? =
        try { block(); null } catch (e: LarkPinFailure) { e.kind }

    @Test fun pinsOfAnotherAccountAreRefused() = runTest {
        val t = PinTransport()
        val repo = repository(t)
        assertEquals("acct", repo.larkChatPins("f", null, null).accountId)
        assertNull(kindOf { repo.larkChatPins("f", "acct", null) })
        t.pinsAccount = "other"
        assertEquals(LarkPinFailure.Kind.AccountChanged, kindOf { repo.larkChatPins("f", "acct", null) })
        t.pinsAccount = ""
        assertEquals(LarkPinFailure.Kind.AccountChanged, kindOf { repo.larkChatPins("f", null, null) })
    }

    @Test fun anOldHubNeedsAnUpdateAndTheHubsCodesKeepTheirMeaning() = runTest {
        val t = PinTransport()
        val repo = repository(t)
        for (status in listOf(404, 405)) {
            t.pinsFailure = HubFailure(HubError.Server, status = status)
            assertEquals(LarkPinFailure.Kind.UpgradeRequired, kindOf { repo.larkChatPins("f", null, null) })
        }
        // A 404 the hub names (the chat is gone) is not an old hub.
        t.pinsFailure = HubFailure(HubError.Server, status = 404, code = "lark_chat_not_found")
        assertTrue(runCatching { repo.larkChatPins("f", null, null) }.exceptionOrNull() is HubFailure)
        t.pinsFailure = HubFailure(HubError.Server, status = 409, code = "lark_account_changed")
        assertEquals(LarkPinFailure.Kind.AccountChanged, kindOf { repo.larkChatPins("f", null, null) })
    }

    @Test fun aTargetSendsTheTopicFlagAndChecksTheAccount() = runTest {
        val t = PinTransport()
        val repo = repository(t)
        val target = repo.larkPinTarget("f", "m1", "acct", topic = true)
        assertEquals(listOf(true), t.targets)
        assertEquals("m1", target.message.id)
        assertEquals("root", target.root?.id)
        t.targetAccount = "other"
        assertEquals(LarkPinFailure.Kind.AccountChanged, kindOf { repo.larkPinTarget("f", "m1", "acct", topic = false) })
    }

    @Test fun changesAreConfirmedByTheHubsAnswer() = runTest {
        val t = PinTransport()
        val repo = repository(t)
        // "Pin" from a message reads the account from the chat's pins.
        assertNull(kindOf { repo.larkChangePin("f", "m1", null, null) })
        assertEquals(listOf("acct"), t.changes)
        // "Unpin" answered as still pinned: unknown.
        assertEquals(LarkPinFailure.Kind.ResultUnknown, kindOf { repo.larkChangePin("f", "m1", "p1", "acct") })
        t.changeBody = buildJsonObject { put("account_id", "other"); put("pinned", false) }
        assertEquals(LarkPinFailure.Kind.AccountChanged, kindOf { repo.larkChangePin("f", "m1", "p1", "acct") })
        t.changeFailure = HubFailure(HubError.Server, status = 502, code = "lark_pin_result_unknown")
        assertEquals(LarkPinFailure.Kind.ResultUnknown, kindOf { repo.larkChangePin("f", "m1", "p1", "acct") })
        t.changeFailure = HubFailure(HubError.Server, status = 405)
        assertEquals(LarkPinFailure.Kind.UpgradeRequired, kindOf { repo.larkChangePin("f", "m1", "p1", "acct") })
    }
}
