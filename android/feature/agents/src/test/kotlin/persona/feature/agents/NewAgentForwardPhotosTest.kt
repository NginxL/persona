package persona.feature.agents

import android.graphics.Bitmap
import android.net.Uri
import androidx.core.content.FileProvider
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.lifecycle.viewModelScope
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.ByteArrayOutputStream
import java.io.File
import java.util.Base64
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AgentCreationService
import persona.core.contracts.api.AgentDirectories
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.AgentStartRequest
import persona.core.contracts.api.AgentStartResult
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.ChatComposerService
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.ComposerDelivery
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.ModelPurposeDetail
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionDirectory
import persona.core.contracts.api.SessionFileContent
import persona.core.contracts.api.SessionFiles
import persona.core.contracts.api.SessionGitCommit
import persona.core.contracts.api.SessionGitDiff
import persona.core.contracts.api.SessionGitLog
import persona.core.contracts.api.SessionGitShow
import persona.core.contracts.api.SessionGitStatus
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.PersonaTheme
import persona.feature.chat.ChatChromeBindings
import persona.feature.chat.ChatForwarding
import persona.feature.chat.ChatScreen
import persona.feature.chat.LocalChatForwarding

/** A forward to New Agent carries the readable photos into its form and its actual first message (#4020). */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
class NewAgentForwardPhotosTest {
    @get:Rule val rule = createComposeRule()

    @Before fun ownFileProvider() {
        val cache = FileProvider::class.java.getDeclaredField("sCache").apply { isAccessible = true }.get(null) as MutableMap<*, *>
        cache.clear()
        val provider = Robolectric.buildContentProvider(FileProvider::class.java).create().get()
        provider.delete(Uri.parse("content://${RuntimeEnvironment.getApplication().packageName}.chat.files/chat-captures/none"), null, null)
    }

    private val source = Session("macbook", "source", title = "Source", cwd = "/source", agent = "claude", status = "idle", state = "idle")
    private val paths = listOf("/source/new-agent/first.png", "/source/new-agent/second.png")
    private val raw = "My own words\n\n<attached_images>\n${paths.joinToString("\n")}\n</attached_images>"

    private class Files : SessionFiles {
        val reads = mutableListOf<Triple<String, String, String>>()
        private val png = ByteArrayOutputStream().also { out ->
            check(Bitmap.createBitmap(8, 8, Bitmap.Config.ARGB_8888).compress(Bitmap.CompressFormat.PNG, 100, out))
        }.toByteArray()
        override suspend fun file(machineId: String, cwd: String, path: String): SessionFileContent {
            reads += Triple(machineId, cwd, path)
            return SessionFileContent(path, path.substringAfterLast('/'), png.size.toLong(), "image/png",
                data = Base64.getEncoder().encodeToString(png))
        }
        override suspend fun directory(machineId: String, cwd: String, path: String) = SessionDirectory(path)
        override suspend fun status(machineId: String, cwd: String) = SessionGitStatus(false)
        override suspend fun diff(machineId: String, cwd: String, path: String, staged: Boolean) = SessionGitDiff()
        override suspend fun log(machineId: String, cwd: String, skip: Int, limit: Int) = SessionGitLog()
        override suspend fun show(machineId: String, cwd: String, hash: String) = SessionGitShow(SessionGitCommit(hash))
    }

    private class Composer : ChatComposerService {
        override val deliveries = MutableStateFlow(emptyMap<String, ComposerDelivery>())
        val uploads = mutableListOf<Pair<SessionKey, String>>()
        override suspend fun upload(session: SessionKey, name: String, file: File): String {
            uploads += session to name
            return "/up/${session.machineId}/$name"
        }
        override suspend fun interrupt(session: SessionKey) = Unit
        override suspend fun retryRefused(clientMessageId: String, clearDraft: Boolean) = error("unused")
    }

    private class Store : AgentCreationStore {
        override suspend fun lastMachine(scope: String) = "studio"
        override suspend fun defaults(scope: String, machine: String) = AgentDefaults()
        override suspend fun saveDefaults(scope: String, machine: String, defaults: AgentDefaults) = Unit
        override suspend fun pending(scope: String): AgentStartOperation? = null
        override suspend fun pending(scope: String, value: AgentStartOperation?) = Unit
    }

