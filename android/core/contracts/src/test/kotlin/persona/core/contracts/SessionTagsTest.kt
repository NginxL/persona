package persona.core.contracts

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class SessionTagsTest {
    @Test fun missingAndNullTagsDecodeLikeAnOlderHub() {
        val missing = JsonCodec.decodeFromString<Session>("""{"machine_id":"mac","session_id":"c1","assistant":true}""")
        val explicitNull = JsonCodec.decodeFromString<Session>(
            """{"machine_id":"mac","session_id":"c1","assistant":true,"tags":null}""")

        assertNull(missing.tags)
        assertNull(explicitNull.tags)
        assertEquals("c1", missing.sessionId)
        assertEquals("c1", explicitNull.sessionId)
    }

    @Test fun tagsDecodeWithoutChangingTheirCaseOrOrder() {
        val session = JsonCodec.decodeFromString<Session>(
            """{"machine_id":"mac","session_id":"c1","assistant":true,"tags":["Planning","旅行","Planning"]}""")

        assertEquals(listOf("Planning", "旅行", "Planning"), session.tags)
    }
}
