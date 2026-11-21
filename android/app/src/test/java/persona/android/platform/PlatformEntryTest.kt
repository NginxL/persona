package persona.android.platform

import persona.core.contracts.JsonCodec
import persona.core.contracts.LockTestEvent
import persona.core.contracts.ServerEvent
import persona.core.contracts.SessionKey
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File
import java.io.IOException

class PlatformEntryTest {
    @get:Rule val temporary = TemporaryFolder()

    @Test fun offlineSharesSurviveRestartAndResumeAttachmentsBeforeOneMainSend() = runTest {
        val directory = temporary.newFolder()
        val first = File(directory, "first.bin").apply { writeText("first attachment") }
        val second = File(directory, "second.bin").apply { writeText("second attachment") }
        val file = File(directory, "entry.json")
        val entry = QueuedShare("stable-id", "文件", "", listOf(
            SharedAttachment(first.name, "first.txt"), SharedAttachment(second.name, "second.txt")), queued = true)
        writePlatformFile(file, JsonCodec.encodeToString(QueuedShare.serializer(), entry))
        val uploads = mutableListOf<String>()
        val prepared = mutableListOf<String>()
        val sent = mutableListOf<Triple<String, String, String>>()
        val main: suspend (String, String, String) -> Unit = { text, id, via -> sent += Triple(id, text, via) }
        val agent: suspend (SessionKey, String, String, String) -> Unit = { _, _, _, _ -> error("Wrong destination") }
        val target = SessionKey("machine", "main-session")
        assertTrue(deliverQueuedShare(file, false, "Shared content", { _ -> error("Offline must not resolve main") },
            { _, _, _, _ -> error("Offline must not upload") }, main, agent))
        assertTrue(first.exists() && second.exists())
        try {
            deliverQueuedShare(file, true, "Shared content", { via -> prepared += via; target }, { _, attachment, _, _ ->
                uploads += attachment.name
                if (attachment.name == second.name) throw IOException("Disconnected")
                "/tmp/shared/first.txt"
            }, main, agent)
            fail("Expected interrupted upload")
        } catch (_: IOException) { }
        assertTrue(sent.isEmpty())
        assertTrue(first.exists() && second.exists())
        val restored = JsonCodec.decodeFromString<QueuedShare>(file.readText())
        assertEquals("/tmp/shared/first.txt", restored.attachments.first().path)
        assertFalse(deliverQueuedShare(file, true, "Shared content", { via -> prepared += via; target }, { _, attachment, _, _ ->
            uploads += attachment.name
            assertEquals("second attachment", attachment.readText())
            "/tmp/shared/second.txt"
        }, main, agent))
        assertEquals(listOf(first.name, second.name, second.name), uploads)
        assertEquals("stable-id", sent.single().first)
        assertTrue(sent.single().second.contains("'/tmp/shared/first.txt' '/tmp/shared/second.txt'"))
        assertFalse(directory.exists())

        val textFile = File(temporary.newFolder(), "entry.json")
        writePlatformFile(textFile, JsonCodec.encodeToString(QueuedShare.serializer(), QueuedShare("offline-text", "", "你好", queued = true)))
        assertFalse(deliverQueuedShare(textFile, false, "Shared content", { _ -> error("Main is resolved by core outbox later") },
            { _, _, _, _ -> error("No attachments") }, main, agent))
        assertEquals("offline-text", sent.last().first)
        assertTrue(prepared.all { it == persona.core.contracts.api.MessageVia.Share })
        assertEquals(2, prepared.size)
        assertTrue(sent.all { it.third == persona.core.contracts.api.MessageVia.Share })
    }

    @Test fun mainConversationUploadsGoToTheShareOwnersHub() = runTest {
        // #4816: the selected (non-primary) group's main target has a plain id the primary group would claim.
        val directory = temporary.newFolder()
        File(directory, "photo.bin").writeText("photo")
        val file = File(directory, "entry.json")
        writePlatformFile(file, JsonCodec.encodeToString(QueuedShare.serializer(), QueuedShare("main-photo", "", "",
            listOf(SharedAttachment("photo.bin", "photo.jpg")), queued = true)))
        val hubs = mutableListOf<String>()
        assertFalse(deliverQueuedShare(file, true, "Shared content", { via ->
            assertEquals(persona.core.contracts.api.MessageVia.Share, via)
            SessionKey("machine", "main-session")
        },
            { machine, _, _, main -> hubs += shareUploadApi(main, machine, "owner") { "primary" }; "/tmp/shared/photo.jpg" },
            { _, _, via -> assertEquals(persona.core.contracts.api.MessageVia.Share, via) },
            { _, _, _, _ -> error("Wrong destination") }))
        assertEquals(listOf("owner"), hubs)
        assertEquals("primary", shareUploadApi(false, "machine", "owner") { "primary" })
    }

    @Test fun unsubmittedSharesExpireAfterADayWhileFreshDraftsAndQueuedSharesStay() = runTest {
        fun staged(id: String, queued: Boolean, ageMillis: Long): File {
            val directory = temporary.newFolder(id)
            File(directory, "copy.bin").writeText("video")
            val file = File(directory, "entry.json")
            writePlatformFile(file, JsonCodec.encodeToString(QueuedShare.serializer(),
                QueuedShare(id, "", "", listOf(SharedAttachment("copy.bin", "video.mp4")), queued = queued)))
            check(file.setLastModified(System.currentTimeMillis() - ageMillis))
            return file
        }
        val day = 24L * 60 * 60 * 1000
        val expired = staged("expired", false, 2 * day)
        val fresh = staged("fresh", false, 60_000)
        val waiting = staged("waiting", true, 2 * day)
        for (file in listOf(expired, fresh, waiting)) assertEquals(file === waiting, deliverQueuedShare(file, false, "Shared content",
            { _ -> error("Offline") }, { _, _, _, _ -> error("Offline") }, { _, _, _ -> error("Offline") }, { _, _, _, _ -> error("Offline") }))
        assertFalse(expired.parentFile!!.exists())
        assertTrue(File(fresh.parentFile, "copy.bin").exists())
        assertTrue(File(waiting.parentFile, "copy.bin").exists())
    }

    @Test fun lockEventsRequireIndependentFieldsAndReceiptPreservesUnknownDisplay() {
        val event = ServerEvent.decode("""{"type":"lock_test","lock_test_id":"test-id","lock_test_due_at":"2026-10-31T00:00:30Z","lock_test_sent_at":"2026-10-31T00:00:30Z"}""")
        assertTrue(event is LockTestEvent)
        assertEquals(event, ServerEvent.decode(JsonCodec.encodeToString(ServerEvent.serializer(), event!!)))
        assertNull(ServerEvent.decode("""{"type":"lock_test","id":"wrong-id"}"""))
        val sent = decodeLockTest(JsonCodec.parseToJsonElement("""{"test_id":"test-id","status":"sent","due_at":"2026-10-31T00:00:30Z"}""").jsonObject)
        assertNull(sent.receivedAt)
        assertNull(sent.notificationShown)
        val received = decodeLockTest(JsonCodec.parseToJsonElement("""{"test_id":"test-id","status":"received","due_at":"2026-10-31T00:00:30Z","received_at":"2026-10-31T00:00:31Z","notification_shown":false}""").jsonObject)
        assertEquals(false, received.notificationShown)
        assertNotNull(received.receivedAt)
        assertFalse(needsLocalNetworkPermission(36, 37))
        assertFalse(needsLocalNetworkPermission(37, 36))
        assertTrue(needsLocalNetworkPermission(37, 37))
    }
}
