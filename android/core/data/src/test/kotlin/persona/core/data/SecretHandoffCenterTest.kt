package persona.core.data

import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.Interaction
import persona.core.contracts.Resolution

/** The center validates the approval marker against the actual pending card, never the caller's say-so. */
class SecretHandoffCenterTest {
    private val handoff = Interaction("ix", machineId = "hub", sessionId = "chat", kind = "permission", toolName = "secret_handoff",
        toolInput = buildJsonObject { put("variables", kotlinx.serialization.json.JsonArray(listOf(buildJsonObject { put("name", "TOKEN") }))) })
    private val ordinary = handoff.copy(id = "ordinary", toolName = "Bash")

    @Test fun onlyAnAllowForThePendingHandoffIsMarked() = runTest {
        suspend fun forwarded(card: Interaction?, id: String, resolution: Resolution): Boolean? {
            val transport = FakeChatTransport().apply { pending = listOfNotNull(card) }
            val center = HubInteractionCenter(transport, FakeConnection(), backgroundScope)
            center.refresh()
            center.resolveOutcome(id, resolution, handoffCard = true)
            return transport.handoffCard
        }
        assertTrue(forwarded(handoff, handoff.id, Resolution(allow = true)) == true)
        assertTrue(forwarded(handoff, handoff.id, Resolution(allow = true, always = true)) == true)
        assertFalse(forwarded(handoff, handoff.id, Resolution(allow = false)) == true)
        assertFalse(forwarded(ordinary, ordinary.id, Resolution(allow = true)) == true)
        assertFalse(forwarded(null, "stale", Resolution(allow = true)) == true)
    }
}
