package persona.feature.assistant

import android.graphics.Bitmap
import android.net.Uri
import androidx.core.content.FileProvider
import androidx.activity.ComponentActivity
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextReplacement
import androidx.compose.ui.test.performTouchInput
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.ByteArrayOutputStream
import java.io.File
import java.util.Base64
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.After
import org.junit.Assert.assertEquals
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
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.AssistantRepository
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.ChatComposerService
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.ComposerDelivery
import persona.core.contracts.api.DraftSnapshot
import persona.core.contracts.api.HubManager
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MainConversationSender
import persona.core.contracts.api.ManagerRoster
import persona.core.contracts.api.MessageSender
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
import persona.core.designsystem.LocalManagerRoster
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import persona.feature.chat.ChatChromeBindings
import persona.feature.chat.ChatForwarding
import persona.feature.chat.ChatScreen
import persona.feature.chat.ForwardPlacements
import persona.feature.chat.LocalChatForwarding

/** A forward to Persona's blank home keeps its photos in the first conversation's ordinary send (#4020). */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
class AssistantForwardPhotosTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val source = Session("mac", "source", title = "Source", cwd = "/source", agent = "claude", status = "idle", state = "idle")
    private val main = SessionKey("mac", "main")
    private val paths = listOf("/source/home/first.png", "/source/home/second.png")
    private val raw = "My own words\n\n<attached_images>\n${paths.joinToString("\n")}\n</attached_images>"
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    @Before fun setup() {
        AssistantManagerChoice.choose("hub", null, "assistant")
        val cache = FileProvider::class.java.getDeclaredField("sCache").apply { isAccessible = true }.get(null) as MutableMap<*, *>
        cache.clear()
        val provider = Robolectric.buildContentProvider(FileProvider::class.java).create().get()
        provider.delete(Uri.parse("content://${RuntimeEnvironment.getApplication().packageName}.chat.files/chat-captures/none"), null, null)
    }
    @After fun cleanup() {
        AssistantManagerChoice.reset()
        ForwardPlacements.home = emptyList()
    }

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
        val uploads = java.util.Collections.synchronizedList(mutableListOf<Pair<SessionKey, String>>())
        override suspend fun upload(session: SessionKey, name: String, file: File): String {
            uploads += session to name
            return "/up/${session.machineId}/$name"
        }
        override suspend fun interrupt(session: SessionKey) = Unit
        override suspend fun retryRefused(clientMessageId: String, clearDraft: Boolean) = error("unused")
    }

    private inner class Hub : AssistantRepository, MessageSender, MainConversationSender {
        override val assistant = MutableStateFlow(AssistantSnapshot(mainSession = main,
            conversations = listOf(Session(machineId = "mac", sessionId = "main", title = "Main", assistant = true))))
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        val sent = mutableListOf<Pair<SessionKey, String>>()
        override suspend fun refresh() = Unit
        override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = Unit
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) {
            sent += session to text
        }
        suspend fun start(): SessionKey = SessionKey("mac", "new-1")
    }

    @Test fun blankPersonaHomeAttachesForwardedPhotosAndSendsThemInTheFirstMessage() {
        val files = Files()
        val composer = Composer()
        val composerState = persona.feature.chat.ChatComposerState()
        val upload: persona.feature.chat.ComposerAttachmentUpload = { service, session, attachment, onStaged ->
            onStaged(attachment.size ?: 0)
            service.upload(session, attachment.name, File(attachment.name))
        }
        val hub = Hub()
        var home by mutableStateOf(false)
        val chats = object : ChatRepository {
            override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(loading = false,
                items = listOf(Item("own-1", 1, role = "user", kind = "text", text = raw))))
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
        }
        val sessions = object : SessionsRepository {
            override val sessions = MutableStateFlow(listOf(source))
            override val machines = MutableStateFlow(listOf(Machine("mac", "MacBook", online = true)))
            override suspend fun refresh() = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow(emptyList<Interaction>())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }
        val forwarding = ChatForwarding(
            agents = MutableStateFlow(listOf(source)), machines = sessions.machines,
            assistant = MutableStateFlow(AssistantSnapshot(null, emptyList())), personaGroupOf = { true },
            conversationTitle = { it.title }, memory = { null }, openChat = { _, _ -> error("blank home expected") },
            openPersonaHome = { home = true }, openNewAgent = { error("blank home expected") })
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(LocalChatForwarding provides forwarding) {
                    if (home) {
                        CompositionLocalProvider(LocalManagerRoster provides ManagerRoster(listOf(
                            HubManager("assistant", "Helper", online = true, isDefault = true)), "assistant")) {
                            AssistantScreen(navigator, hub, hub, hub::start, composerService = composer,
                                composerUpload = upload,
                                mainConversationSender = hub, chatContent = { Text("chat:${it.sessionId}") },
                                composerState = composerState)
                        }
                    } else {
                        ChatScreen(source.key, chats, hub, interactions, sessions, onBack = {},
                            chrome = ChatChromeBindings(files = files), showTopBar = false)
                    }
                }
            }
        }
        rule.waitUntil(10_000) { rule.onAllNodesWithText("My own words").fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("My own words").performTouchInput { longClick() }
        rule.waitForIdle()
        rule.onNodeWithText("Forward").performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("forward-persona", useUnmergedTree = true).performClick()
        rule.waitUntil(5_000) { home }
        rule.onNodeWithText("chat:main").assertExists()
        rule.onNodeWithContentDescription("New chat").performClick()
        rule.waitForIdle()

        assertTrue("the source computer reads its own photos", paths.all { path ->
            files.reads.any { it == Triple(source.machineId, source.cwd, path) }
        })
        val forward = ForwardPlacements.home.single()
        fun waitMain(timeout: Long = 10_000, condition: () -> Boolean) = rule.waitUntil(timeout) {
            // Background uploads resume through Robolectric's main dispatcher, not Compose's idle detection alone.
            androidx.compose.runtime.snapshots.Snapshot.sendApplyNotifications()
            org.robolectric.Shadows.shadowOf(android.os.Looper.getMainLooper()).idle()
            condition()
        }
        waitMain { composerState.attachments.size == 2 && composerState.ready }
        rule.onNodeWithContentDescription("Photo 1", useUnmergedTree = true).assertExists()
        rule.onNodeWithContentDescription("Photo 2", useUnmergedTree = true).assertExists()
        assertEquals(setOf(main to "first.png", main to "second.png"), composer.uploads.toSet())
        rule.onNode(hasSetTextAction()).performTextReplacement("Review home")
        waitMain { runCatching { rule.onNodeWithContentDescription("Send").assertIsEnabled() }.isSuccess }
        rule.onNodeWithContentDescription("Send").assertIsEnabled().performClick()
        waitMain(5_000) { hub.sent.isNotEmpty() }
        val expected = forward.xml() + "\n\nReview home\n\n<attached_images>\n/up/mac/first.png\n/up/mac/second.png\n</attached_images>"
        assertEquals(listOf(SessionKey("mac", "new-1") to expected), hub.sent)
    }
}
