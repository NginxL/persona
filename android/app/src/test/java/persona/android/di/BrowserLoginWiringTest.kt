package persona.android.di

import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.net.NetworkInfo
import android.security.keystore.KeyGenParameterSpec
import androidx.arch.core.executor.ArchTaskExecutor
import androidx.arch.core.executor.TaskExecutor
import androidx.test.core.app.ApplicationProvider
import java.net.InetAddress
import java.security.Security
import java.util.concurrent.ConcurrentLinkedQueue
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.shadows.ShadowNetworkCapabilities
import org.robolectric.shadows.ShadowNetworkInfo
import persona.android.PersonaApp
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.BrowserLoginDetails
import persona.core.data.HubCredentials
import persona.core.data.HubDataStore
import persona.core.data.StoredScope
import persona.core.data.addPairedHub

/** The live app's adapter must not drop browserLogin behind DeviceSettings's default implementation (#10319). */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = PersonaApp::class, shadows = [ShadowConnectionRuntimePersonaApp::class])
class BrowserLoginWiringTest {
    private val app: PersonaApp = ApplicationProvider.getApplicationContext()
    private var runtime: ConnectionRuntime? = null

    @Before fun setUpRuntime() {
        Security.addProvider(FakeAndroidKeyStoreProvider())
        ArchTaskExecutor.getInstance().setDelegate(object : TaskExecutor() {
            override fun executeOnDiskIO(runnable: Runnable) = runnable.run()
            override fun executeOnMainThread(runnable: Runnable) = runnable.run()
            override fun postToMainThread(runnable: Runnable) = runnable.run()
            override fun isMainThread() = true
        })
        val manager = app.getSystemService(ConnectivityManager::class.java)
        shadowOf(manager).setActiveNetworkInfo(ShadowNetworkInfo.newInstance(
            NetworkInfo.DetailedState.CONNECTED, ConnectivityManager.TYPE_WIFI, 0, true, NetworkInfo.State.CONNECTED))
        val capabilities = ShadowNetworkCapabilities.newInstance()
        shadowOf(capabilities).addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
        shadowOf(manager).setNetworkCapabilities(manager.activeNetwork, capabilities)
    }

    @After fun closeRuntime() {
        runtime?.groups?.value?.forEach { it.close() }
        runtime?.let { runBlocking { it.dataStore.close() } }
        ArchTaskExecutor.getInstance().setDelegate(null)
        Security.removeProvider("AndroidKeyStore")
        app.scope.cancel()
    }

    @Test fun realAdapterForwardsInspectionAllowAndCancelAndReturnsTheirResponses() = runBlocking {
        Server().use { server ->
            savePairing(server.hub.url("/").toString())
            val runtime = ConnectionRuntime(app).also { runtime = it }
            val wired = WiredDeviceSettings(app, runtime,
                StoredTranslationSettings(app.getSharedPreferences("browser-login-wiring-test", 0)))
            runtime.start()
            runtime.settled()
            val api = withTimeout(8_000) { wired.binding.first { it?.deviceSettings != null }!!.deviceSettings!! }

            val inspected = api.browserLogin(server.id, "test-hub")
            val allowed = api.browserLogin(server.id, "test-hub", allow = true)
            val cancelled = api.browserLogin(server.id, "test-hub", allow = false)

            assertEquals("Chrome · Mac", inspected.name)
            assertEquals("47", field(allowed, "number"))
            assertEquals("denied", field(cancelled, "state"))
            val requests = server.approvals.toList()
            assertEquals("inspection, Allow and Cancel must all reach the real HTTP adapter", 3, requests.size)
            val bodies = requests.map { JsonCodec.parseToJsonElement(it.body.peek().readUtf8()).jsonObject }
            assertEquals(listOf(null, true, false), bodies.map { it["allow"]?.jsonPrimitive?.booleanOrNull })
            assertTrue(bodies.all { it["id"]?.jsonPrimitive?.content == server.id && it["hub"]?.jsonPrimitive?.content == "test-hub" })
            assertNull(bodies[0]["show_number"])
            assertEquals(true, bodies[1]["show_number"]?.jsonPrimitive?.booleanOrNull)
            assertNull(bodies[2]["show_number"])
        }
    }

    private suspend fun savePairing(address: String) {
        val parent = CoroutineScope(SupervisorJob() + Dispatchers.IO)
        val store = HubDataStore(app, parent) { _, _ -> error("The fixture only writes pairing data") }
        try {
            val scope = store.storeHub(HubCredentials(address, "test-hub", null, emptyList(), "test-token", deviceId = "device-123"))
            store.change { it.addPairedHub(StoredScope.of(scope), null, 1) }
        } finally {
            store.close()
            parent.cancel()
        }
    }

    private fun field(details: BrowserLoginDetails, name: String): Any? {
        val field = details.javaClass.declaredFields.firstOrNull { it.name == name }
            ?: throw AssertionError("BrowserLoginDetails.$name is missing")
        return field.apply { isAccessible = true }.get(details)
    }

    private class Server : AutoCloseable {
        val id = "a".repeat(40) + "b-_"
        val approvals = ConcurrentLinkedQueue<RecordedRequest>()
        val hub = MockWebServer().apply {
            dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse = when (request.requestUrl!!.encodedPath) {
                    "/api/v1/health" -> MockResponse().setBody("""{"ok":true,"hub_id":"test-hub"}""")
                    "/api/v1/ws" -> MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                        override fun onOpen(webSocket: WebSocket, response: Response) {
                            webSocket.send("""{"type":"hello","hub_id":"test-hub","features":[],"endpoints":[]}""")
                        }
                    })
                    "/api/v1/auth/browser-login/approve" -> {
                        approvals += request
                        val body = JsonCodec.parseToJsonElement(request.body.peek().readUtf8()).jsonObject
                        val allow = body["allow"]?.jsonPrimitive?.booleanOrNull
                        val state = when (allow) {
                            true -> "confirm"
                            false -> "denied"
                            null -> "scanned"
                        }
                        val number = if (allow == true) ""","number":"47"""" else ""
                        MockResponse().setBody("""{"name":"Chrome · Mac","state":"$state","source":"203.0.113.77","network":"lan","age_seconds":12$number}""")
                    }
                    "/api/v1/devices/device-123/prefs" -> MockResponse().setBody("""{"input":true,"done":"long"}""")
                    else -> MockResponse().setBody("{}")
                }
            }
            start(InetAddress.getLoopbackAddress(), 0)
        }

        override fun close() = hub.close()
    }
}
