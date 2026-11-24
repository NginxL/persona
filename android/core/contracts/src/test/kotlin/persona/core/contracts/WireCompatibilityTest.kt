package persona.core.contracts

import kotlinx.serialization.SerializationException
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

class WireCompatibilityTest {
    @Test fun extensionAlertsPreserveTheirKindWithAndWithoutASession() {
        for ((machine, session) in listOf("machine" to "session", "" to "")) {
            val event = ServerEvent.decode("""{"type":"notify","kind":"alert","machine_id":"$machine","session_id":"$session","title":"任务提醒","body":"任务需要查看"}""") as NotifyEvent
            assertEquals("alert", (event.kind).zhAny())
            assertEquals(machine, event.machineId)
            assertEquals(session, event.sessionId)
            assertEquals("任务提醒", (event.title).zhAny())
            assertEquals(event, ServerEvent.decode(JsonCodec.encodeToString<ServerEvent>(event)))
        }
        // The caller's id (POST /api/v1/notify "id", #5614); absent on the hub's own events.
        val withId = ServerEvent.decode("""{"type":"notify","kind":"alert","machine_id":"","session_id":"","title":"进度","id":"progress-1"}""") as NotifyEvent
        assertEquals("progress-1", withId.id)
        assertEquals(withId, ServerEvent.decode(JsonCodec.encodeToString<ServerEvent>(withId)))
        assertNull((ServerEvent.decode("""{"type":"notify","kind":"alert","machine_id":"","session_id":""}""") as NotifyEvent).id)
    }

