package persona.core.hub

import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.*
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer
import org.junit.Assert.*
import org.junit.Test

class HttpAgentOperationsTest {
    @Test fun pathsBodiesAndFailuresMatchHubContractWithoutRetryingWrites() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAgentOperations("scope", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("{}"))
            api.renameSession(SessionKey("machine/id", "session#id"), "新名称", null)
            val rename = server.takeRequest()
            assertEquals("POST", (rename.method).zhAny())
            assertEquals("/api/v1/sessions/machine%2Fid/session%23id/rename", (rename.path).zhAny())
            assertEquals("Bearer test-token", (rename.getHeader("Authorization")).zhAny())
            assertEquals(buildJsonObject { put("title", "新名称") }, JsonCodec.parseToJsonElement(rename.body.readUtf8()))
            server.enqueue(MockResponse().setBody("{}"))
            api.renameSpace("machine", "workspace/id", "新空间")
            assertEquals("/api/v1/machines/machine/spaces/workspace%2Fid/rename", (server.takeRequest().path).zhAny())
            server.enqueue(MockResponse().setBody("{}"))
            api.saveMutes(AgentOperationPreferences(spaces = listOf("machine/新空间"), allowed = setOf("machine/session")))
            val mute = server.takeRequest()
            assertEquals("PUT", (mute.method).zhAny())
            assertEquals("/api/v1/mutes", (mute.path).zhAny())
            assertEquals("machine/session", (JsonCodec.parseToJsonElement(mute.body.readUtf8()).jsonObject.getValue("allowed").jsonArray.single().jsonPrimitive.content).zhAny())
            server.enqueue(MockResponse().setResponseCode(400).setBody("{}"))
            try { api.closeSession(SessionKey("machine", "session")); fail("Close must fail") }
            catch (e: AgentOperationFailure) { assertFalse(e.uncertain) }
            assertEquals("/api/v1/sessions/machine/session/close", (server.takeRequest().path).zhAny())
            server.enqueue(MockResponse().setResponseCode(503).setBody("{}"))
            try { api.closeSession(SessionKey("machine", "session")); fail("Close must be uncertain") }
            catch (e: AgentOperationFailure) { assertTrue(e.uncertain) }
            server.takeRequest()
            assertEquals(5, server.requestCount)
        }
    }

    @Test fun attachPostsToTheSessionAndRefusalsAreNotUncertain() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAgentOperations("scope", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("{}"))
            api.attachSession(SessionKey("server", "c0de-thread"))
            val attach = server.takeRequest()
            assertEquals("POST", (attach.method).zhAny())
            assertEquals("/api/v1/sessions/server/c0de-thread/attach", (attach.path).zhAny())
            server.enqueue(MockResponse().setResponseCode(403).setBody("""{"error":"这个会话不能实时接入","code":"codex_read_only","delivered":"none"}"""))
            try { api.attachSession(SessionKey("server", "history")); fail("Attach must fail") }
            catch (e: AgentOperationFailure) { assertFalse(e.uncertain) }
        }
    }

    @Test fun deleteSendsTheSessionsDeleteAndRefusalsAreNotUncertain() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAgentOperations("scope", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"ok":true}"""))
            api.deleteSession(SessionKey("server", "s1"))
            val delete = server.takeRequest()
            assertEquals("DELETE", (delete.method).zhAny())
            assertEquals("/api/v1/sessions/server/s1", (delete.path).zhAny())
            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"这个 Agent 还在运行，先关闭再删除"}"""))
            try { api.deleteSession(SessionKey("server", "s1")); fail("Delete must fail") }
            catch (e: AgentOperationFailure) { assertFalse(e.uncertain) }
        }
    }

    @Test fun forkSessionPostsEmptyAtItemIdAndReturnsTheBranchedSessionId() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAgentOperations("scope", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"session_id":"branched-42"}"""))
            assertEquals("branched-42", api.forkSession(SessionKey("server", "source-1")))
            val fork = server.takeRequest()
            assertEquals("POST", (fork.method).zhAny())
            assertEquals("/api/v1/sessions/server/source-1/fork", (fork.path).zhAny())
            assertEquals(buildJsonObject { put("at_item_id", "") }, JsonCodec.parseToJsonElement(fork.body.readUtf8()))
            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"这一轮还没做完，等它停下再分支","code":"fork_turn_running"}"""))
            try { api.forkSession(SessionKey("server", "source-1")); fail("Fork must fail while turn runs") }
            catch (e: AgentOperationFailure) { assertFalse(e.uncertain) }
        }
    }

    @Test fun codexTakeoverEndpointsPreserve409CodeAndStartContinuation(): Unit = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAgentOperations("scope", server.url("/").toString(), "test-token", OkHttpClient())
            val key = SessionKey("mac one", "thread/2")
            val expectedPath = "/api/v1/machines/mac%20one/codex/thread%2F2/takeover"
            server.enqueue(MockResponse().setBody("""{"running":true,"can_resume":true,"token":"tok-2","cwd":"/work","transcript_path":"/tmp/t","continuation_prompt":"Resume"}"""))
            val info = api.codexTakeoverInfo(key)
            assertTrue(info.running && info.canResume && info.token == "tok-2")
            assertEquals(expectedPath, server.takeRequest().path)

            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"sealed","code":"codex_takeover_continue_only"}"""))
            val conflict = runCatching { api.codexTakeover(key, "tok-2", confirmStop = true) }.exceptionOrNull() as HubFailure
            assertEquals(409, conflict.status)
            assertEquals(persona.core.contracts.CODEX_TAKEOVER_CONTINUE_ONLY, conflict.code)
            val postReq = server.takeRequest()
            assertEquals("POST", postReq.method)
            assertEquals(expectedPath, postReq.path)

            val opId = "44444444-4444-4444-8444-444444444444"
            server.enqueue(MockResponse().setBody("""{"session":{"machine_id":"","session_id":"continued-1","status":"working"}}"""))
            val created = api.startCodexContinuation(key, info, opId, todoId = "todo-7") as AgentStartResult.Created
            assertEquals(SessionKey("mac one", "continued-1"), created.session.key)
            val startReq = server.takeRequest()
            assertEquals("POST", startReq.method)
            assertEquals("/api/v1/sessions", startReq.path)
            assertEquals(opId, startReq.getHeader("X-Persona-Op-Id"))
            val startJson = JsonCodec.parseToJsonElement(startReq.body.readUtf8()).jsonObject
            assertEquals("codex", startJson["launcher"]?.jsonPrimitive?.content)
            assertEquals("todo-7", startJson["todo_id"]?.jsonPrimitive?.content)
            assertEquals(false, startJson["async"]?.jsonPrimitive?.boolean)

            // Reconciling a failed receipt returns Rejected so the caller may safely retry fresh.
            val failedOpId = "55555555-5555-4555-8555-555555555555"
            val encodedError = java.util.Base64.getEncoder().encodeToString("""{"error":"no launcher"}""".toByteArray())
            server.enqueue(MockResponse().setBody("""{"kind":"start","state":"failed","result":{"status":409,"body":"$encodedError"}}"""))
            val rejected = api.codexContinuationOperation(key, failedOpId) as AgentStartResult.Rejected
            assertEquals("no launcher", rejected.message.shown)
            assertEquals("/api/v1/ops/$failedOpId", server.takeRequest().path)

            // Ambiguous 503 assistant_offline stays Unknown so the receipt is preserved rather than starting twice.
            val offlineOpId = "77777777-7777-4777-8777-777777777777"
            server.enqueue(MockResponse().setResponseCode(503).setBody("""{"error":"assistant offline","code":"assistant_offline"}"""))
            server.enqueue(MockResponse().setResponseCode(503).setBody("""{"error":"assistant offline","code":"assistant_offline"}"""))
            assertEquals(AgentStartResult.Unknown, api.startCodexContinuation(key, info, offlineOpId))
            server.takeRequest(); server.takeRequest()
            Unit
        }
    }
}
