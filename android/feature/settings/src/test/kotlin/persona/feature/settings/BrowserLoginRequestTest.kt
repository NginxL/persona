package persona.feature.settings

import org.junit.Assert.*
import org.junit.Test

class BrowserLoginRequestTest {
    private val id = "A".repeat(43)
    private val code = "persona://browser-login?request=$id&hub=test-hub"

    @Test fun acceptsOnlyARequestForThePairedHub() {
        assertEquals(BrowserLoginRequest(id, "test-hub"), browserLoginRequest(code))
    }

    @Test fun refusesCredentialsRedirectsDuplicateFieldsAndMalformedCodes() {
        listOf(
            code.replace("persona:", "https:"), code + "&request=$id", code + "&url=https://untrusted.example",
            code.replace("browser-login", "user@browser-login"), code + "#fragment",
            code.replace(id, "short"), code.replace(id, "%zz"), "not a link",
        ).forEach { assertNull(it, browserLoginRequest(it)) }
    }
}
