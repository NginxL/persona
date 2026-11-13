package persona.core.rustcore

import android.os.Build
import android.system.Os
import android.system.OsConstants
import android.util.Log
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File
import java.util.concurrent.CopyOnWriteArrayList
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.rustcore.wire.Action
import persona.core.rustcore.wire.ChatEvent
import persona.core.rustcore.wire.ChatSnapshot
import persona.core.rustcore.wire.Config
import persona.core.rustcore.wire.ListOp
import persona.core.rustcore.wire.WireBatch

/** A release-test-APK diagnostic, not a physical-device or production-screen performance acceptance result. */
class RustCoreDeviceBenchmark {
    private class Host : CoreHost {
        val versions = CopyOnWriteArrayList<Long>()
        override fun changed(version: Long) { versions.add(version) }
        override fun run(effect: persona.core.rustcore.wire.Effect) = Unit
    }

    @Test fun dispatchAndFrameCosts() {
        val host = Host()
        val chat = "benchmark/continuity"
        RustCore(Config(), host).use { core ->
            assertTrue(core.dispatch(Action.OpenChat(chat, ChatSnapshot("[]", null, 0, null, false))))
            assertTrue("warm-up flush", core.flush(5_000))
            val dispatch = LongArray(SAMPLES)
            val frame = LongArray(SAMPLES)
            val list = mutableListOf<String>()
            // Reconnected performs real bincode encoding and the public JNI dispatch, including a host effect, without
            // worker list mutation. Worker synchronization is outside both timed spans.
            for (index in 0 until SAMPLES + WARM_UP) {
                val start = System.nanoTime()
                assertTrue(core.dispatch(Action.Chat(chat, ChatEvent.Reconnected)))
                val dispatchNanos = System.nanoTime() - start
                assertTrue("flush before next dispatch", core.flush(5_000))
                if (index >= WARM_UP) dispatch[index - WARM_UP] = dispatchNanos
            }
            // Each frame has a real patch, not an empty drain after the first duplicate item. The fresh snapshot
            // replaces one bounded item; flush and snapshot encoding are deliberately outside the frame span.
            val snapshot = ChatSnapshot(
                """[{"id":"benchmark-item","kind":"text","text":"benchmark","seq":1}]""",
                "history", 1, null, true,
            )
            for (index in 0 until SAMPLES + WARM_UP) {
                assertTrue(core.dispatch(Action.OpenChat(chat, snapshot)))
                assertTrue("flush before drain", core.flush(5_000))
                list.clear()
                val frameStart = System.nanoTime()
                val patch = core.drain(chat)
                for (op in patch.ops) {
                    when (op) {
                        is ListOp.Remove -> list.removeAt(op.index.toInt())
                        is ListOp.Insert -> list.add(op.index.toInt(), op.json)
                        is ListOp.Update -> list[op.index.toInt()] = op.json
                    }
                }
                val frameNanos = System.nanoTime() - frameStart
                check(patch.ops.isNotEmpty()) { "benchmark measured an empty patch" }
                check(list.single().contains("benchmark-item"))
                if (index >= WARM_UP) frame[index - WARM_UP] = frameNanos
            }
            assertEquals(1, list.size)
            assertTrue("the real worker emitted patches", host.versions.isNotEmpty())
            val context = InstrumentationRegistry.getInstrumentation().targetContext
            val nonce = InstrumentationRegistry.getArguments().getString("rustCoreBenchmarkNonce")
                ?: error("missing per-run benchmark nonce")
            val metadata = JSONObject()
                .put("nonce", nonce)
                .put("scope", "optimized instrumentation APK; emulator diagnostics, not physical-device acceptance")
                .put("samples", SAMPLES).put("warmUp", WARM_UP)
                .put("model", Build.MODEL).put("device", Build.DEVICE)
                .put("abi", Build.SUPPORTED_ABIS.firstOrNull()).put("api", Build.VERSION.SDK_INT)
                .put("build", Build.DISPLAY).put("kernelPageBytes", pageSize())
                .put("dispatchMicros", statistics(dispatch))
                .put("drainDecodeApplyMicros", statistics(frame))
            val report = File(context.getExternalFilesDir(null), "rust-core-benchmark.json")
            report.writeText(metadata.toString(2) + "\n")
            Log.i("RustCoreBenchmark", metadata.toString())
            println("RustCoreBenchmark: $metadata")
        }
    }

    private fun pageSize(): Long = try { Os.sysconf(OsConstants._SC_PAGESIZE) } catch (_: Exception) { -1 }

    private fun statistics(values: LongArray): JSONObject {
        val sorted = values.sorted()
        fun percentile(percent: Double): Double = sorted[((sorted.size - 1) * percent).toInt()] / 1_000.0
        return JSONObject()
            .put("p50", percentile(.50)).put("p99", percentile(.99))
            .put("max", sorted.last() / 1_000.0)
    }

    companion object {
        private const val SAMPLES = 1_000
        private const val WARM_UP = 100
    }
}