    private class Sender : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        val sent = mutableListOf<Triple<SessionKey, String, String>>()
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) {
            sent += Triple(session, text, clientMessageId)
        }
    }

    private class Service : AgentCreationService {
        override val scope = "hub/pairing"
        var request: AgentStartRequest? = null
        override suspend fun launchers(machineId: String) = listOf(AgentLauncher("claude"))
        override suspend fun directories(machineId: String, path: String) = AgentDirectories(path.ifEmpty { "/new-agent" })
        override suspend fun start(request: AgentStartRequest, operationId: String): AgentStartResult {
            this.request = request
            return AgentStartResult.Created(Session(machineId = request.machineId, sessionId = "created", status = "idle", starting = true))
        }
        override suspend fun operation(operationId: String): AgentStartResult = error("unused")
        override suspend fun newAgentModel(): ModelPurposeDetail? = null
    }

    /** Drives ChatScreen's real ChatMessageActions and returns exactly what openNewAgent receives today. */
    private fun forwardToNewAgent(files: Files): String {
        var context: String? = null
        val repository = object : ChatRepository {
            override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(loading = false,
                items = listOf(Item("own-1", 1, role = "user", kind = "text", text = raw))))
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
        }
        val sessions = object : SessionsRepository {
            override val sessions = MutableStateFlow(listOf(source))
            override val machines = MutableStateFlow(listOf(Machine("macbook", "MacBook", online = true),
                Machine("studio", "Studio", online = true)))
            override suspend fun refresh() = Unit
        }
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow(emptyList<Interaction>())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }
        val forwarding = ChatForwarding(
            agents = MutableStateFlow(listOf(source)), machines = sessions.machines,
            assistant = MutableStateFlow(AssistantSnapshot(null, emptyList())), personaGroupOf = { true },
            conversationTitle = { it.title }, memory = { null }, openChat = { _, _ -> error("New Agent expected") },
            openPersonaHome = {}, openNewAgent = { context = it })
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(LocalChatForwarding provides forwarding) {
                    ChatScreen(source.key, repository, sender, interactions, sessions, onBack = {},
                        chrome = ChatChromeBindings(files = files), showTopBar = false)
                }
            }
        }
        rule.waitUntil(10_000) { rule.onAllNodesWithText("My own words").fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("My own words").performTouchInput { longClick() }
        rule.waitForIdle()
        rule.onNodeWithText("Forward").performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("forward-new-agent", useUnmergedTree = true).performClick()
        rule.waitUntil(5_000) { context != null }
        return requireNotNull(context)
    }

    @Test fun forwardedPhotosAreUploadedByNewAgentAndIncludedInItsExactFirstMessage() {
        val files = Files()
        val captured = forwardToNewAgent(files)
        assertTrue("the source computer reads its own photos", paths.all { path ->
            files.reads.any { it == Triple(source.machineId, source.cwd, path) }
        })

        runTest {
            Dispatchers.setMain(StandardTestDispatcher(testScheduler))
            val composer = Composer()
            val sender = Sender()
            val service = Service()
            val uploads = NewAgentUploads(
                service = { composer },
                upload = { service, session, attachment, onStaged ->
                    onStaged(attachment.size ?: 0)
                    service.upload(session, attachment.name, File(attachment.name))
                },
                photosOnly = { "Please look at the photos" },
                filesOnly = { "Please look at the attached files" },
                errorText = { name, error -> "$name: ${error.message}" },
            )
            val model = NewAgentViewModel(service, Store(), MutableStateFlow(listOf(Machine("studio", "Studio", online = true))),
                sender, uploads = uploads, prefill = NewAgentPrefill(context = captured))
            try {
                advanceUntilIdle()
                assertEquals(listOf("first.png", "second.png"), model.state.value.attachments.map { it.name })
                assertEquals(listOf("/up/studio/first.png", "/up/studio/second.png"), model.state.value.attachments.map { it.path })
                assertEquals(setOf(SessionKey("studio", "") to "first.png", SessionKey("studio", "") to "second.png"),
                    composer.uploads.toSet())

                model.submit()
                advanceUntilIdle()
                assertNull(service.request?.prompt)
                assertEquals(true, service.request?.async)
                val expected = captured + "\n\n<attached_images>\n/up/studio/first.png\n/up/studio/second.png\n</attached_images>"
                assertEquals(listOf(Triple(SessionKey("studio", "created"), expected, sender.sent.single().third)), sender.sent)
            } finally {
                model.viewModelScope.cancel()
                Dispatchers.resetMain()
            }
        }
    }
}
