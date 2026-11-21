package persona.core.hub

import persona.core.contracts.JsonCodec
import persona.core.contracts.api.NewProvider
import persona.core.contracts.api.PresetModel
import persona.core.contracts.api.ProviderProbe
import persona.core.contracts.api.ProviderUpdate
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.mockwebserver.MockResponse
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer
import org.junit.Assert.*
import org.junit.Test

/** "Add model provider"'s calls: presets (none from an older hub), and the body that adds one, blank fields left out. */
class HttpProviderSetupTest {
    private fun <T> withHub(block: suspend (MockWebServer, HttpProviderSetup) -> T): T = runBlocking {
        MockWebServer().use { server ->
            val http = OkHttpClient.Builder().retryOnConnectionFailure(false).build()
            block(server, HttpProviderSetup { path, method, body ->
                http.newCall(Request.Builder().url(server.url(path)).method(method, body).build()).execute()
            })
        }
    }

    @Test fun presetsDecodeAndAnOlderHubHasNone() = withHub { server, providers ->
        server.enqueue(MockResponse().setBody("""{"presets":[{"id":"example-gateway/team","plugin":"example-gateway","name":"示例网关 · 团队",
            "kind":"launcher","base_url":"https://gateway.example.com","model":"example-pro","models":[{"id":"example-mini","name":"Example Mini"}],
            "key_hint":"以 eg- 开头","unknown":1},{"id":"example-gateway/api","kind":"api","api":"responses","base_url":"https://api.example.com/v1",
            "models":[{"id":"m1"}],"models_error":"超时"}]}"""))
        val presets = providers.presets()
        assertEquals("/api/v1/launchers/presets", (server.takeRequest().path).zhAny())
        assertEquals(listOf("example-gateway/team", "example-gateway/api"), (presets.map { it.id }).zhAny())
        assertEquals("example-pro", (presets[0].defaultModel).zhAny())
        assertEquals(listOf("example-pro", "example-mini"), (presets[0].choices.map { it.id }).zhAny())
        assertEquals(listOf("example-pro", "Example Mini"), (presets[0].choices.map { it.label }).zhAny())
        assertEquals("以 eg- 开头", (presets[0].keyHint).zhAny())
        assertEquals("m1", (presets[1].defaultModel).zhAny())
        assertEquals("超时", (presets[1].modelsError).zhAny())

        server.enqueue(MockResponse().setResponseCode(404).setBody("""{"error":"没有这个启动器"}"""))
        assertTrue(providers.presets().isEmpty())
        assertEquals("/api/v1/launchers/presets", (server.takeRequest().path).zhAny())
    }

    @Test fun pastedConfigurationIsSentOnlyInTheBodyAndEveryDraftFieldIsRead() = withHub { server, providers ->
        server.enqueue(MockResponse().setBody("""{"format":"shell","drafts":[{"label":"cc_work","aliases":["cc_work"],
            "provider":{"name":"cc_work","display_name":"Work gateway","kind":"launcher","base_url":"https://gateway.example.com",
            "api_key":"eg-secret","model":"gw-pro","models":[{"id":"gw-pro","context_window":1000000}],"sonnet_model":"gw-mid",
            "headers":"X-Team: demo","env":"CLAUDE_CODE_MAX_OUTPUT_TOKENS=8000","timeout_ms":600000,"args":["--verbose"],"unknown":1},
            "env_names":["CLAUDE_CODE_MAX_OUTPUT_TOKENS"],"skipped_env":[{"name":"OTHER_TOKEN","reason":"credential"}],
            "skipped_headers":[{"name":"X-Api-Key","reason":"credential"}],
            "matches":[{"name":"work","display_name":"Work","same_key":true}],"notes":[{"code":"no_key","line":2}]}],
            "notes":[{"code":"not_claude","line":3,"name":"other"},{"code":"future_note","name":"future"}]}"""))
        val text = "alias cc_work='ANTHROPIC_AUTH_TOKEN=eg-secret claude'"
        val answer = providers.parse(text)
        val request = server.takeRequest()
        assertEquals("POST", request.method)
        assertEquals("/api/v1/launchers/import/parse", request.path)
        assertEquals(text, JsonCodec.parseToJsonElement(request.body.readUtf8()).jsonObject["text"]!!.jsonPrimitive.content)
        assertFalse("the configuration is never a URL parameter", request.requestUrl.toString().contains("eg-secret"))
        assertEquals("shell", answer.format)
        val draft = answer.drafts.single()
        assertEquals("cc_work", draft.label)
        assertEquals("eg-secret", draft.provider.apiKey)
        assertEquals("Work gateway", draft.provider.displayName)
        assertEquals(1000000, draft.provider.models.single().contextWindow)
        assertEquals("gw-mid", draft.provider.sonnetModel)
        assertEquals("X-Team: demo", draft.provider.headers)
        assertEquals(600000, draft.provider.timeoutMs)
        assertEquals(listOf("--verbose"), draft.provider.args)
        assertEquals("OTHER_TOKEN", draft.skippedEnv.single().name)
        assertEquals("X-Api-Key", draft.skippedHeaders.single().name)
        assertTrue(draft.matches.single().sameKey)
        assertEquals(2, draft.notes.single().line)
        assertEquals(listOf("not_claude", "future_note"), answer.notes.map { it.code })
        assertEquals("other", answer.notes.first().name)
    }

