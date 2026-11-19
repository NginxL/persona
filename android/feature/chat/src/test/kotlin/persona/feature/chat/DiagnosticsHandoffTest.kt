package persona.feature.chat

import java.io.File
import java.io.IOException
import java.nio.file.Files
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.emptyFlow
import kotlinx.coroutines.test.runTest
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.HubScope
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*

class DiagnosticsHandoffTest {
    private val file: File = Files.createTempFile("persona-diagnostics-android-", ".zip").toFile().apply { writeBytes(byteArrayOf(1, 2, 3, 4)) }
    @After fun cleanUp() { file.delete() }

    @Test fun agentDraftKeepsExistingContentAndRestoresAnUploadedFileCard() = runTest {
        val target = SessionKey("machine", "session")
        val drafts = Drafts(DraftKey.Chat(target), "keep this", version = 7)
        val composer = Composer("/up/diagnostics.zip")
        val saved = prepareDiagnosticsDraft(drafts, composer, target, mainTarget = { null }, file, "Please inspect", attachmentId = "zip")

        assertEquals(listOf("machine/session/attachment"), composer.uploads)
        assertEquals("keep this\n\nPlease inspect", saved.text)
        assertEquals(8, saved.version)
        val attachment = saved.attachments.single()
        assertEquals("zip", attachment.id)
        assertEquals(DraftAttachmentType.File, attachment.type)
        assertEquals(DraftUploadState.Uploaded, attachment.uploadState)
        assertEquals("/up/diagnostics.zip", attachment.uploadedPath)
        assertEquals(file.name, attachment.name)
        assertEquals(file.length(), attachment.size)
        assertEquals(DraftSessionVersion("history", "pane"), saved.sessionVersion)

        val restored = ChatComposerState(saved.composerDraft())
        assertTrue(restored.ready)
        assertEquals("keep this\n\nPlease inspect", restored.value.text)
        assertTrue(restored.message().contains("<persona-file kind=\"file\" path=\"/up/diagnostics.zip\""))
    }

    @Test fun mainChatUsesItsResolvedUploadTargetAndAssistantDraft() = runTest {
        val main = SessionKey("main-machine", "main-session")
        val drafts = Drafts(DraftKey.Assistant(), "", version = 2)
        val composer = Composer("/up/main.zip")
        val saved = prepareDiagnosticsDraft(drafts, composer, null, mainTarget = { main }, file, "Inspect Android", attachmentId = "main")

        assertEquals(listOf("main-machine/main-session/attachment"), composer.uploads)
        assertEquals(DraftKey.Assistant(), saved.key)
        assertEquals("Inspect Android", saved.text)
        assertEquals("/up/main.zip", saved.attachments.single().uploadedPath)
    }

    @Test fun uploadFailureDoesNotTouchTheExistingDraft() = runTest {
        val target = SessionKey("machine", "session")
        val original = Drafts(DraftKey.Chat(target), "unsent words", version = 4)
        val before = original.stored
        val composer = Composer(IOException("offline"))

        val failure = runCatching {
            prepareDiagnosticsDraft(original, composer, target, mainTarget = { null }, file, "must not save")
        }.exceptionOrNull()

        assertTrue(failure is IOException)
        assertEquals(listOf("machine/session/attachment"), composer.uploads)
        assertEquals(before, original.stored)
        assertEquals(0, original.saves)
        assertFalse(original.stored.text.contains("must not save"))
    }

    private class Drafts(key: DraftKey, text: String, version: Long) : DraftRepository {
        var stored = DraftSnapshot(HubScope("hub", "pairing"), key, text, version,
            sessionVersion = DraftSessionVersion("history", "pane"))
        var saves = 0
        override val structured = true
        override fun observe(key: DraftKey) = emptyFlow<DraftSnapshot>()
        override suspend fun read(key: DraftKey): DraftSnapshot = stored
        override suspend fun save(key: DraftKey, text: String, expectedVersion: Long): DraftSnapshot = error("text-only save is not used")
        override suspend fun save(
            key: DraftKey, text: String, expectedVersion: Long,
            attachments: List<DraftAttachment>, sessionVersion: DraftSessionVersion?,
        ): DraftSnapshot {
            check(expectedVersion == stored.version)
            saves++
            stored = stored.copy(text = text, version = stored.version + 1, attachments = attachments, sessionVersion = sessionVersion)
            return stored
        }
    }

    private class Composer(val outcome: Any) : ChatComposerService {
        constructor(path: String) : this(outcome = path as Any)
        val uploads = mutableListOf<String>()
        override val deliveries = MutableStateFlow(emptyMap<String, ComposerDelivery>())
        override suspend fun upload(session: SessionKey, name: String, file: File): String = error("ordinary files use an explicit destination")
        override suspend fun upload(session: SessionKey, name: String, file: File, destination: String): String {
            uploads += "${session.machineId}/${session.sessionId}/$destination"
            when (outcome) {
                is String -> return outcome
                is IOException -> throw outcome
                else -> error("unknown fixture")
            }
        }
        override suspend fun interrupt(session: SessionKey) = Unit
        override suspend fun retryRefused(clientMessageId: String, clearDraft: Boolean): ComposerDelivery = error("not used")
    }
}
