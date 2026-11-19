package persona.android.dev

import android.app.Application
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.PreferenceDataStoreFactory
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.lifecycle.SavedStateHandle
import java.io.File
import java.io.IOException
import java.lang.reflect.Proxy
import java.nio.file.Files
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.emptyFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import persona.android.R
import persona.core.contracts.HubScope
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantRepository
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.ChatComposerService
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.ComposerDelivery
import persona.core.contracts.api.DraftAttachment
import persona.core.contracts.api.DraftKey
import persona.core.contracts.api.DraftRepository
import persona.core.contracts.api.DraftSessionVersion
import persona.core.contracts.api.DraftSnapshot
import persona.core.contracts.api.SessionsRepository
import persona.feature.chat.ChatAppBindings
import persona.feature.chat.ChatWiringSource

@OptIn(ExperimentalCoroutinesApi::class)
@RunWith(RobolectricTestRunner::class)
class DiagnosticsShareViewModelTest {
    @Before fun setMain() = Dispatchers.setMain(UnconfinedTestDispatcher())
    @After fun resetMain() = Dispatchers.resetMain()

    @Test fun successPublishesOpenChatOnlyAfterTheDraftIsSaved() = runTest {
        val fixture = Fixture()
        try {
            val gate = CompletableDeferred<String>()
            fixture.composer.gate = gate
            val preparing = launch { fixture.model.prepare(fixture.target) }
            assertTrue(fixture.drafts.stored.attachments.isEmpty())
            assertNull(fixture.model.openChat)
            gate.complete("/up/diagnostics.zip")
            preparing.join()
            assertEquals(DiagnosticsOpenChat(fixture.target), fixture.model.openChat)
            assertEquals("/up/diagnostics.zip", fixture.drafts.stored.attachments.single().uploadedPath)
            assertTrue(fixture.drafts.stored.text.contains("Please inspect"))
            assertNull(fixture.model.error)
        } finally { fixture.close() }
    }

    @Test fun mainChatOpensThePreparedConversation() = runTest {
        val fixture = Fixture(main = true)
        try {
            fixture.model.prepare(null)
            assertEquals(DiagnosticsOpenChat(SessionKey("main-machine", "main-session")), fixture.model.openChat)
            assertEquals(listOf(SessionKey("main-machine", "main-session")), fixture.composer.uploads)
            assertEquals(1, fixture.drafts.stored.version)
        } finally { fixture.close() }
    }

    @Test fun uploadFailureDoesNotOpenTheChat() = runTest {
        val fixture = Fixture()
        try {
            fixture.composer.failure = IOException("offline")
            fixture.model.prepare(fixture.target)
            assertNull(fixture.model.openChat)
            assertEquals(fixture.context.getString(R.string.app_diagnostics_prepare_failed), fixture.model.error)
            assertTrue(fixture.drafts.stored.text.isEmpty())
        } finally { fixture.close() }
    }

    @Test fun recentTargetFailureStillOpensTheSavedDraftWithoutAnotherUpload() = runTest {
        val fixture = Fixture(recentTargetFailure = true)
        try {
            fixture.model.prepare(fixture.target)
            assertEquals(DiagnosticsOpenChat(fixture.target), fixture.model.openChat)
            assertNull(fixture.model.error)
            assertEquals(1, fixture.composer.uploads.size)

            fixture.model.prepare(fixture.target)
            assertEquals(1, fixture.composer.uploads.size)
            assertEquals(1, fixture.drafts.stored.version)
        } finally { fixture.close() }
    }

    @Test fun missingBindingsTimeOutWithAnErrorInsteadOfOpeningOrCancelling() = runTest {
        val fixture = Fixture(agentAvailable = false)
        try {
            val preparing = launch { fixture.model.prepare(fixture.target) }
            advanceTimeBy(10_001)
            preparing.join()
            assertNull(fixture.model.openChat)
            assertEquals(fixture.context.getString(R.string.app_diagnostics_prepare_failed), fixture.model.error)
            assertTrue(fixture.composer.uploads.isEmpty())
        } finally { fixture.close() }
    }

    @Test fun mainChatRejectsAPairingChangeBeforeUploading() = runTest {
        val fixture = Fixture(main = true)
        try {
            fixture.current.value = fixture.bindings.copy(scope = HubScope("other", "changed"))
            fixture.model.prepare(null)
            assertNull(fixture.model.openChat)
            assertEquals(fixture.context.getString(R.string.app_share_pairing_changed), fixture.model.error)
            assertTrue(fixture.composer.uploads.isEmpty())
        } finally { fixture.close() }
    }

    @Test fun agentChatRejectsAPairingChangeBeforeUploading() = runTest {
        val fixture = Fixture()
        try {
            fixture.agent.value = fixture.bindings.copy(scope = HubScope("other", "changed"))
            fixture.model.prepare(fixture.target)
            assertNull(fixture.model.openChat)
            assertEquals(fixture.context.getString(R.string.app_share_pairing_changed), fixture.model.error)
            assertTrue(fixture.composer.uploads.isEmpty())
        } finally { fixture.close() }
    }

    @Test fun restoredPairingRecoversThePendingShare() = runTest {
        val fixture = Fixture(pairingInitiallyMissing = true)
        try {
            assertTrue(!fixture.model.paired)
            fixture.current.value = fixture.bindings
            runCurrent()
            assertTrue(fixture.model.paired)
            assertNull(fixture.model.error)
            fixture.model.prepare(fixture.target)
            assertEquals(DiagnosticsOpenChat(fixture.target), fixture.model.openChat)
            assertEquals(1, fixture.composer.uploads.size)
        } finally { fixture.close() }
    }

