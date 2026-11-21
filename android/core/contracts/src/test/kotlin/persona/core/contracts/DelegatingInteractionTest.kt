package persona.core.contracts

import kotlinx.serialization.encodeToString
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.Instant

/** The additive interaction fields for a question the assistant is looking at or has handed to the user (#544). */
class DelegatingInteractionTest {
    @Test fun interactionDecodesDelegatingAndEveryDelegateField() {
        val card = JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix_delegate","machine_id":"box","session_id":"s1","kind":"question","delegating":true,""" +
                """"delegate":{"what":"Choose the database driver","why_now":"Queries start today","recommend":"pgx",""" +
                """"cost":"A later migration rewrites the queries","reason":"This changes the release","deadline":"2026-11-08T10:00:00Z"}}""",
        )

        assertTrue(card.delegating)
        val delegate = card.delegate!!
        assertEquals("Choose the database driver", delegate.what)
        assertEquals("Queries start today", delegate.whyNow)
        assertEquals("pgx", delegate.recommend)
        assertEquals("A later migration rewrites the queries", delegate.cost)
        assertEquals("This changes the release", delegate.reason)
        assertEquals(Instant.parse("2026-11-08T10:00:00Z"), parseTime(delegate.deadline))

        // Pending-card snapshots retain the additive fields under their wire names.
        val wire = JsonCodec.encodeToString(card)
        assertTrue(wire, wire.contains(""""delegating":true"""))
        assertTrue(wire, wire.contains(""""why_now":"Queries start today""""))
        assertEquals(card, JsonCodec.decodeFromString<Interaction>(wire))
    }

    @Test fun absentDelegateFieldsRetainTheLegacyDefaults() {
        val oldHub = JsonCodec.decodeFromString<Interaction>("""{"id":"ix_old","kind":"question"}""")
        assertFalse(oldHub.delegating)
        assertNull(oldHub.delegate)
        assertFalse(JsonCodec.encodeToString(oldHub).contains("delegating"))
        assertFalse(JsonCodec.encodeToString(oldHub).contains("delegate"))

        val empty = JsonCodec.decodeFromString<Interaction>("""{"id":"ix_empty","kind":"question","delegate":{}}""")
        assertFalse(empty.delegating)
        assertEquals(Delegate(), empty.delegate)

        // As on the other clients, one malformed optional value drops only itself, never the question.
        val odd = JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix_odd","kind":"question","delegate":{"what":3,"recommend":"pgx"}}""",
        )
        assertNull(odd.delegate?.what)
        assertEquals("pgx", odd.delegate?.recommend)
    }
}