    @Test fun addingSendsOnlyWhatWasFilledIn() = withHub { server, providers ->
        server.enqueue(MockResponse().setBody("""{"ok":true}"""))
        providers.add(NewProvider("jv_team", "eg-secret", preset = "example-gateway/team"))
        val request = server.takeRequest()
        assertEquals("POST", (request.method).zhAny())
        assertEquals("/api/v1/launchers", (request.path).zhAny())
        val body = JsonCodec.parseToJsonElement(request.body.readUtf8()).jsonObject
        assertEquals(setOf("name", "api_key", "preset"), body.keys)
        assertEquals("example-gateway/team", (body["preset"]!!.jsonPrimitive.content).zhAny())

        server.enqueue(MockResponse().setResponseCode(400).setBody("""{"error":"没有这个供应商预设（提供它的扩展停用或卸载了？）"}"""))
        val refused = runCatching { providers.add(NewProvider("jv_team", "k", preset = "gone/x")) }.exceptionOrNull() as HubFailure
        assertEquals(400, refused.status)
        assertTrue(refused.message.contains("没有这个供应商预设"))
    }

    // A launcher that is slow to answer (cold CLI, slow gateway): the hub allows the check 100 s and each import step
    // 30 s, so the phone must outwait it (iOS: 110 / 45) rather than fail at the default 15 (#4813).
    @Test fun checkAndImportWaitLongerThanTheDefault() = runBlocking {
        MockWebServer().use { server ->
            val http = OkHttpClient.Builder().retryOnConnectionFailure(false).build()
            val timeouts = mutableMapOf<String, Long?>()
            val providers = HttpProviderSetup { path, method, body, timeout ->
                timeouts["$method ${path.substringBefore('?')}"] = timeout
                http.newCall(Request.Builder().url(server.url(path)).method(method, body).build()).execute()
            }
            server.enqueue(MockResponse().setBody("""{"machine":"m1","check":{}}"""))
            providers.check("jv_team", "m1")
            server.enqueue(MockResponse().setBody("""{"providers":[]}"""))
            providers.importPreview("m1")
            server.enqueue(MockResponse().setBody("""{}"""))
            providers.import("m1", listOf("eg"))
            server.enqueue(MockResponse().setBody("""{"launchers":[]}"""))
            providers.launchers()
            assertEquals(mapOf("POST /api/v1/launchers/jv_team/check" to 110L, "GET /api/v1/launchers/import" to 45L,
                "POST /api/v1/launchers/import" to 45L, "GET /api/v1/launchers" to null), timeouts)
        }
    }

