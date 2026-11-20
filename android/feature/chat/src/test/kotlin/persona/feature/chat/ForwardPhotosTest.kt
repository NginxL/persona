package persona.feature.chat

import android.content.Context
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.io.IOException
import java.util.Base64
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.ImagePayload
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatComposerService
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.ComposerDelivery
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionDirectory
import persona.core.contracts.api.SessionFileContent
import persona.core.contracts.api.SessionFiles
import persona.core.contracts.api.SessionGitDiff
import persona.core.contracts.api.SessionGitLog
import persona.core.contracts.api.SessionGitShow
import persona.core.contracts.api.SessionGitStatus
import persona.core.contracts.api.SessionsRepository
import persona.core.contracts.api.AssistantSnapshot
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.R as DesignR

/** Your own message's photos travel through the real Forward to handoff into the target's real composer (#4020). */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
class ForwardPhotosTest {
    @get:Rule val rule = createComposeRule()
    private val context = ApplicationProvider.getApplicationContext<Context>()

    @Before fun provider() = useChatFileProvider(context)
    @After fun clearPlacements() {
        ForwardPlacements.take(agent.key)
        ForwardPlacements.take(persona.key)
        ForwardPlacements.home = emptyList()
    }

    private val source = Session("macbook", "source", title = "Source", cwd = "/source", agent = "claude", status = "idle", state = "idle")
    private val agent = Session("studio", "target", title = "Target", cwd = "/target", agent = "claude", status = "idle", state = "idle")
    private val persona = Session("server", "main", title = "Persona", cwd = "/persona", assistant = true, status = "idle", state = "idle")
    private val machines = listOf(Machine("macbook", "MacBook", online = true), Machine("studio", "Studio", online = true),
        Machine("server", "Server", online = true))

    private class Files(unreadable: Set<String>) : SessionFiles {
        private val failures = unreadable
        val reads = mutableListOf<Triple<String, String, String>>()
        private val png = Base64.getDecoder().decode("iVBORw0KGgoAAAANSUhEUgAAAAIAAAABCAYAAAD0In+KAAAAFklEQVR4nAAJAPb/AgAAAAAAAAAAAwAAGwADK+C1DwAAAABJRU5ErkJggg==")

        override suspend fun file(machineId: String, cwd: String, path: String): SessionFileContent {
            reads += Triple(machineId, cwd, path)
            if (path in failures) throw IOException("unreadable")
            return SessionFileContent(path, path.substringAfterLast('/'), png.size.toLong(), "image/png",
                data = Base64.getEncoder().encodeToString(png))
        }
        override suspend fun directory(machineId: String, cwd: String, path: String) = SessionDirectory(path)
        override suspend fun status(machineId: String, cwd: String) = SessionGitStatus(false)
        override suspend fun diff(machineId: String, cwd: String, path: String, staged: Boolean) = SessionGitDiff()
        override suspend fun log(machineId: String, cwd: String, skip: Int, limit: Int) = SessionGitLog()
        override suspend fun show(machineId: String, cwd: String, hash: String) = SessionGitShow(persona.core.contracts.api.SessionGitCommit(hash))
    }

    private class Uploads : ChatComposerService {
        override val deliveries = MutableStateFlow(emptyMap<String, ComposerDelivery>())
        val calls = mutableListOf<Pair<SessionKey, String>>()
        override suspend fun upload(session: SessionKey, name: String, file: File): String {
            calls += session to name
            return "/uploaded/${session.machineId}/$name"
        }
        override suspend fun interrupt(session: SessionKey) = Unit
        override suspend fun retryRefused(clientMessageId: String, clearDraft: Boolean) = error("unused")
    }

    private class TargetFeedback : AppFeedback {
        val notices = mutableListOf<String>()
        override fun failure(error: Throwable, operation: Boolean) = false
        override fun notice(message: String): Boolean {
            notices += message
            return true
        }
    }

