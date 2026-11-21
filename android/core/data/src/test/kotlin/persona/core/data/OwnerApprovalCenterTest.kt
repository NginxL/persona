package persona.core.data

import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.ItemsResponse
import persona.core.contracts.ServerEvent
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatTransport
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.ItemsQuery
import persona.core.contracts.api.ResolveOutcome

@OptIn(ExperimentalCoroutinesApi::class)
class OwnerApprovalCenterTest {
    private val proposal = Interaction(
        "ix_owner_batch_1", "server", "main", kind = "permission",
        approvalBatchId = "oab_1", ownerProposal = true, ownerApprovalPhrases = listOf("发"),
    )

    @Test fun failedExactReplyKeepsTheCardAndSuccessNeverUsesOrdinaryResolve() = runTest {
        val transport = Transport(proposal)
        val center = HubInteractionCenter(transport, Connection(), backgroundScope)
        center.refresh()
        assertEquals(listOf(proposal), center.pending.value)

        transport.fail = true
        assertTrue(runCatching { center.replyOwnerApproval(proposal, "发") }.isFailure)
        assertEquals("an unconfirmed reply leaves the proposal answerable", listOf(proposal), center.pending.value)
        assertEquals(0, transport.ordinaryResolves)

        transport.fail = false
        center.replyOwnerApproval(proposal, "发")
        assertEquals(emptyList<Interaction>(), center.pending.value)
        assertEquals(listOf("发" to "ix_owner_batch_1", "发" to "ix_owner_batch_1"), transport.replies)
        assertEquals(0, transport.ordinaryResolves)
    }

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject): Boolean = true
    }

    private class Transport(private val pending: Interaction) : ChatTransport {
        var fail = false
        val replies = mutableListOf<Pair<String, String>>()
        var ordinaryResolves = 0

        override suspend fun machines() = listOf(Machine("server"))
        override suspend fun sessions() = emptyList<Session>()
        override suspend fun items(session: SessionKey, query: ItemsQuery): ItemsResponse = error("unexpected items request")
        override suspend fun interactions() = listOf(pending)
        override suspend fun resolve(id: String, resolution: Resolution) { resolveOutcome(id, resolution) }
        override suspend fun resolveOutcome(id: String, resolution: Resolution): ResolveOutcome {
            ordinaryResolves++
            error("exact reply must not use ordinary resolve")
        }
        override suspend fun replyOwnerApproval(interaction: Interaction, phrase: String) {
            replies += phrase to interaction.id
            check(!fail) { "connection cut off" }
        }
        override suspend fun image(session: SessionKey, itemId: String) = byteArrayOf()
        override suspend fun fullItem(session: SessionKey, itemId: String) = Item(itemId)
    }
}