    @Test fun anArchiveOutsideTheExportDirectoryIsRejected() = runTest {
        val fixture = Fixture(readArchive = false)
        try {
            val outside = File.createTempFile("outside-dev-exports", ".zip")
            fixture.model.read(outside.absolutePath)
            fixture.model.prepare(fixture.target)
            assertEquals(fixture.context.getString(R.string.app_share_read_failed), fixture.model.error)
            assertNull(fixture.model.openChat)
            assertTrue(fixture.composer.uploads.isEmpty())
            outside.delete()
        } finally { fixture.close() }
    }

    private class Fixture(
        main: Boolean = false,
        agentAvailable: Boolean = true,
        readArchive: Boolean = true,
        recentTargetFailure: Boolean = false,
        pairingInitiallyMissing: Boolean = false,
    ) {
        val context = androidx.test.core.app.ApplicationProvider.getApplicationContext<Application>()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val target = SessionKey("machine", "session")
        val hubScope = HubScope("hub", "pairing")
        val key: DraftKey = if (main) DraftKey.Assistant() else DraftKey.Chat(target)
        val drafts = Drafts(DraftSnapshot(hubScope, key))
        val composer = Composer()
        val bindings = ChatAppBindings(
            hubScope, scope, drafts, unused(), unused(), composer, unused(), unused(), unused(),
            mainUploadTarget = { SessionKey("main-machine", "main-session") },
        )
        val current = MutableStateFlow<ChatAppBindings?>(if (pairingInitiallyMissing) null else bindings)
        val agent = MutableStateFlow<ChatAppBindings?>(if (agentAvailable) bindings else null)
        private val temp = Files.createTempDirectory("diagnostics-view-model").toFile()
        private val store = if (recentTargetFailure) object : DataStore<Preferences> {
            override val data = flowOf(emptyPreferences())
            override suspend fun updateData(transform: suspend (Preferences) -> Preferences): Preferences =
                throw IOException("recent targets unavailable")
        } else PreferenceDataStoreFactory.create(scope = scope) { File(temp, "targets.preferences_pb") }
        private val archive = File(File(context.cacheDir, "dev-exports").apply { mkdirs() }, "view-model-test.zip").apply { writeBytes(byteArrayOf(1)) }
        private val wiring = object : ChatWiringSource {
            override val current = this@Fixture.current
            override fun bindingsFor(session: SessionKey) = agent
        }
        private val assistant = object : AssistantRepository by unused() {
            override val assistant = MutableStateFlow(AssistantSnapshot())
            override suspend fun refresh() = Unit
        }
        private val sessions = object : SessionsRepository by unused() {
            override val machines = MutableStateFlow(emptyList<persona.core.contracts.Machine>())
            override val sessions = MutableStateFlow(emptyList<persona.core.contracts.Session>())
            override suspend fun refresh() = Unit
        }
        val model = DiagnosticsShareViewModel(context, assistant, sessions, wiring, SavedStateHandle(), store)

        init { if (readArchive) model.read(archive.absolutePath) }
        fun close() {
            archive.delete()
            temp.deleteRecursively()
            scope.cancel()
        }
    }

    private class Drafts(initial: DraftSnapshot) : DraftRepository {
        var stored = initial
        override val structured = true
        override fun observe(key: DraftKey) = emptyFlow<DraftSnapshot>()
        override suspend fun read(key: DraftKey) = stored
        override suspend fun save(key: DraftKey, text: String, expectedVersion: Long): DraftSnapshot = error("text-only save")
        override suspend fun save(key: DraftKey, text: String, expectedVersion: Long, attachments: List<DraftAttachment>,
            sessionVersion: DraftSessionVersion?): DraftSnapshot {
            check(expectedVersion == stored.version)
            stored = stored.copy(text = text, version = stored.version + 1, attachments = attachments, sessionVersion = sessionVersion)
            return stored
        }
    }

    private class Composer : ChatComposerService {
        val uploads = mutableListOf<SessionKey>()
        var gate: CompletableDeferred<String>? = null
        var failure: IOException? = null
        override val deliveries = MutableStateFlow(emptyMap<String, ComposerDelivery>())
        override suspend fun upload(session: SessionKey, name: String, file: File): String = error("ordinary upload")
        override suspend fun upload(session: SessionKey, name: String, file: File, destination: String): String {
            uploads += session
            failure?.let { throw it }
            return gate?.await() ?: "/up/diagnostics.zip"
        }
        override suspend fun interrupt(session: SessionKey) = Unit
        override suspend fun retryRefused(clientMessageId: String, clearDraft: Boolean): ComposerDelivery = error("not used")
    }

    private companion object {
        inline fun <reified T> unused(): T = Proxy.newProxyInstance(T::class.java.classLoader, arrayOf(T::class.java)) { proxy, method, args ->
            when (method.name) {
                "toString" -> "unused ${T::class.java.simpleName}"
                "hashCode" -> System.identityHashCode(proxy)
                "equals" -> proxy === args?.firstOrNull()
                else -> throw UnsupportedOperationException("${T::class.java.name}.${method.name}")
            }
        } as T
    }
}