    // Editing (docs/integrations/launchers.md "API"): PUT carries the base fields even when empty (the hub replaces them) and
    // leaves out what did not change, so the stored key, headers and scope stay.
    @Test fun anEditSendsTheBaseFieldsAndOnlyWhatChanged() = withHub { server, providers ->
        server.enqueue(MockResponse().setBody("""{"launcher":{"name":"jv_gw"}}"""))
        providers.update("jv_gw", ProviderUpdate("jv_gw", "launcher", "messages", "https://gateway.example.com", "gw-pro", "", "", "", "", "",
            emptyList(), auth = "bearer", sonnetModel = "gw-mid", headers = "", delivery = "push", machineKeys = mapOf("mac" to "")))
        val request = server.takeRequest()
        assertEquals("PUT", request.method)
        assertEquals("/api/v1/launchers/jv_gw", request.path)
        val body = JsonCodec.parseToJsonElement(request.body.readUtf8()).jsonObject
        assertEquals(setOf("name", "kind", "api", "base_url", "model", "small_model", "subagent_model", "custom_model", "custom_model_name",
            "custom_model_description", "args", "auth", "sonnet_model", "headers", "delivery", "machine_keys"), body.keys)
        assertEquals("", body["small_model"]!!.jsonPrimitive.content)
        assertEquals("", body["headers"]!!.jsonPrimitive.content)
        assertEquals("", body["machine_keys"]!!.jsonObject["mac"]!!.jsonPrimitive.content)
        assertFalse("the key is kept", "api_key" in body)

        server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"这台电脑不在范围内","code":"launcher_out_of_scope"}"""))
        val refused = runCatching { providers.update("jv_gw", ProviderUpdate("jv_gw", "launcher", "messages", "", "m", "", "", "", "", "", emptyList())) }
            .exceptionOrNull() as HubFailure
        assertEquals(409, refused.status)
        assertEquals("launcher_out_of_scope", refused.code)
    }

    @Test fun aManagedProvidersScopeAndParametersAreRead() = withHub { server, providers ->
        server.enqueue(MockResponse().setBody("""{"launchers":[{"name":"jv_gw","source":"persona","agent":"codex","base_url":"https://gw.example.com",
            "model":"gw-pro","models":[{"id":"gw-pro","context_window":1000000,"efforts":["high"],"default_effort":"high"}],"opus_model":"gw-max",
            "proxy":"http://127.0.0.1:7890","timeout_ms":600000,"has_env":true,"share":"selected","scope_machines":["mac"],"delivery":"push",
            "own_service":"use","key_machines":["mac"],"legacy_scope":true}]}"""))
        val l = providers.launchers().single()
        assertEquals("codex", l.harness)
        assertTrue(l.hasScope)
        assertEquals(listOf("mac"), l.scopeMachines)
        assertEquals("push", l.delivery)
        assertEquals("use", l.ownService)
        assertEquals(listOf("mac"), l.keyMachines)
        assertTrue(l.legacyScope)
        assertEquals(PresetModel("gw-pro", contextWindow = 1000000, efforts = listOf("high"), defaultEffort = "high"), l.models.single())
        assertEquals(600000, l.timeoutMs)
        assertEquals("http://127.0.0.1:7890", l.proxy)
    }

    @Test fun referencesAreExplicitOnCreateAndUpdateAndNeverInAProbe() = withHub { server, providers ->
        server.enqueue(MockResponse().setBody("""{"ok":true}"""))
        providers.add(NewProvider("work", null, apiKeyReference = "op://Dev/Work/token",
            machineKeyReferences = mapOf("m-1" to "op://Dev/Work/laptop")))
        var body = JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()).jsonObject
        assertEquals("op://Dev/Work/token", body["api_key_reference"]!!.jsonPrimitive.content)
        assertEquals("op://Dev/Work/laptop", body["machine_key_references"]!!.jsonObject["m-1"]!!.jsonPrimitive.content)
        assertFalse("api_key" in body)
        assertFalse("machine_keys" in body)

        server.enqueue(MockResponse().setBody("""{"ok":true}"""))
        providers.add(NewProvider("literal", "op://looks-like/a/reference"))
        body = JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()).jsonObject
        assertEquals("op://looks-like/a/reference", body["api_key"]!!.jsonPrimitive.content)
        assertFalse("api_key_reference" in body)

        server.enqueue(MockResponse().setBody("""{"launcher":{"name":"work"}}"""))
        providers.update("work", ProviderUpdate("work", "launcher", "messages", "https://gw.example.com", "m", "", "", "", "", "",
            emptyList(), apiKeyReference = "op://Dev/Work/new", machineKeyReferences = mapOf("m-2" to "op://Dev/Work/other")))
        body = JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()).jsonObject
        assertEquals("op://Dev/Work/new", body["api_key_reference"]!!.jsonPrimitive.content)
        assertEquals("op://Dev/Work/other", body["machine_key_references"]!!.jsonObject["m-2"]!!.jsonPrimitive.content)
        assertFalse("api_key" in body)
        assertFalse("machine_keys" in body)

        server.enqueue(MockResponse().setBody("""{"ok":true}"""))
        providers.test(ProviderProbe(name = "work", baseUrl = "https://gw.example.com"))
        body = JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()).jsonObject
        assertFalse("api_key_reference" in body)
        assertFalse("machine_key_references" in body)
    }

    @Test fun storedKeyReferencesDecodeAndOldHubsStayEmpty() = withHub { server, providers ->
        server.enqueue(MockResponse().setBody("""{"launchers":[
            {"name":"work","source":"persona","has_key":true,"key_reference":{"provider":"example-vault","provider_name":"Example Vault","reference":"op://Dev/Work/token","title":"Work token"},"machine_key_references":{"m-1":{"provider":"example-vault","provider_name":"Example Vault","reference":"op://Dev/Work/laptop","title":"Laptop token"}}},
            {"name":"old","source":"persona","has_key":true}
        ]}"""))
        val list = providers.launchers()
        assertEquals("Example Vault", list[0].keyReference!!.providerLabel)
        assertEquals("Work token", list[0].keyReference!!.title)
        assertEquals("op://Dev/Work/laptop", list[0].machineKeyReferences["m-1"]!!.reference)
        assertNull(list[1].keyReference)
        assertTrue(list[1].machineKeyReferences.isEmpty())
    }

    @Test fun aProbeOfAStoredProviderGoesByName() = withHub { server, providers ->
        server.enqueue(MockResponse().setBody("""{"ok":true,"ms":12}"""))
        providers.test(ProviderProbe(baseUrl = "https://gw.example.com", auth = "bearer", model = "m", name = "jv_gw", headers = ""))
        val body = JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()).jsonObject
        assertEquals("jv_gw", body["name"]!!.jsonPrimitive.content)
        // "" sends no headers for this request, over the stored ones.
        assertEquals("", body["headers"]!!.jsonPrimitive.content)
        assertFalse("api_key" in body)
    }
}
