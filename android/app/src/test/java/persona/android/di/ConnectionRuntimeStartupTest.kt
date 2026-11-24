package persona.android.di

import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.net.NetworkInfo
import android.security.keystore.KeyGenParameterSpec
import androidx.arch.core.executor.ArchTaskExecutor
import androidx.arch.core.executor.TaskExecutor
import androidx.test.core.app.ApplicationProvider
import java.io.InputStream
import java.io.OutputStream
import java.security.Key
import java.security.KeyStore
import java.security.KeyStoreSpi
import java.security.Provider
import java.security.SecureRandom
import java.security.Security
import java.security.cert.Certificate
import java.security.spec.AlgorithmParameterSpec
import java.util.Collections
import java.util.Date
import java.util.Enumeration
import java.util.concurrent.TimeUnit
import javax.crypto.KeyGeneratorSpi
import javax.crypto.SecretKey
import javax.crypto.spec.SecretKeySpec
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.cancel
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.Implementation
import org.robolectric.annotation.Implements
import org.robolectric.annotation.RealObject
import org.robolectric.shadows.ShadowApplication
import org.robolectric.shadows.ShadowNetworkCapabilities
import org.robolectric.shadows.ShadowNetworkInfo
import persona.android.PersonaApp
import persona.android.data.PairingStore
import persona.core.contracts.Hello
import persona.core.contracts.HubScope
import persona.core.contracts.ServerEvent
import persona.core.data.HubCredentials
import persona.core.data.HubDataStore
import persona.core.data.StoredGroup
import persona.core.data.StoredHub
import persona.core.data.StoredScope
import persona.core.data.addPairedHub
import persona.core.hub.HeartbeatPolicy
import persona.core.hub.HubNetwork
import persona.core.hub.HubNetworkTransport
import persona.core.hub.OkHttpHubConnection
import kotlin.concurrent.thread

/** Startup restores pairings off the caller and starts each restored connection on the network Android already knows (#4055). */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = PersonaApp::class, shadows = [ShadowConnectionRuntimePersonaApp::class])
class ConnectionRuntimeStartupTest {
    private val app: PersonaApp = ApplicationProvider.getApplicationContext()

    @Before fun allowStartupOnTheTestControlledCaller() {
        fakeAndroidKeys.clear()
        Security.addProvider(FakeAndroidKeyStoreProvider())
        ArchTaskExecutor.getInstance().setDelegate(object : TaskExecutor() {
            override fun executeOnDiskIO(runnable: Runnable) = runnable.run()
            override fun executeOnMainThread(runnable: Runnable) = runnable.run()
            override fun postToMainThread(runnable: Runnable) = runnable.run()
            override fun isMainThread() = true
        })
    }

    @After fun resetTaskExecutor() {
        ArchTaskExecutor.getInstance().setDelegate(null)
        Security.removeProvider("AndroidKeyStore")
        fakeAndroidKeys.clear()
        app.scope.cancel()
    }

    @Test fun startReturnsWhileRestoreIsPausedAndSettledWaitsForTheGroups() = runBlocking {
        savePairing()
        val runtime = ConnectionRuntime(app)
        val restoreLock = Mutex().also { it.lock() }
        val restoreStarted = CompletableDeferred<Unit>()
        val pausingLock = object : Mutex by restoreLock {
            override suspend fun lock(owner: Any?) {
                restoreStarted.complete(Unit)
                restoreLock.lock(owner)
            }
        }
        field(runtime.dataStore, "lock", pausingLock)
        val startFinished = CompletableDeferred<Throwable?>()
        val _ = thread(isDaemon = true, name = "connection-runtime-start") {
            startFinished.complete(runCatching { runtime.start() }.exceptionOrNull())
        }
        var released = false
        try {
            withTimeout(5_000) { restoreStarted.await() }
            val returnedWhilePaused = withTimeoutOrNull(5_000) {
                assertNull(startFinished.await())
                true
            } == true
            val settled = async(start = CoroutineStart.UNDISPATCHED) { runtime.settled() }
            val settledWhilePaused = settled.isCompleted

            restoreLock.unlock()
            released = true
            assertNull(withTimeout(5_000) { startFinished.await() })
            withTimeout(5_000) { settled.await() }

            assertTrue("start() must return while registry restore is paused", returnedWhilePaused)
            assertFalse("settled() must not complete before restore installs the groups", settledWhilePaused)
            assertEquals(1, runtime.groups.value.size)
            assertEquals("test-hub", runtime.activeCredentials()?.hubId)
        } finally {
            if (!released && restoreLock.isLocked) restoreLock.unlock()
            runtime.groups.value.forEach { it.close() }
            runtime.dataStore.close()
        }
    }

