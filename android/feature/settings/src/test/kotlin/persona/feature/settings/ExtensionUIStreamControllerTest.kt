package persona.feature.settings

import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.test.*
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.contracts.generated.*
import persona.core.contracts.stream.*

@OptIn(ExperimentalCoroutinesApi::class)
class ExtensionUIStreamControllerTest {
    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        // Protocol-only synthetic tests; live acceptance never injects hello or transport frames.
        override var lastHello: Hello? = Hello(features = listOf("extension_ui_stream_v1"), hubId = "synthetic-hub", clientId = "synthetic-principal")
        val sent = mutableListOf<JsonObject>()
        override fun send(message: JsonObject): Boolean { sent.add(message); return true }
        override fun reconnect() {}
        suspend fun frame(payload: String) { events.emit(checkNotNull(ServerEvent.decode(payload))) }
    }
    private val snapshot = """{"protocol":1,"session":"session-a","epoch":"epoch-a","version":1,"root_key":"root","view":{"sections":[{"key":"section","items":[{"type":"button","key":"button","title":"Save","action":{"id":"callback"}}]}]}}"""
    private fun receipt(request: JsonObject, result: String = "{\"toast\":\"Saved\"}") = """{"type":"extension_ui_stream","kind":"receipt","manager":"manager-a","request_id":${request.getValue("request_id")},"receipt":{"session":"session-a","epoch":"epoch-a","event_id":${request.getValue("interaction").jsonObject.getValue("event_id")},"version":2,"status":"accepted","result":$result}}"""

    @Test fun canonicalReceiptsWaitForTheExactEmptyFenceAndDeduplicateEventIdentity() = runTest {
        val connection = Connection()
        val controller = ExtensionUIStreamController(ExtensionStreamBinding(connection,"owner","group","manager-a"),"example","home", dispatcher = StandardTestDispatcher(testScheduler))
        val session = controller.adapter.session
        val effects = mutableListOf<NativeStreamPresentation>()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { controller.presentations.toList(effects) }
        runCurrent()
        session.visibility(true,true)
        val open = connection.sent.single().getValue("request").jsonObject
        assertEquals(JsonPrimitive(""),open["state"])
        // UI version 10 (#9727): streams use the pages' version while the hub still downgrades graphs to rows.
        assertEquals(JsonPrimitive(10),open.getValue("context").jsonObject["ui_version"])
        assertEquals(JsonPrimitive("manager-a"),connection.sent.single()["manager"])
        connection.frame("""{"type":"extension_ui_stream","kind":"snapshot","manager":"manager-a","request_id":${open.getValue("request_id")},"snapshot":$snapshot}""")
        runCurrent()
        assertEquals(NativeStreamPhase.Ready,session.state.value.phase)
        session.event("button","callback","press",JsonObject(emptyMap()))
        val event = connection.sent.last().getValue("request").jsonObject
        connection.frame(receipt(event));runCurrent()
        assertTrue(effects.isEmpty());assertEquals(1,session.state.value.pendingEvents)
        // Wrong manager cannot supply the fence or satisfy private receipt correlation.
        val fence = """{"type":"extension_ui_stream","kind":"delta","manager":"manager-a","delta":{"protocol":1,"session":"session-a","epoch":"epoch-a","base_version":1,"version":2,"operations":[]}}"""
        connection.frame(fence.replace("manager-a","manager-b"));runCurrent();assertTrue(effects.isEmpty())
        connection.frame(fence);runCurrent()
        assertEquals(2L,session.state.value.tree!!.snapshot.version)
        assertEquals("Saved",effects.single().result.toast)
        assertEquals(0,session.state.value.pendingEvents)
        connection.frame(receipt(event));runCurrent();assertEquals(1,effects.size)
        controller.close()
    }

    @Test fun eventSnapshotIsAtomicAndDoesNotConsumeReceiptCorrelation() = runTest {
        val connection = Connection()
        val controller = ExtensionUIStreamController(ExtensionStreamBinding(connection, "owner", "group", "manager-a"), "example", "home", dispatcher = StandardTestDispatcher(testScheduler))
        val session = controller.adapter.session
        val effects = mutableListOf<NativeStreamPresentation>()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { controller.presentations.toList(effects) }
        runCurrent(); session.visibility(true, true)
        val open = connection.sent.single().getValue("request").jsonObject
        connection.frame("""{"type":"extension_ui_stream","kind":"snapshot","manager":"manager-a","request_id":${open.getValue("request_id")},"snapshot":$snapshot}""")
        runCurrent(); session.event("button", "callback", "press", JsonObject(emptyMap()))
        val event = connection.sent.last().getValue("request").jsonObject
        val next = snapshot.replace("\"version\":1", "\"version\":2").replace("\"title\":\"Save\"", "\"title\":\"Saved\"")
        connection.frame("""{"type":"extension_ui_stream","kind":"snapshot","manager":"manager-a","request_id":${event.getValue("request_id")},"snapshot":$next}""")
        runCurrent(); assertEquals(2L, session.state.value.tree!!.snapshot.version)
        assertEquals(1, session.state.value.pendingEvents); assertTrue(effects.isEmpty())
        connection.frame(receipt(event)); runCurrent()
        assertEquals("Saved", effects.single().result.toast); assertEquals(0, session.state.value.pendingEvents)
        connection.frame("""{"type":"extension_ui_stream","kind":"snapshot","manager":"manager-a","snapshot":${next.replace("\"version\":2", "\"version\":3")}}""")
        runCurrent(); assertEquals(3L, session.state.value.tree!!.snapshot.version)
        assertEquals(1, effects.size)
        controller.close()
    }

    @Test fun hiddenAllocationUsesAResumeBarrierBeforeCloseOnANewConnection() = runTest {
        val connection = Connection()
        val controller = ExtensionUIStreamController(ExtensionStreamBinding(connection, "owner", "group", "manager-a"), "example", "home", dispatcher = StandardTestDispatcher(testScheduler))
        val session = controller.adapter.session
        runCurrent(); session.visibility(true, true)
        val open = connection.sent.single().getValue("request").jsonObject
        connection.frame("""{"type":"extension_ui_stream","kind":"snapshot","manager":"manager-a","request_id":${open.getValue("request_id")},"snapshot":$snapshot}""")
        runCurrent(); session.visibility(false, true)
        connection.lastHello = Hello(features = listOf("extension_ui_stream_v1"), clientId = "new-synthetic-connection")
        val closing = async { controller.close() }
        runCurrent()
        val resume = connection.sent.last().getValue("request").jsonObject
        assertEquals(JsonPrimitive("resume"), resume["request"])
        connection.frame("""{"type":"extension_ui_stream","kind":"snapshot","manager":"manager-a","request_id":${resume.getValue("request_id")},"snapshot":$snapshot}""")
        runCurrent(); closing.await()
        assertEquals(JsonPrimitive("close"), connection.sent.last().getValue("request").jsonObject["request"])
        assertEquals(NativeStreamPhase.Closed, session.state.value.phase)
    }

    @Test fun missingAuthenticatedHelloCannotCreateAnOwnedStream() = runTest {
        val connection = Connection().also { it.lastHello = null }
        assertThrows(IllegalArgumentException::class.java) {
            ExtensionUIStreamController(ExtensionStreamBinding(connection, "owner", "group", "manager-a"), "example", "home", dispatcher = StandardTestDispatcher(testScheduler))
        }
        assertTrue(connection.sent.isEmpty())
    }

    @Test fun fileLinesRemainBoundToTheSavedFileSideAndRange() {
        val event = ExtensionUIStreamEvent(protocol = 1, session = "session-a", epoch = "epoch-a", version = 1,
            eventId = "event-a", key = "diff-a", action = "context", event = "press",
            value = buildJsonObject { put("path", "file.txt"); put("side", "new"); put("from", "5") })
        val lines = ExtensionUIStreamFileLines(path = "file.txt", side = "new", start = 5, lines = listOf("sample line"), total = 10)
        fun result(l: ExtensionUIStreamFileLines) = nativeStreamTargetResult(NativeStreamPresentation(event, null, ExtensionUIStreamActionResult(lines = l)))
        assertEquals("file.txt", result(lines)!!.lines!!.path)
        assertEquals(5, result(lines)!!.lines!!.start)
        assertNull(result(lines.copy(path = "other.txt")))
        assertNull(result(lines.copy(side = "old")))
        assertNull(result(lines.copy(start = 4)))
    }

    private fun TestScope.controller(connection: Connection, approvals: ChatTransport? = null) = ExtensionUIStreamController(
        ExtensionStreamBinding(connection, "owner", "group", "manager-a", approvals), "example", "home",
        dispatcher = StandardTestDispatcher(testScheduler), responseDeadlineMillis = 100,
        deadlineDispatcher = StandardTestDispatcher(testScheduler))
    private suspend fun TestScope.open(controller: ExtensionUIStreamController, connection: Connection, payload: String = snapshot) {
        runCurrent(); controller.adapter.session.visibility(true, true)
        val request = connection.sent.last().getValue("request").jsonObject
        connection.frame("""{"type":"extension_ui_stream","kind":"snapshot","manager":"manager-a","request_id":${request.getValue("request_id")},"snapshot":$payload}""")
        runCurrent()
    }
    private suspend fun Connection.closed() {
        val request = sent.last().getValue("request").jsonObject
        frame("""{"type":"extension_ui_stream","kind":"closed","manager":"manager-a","request_id":${request.getValue("request_id")},"session":"session-a"}""")
    }

    private class Approvals : ChatTransport {
        val answers = mutableListOf<Pair<String, Resolution>>()
        override suspend fun resolve(id: String, resolution: Resolution) { answers += id to resolution }
        override suspend fun machines() = emptyList<Machine>()
        override suspend fun sessions() = emptyList<Session>()
        override suspend fun interactions() = emptyList<Interaction>()
        override suspend fun items(session: SessionKey, query: ItemsQuery): ItemsResponse = error("Unused")
        override suspend fun image(session: SessionKey, itemId: String): ByteArray = error("Unused")
        override suspend fun fullItem(session: SessionKey, itemId: String): Item = error("Unused")
    }
    private val confirmedAction = """{"id":"callback","confirm":"Frozen confirmation","values":{"target":"frozen"}}"""
    private fun permission() = Interaction("permission", sessionId = "session-a", kind = "permission", toolName = "extension.ui",
        summary = "Frozen confirmation", toolInput = JsonCodec.parseToJsonElement(confirmedAction), expiresAt = "2099-01-01T00:00:00Z", manager = "manager-a")

    @Test fun hostPermissionMatchesTheFrozenPageAndNeverApprovesWithoutAnExplicitDecision() = runTest {
        val connection = Connection(); val approvals = Approvals(); val controller = controller(connection, approvals)
        open(controller, connection, snapshot.replace("{\"id\":\"callback\"}", confirmedAction))
        controller.adapter.session.event("button", "callback", "press", JsonObject(emptyMap()))
        val permission = permission()
        for (other in listOf(permission.copy(manager = "another-manager"), permission.copy(sessionId = "other-fixture-session"), permission.copy(summary = "Replacement summary"),
            permission.copy(toolInput = JsonCodec.parseToJsonElement(confirmedAction.replace("frozen", "replacement"))),
            permission.copy(expiresAt = "2000-01-01T00:00:00Z"))) {
            connection.events.emit(InteractionEvent(other)); runCurrent(); assertNull(controller.adapter.permission)
        }
        connection.events.emit(InteractionEvent(permission)); runCurrent()
        assertSame(permission, controller.adapter.permission); assertTrue(approvals.answers.isEmpty())
        controller.adapter.decidePermission!!.invoke(permission, true); runCurrent()
        assertEquals("permission" to Resolution(allow = true), approvals.answers.single())
        controller.close(); connection.closed(); runCurrent()
    }

    @Test fun hiddenAndRetiredPagePermissionsCannotApproveTheSavedCallback() = runTest {
        val connection = Connection(); val approvals = Approvals(); val controller = controller(connection, approvals)
        val payload = snapshot.replace("{\"id\":\"callback\"}", confirmedAction)
        open(controller, connection, payload)
        val session = controller.adapter.session
        session.event("button", "callback", "press", JsonObject(emptyMap()))
        val permission = permission(); connection.events.emit(InteractionEvent(permission)); runCurrent()
        assertSame(permission, controller.adapter.permission)
        controller.adapter.shown = false
        controller.adapter.decidePermission!!.invoke(permission, true); runCurrent(); assertTrue(approvals.answers.isEmpty())
        session.visibility(false, true); runCurrent(); assertNull(controller.adapter.permission)
        controller.adapter.shown = true; session.visibility(true, true)
        val resume = connection.sent.last().getValue("request").jsonObject
        connection.frame("""{"type":"extension_ui_stream","kind":"snapshot","manager":"manager-a","request_id":${resume.getValue("request_id")},"snapshot":${payload.replace("epoch-a", "epoch-b")}}""")
        runCurrent(); connection.events.emit(InteractionEvent(permission)); runCurrent()
        controller.adapter.decidePermission!!.invoke(permission, true); runCurrent()
        assertNull(controller.adapter.permission); assertTrue(approvals.answers.isEmpty())
        controller.close(); connection.closed(); runCurrent()
    }

    @Test fun finalRemovalReceivesLateAllocationEvenAfterItsUiJobIsCancelled() = runTest {
        val connection = Connection(); val controller = controller(connection)
        runCurrent(); controller.adapter.session.visibility(true, true)
        val original = connection.sent.single().getValue("request").jsonObject
        val ui = launch { controller.close(); awaitCancellation() }
        runCurrent(); ui.cancelAndJoin()
        connection.frame("""{"type":"extension_ui_stream","kind":"snapshot","manager":"manager-a","request_id":${original.getValue("request_id")},"snapshot":$snapshot}""")
        runCurrent()
        assertEquals(JsonPrimitive("close"), connection.sent.last().getValue("request").jsonObject["request"])
        assertNull(controller.adapter.session.state.value.tree)
        assertEquals(1, connection.sent.count { it.getValue("request").jsonObject["request"] == JsonPrimitive("open") })
        connection.closed(); runCurrent()
    }

    @Test fun finalOfflineRemovalResumesThenClosesWithoutReopeningOrRendering() = runTest {
        val connection = Connection(); val controller = controller(connection); open(controller, connection)
        connection.state.value = ConnectionState.Connecting; runCurrent(); controller.close()
        val before = connection.sent.size
        connection.lastHello = Hello(features = listOf("extension_ui_stream_v1"), clientId = "replacement-principal")
        connection.state.value = ConnectionState.Online; runCurrent()
        val resume = connection.sent.last().getValue("request").jsonObject
        assertEquals(before + 1, connection.sent.size); assertEquals(JsonPrimitive("resume"), resume["request"])
        connection.frame("""{"type":"extension_ui_stream","kind":"snapshot","manager":"manager-a","request_id":${resume.getValue("request_id")},"snapshot":$snapshot}""")
        runCurrent(); assertEquals(JsonPrimitive("close"), connection.sent.last().getValue("request").jsonObject["request"])
        assertNull(controller.adapter.session.state.value.tree)
        connection.closed(); runCurrent()
    }

    @Test fun unknownOpenCleanupRetriesOnlyTheOriginalFrozenRequestAndHasABound() = runTest {
        val connection = Connection(); val controller = controller(connection)
        runCurrent(); controller.adapter.session.visibility(true, true)
        val original = connection.sent.single()
        controller.close(); runCurrent(); advanceTimeBy(100); runCurrent()
        assertEquals(listOf(original, original), connection.sent)
        advanceTimeBy(100); runCurrent()
        connection.frame("""{"type":"extension_ui_stream","kind":"snapshot","manager":"manager-a","request_id":${original.getValue("request").jsonObject.getValue("request_id")},"snapshot":$snapshot}""")
        runCurrent(); assertEquals(2, connection.sent.size)
    }

    @Test fun busyReadErrorRetainsAllocationAndLetsRefreshRetryWithoutAnotherOpen() = runTest {
        val connection = Connection(); val controller = controller(connection); open(controller, connection)
        val session = controller.adapter.session
        session.event("button", "callback", "press", JsonObject(emptyMap())); session.requestSnapshot()
        val read = connection.sent.last().getValue("request").jsonObject
        connection.frame("""{"type":"extension_ui_stream","kind":"error","manager":"manager-a","request_id":${read.getValue("request_id")},"error":{"code":"inactive","message":"Busy"}}""")
        runCurrent(); session.requestSnapshot()
        assertEquals(1, connection.sent.count { it.getValue("request").jsonObject["request"] == JsonPrimitive("open") })
        assertEquals(JsonPrimitive("snapshot"), connection.sent.last().getValue("request").jsonObject["request"])
        assertEquals("session-a", session.state.value.tree!!.snapshot.session)
        controller.close(); connection.closed(); runCurrent()
    }

    @Test fun onlyTypedExpiredReadErrorCanAllocateAReplacement() = runTest {
        val connection = Connection(); val controller = controller(connection); open(controller, connection)
        controller.adapter.session.requestSnapshot()
        val read = connection.sent.last().getValue("request").jsonObject
        connection.frame("""{"type":"extension_ui_stream","kind":"error","manager":"manager-a","request_id":${read.getValue("request_id")},"error":{"code":"expired","message":"Localized text is not parsed"}}""")
        runCurrent()
        assertEquals(2, connection.sent.count { it.getValue("request").jsonObject["request"] == JsonPrimitive("open") })
        controller.close(); advanceTimeBy(200); runCurrent()
    }

    @Test fun missingResumeAndRecoveryRepliesRetryReadOnlyThenCloseWithinDeadlines() = runTest {
        val connection = Connection(); val controller = controller(connection); open(controller, connection)
        val session = controller.adapter.session
        session.visibility(false, true); session.visibility(true, true)
        runCurrent(); advanceTimeBy(600); runCurrent()
        assertEquals(NativeStreamPhase.Closed, session.state.value.phase)
        assertEquals(1, connection.sent.count { it.getValue("request").jsonObject["request"] == JsonPrimitive("open") })
        assertTrue(connection.sent.any { it.getValue("request").jsonObject["request"] == JsonPrimitive("snapshot") })
        connection.closed(); runCurrent()
    }

    @Test fun completedReceiptEvictionDoesNotImposeALifetimeActionLimit() = runTest {
        val connection = Connection(); val controller = controller(connection); open(controller, connection)
        val effects = mutableListOf<NativeStreamPresentation>()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { controller.presentations.toList(effects) }
        val session = controller.adapter.session
        repeat(600) { index ->
            assertTrue(session.event("button", "callback", "press", JsonObject(emptyMap())))
            val event = connection.sent.last().getValue("request").jsonObject
            val version = index + 2
            connection.frame("""{"type":"extension_ui_stream","kind":"delta","manager":"manager-a","delta":{"protocol":1,"session":"session-a","epoch":"epoch-a","base_version":${version - 1},"version":$version,"operations":[]}}""")
            connection.frame(receipt(event).replace("\"version\":2", "\"version\":$version")); runCurrent()
            connection.frame(receipt(event).replace("\"version\":2", "\"version\":$version")); runCurrent()
        }
        assertEquals(600, effects.size); assertEquals(NativeStreamPhase.Ready, session.state.value.phase)
        assertEquals(0, session.state.value.pendingEvents)
        controller.close(); connection.closed(); runCurrent()
    }

    @Test fun resultAuthorityAndWholeViewPayloadsNeverDecodeAsPresentation() {
        val event = JsonCodec.parseToJsonElement("""{"request_id":"request","interaction":{"event_id":"event"}}""").jsonObject
        listOf("{\"view\":{}}","{\"items\":[]}","{\"html\":\"script\"}","{\"approved\":true}","{\"push\":{\"page\":\"assistant\"}}", "{\"lines\":{\"path\":\"file.txt\",\"start\":0,\"lines\":[]}}")
            .forEach { assertNull(ServerEvent.decode(receipt(event,it))) }
        assertNotNull(ServerEvent.decode(receipt(event,"{\"lines\":{\"path\":\"file.txt\",\"start\":1,\"lines\":[],\"total\":0}}")))
    }
}
