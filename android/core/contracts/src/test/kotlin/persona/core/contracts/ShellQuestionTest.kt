package persona.core.contracts

import java.time.Instant
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ShellQuestionTest {
    @Test fun planTaskFailureFieldsDecodeInOrderAndRoundTrip() {
        val task = JsonCodec.decodeFromString(PlanTask.serializer(), """{
            "title":"Renew the certificate","prompt":"p","error":"stopped on a question",
            "error_code":"shell_waiting",
            "error_args":{"line":"Continue?","choices":"y,n,enter","pane":"w1:p2","default":"y"},
            "retry_with":["claude","work2","fast"]
        }""")

        assertEquals("shell_waiting", task.errorCode)
        assertEquals(mapOf("line" to "Continue?", "choices" to "y,n,enter", "pane" to "w1:p2", "default" to "y"), task.errorArgs)
        assertEquals(listOf("claude", "work2", "fast"), task.retryWith)

        val wire = JsonCodec.encodeToString(PlanTask.serializer(), task)
        assertEquals(task, JsonCodec.decodeFromString(PlanTask.serializer(), wire))
    }

    @Test fun legacyPlanTaskOmitsFailureFieldsAndRoundTrips() {
        val task = JsonCodec.decodeFromString(PlanTask.serializer(), """{"title":"Legacy","prompt":"p"}""")

        assertNull(task.errorCode)
        assertNull(task.errorArgs)
        assertNull(task.retryWith)
        val wire = JsonCodec.encodeToString(PlanTask.serializer(), task)
        assertEquals(task, JsonCodec.decodeFromString(PlanTask.serializer(), wire))
        val encoded = JsonCodec.parseToJsonElement(wire).jsonObject
        assertFalse("error_code" in encoded)
        assertFalse("error_args" in encoded)
        assertFalse("retry_with" in encoded)
    }

    @Test fun sessionStartFailureFieldsDecodeAndRoundTrip() {
        val session = JsonCodec.decodeFromString(Session.serializer(), """{
            "machine_id":"box","session_id":"s1","status":"offline","start_error":"stopped on a question",
            "start_error_code":"shell_waiting",
            "start_error_args":{"line":"Press Enter to continue","choices":"enter,cancel","kind":"press_key"}
        }""")

        assertEquals("shell_waiting", session.startErrorCode)
        assertEquals(mapOf("line" to "Press Enter to continue", "choices" to "enter,cancel", "kind" to "press_key"), session.startErrorArgs)
        assertEquals(listOf("enter", "cancel"), shellQuestion(session.startErrorCode, session.startErrorArgs)?.choices)

        val wire = JsonCodec.encodeToString(Session.serializer(), session)
        assertEquals(session, JsonCodec.decodeFromString(Session.serializer(), wire))
    }

    @Test fun legacySessionOmitsStartFailureFieldsAndRoundTrips() {
        val session = JsonCodec.decodeFromString(Session.serializer(), """{"session_id":"legacy"}""")

        assertNull(session.startErrorCode)
        assertNull(session.startErrorArgs)
        val wire = JsonCodec.encodeToString(Session.serializer(), session)
        assertEquals(session, JsonCodec.decodeFromString(Session.serializer(), wire))
        val encoded = JsonCodec.parseToJsonElement(wire).jsonObject
        assertFalse("start_error_code" in encoded)
        assertFalse("start_error_args" in encoded)
    }

    @Test fun shellQuestionRequiresExactCodeLineAndAtLeastOneChoice() {
        val args = mapOf("line" to "Continue?", "choices" to "y,n")
        for (code in listOf(null, "", "SHELL_WAITING", "shell_waiting ", "dialog_open")) {
            assertNull(code.orEmpty(), shellQuestion(code, args))
        }
        assertNull(shellQuestion(SHELL_WAITING, null))
        assertNull(shellQuestion(SHELL_WAITING, emptyMap()))
        assertNull(shellQuestion(SHELL_WAITING, args + ("line" to "")))
        assertNull(shellQuestion(SHELL_WAITING, mapOf("line" to "Continue?")))
        assertNull(shellQuestion(SHELL_WAITING, args + ("choices" to "")))
        assertNull(shellQuestion(SHELL_WAITING, args + ("choices" to " , , ")))
        assertEquals(ShellQuestion(line = "Continue?", choices = listOf("y", "n")), shellQuestion(SHELL_WAITING, args))
    }

    @Test fun shellQuestionReadsTerminalDetailsAndTrimsOrderedChoices() {
        val question = shellQuestion(SHELL_WAITING, mapOf(
            "line" to "[oh-my-zsh] Would you like to update? [Y/n]",
            "choices" to " y, n , ,enter, q ",
            "kind" to "update",
            "screen" to "Previous line\n[oh-my-zsh] Would you like to update? [Y/n]",
            "pane" to "w1:p2",
            "line_mode" to "true",
            "secret" to "true",
            "default" to "y",
            "kept_until" to "2026-11-20T12:00:01Z",
        ))

        assertNotNull(question)
        assertEquals(ShellQuestion(
            line = "[oh-my-zsh] Would you like to update? [Y/n]",
            choices = listOf("y", "n", "enter", "q"),
            kind = "update",
            screen = "Previous line\n[oh-my-zsh] Would you like to update? [Y/n]",
            pane = "w1:p2",
            lineMode = true,
            secret = true,
            default = "y",
            keptUntil = "2026-11-20T12:00:01Z",
        ), question)
    }

    @Test fun shellQuestionStaysOpenUntilAParseableExpiry() {
        val now = Instant.parse("2026-11-20T12:00:00Z")
        fun question(keptUntil: String?) = ShellQuestion(
            line = "Continue?", choices = listOf("y", "n"), keptUntil = keptUntil,
        )

        for (missingOrMalformed in listOf(null, "", "not-a-time")) {
            assertTrue(missingOrMalformed.orEmpty(), question(missingOrMalformed).isOpenAt(now))
        }
        assertTrue(question("2026-11-20T12:00:00.001Z").isOpenAt(now))
        assertFalse(question("2026-11-20T12:00:00Z").isOpenAt(now))
        assertFalse(question("2026-11-20T11:59:59.999Z").isOpenAt(now))
    }
}
