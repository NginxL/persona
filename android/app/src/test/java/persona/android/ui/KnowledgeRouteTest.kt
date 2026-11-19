package persona.android.ui

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.toList
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.Endpoint
import persona.core.contracts.Health
import persona.core.contracts.HubScope
import persona.core.contracts.api.*
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import persona.core.hub.HubRoute

class KnowledgeRouteTest {
    private val lan = HubRoute(Endpoint("https://hub.example.com:8443", "lan"), Health(ok = true))
    private val public = HubRoute(Endpoint("https://hub.example.net", "name"), Health(ok = true))

    @Test fun aReconnectKeepsTheLastRepositoryAndANewRouteBuildsAnother() = runTest(UnconfinedTestDispatcher()) {
        val route = MutableStateFlow<HubRoute?>(lan)
        val built = mutableListOf<String>()
        val job = launch { route.followingLastRoute { "repo:${it.endpoint.url}" }.toList(built) }
        route.value = null
        assertEquals(listOf("repo:https://hub.example.com:8443"), built)
        route.value = public
        assertEquals(listOf("repo:https://hub.example.com:8443", "repo:https://hub.example.net"), built)
        job.cancel()
    }

    @Test fun withoutARouteTheKeptRepositorySendsNothingAndFailsAsUnreachable() = runTest {
        val calls = mutableListOf<String>()
        val inner = object : KnowledgeRepository {
            override val scope = HubScope("hub1", "gen1")
            override suspend fun list(kind: KnowledgeKind, tag: String?): KnowledgeList { calls += "list"; return KnowledgeList() }
            override suspend fun detail(kind: KnowledgeKind, id: String): KnowledgeDetail = error("unused")
            override suspend fun search(query: String): List<KnowledgeHit> { calls += "search"; return emptyList() }
            override suspend fun handOff(id: String, request: MeetingHandoffRequest): MeetingHandoff {
                calls += "handOff ${request.machineId}"
                return MeetingHandoff("h1", "2026-11-19T12:00:00Z", request.machineId, request.sessionId, state = "sent")
            }
        }
        var route = false
        val kept = OnlyWithRoute(inner) { route }
        assertEquals(inner.scope, kept.scope)
        val failure = runCatching { kept.list(KnowledgeKind.entries.first()) }.exceptionOrNull()
        assertEquals(HubError.NoNetwork, (failure as HubFailure).kind)
        assertTrue(runCatching { kept.search("x") }.isFailure)
        assertEquals(emptyList<String>(), calls)
        route = true
        kept.list(KnowledgeKind.entries.first())
        val request = MeetingHandoffRequest("mac", "site", "Website redesign", "", false)
        route = false
        assertTrue(runCatching { kept.handOff("r1", request) }.isFailure)
        route = true
        kept.handOff("r1", request)
        assertEquals(listOf("list", "handOff mac"), calls)
    }
}
