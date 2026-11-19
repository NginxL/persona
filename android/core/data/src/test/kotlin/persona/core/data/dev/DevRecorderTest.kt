package persona.core.data.dev

import java.io.File
import java.nio.file.Files
import java.time.Instant
import java.util.concurrent.CountDownLatch
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.AbstractExecutorService
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.double
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.long
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.generated.ExtensionErrorReport

class DevRecorderTest {
    private val directory: File = Files.createTempDirectory("dev-recorder").toFile()
    private val fake = Fake()
    private val recorder = DevRecorder(DevStore(directory), fake::now, { fake.cpu }, "512")
    @After fun cleanUp() { recorder.close(); directory.deleteRecursively() }

    @Test fun aMinuteCountsRoutesFramesPagesAndCpu() {
        recorder.start(active = true, page = "assistant", DevDeviceState(battery = 0.8))
        recorder.path(DevNetPath("cellular", expensive = true))
        val url = "https://hub.example.com/api/v1/machines/m1/sessions/s1/items?since=4"
        recorder.request("get", url, 200, 1000, 100, 30)
        recorder.request("GET", url, 200, 500, 100, 50)
        recorder.request("GET", "https://hub.example.com/api/v1/sessions", 502, 10, 10, 900)
        recorder.request("GET", "https://hub.example.com/api/v1/sessions", 502, 10, 10, 900)
        recorder.frame("sessions", 300)
        recorder.frame("sessions", 300)
        recorder.frame("item", 50)
        recorder.socket("dropped")
        recorder.socket("connected")
        fake.advance(20, 50)
        recorder.page("chat")
        fake.advance(40, 200)
        recorder.roll(DevDeviceState(thermal = "fair", battery = 0.7, lowPower = true))
        recorder.settle()

        val events = allEvents()
        assertTrue("a foreground minute is recorded", events.any { it.string("kind") == "minute" })
        val minute = events.first { it.string("kind") == "minute" }
        assertEquals(250, minute.int("cpu_ms"))
        assertEquals("fair", minute.string("thermal"))
        assertEquals(true, minute.value("low_power").jsonPrimitive.content.toBoolean())
        assertEquals(0.7, minute.value("battery").jsonPrimitive.double, 0.0)
        assertEquals(mapOf("assistant" to 20, "chat" to 40), minute.value("pages").jsonObject.mapValues { it.value.jsonPrimitive.int })
        assertEquals("cellular", minute.value("net").jsonObject.string("type"))
        assertEquals(true, minute.value("net").jsonObject.value("expensive").jsonPrimitive.content.toBoolean())

        val http = minute.value("http").jsonArray.map { it.jsonObject }
        val items = http.single { it.string("route") == "GET /machines/:id/sessions/:id/items" }
        assertEquals(2, items.int("n"))
        assertEquals(1500, items.int("in"))
        assertEquals(200, items.int("out"))
        assertEquals(50, items.int("p50_ms"))
        assertEquals(50, items.int("max_ms"))
        assertEquals(2, items.value("status").jsonObject.int("200"))
        assertEquals(4, minute.int("requests"))

        val ws = minute.value("ws").jsonObject
        assertEquals(2, ws.value("frames").jsonObject.int("sessions"))
        assertEquals(1, ws.value("frames").jsonObject.int("item"))
        assertEquals(650, ws.int("bytes"))
        assertEquals(1, ws.int("connects"))
        assertEquals(1, ws.int("drops"))
        assertEquals(1, events.count { it.string("kind") == "request_error" })
        assertEquals("GET /sessions", events.single { it.string("kind") == "request_error" }.string("route"))
        assertTrue(events.any { it.string("kind") == "page" && it.string("page") == "chat" })
        assertFalse(events.toString().contains("m1"))
        assertFalse(events.toString().contains("since=4"))
    }

    @Test fun foregroundAndBackgroundStretchesCloseWithTheirOwnDeltas() {
        recorder.start(active = true, page = "assistant", DevDeviceState(thermal = "nominal", battery = 0.8))
        fake.advance(60, 300)
        recorder.lifecycle(false, DevDeviceState(thermal = "fair", battery = 0.7))
        fake.advance(120, 50)
        recorder.lifecycle(true, DevDeviceState(thermal = "serious", battery = 0.6))
        recorder.settle()

        val events = allEvents()
        assertTrue("the foreground period is recorded", events.any { it.string("kind") == "foreground" })
        val foreground = events.first { it.string("kind") == "foreground" }
        assertEquals(60, foreground.int("seconds"))
        assertEquals(0.3, foreground.value("cpu_s").jsonPrimitive.double, 0.0)
        assertEquals("nominal", foreground.value("thermal").jsonObject.string("start"))
        assertEquals("fair", foreground.value("thermal").jsonObject.string("end"))
        assertEquals(0.8, foreground.value("battery").jsonObject.value("start").jsonPrimitive.double, 0.0)
        assertEquals(0.7, foreground.value("battery").jsonObject.value("end").jsonPrimitive.double, 0.0)
        assertEquals(mapOf("assistant" to 60), foreground.value("pages").jsonObject.mapValues { it.value.jsonPrimitive.int })

        assertTrue("the background stretch is recorded", events.any { it.string("kind") == "minute" && it.value("background").jsonPrimitive.content.toBoolean() })
        val background = events.first { it.string("kind") == "minute" && it.value("background").jsonPrimitive.content.toBoolean() }
        assertEquals(50, background.int("cpu_ms"))
        assertEquals("serious", background.string("thermal"))
        assertEquals(120, java.time.Duration.between(Instant.parse(background.string("from")), Instant.parse(background.string("to"))).seconds)
        assertTrue(events.any { it.string("kind") == "app" && it.string("event") == "background" })
        assertTrue(events.any { it.string("kind") == "app" && it.string("event") == "foreground" })
    }