    private inner class Scenario(name: String, count: Int, unreadable: Set<Int> = emptySet(), private val style: AppStyle = AppStyle.Glass,
        private val dark: Boolean = false) {
        val paths = List(count) { index -> "/$name/$index.png" }
        val raw = "My own words\n\n<attached_images>\n${paths.joinToString("\n")}\n</attached_images>"
        val files = Files(unreadable.map(paths::get).toSet())
        val uploads = Uploads()
        val composer = ChatComposerState().apply { value = TextFieldValue("Review these", TextRange(12)) }
        val notices = mutableListOf<String>()
        val targetFeedback = TargetFeedback()
        val sent = mutableListOf<Pair<SessionKey, String>>()
        private var opened by mutableStateOf<SessionKey?>(null)
        private val menu = MessageMenuState()
        private val sender = object : MessageSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) {
                sent += session to text
            }
        }
        private val repository = object : ChatRepository {
            override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(loading = false))
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
        }
        private val sessions = object : SessionsRepository {
            override val sessions = MutableStateFlow(listOf(source, agent, persona))
            override val machines = MutableStateFlow(this@ForwardPhotosTest.machines)
            override suspend fun refresh() = Unit
        }
        private val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow(emptyList<Interaction>())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }

        fun show(target: ForwardTarget) {
            val forwarding = ChatForwarding(
                agents = MutableStateFlow(listOf(source, agent)), machines = MutableStateFlow(machines),
                assistant = MutableStateFlow(AssistantSnapshot(persona.key, emptyList())), personaGroupOf = { true },
                conversationTitle = { it.title }, memory = { null },
                openChat = { key, _ -> opened = key }, openPersonaHome = {}, openNewAgent = {})
            val rowActions = ChatRowActions(source.key, fullText = { raw }, openPath = null,
                photos = AttachedPhotos(files, source.machineId, source.cwd), pictures = { emptyList() }, openImages = { _, _ -> },
                menu = menu, retryLineId = null, onRetry = {})
            rule.setContent {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    CompositionLocalProvider(LocalChatForwarding provides forwarding, LocalChatRowActions provides rowActions) {
                        Box(Modifier.testTag("forward-photo-shot").fillMaxSize().background(PersonaTheme.palette.background)) {
                            val key = opened
                            if (key == null) {
                                ChatMessageActions(source.key, source, machines.first(), ChatRenderStyle.Agent, repository,
                                    readOnly = false, composer = ChatComposerState(), messageMenu = menu, quoteTarget = {}, onContext = {},
                                    onNotice = { notices += it }, onFork = null, rewindMenu = null, onDelete = {})
                            } else {
                                CompositionLocalProvider(LocalAppFeedback provides targetFeedback) {
                                    ChatScreen(key, repository, sender, interactions, sessions, onBack = {},
                                        chrome = ChatChromeBindings(files = files),
                                        renderStyle = if (key == persona.key) ChatRenderStyle.Assistant else ChatRenderStyle.Agent,
                                        composerService = uploads,
                                    composerUpload = { service, session, attachment, onStaged ->
                                        onStaged(attachment.size ?: 0)
                                        service.upload(session, attachment.name, File(attachment.name))
                                    },
                                    composerState = composer, showTopBar = false)
                                }
                            }
                        }
                    }
                }
            }
            rule.runOnIdle { menu.open(MessageTarget("own-1", "My own words", user = true, raw = raw)) }
            rule.waitForIdle()
            rule.onNodeWithText(context.getString(R.string.chat_menu_forward)).performClick()
            rule.waitForIdle()
            val tag = when (target) {
                ForwardTarget.Persona -> "forward-persona"
                is ForwardTarget.Chat -> "forward-session-${target.key.sessionId}"
                ForwardTarget.NewAgent -> "forward-new-agent"
            }
            rule.onNodeWithTag(tag, useUnmergedTree = true).performClick()
            rule.waitUntil(5_000) { opened != null }
            rule.waitForIdle()
        }

        fun awaitAttachments(count: Int) {
            rule.waitUntil(10_000) { composer.attachments.size == count && composer.ready }
            rule.waitForIdle()
        }

        fun send(expectedPhotos: Int) {
            val expectedPaths = uploads.calls.map { (session, name) -> "/uploaded/${session.machineId}/$name" }
            rule.onNodeWithContentDescription(context.getString(DesignR.string.designsystem_send)).performClick()
            rule.waitUntil(5_000) { sent.isNotEmpty() }
            val (target, text) = sent.single()
            assertEquals(opened, target)
            assertTrue(text, text.contains("<persona-context kind=\"forward\""))
            assertTrue(text, text.contains("My own words"))
            assertTrue(text, text.contains("Review these"))
            assertEquals(expectedPaths.sorted(), messageModel(text, user = true).images)
            assertEquals(expectedPhotos, expectedPaths.size)
        }
    }

    @Test fun cancellationWhileRestoringAForwardRemovesItsPartiallyStagedPhotos() {
        val folder = File(context.cacheDir, "chat-captures")
        val before = folder.list().orEmpty().toSet()
        val result = runCatching {
            kotlinx.coroutines.runBlocking {
                forwardedPhotos(context, listOf("ok.png", "cancel.png"), read = { path ->
                    if (path == "cancel.png") {
                        assertTrue(folder.list().orEmpty().toSet().size > before.size)
                        throw kotlinx.coroutines.CancellationException("left the picker")
                    }
                    ImagePayload(data = "aGVsbG8=", mime = "image/png")
                })
            }
        }
        assertTrue(result.exceptionOrNull() is kotlinx.coroutines.CancellationException)
        assertEquals(before, folder.list().orEmpty().toSet())
    }

    private fun existingChat(target: ForwardTarget) {
        val scenario = Scenario(if (target == ForwardTarget.Persona) "persona" else "agent", count = 5)
        scenario.show(target)
        // The composer has four places: the fifth source photo is never even read.
        assertEquals(scenario.paths.take(4), scenario.files.reads.map { it.third })
        assertTrue(scenario.files.reads.all { it.first == source.machineId && it.second == source.cwd })
        scenario.awaitAttachments(4)
        assertEquals(4, scenario.composer.attachments.count { it.image })
        assertEquals(4, scenario.uploads.calls.size)
        assertTrue(scenario.uploads.calls.all { it.first == (target as? ForwardTarget.Chat)?.key || target == ForwardTarget.Persona && it.first == persona.key })
        assertEquals((0..3).map { "$it.png" }, scenario.composer.attachments.map { it.name })
        assertFalse(scenario.composer.attachments.any { "4.png" in it.name })
        scenario.send(expectedPhotos = 4)
    }

    @Test fun existingAgentOnAnotherComputerReadsAttachesAndSendsOnlyFourPhotos() = existingChat(ForwardTarget.Chat(agent.key))

    @Test fun personaReadsAttachesAndSendsForwardedPhotos() = existingChat(ForwardTarget.Persona)

    @Test fun anUnreadablePhotoIsOmittedWithAShortNoticeAndTheRestStillSend() {
        val scenario = Scenario("partial", count = 3, unreadable = setOf(1))
        scenario.show(ForwardTarget.Chat(agent.key))
        assertEquals(scenario.paths, scenario.files.reads.map { it.third })
        scenario.awaitAttachments(2)
        assertEquals(2, scenario.composer.attachments.count { it.image })
        assertTrue("a short target-visible photo notice: ${scenario.targetFeedback.notices}", scenario.targetFeedback.notices.any {
            it.isNotBlank() && it.length <= 160 && it.contains("photo", ignoreCase = true)
        })
        scenario.send(expectedPhotos = 2)
    }

    private fun shot(style: AppStyle, dark: Boolean) {
        val scenario = Scenario("${style.name.lowercase()}-${if (dark) "dark" else "light"}", count = 2, style = style, dark = dark)
        scenario.show(ForwardTarget.Chat(agent.key))
        assertEquals(scenario.paths, scenario.files.reads.map { it.third })
        scenario.awaitAttachments(2)
        for (index in 1..2) {
            rule.onNodeWithContentDescription(context.getString(R.string.chat_attach_photo, index)).assertExists()
        }
        rule.onAllNodesWithContentDescription(context.getString(R.string.chat_attach_photo, 3)).assertCountEquals(0)
        rule.onNodeWithTag(FORWARD_ROUTE_TAG, useUnmergedTree = true).assertExists()
        val bitmap = rule.onNodeWithTag("forward-photo-shot").captureToImage().asAndroidBitmap()
        assertTrue(bitmap.width > 0 && bitmap.height > 0)
        val out = File(System.getProperty("persona.shots") ?: "build/forward-photo-composer-shots").apply { mkdirs() }
        File(out, "composer-${style.name.lowercase()}-${if (dark) "dark" else "light"}.png").outputStream().use {
            assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
        }
    }

    @Test fun forwardedPhotosInGlassLightComposer() = shot(AppStyle.Glass, false)
    @Test fun forwardedPhotosInGlassDarkComposer() = shot(AppStyle.Glass, true)
    @Test fun forwardedPhotosInBriefLightComposer() = shot(AppStyle.Brief, false)
    @Test fun forwardedPhotosInBriefDarkComposer() = shot(AppStyle.Brief, true)
    @Test fun forwardedPhotosInBoardLightComposer() = shot(AppStyle.Board, false)
    @Test fun forwardedPhotosInBoardDarkComposer() = shot(AppStyle.Board, true)
}
