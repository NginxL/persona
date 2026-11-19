package persona.core.hub

import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import persona.core.contracts.api.UsageHistoryApi
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer

class UsageHistoryApiTest {
    @Test fun daysUseTheSelectedManagerAndDecodeEveryTokenKind() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient()).forManager("work") as UsageHistoryApi
            server.enqueue(MockResponse().setBody("""{"usage":{"zone":"Asia/Shanghai","stored":true,"days":[
                {"date":"2026-11-16","in":1,"cache_write":2,"cache_read":4,"out":8,"req":3},
                {"date":"2026-11-17","in":10,"cache_write":20,"cache_read":40,"out":80,"req":5}
            ]}}"""))

            val answer = api.usageDays(140)
            assertEquals("Asia/Shanghai", answer.zone)
            assertTrue(answer.stored)
            assertEquals(listOf("2026-11-16", "2026-11-17"), answer.days.map { it.date })
            assertEquals(15L, answer.days[0].total)
            assertEquals(150L, answer.days[1].total)
            assertEquals(3, answer.days[0].requests)

            val request = server.takeRequest()
            assertEquals("GET", request.method)
            assertEquals("/api/v1/usage/days", request.requestUrl?.encodedPath)
            assertEquals("140", request.requestUrl?.queryParameter("days"))
            assertEquals("work", request.requestUrl?.queryParameter(ManagerQuery))
            assertEquals("work", request.getHeader(ManagerHeader))
        }
    }

    @Test fun hoursDecodeClockChangesMachineSharesAndTheTotal() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"usage":{"zone":"America/New_York","stored":false,
                "hours":[
                    {"t":1760000000,"date":"2026-11-01","hour":1,"in":1,"cache_write":2,"cache_read":3,"out":4,"req":1},
                    {"t":1760003600,"date":"2026-11-01","hour":1,"in":5,"cache_write":6,"cache_read":7,"out":8,"req":2}
                ],
                "machines":[{"id":"old","name":"","in":6,"cache_write":8,"cache_read":10,"out":12,"req":3}],
                "total":{"in":6,"cache_write":8,"cache_read":10,"out":12,"req":3}
            }}"""))

            val answer = api.usageHours(7)
            assertFalse(answer.stored)
            assertEquals(listOf(1, 1), answer.hours.map { it.hour })
            assertEquals(listOf(1_760_000_000L, 1_760_003_600L), answer.hours.map { it.t })
            assertEquals(36L, answer.total.total)
            assertEquals(36L, answer.machines.single().total)
            assertEquals("", answer.machines.single().name)

            val request = server.takeRequest()
            assertEquals("/api/v1/usage/hours", request.requestUrl?.encodedPath)
            assertEquals("7", request.requestUrl?.queryParameter("days"))
        }
    }

    @Test fun invalidRangesFailBeforeAnyNetworkRead() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            for (call in listOf<suspend () -> Unit>({ api.usageDays(0) }, { api.usageDays(372) }, { api.usageHours(8) })) {
                try {
                    call()
                    fail("invalid range must be rejected")
                } catch (_: IllegalArgumentException) {
                }
            }
            assertEquals(0, server.requestCount)
        }
    }
}