    @Test fun restoredTransportStartsOnActiveNetworkBeforeACallback() {
        val manager = app.getSystemService(ConnectivityManager::class.java)
        shadowOf(manager).setActiveNetworkInfo(ShadowNetworkInfo.newInstance(
            NetworkInfo.DetailedState.CONNECTED, ConnectivityManager.TYPE_WIFI, 0, true, NetworkInfo.State.CONNECTED))
        val active = manager.activeNetwork
        val capabilities = ShadowNetworkCapabilities.newInstance()
        shadowOf(capabilities).addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
        shadowOf(manager).setNetworkCapabilities(active, capabilities)
        val group = group()
        try {
            group.createTransport(HubScope("test-hub", "generation"), credentials(), members = emptyList())
            assertEquals(HubNetwork(active.toString(), available = true, transport = HubNetworkTransport.Wifi),
                network(group.connection))
            group.updateNetwork(HubNetwork("refreshed", available = false))
            assertEquals(HubNetwork("refreshed", available = false), network(group.connection))
        } finally { group.close() }
    }

    @Test fun restoredTransportStartsOfflineBeforeACallbackWhenThereIsNoActiveNetwork() {
        val manager = app.getSystemService(ConnectivityManager::class.java)
        shadowOf(manager).setActiveNetworkInfo(null)
        assertNull(manager.activeNetwork)
        val group = group()
        try {
            group.createTransport(HubScope("test-hub", "generation"), credentials(), members = emptyList())
            assertEquals(HubNetwork("offline", available = false), network(group.connection))
        } finally { group.close() }
    }

    @Test fun transportDoesNotDialBeforeActivate() = runBlocking {
        val manager = app.getSystemService(ConnectivityManager::class.java)
        shadowOf(manager).setActiveNetworkInfo(ShadowNetworkInfo.newInstance(
            NetworkInfo.DetailedState.CONNECTED, ConnectivityManager.TYPE_WIFI, 0, true, NetworkInfo.State.CONNECTED))
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setResponseCode(500))
            server.start()
            val group = group()
            try {
                group.createTransport(HubScope("test-hub", "generation"), credentials(server.url("/").toString()), members = emptyList())
                val early = withContext(Dispatchers.IO) { server.takeRequest(250, TimeUnit.MILLISECONDS) }
                assertNull("A restored connection must wait for activate() before dialing", early)

                // activate() uses this same public reconnect entry point after installing data and collectors.
                group.connection.reconnect()
                val requested = withContext(Dispatchers.IO) { server.takeRequest(5, TimeUnit.SECONDS) }
                assertTrue("The post-activation reconnect must reach the hub", requested != null)
            } finally { group.close() }
        }
    }

    private suspend fun savePairing() {
        val parent = CoroutineScope(SupervisorJob() + Dispatchers.IO)
        val store = HubDataStore(app, parent) { _, _ -> error("The fixture only writes pairing data") }
        try {
            val scope = store.storeHub(credentials())
            store.change { it.addPairedHub(StoredScope.of(scope), null, 1) }
        } finally {
            store.close()
            parent.cancel()
        }
    }

    private fun group() = GroupRuntime(
        StoredGroup("g00000001", storage = StoredScope("test-hub", "generation"),
            hubs = listOf(StoredHub("test-hub", "generation"))),
        namespace = null,
        app = app,
        settings = GroupConnectionSettings("test", JsonPrimitive(1), buildJsonObject { }, HeartbeatPolicy(), 1_000),
        listener = object : GroupRuntime.Listener {
            override suspend fun onEvent(group: GroupRuntime, event: ServerEvent) = Unit
            override fun onHello(group: GroupRuntime, hello: Hello) = Unit
            override fun onRevoked(group: GroupRuntime) = Unit
            override fun onMemberRevoked(group: GroupRuntime, hubId: String, ownToken: Boolean) = Unit
        },
    )

    private fun credentials(address: String = "https://127.0.0.1") =
        HubCredentials(address, "test-hub", null, emptyList(), "test-token")

    private fun network(connection: OkHttpHubConnection): HubNetwork =
        OkHttpHubConnection::class.java.getDeclaredField("network").apply { isAccessible = true }.get(connection) as HubNetwork

    private fun field(target: Any, name: String, value: Any) {
        target.javaClass.getDeclaredField(name).apply { isAccessible = true }.set(target, value)
    }
}