    @Test fun connectionEventsAndOptionalFieldsFollowD5() {
        val endpoint = """{"url":"https://example.com:443","kind":"name","port":443,"scheme":"https","seen_at":"2026-01-02T03:04:05Z"}"""
        val hello = ServerEvent.decode("""{"type":"hello","server_version":"example","client_id":"c1","hub_id":"hub_example","tls_port":443,"fp":"example","endpoints":[$endpoint],"features":["endpoints"]}""") as Hello
        assertEquals("hub_example", (hello.hubId).zhAny())
        assertEquals("example", (hello.serverVersion).zhAny())
        assertEquals("c1", (hello.clientId).zhAny())
        assertEquals("example", (hello.fp).zhAny())
        assertEquals(443, hello.tlsPort)
        assertEquals(listOf("endpoints"), (hello.features).zhAny())
        assertEquals("name", (hello.endpoints.single().kind).zhAny())
        assertEquals("2026-01-02T03:04:05Z", (hello.endpoints.single().seenAt).zhAny())
        val endpoints = JsonCodec.decodeFromString<EndpointsResponse>("""{"hub_id":"hub_example","tls_port":443,"fp":"example","endpoints":[$endpoint]}""")
        assertEquals(hello.endpoints, endpoints.endpoints)
        assertEquals(hello.hubId, endpoints.hubId)
        assertEquals(hello.fp, endpoints.fp)
        assertEquals(hello.tlsPort, endpoints.tlsPort)
        assertEquals(endpoints.endpoints, (ServerEvent.decode("""{"type":"endpoints","endpoints":[$endpoint]}""") as EndpointsEvent).endpoints)
        assertTrue((ServerEvent.decode("""{"type":"endpoints","endpoints":[]}""") as EndpointsEvent).endpoints.isEmpty())
        val health = JsonCodec.decodeFromString<Health>("""{"ok":true,"hub_id":"hub_example","tls_port":443,"fp":"example"}""")
        assertTrue(health.ok)
        assertEquals(hello.hubId, health.hubId)
        assertEquals(hello.tlsPort, health.tlsPort)
        assertEquals(hello.fp, health.fp)
        assertTrue(health.features.isEmpty())
        val ack = ServerEvent.decode("""{"type":"client_hello_ack","heartbeat":{"interval":240,"pong_timeout":15}}""") as ClientHelloAck
        assertEquals(240, ack.heartbeat.interval)
        assertEquals(15, ack.heartbeat.pongTimeout)
        assertEquals("", ((ServerEvent.decode("""{"type":"pong","id":""}""") as Pong).id).zhAny())
        assertNull(ServerEvent.decode("""{"type":"pong","id":1}"""))
        assertNull(ServerEvent.decode("""{"type":"pong"}"""))
        assertNull(ServerEvent.decode("""{"type":"future","items":false}"""))
        val notify = ServerEvent.decode("""{"type":"notify","kind":"interaction","machine_id":"m1","session_id":"s1","title":"通知","subtitle":"示例任务","body":"待确认","interaction_id":"ix_example"}""") as NotifyEvent
        assertEquals("interaction", (notify.kind).zhAny())
        assertEquals("m1", (notify.machineId).zhAny())
        assertEquals("s1", (notify.sessionId).zhAny())
        assertEquals("通知", (notify.title).zhAny())
        assertEquals("示例任务", (notify.subtitle).zhAny())
        assertEquals("待确认", (notify.body).zhAny())
        assertEquals("ix_example", (notify.interactionId).zhAny())
        assertFalse(notify.quiet)
        assertEquals(notify, ServerEvent.decode(JsonCodec.encodeToString<ServerEvent>(notify)))
        // Inside the device's quiet hours (#1863) the hub marks a card `quiet`.
        val hushed = ServerEvent.decode("""{"type":"notify","kind":"interaction","machine_id":"m1","session_id":"s1","interaction_id":"ix_example","quiet":true}""") as NotifyEvent
        assertTrue(hushed.quiet)
        assertEquals(hushed, ServerEvent.decode(JsonCodec.encodeToString<ServerEvent>(hushed)))
        // Every kind the hub's notifyWSFrom sends besides interaction and alert ("reply": Awaiting reply, #4035).
        for (kind in listOf("done", "assistant", "reply")) {
            val event = ServerEvent.decode("""{"type":"notify","kind":"$kind","machine_id":"m1","session_id":"s1","body":"示例"}""") as NotifyEvent
            assertEquals(kind, event.kind)
            assertNull(event.interactionId)
        }
        assertEquals("unknown", (ServerEvent.decode("""{"type":"notify","kind":"future","machine_id":"m1","session_id":"s1"}""") as NotifyEvent).kind)

        val session = JsonCodec.decodeFromString<Session>("""{"session_id":"s1","title":"保留","status":"working","background":[{"id":false}],"loop":{"ticks":"bad","prompt":"保留"},"updated_at":"1999-12-31T23:59:59Z","rev":42}""")
        // An optional nested field is dropped at its own boundary, without deleting its valid siblings.
        assertEquals("保留", (session.title).zhAny())
        assertEquals("保留", (session.loop?.prompt).zhAny())
        assertNull(session.loop?.ticks)
        assertEquals(42L, session.rev)
        assertNull(session.updatedAt)
        assertEquals(AgentState.Working, session.agentState)
        val badOptional = JsonCodec.decodeFromString<Session>("""{"session_id":"s1","background":"bad","queued":{},"assistant":"bad","seen_at":true}""")
        assertNull(badOptional.background)
        assertNull(badOptional.queued)
        assertNull(badOptional.seenAt)
        assertFalse(badOptional.assistant)
        assertNotNull(parseTime("2000-01-01T00:00:00Z"))
        assertNull(parseTime("1999-12-31T23:59:59.999Z"))
        assertNull(parseTime("invalid"))
        val item = JsonCodec.decodeFromString<Item>("""{"id":"i1","kind":"future","image":false,"tool_input":{"custom_Key":{"keep_snake_case":true}}}""")
        assertEquals("unknown", (item.kind).zhAny())
        assertNull(item.image)
        assertEquals(JsonPrimitive(true), item.toolInput?.jsonObject?.get("custom_Key")?.jsonObject?.get("keep_snake_case"))
        val sessions = ServerEvent.decode("""{"type":"sessions","machine_id":"m1","sessions":[{"machine_id":"wrong","session_id":"s1"}]}""") as SessionsEvent
        assertEquals("m1", (sessions.sessions.single().machineId).zhAny())
        val error = JsonCodec.decodeFromString<ErrorBody>("""{"error":"示例","code":"future_code","delivered":"future","retryable":false,"draft":"保留","clearable":"保留"}""")
        assertEquals("future_code", (error.code).zhAny())
        assertEquals("unknown", (error.delivered).zhAny())
        assertEquals(false, error.retryable)
        assertEquals("保留", (error.draft).zhAny())
        assertEquals("保留", (error.clearable).zhAny())
        val legacyError = JsonCodec.decodeFromString<ErrorBody>("""{"error":"示例"}""")
        assertNull(legacyError.delivered)
        assertNull(legacyError.retryable)
        assertThrows(SerializationException::class.java) { val _ = JsonCodec.decodeFromString<Session>("""{"session_id":false}""") }
    }

    @Test fun fileContentKeepsTheHubsRevision() {
        val streamed = JsonCodec.decodeFromString<persona.core.contracts.api.SessionFileContent>(
            """{"path":"/m/a.wav","name":"a.wav","size":10,"stream":true,"revision":"a-179ac3b2c1e00000"}""")
        assertEquals("a-179ac3b2c1e00000", streamed.revision)
        assertNull(JsonCodec.decodeFromString<persona.core.contracts.api.SessionFileContent>("""{"path":"/m/a.wav","name":"a.wav"}""").revision)
    }
}
