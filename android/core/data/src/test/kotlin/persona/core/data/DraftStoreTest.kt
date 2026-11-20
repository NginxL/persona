package persona.core.data

import androidx.room.Room
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import persona.core.contracts.HubScope
import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey
import persona.core.contracts.api.DraftAttachment
import persona.core.contracts.api.DraftAttachmentType
import persona.core.contracts.api.DraftKey
import persona.core.contracts.api.DraftSessionVersion

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class DraftStoreTest {
    @Test fun savedPromptProvenanceRoundTripsAlongsideLegacyDraftMetadata() = runTest {
        val scope = HubScope("hub", "draft-test")
        val session = SessionKey("machine", "session")
        val key = DraftKey.Chat(session)
        val attachment = DraftAttachment("file", DraftAttachmentType.File, "content://file", "example.txt")
        val database = Room.inMemoryDatabaseBuilder(RuntimeEnvironment.getApplication(), PersistentDatabase::class.java).build()
        try {
            database.records().putDraft(DraftRow(scope.path, session.machineId, session.sessionId, "/clear", 1,
                attachments = """[{"id":"file","type":"File","uri":"content://file","name":"example.txt"}]""",
                session_version = """{"history":"history","instance":"pane"}"""))
            val store = DraftStore(database, scope)
            val old = store.read(key)
            assertFalse(old.hasPickedSavedPrompt)
            assertEquals(listOf(attachment), old.attachments)
            assertEquals(DraftSessionVersion("history", "pane"), old.sessionVersion)

            var revision = old.version
            for (version in listOf(old.sessionVersion, DraftSessionVersion(), null)) {
                val saved = store.save(key, old.text, revision, old.attachments, version, hasPickedSavedPrompt = true)
                assertEquals(old.copy(version = revision + 1, sessionVersion = version, hasPickedSavedPrompt = true), saved)
                assertEquals(saved, DraftStore(database, scope).read(key))
                if (version != null) {
                    val row = requireNotNull(database.records().draft(scope.path, session.machineId, session.sessionId))
                    assertEquals(version, JsonCodec.decodeFromString<DraftSessionVersion>(requireNotNull(row.session_version)))
                }
                revision = saved.version
            }

            val manual = store.save(key, old.text, revision)
            assertFalse(manual.hasPickedSavedPrompt)
            assertEquals(manual, DraftStore(database, scope).read(key))
        } finally {
            database.close()
        }
    }
}
