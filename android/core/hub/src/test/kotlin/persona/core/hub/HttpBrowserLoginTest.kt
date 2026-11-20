package persona.core.hub

import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.boolean
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.RecordedRequest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.BrowserLoginDetails
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer

/** The number-matching browser sign-in wire contract (#10319). */
class HttpBrowserLoginTest {
    private val id = "a".repeat(40) + "b-_"
    private val hub = "hub-1"

    @Test fun inspectionSendsOnlyIdAndHubAndDecodesTheRequestFacts() = runBlocking {
        MockWebServer().use { server ->
            val api = api(server.url("/").toString())
            server.enqueue(MockResponse().setBody(answer(state = "scanned", network = "internet")))
            val details = api.browserLogin(id, hub)

            val sent = server.takeRequest()
            assertEquals("POST", sent.method)
            assertEquals("/api/v1/auth/browser-login/approve", sent.path)
            assertEquals("Bearer example-token", sent.getHeader("Authorization"))
            val body = body(sent)
            assertEquals("inspection must not allow or ask for a number", setOf("id", "hub"), body.keys)
            assertEquals(id, body.getValue("id").jsonPrimitive.content)
            assertEquals(hub, body.getValue("hub").jsonPrimitive.content)
            assertEquals("Chrome · Mac", details.name)
            assertEquals("scanned", field(details, "state"))
            assertEquals("internet", field(details, "network"))
            assertEquals(12, details.ageSeconds)
            assertNull(field(details, "number"))
        }
    }

    @Test fun allowSendsShowNumberAndDecodesTheTwoDigitAnswer() = runBlocking {
        MockWebServer().use { server ->
            val api = api(server.url("/").toString())
            server.enqueue(MockResponse().setBody(answer(state = "confirm", number = "47")))
            val details = api.browserLogin(id, hub, allow = true)

            val body = body(server.takeRequest())
            assertEquals("Allow must explicitly ask this hub for the number", setOf("id", "hub", "allow", "show_number"), body.keys)
            assertEquals(id, body.getValue("id").jsonPrimitive.content)
            assertEquals(hub, body.getValue("hub").jsonPrimitive.content)
            assertTrue(body.getValue("allow").jsonPrimitive.boolean)
            assertTrue(body.getValue("show_number").jsonPrimitive.boolean)
            assertEquals("confirm", field(details, "state"))
            assertEquals("47", field(details, "number"))
        }
    }

    @Test fun cancelSendsOnlyIdHubAndAllowFalse() = runBlocking {
        MockWebServer().use { server ->
            val api = api(server.url("/").toString())
            server.enqueue(MockResponse().setBody(answer(state = "denied")))
            api.browserLogin(id, hub, allow = false)

            val body = body(server.takeRequest())
            assertEquals(setOf("id", "hub", "allow"), body.keys)
            assertEquals(id, body.getValue("id").jsonPrimitive.content)
            assertEquals(hub, body.getValue("hub").jsonPrimitive.content)
            assertFalse(body.getValue("allow").jsonPrimitive.boolean)
        }
    }

    @Test fun askingAgainDecodesAllFourStatesAndTheApproversNumber() = runBlocking {
        MockWebServer().use { server ->
            val api = api(server.url("/").toString())
            for (state in listOf("confirm", "approved", "voided", "denied")) {
                val number = if (state == "confirm") "47" else null
                server.enqueue(MockResponse().setBody(answer(state = state, number = number)))
                val details = api.browserLogin(id, hub)

                assertEquals("a repeated look sends no new decision", setOf("id", "hub"), body(server.takeRequest()).keys)
                assertEquals(state, field(details, "state"))
                assertEquals("the number is repeated only while this approver waits for the browser", number, field(details, "number"))
            }
        }
    }

    @Test fun olderHubWithoutStateKeepsItAbsent() = runBlocking {
        MockWebServer().use { server ->
            val api = api(server.url("/").toString())
            server.enqueue(MockResponse().setBody("""{"name":"Chrome · Mac","source":"203.0.113.8","age_seconds":12}"""))
            val details = api.browserLogin(id, hub, allow = true)

            assertTrue(body(server.takeRequest()).getValue("show_number").jsonPrimitive.boolean)
            assertNull("an old hub has no number-match state", field(details, "state"))
            assertNull(field(details, "network"))
            assertNull(field(details, "number"))
        }
    }

    private fun api(address: String) = HttpDeviceSettings(address, "example-token", "phone", OkHttpClient())

    private fun answer(state: String, network: String = "lan", number: String? = null): String {
        val numberField = number?.let { ""","number":"$it"""" }.orEmpty()
        // source remains in a real hub response; it is not a fact the app may show.
        return """{"name":"Chrome · Mac","state":"$state","source":"203.0.113.8","network":"$network","age_seconds":12$numberField}"""
    }

    private fun body(request: RecordedRequest): JsonObject =
        JsonCodec.parseToJsonElement(request.body.readUtf8()).jsonObject

    /** Keeps the red-first suite compilable before the response model grows these fields. */
    private fun field(details: BrowserLoginDetails, name: String): Any? {
        val field = details.javaClass.declaredFields.firstOrNull { it.name == name }
            ?: throw AssertionError("BrowserLoginDetails.$name is missing")
        return field.apply { isAccessible = true }.get(details)
    }
}
