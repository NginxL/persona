package persona.core.contracts

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class DevScrubTest {
    @Test fun urlsLoseTheirOriginQueryAndFragment() {
        assertEquals("open /api/v1/pair now", DevScrub.text("open https://user:pw@hub.example.com/api/v1/pair?token=secret#code now"))
        assertEquals("/machines/:id/sessions/:id/items", DevScrub.route(
            "https://hub.example.com/api/v1/machines/m1/sessions/s1/items?since=4#private"))
        assertEquals("/blob/:id", DevScrub.route("/blob/x?y#z"))
        assertEquals("/sessions", DevScrub.route("/api/v1/sessions"))
        assertEquals("/", DevScrub.route("/api/v1"))
    }

    @Test fun freeTextRemovesSecretsAddressesAndHomeFolders() {
        val secret = "a1".repeat(20)
        val cleaned = DevScrub.text(
            "Bearer abcDEF123 token=shh user@example.com 192.0.2.4 2001:db8::1 " +
                "hub.example.com /home/alice/.persona /Users/bob/.persona $secret")
        assertEquals(
            "Bearer <redacted> token=<redacted> <email> <ip> <ip> <host> ~/.persona ~/.persona <redacted>",
            cleaned)
        assertFalse(cleaned.contains("alice"))
        assertFalse(cleaned.contains("abcDEF123"))
    }

    @Test fun authenticationSchemesAndHeaderCredentialsAreCaseInsensitive() {
        assertEquals("bearer <redacted>", DevScrub.text("bearer abc123"))
        assertEquals("BASIC <redacted>", DevScrub.text("BASIC abc123"))
        assertEquals("tOkEn <redacted>", DevScrub.text("tOkEn abc123"))
        val header = DevScrub.text("authorization: bearer abc123")
        assertFalse(header.contains("abc123"))
        assertFalse(header.contains("bearer abc123", ignoreCase = true))
    }

    @Test fun wordsAreCodeChosenOnly() {
        assertEquals("sessions_list", DevScrub.word("sessions_list"))
        assertEquals("other", DevScrub.word("hello world"))
        assertEquals("other", DevScrub.word("会话"))
        assertEquals("other", DevScrub.word("a".repeat(49)))
    }

    @Test fun freeTextIsClippedAtOneKilobyteOnACharacterBoundary() {
        val cleaned = DevScrub.text("界".repeat(400))
        assertTrue(cleaned.endsWith("…"))
        assertTrue(cleaned.toByteArray(Charsets.UTF_8).size <= DevScrub.maxText)
        assertEquals("界".repeat(340) + "…", cleaned)
    }

    @Test fun apiWordsMatchTheWeb() {
        val source = File("../../../web/src/lib/error-reports.ts").readText()
        val start = source.indexOf("const API_WORDS = new Set(").also { assertTrue(it >= 0) }
        val end = source.indexOf(".split(\" \")", startIndex = start).also { assertTrue(it > start) }
        val body = source.substring(start, end)
        val words = body.split('"').filterIndexed { index, _ -> index % 2 == 1 }.joinToString("")
        assertEquals(words.split(' ').filter(String::isNotEmpty).toSet(), DevScrub.apiWords)
    }
}