/** Avoids Hilt and the other application runtimes; ConnectionRuntime is the unit under test. */
@Implements(PersonaApp::class)
class ShadowConnectionRuntimePersonaApp : ShadowApplication() {
    @RealObject private lateinit var app: PersonaApp

    @Implementation fun onCreate() {
        PersonaApp::class.java.getDeclaredField("pairings").apply { isAccessible = true }.set(app, PairingStore(app))
    }
}

/** Robolectric supplies no AndroidKeyStore provider; only key storage/generation is faked, not encryption. */
private val fakeAndroidKeys = mutableMapOf<String, SecretKey>()

class FakeAndroidKeyStoreProvider : Provider("AndroidKeyStore", 1.0, "In-memory test keys") {
    init {
        put("KeyStore.AndroidKeyStore", FakeAndroidKeyStoreSpi::class.java.name)
        put("KeyGenerator.AES", FakeAndroidKeyGeneratorSpi::class.java.name)
    }
}

class FakeAndroidKeyGeneratorSpi : KeyGeneratorSpi() {
    private var alias: String? = null
    override fun engineInit(random: SecureRandom?) = Unit
    override fun engineInit(keysize: Int, random: SecureRandom?) = Unit
    override fun engineInit(params: AlgorithmParameterSpec?, random: SecureRandom?) {
        alias = (params as KeyGenParameterSpec).keystoreAlias
    }
    override fun engineGenerateKey(): SecretKey = SecretKeySpec(ByteArray(32) { it.toByte() }, "AES").also {
        fakeAndroidKeys[checkNotNull(alias)] = it
    }
}

class FakeAndroidKeyStoreSpi : KeyStoreSpi() {
    override fun engineGetKey(alias: String?, password: CharArray?): Key? = alias?.let(fakeAndroidKeys::get)
    override fun engineGetCertificateChain(alias: String?): Array<Certificate>? = null
    override fun engineGetCertificate(alias: String?): Certificate? = null
    override fun engineGetCreationDate(alias: String?): Date? = null
    override fun engineSetKeyEntry(alias: String?, key: Key?, password: CharArray?, chain: Array<Certificate>?) =
        throw UnsupportedOperationException()
    override fun engineSetKeyEntry(alias: String?, key: ByteArray?, chain: Array<Certificate>?) =
        throw UnsupportedOperationException()
    override fun engineSetCertificateEntry(alias: String?, cert: Certificate?) = throw UnsupportedOperationException()
    override fun engineDeleteEntry(alias: String?) { alias?.let(fakeAndroidKeys::remove) }
    override fun engineAliases(): Enumeration<String> = Collections.enumeration(fakeAndroidKeys.keys)
    override fun engineContainsAlias(alias: String?) = alias != null && fakeAndroidKeys.containsKey(alias)
    override fun engineSize() = fakeAndroidKeys.size
    override fun engineIsKeyEntry(alias: String?) = engineContainsAlias(alias)
    override fun engineIsCertificateEntry(alias: String?) = false
    override fun engineGetCertificateAlias(cert: Certificate?): String? = null
    override fun engineStore(stream: OutputStream?, password: CharArray?) = throw UnsupportedOperationException()
    override fun engineLoad(stream: InputStream?, password: CharArray?) = Unit
    override fun engineLoad(param: KeyStore.LoadStoreParameter?) = Unit
}
