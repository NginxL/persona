package persona.core.contracts

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.StarredConversation
import persona.core.contracts.api.StarredIndex
import persona.core.contracts.api.StarredList
import persona.core.contracts.api.StarredSnapshot
import persona.core.contracts.api.StarredSource

class StarredMessagesContractTest {
    @Test fun positionalSourceRequires32HexFingerprint() {
        val stable = StarredSource("mac", "sess", "msg-1", StarredConversation.Assistant)
        assertFalse(stable.isPositional)
        assertTrue(stable.isVerified)
        assertTrue(stable.matches(Item(id = "msg-1")))

        val unverifiedPositional = StarredSource("mac", "sess", "line900#0", StarredConversation.Agent, null)
        assertTrue(unverifiedPositional.isPositional)
        assertFalse(unverifiedPositional.isVerified)
        assertFalse(unverifiedPositional.matches(Item(id = "line900#0")))

        val proof = "a".repeat(32)
        val verifiedPositional = StarredSource("mac", "sess", "line900#0", StarredConversation.Agent, proof)
        assertTrue(verifiedPositional.isVerified)
        assertTrue(verifiedPositional.matches(Item(id = "line900#0", sourceFingerprint = proof)))
        assertFalse(verifiedPositional.matches(Item(id = "line900#0", sourceFingerprint = "b".repeat(32))))
    }

    @Test fun boundedCountsUnicodeCodePointsAndClearsServerFields() {
        val emojiBody = "  " + "\uD83D\uDC08".repeat(4_500) + "  "
        val snapshot = StarredSnapshot(
            id = "sm123",
            machineId = "server",
            sessionId = "s1",
            itemId = "m1",
            conversation = StarredConversation.Assistant,
            role = "user",
            title = "Notes",
            speaker = "",
            starredAt = "2026-11-22T00:00:00Z",
            text = emojiBody,
            omittedChars = 12,
        )
        val bounded = snapshot.bounded()
        assertNull(bounded.id)
        assertNull(bounded.starredAt)
        assertEquals(4_000, bounded.text.codePointCount(0, bounded.text.length))
        assertEquals(512, bounded.omittedChars)
    }

    @Test fun decodesWirePayloadsAndEvents() {
        val index = JsonCodec.decodeFromString<StarredIndex>(
            """{"refs":[{"id":"sm1","machine_id":"server","session_id":"s1","item_id":"m1","conversation":"assistant"}],"can_write":true}""",
        )
        assertTrue(index.canWrite)
        assertEquals(1, index.refs.size)
        assertEquals(StarredConversation.Assistant, index.refs.single().conversation)

        val list = JsonCodec.decodeFromString<StarredList>(
            """{"stars":[{"id":"sm1","machine_id":"server","session_id":"s1","item_id":"line9#0","conversation":"agent","source_fingerprint":"${"a".repeat(32)}","role":"assistant","title":"Review","speaker":"Agent","text":"Looks good","omitted_chars":0}],"can_write":false}""",
        )
        assertFalse(list.canWrite)
        assertEquals("Looks good", list.stars.single().text)
        assertTrue(list.stars.single().source.isVerified)

        val item = JsonCodec.decodeFromString<Item>("""{"id":"line9#0","source_fingerprint":"${"a".repeat(32)}"}""")
        assertEquals("a".repeat(32), item.sourceFingerprint)

        assertEquals(StarredMessagesEvent(), ServerEvent.decode("""{"type":"assistant_starred"}"""))
        val event: ServerEvent = StarredMessagesEvent("work")
        assertEquals(event, ServerEvent.decode(JsonCodec.encodeToString(event)))
    }
}
