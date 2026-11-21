package persona.core.contracts

import org.junit.Assert.*
import org.junit.Test

class BackgroundOutcomesContractTest {
    @Test fun aWholeRowReadsRetainedOutcomesAndOlderSummariesCanOmitThem() {
        val row = JsonCodec.decodeFromString<Session>("""{
          "machine_id":"m","session_id":"s",
          "background":[{"id":"live","kind":"monitor","started_at":"2026-11-20T10:00:00Z"}],
          "background_ended":[{"id":"ended","kind":"shell","status":"failed","started_at":"2026-11-20T10:00:00Z",
          "ended_at":"2026-11-20T10:01:00Z","exit_code":7,"output_file":"/tmp/ended.log"}]
        }""")
        assertNull(row.backgroundTasks.single().status)
        val result = row.backgroundEnded!!.single()
        assertEquals("failed", result.status)
        assertEquals(7, result.exitCode)
        assertEquals("/tmp/ended.log", result.outputFile)
        assertNotNull(parseTime(result.endedAt))
        assertNull(JsonCodec.decodeFromString<Session>("""{"session_id":"s","waiting":false}""").backgroundEnded)
    }
}
