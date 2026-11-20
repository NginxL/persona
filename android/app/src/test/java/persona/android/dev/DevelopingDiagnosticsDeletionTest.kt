package persona.android.dev

import android.app.Application
import androidx.test.core.app.ApplicationProvider
import java.io.File
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.After
import org.junit.Assert.assertFalse
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import persona.core.contracts.DevProbe

@RunWith(RobolectricTestRunner::class)
class DevelopingDiagnosticsDeletionTest {
    private val context = ApplicationProvider.getApplicationContext<Application>()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private val caller = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private lateinit var dev: DevelopingPersona

    @After fun cleanUp() {
        if (::dev.isInitialized) {
            dev.store.directory.deleteRecursively()
            dev.close()
        }
        File(context.cacheDir, "dev-exports").deleteRecursively()
        caller.cancel()
        scope.cancel()
        DevProbe.install(null)
    }

    @Test fun cancellingTheCallerDuringDisableStillDeletesTheStoreAndExports() = runBlocking {
        val gate = DeletionGate { !dev.state.value.enabled }
        dev = DevelopingPersona(context, scope, io = gate, observeSystem = false)
        dev.start()
        dev.setEnabled(true)
        val store = dev.store.directory
        val exports = File(context.cacheDir, "dev-exports")
        File(store, "events").apply { mkdirs() }.resolve("stale.jsonl").writeText("{}\n")
        exports.apply { mkdirs() }.resolve("stale.zip").writeBytes(byteArrayOf(1))

        val disabling = caller.launch { dev.setEnabled(false) }
        withTimeout(5_000) { while (!gate.entered) delay(1) }
        caller.cancel()
        gate.release.countDown()
        disabling.join()

        assertFalse("the store must be deleted even after the caller leaves", store.exists())
        assertFalse("export archives must be deleted even after the caller leaves", exports.exists())
    }

    private class DeletionGate(private val deletion: () -> Boolean) : CoroutineDispatcher() {
        @Volatile var entered = false
        val release = CountDownLatch(1)

        override fun dispatch(context: kotlin.coroutines.CoroutineContext, block: Runnable) {
            if (!deletion()) {
                Dispatchers.Default.dispatch(context, block)
                return
            }
            Dispatchers.Default.dispatch(context) {
                entered = true
                release.await(5, TimeUnit.SECONDS)
                block.run()
            }
        }
    }
}