    @Test fun storageFailuresStayInsideWriterTasks() {
        val uncaught = CopyOnWriteArrayList<Throwable>()
        val previous = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { _, error -> uncaught += error }
        try {
            val events = DevRecorder(DevStore(File(directory, "event-store").apply { writeText("x") }), fake::now, { fake.cpu }, "512")
            events.start(active = true, page = "assistant", device = DevDeviceState())
            events.flush()
            val eventsDone = CountDownLatch(1)
            events.afterWrites { eventsDone.countDown() }
            assertTrue("event writer finished", eventsDone.await(5, TimeUnit.SECONDS))
            events.close()

            val errors = DevRecorder(DevStore(File(directory, "error-store").apply { writeText("x") }), fake::now, { fake.cpu }, "512")
            errors.start(active = true, page = "assistant", device = DevDeviceState())
            errors.error(ExtensionErrorReport(id = "ordinary", time = fake.now().toString()))
            val errorsDone = CountDownLatch(1)
            errors.afterWrites { errorsDone.countDown() }
            assertTrue("error writer finished", errorsDone.await(5, TimeUnit.SECONDS))
            errors.close()

            assertTrue("writer exceptions escaped: $uncaught", uncaught.isEmpty())
        } finally {
            Thread.setDefaultUncaughtExceptionHandler(previous)
        }
    }

    @Test fun stoppingDropsBufferedRecordsAndForgetsTheSink() {
        recorder.start(active = true, page = "assistant", DevDeviceState())
        recorder.request("GET", "/api/v1/sessions", 200, 1, 1, 1)
        recorder.frame("sessions", 1)
        recorder.stop()
        recorder.request("GET", "/api/v1/sessions", 200, 1, 1, 1)
        recorder.frame("sessions", 1)
        recorder.error(ExtensionErrorReport(id = "off", time = fake.now().toString()))
        recorder.settle()

        assertFalse(recorder.isOn)
        assertTrue(allEvents().isEmpty())
        assertTrue(recorder.store.errorLines(fake.now()).isEmpty())
        assertTrue(recorder.store.eventFiles().isEmpty())
    }

    @Test fun aWriteAuthorizedBeforeStopCannotLandAfterTheStoreIsCleared() {
        val executor = GatedExecutor()
        val gated = DevRecorder(DevStore(File(directory, "gated")), fake::now, { fake.cpu }, "512", executor)
        val failure = AtomicReference<Throwable?>()
        try {
            gated.start(active = true, page = "assistant", device = DevDeviceState())
            executor.pauseNextWrite()
            val caller = Thread {
                try { gated.error(ExtensionErrorReport(id = "late", time = fake.now().toString())) }
                catch (error: Throwable) { failure.set(error) }
            }
            caller.start()
            assertTrue("the caller passed the enabled check", executor.entered.await(5, TimeUnit.SECONDS))
            gated.stop()
            gated.settle()
            gated.store.clear()
            executor.release.countDown()
            caller.join(5_000)
            gated.settle()

            assertNull(failure.get())
            assertTrue("a stale write recreated records after deletion", gated.store.errorLines(fake.now()).isEmpty())
        } finally {
            executor.release.countDown()
            gated.close()
        }
    }

    private fun allEvents(): List<JsonObject> = recorder.store.eventFiles().flatMap { file ->
        file.readLines().filter(String::isNotBlank).map { JsonCodec.parseToJsonElement(it).jsonObject }
    }

    private class GatedExecutor : AbstractExecutorService() {
        private val delegate = Executors.newSingleThreadExecutor()
        private val armed = AtomicBoolean()
        val entered = CountDownLatch(1)
        val release = CountDownLatch(1)

        fun pauseNextWrite() { armed.set(true) }
        override fun execute(command: Runnable) {
            if (armed.compareAndSet(true, false)) {
                entered.countDown()
                release.await(5, TimeUnit.SECONDS)
            }
            delegate.execute(command)
        }
        override fun shutdown() = delegate.shutdown()
        override fun shutdownNow(): List<Runnable> = delegate.shutdownNow()
        override fun isShutdown() = delegate.isShutdown
        override fun isTerminated() = delegate.isTerminated
        override fun awaitTermination(timeout: Long, unit: TimeUnit) = delegate.awaitTermination(timeout, unit)
    }

    private class Fake {
        private var instant: Instant = Instant.parse("2026-11-17T12:00:00Z")
        var cpu: Long = 1000
        fun now(): Instant = instant
        fun advance(seconds: Long, cpuMillis: Long) { instant = instant.plusSeconds(seconds); cpu += cpuMillis }
    }
}

private fun JsonObject.value(key: String) = getValue(key)
private fun JsonObject.string(key: String) = value(key).jsonPrimitive.content
private fun JsonObject.int(key: String) = value(key).jsonPrimitive.int
private fun JsonObject.long(key: String) = value(key).jsonPrimitive.long
