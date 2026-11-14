package persona.feature.assistant

import android.content.Context
import java.io.IOException
import androidx.activity.ComponentActivity
import androidx.compose.runtime.*
import androidx.compose.ui.test.*
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.After
import org.junit.Assert.*
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.RuntimeEnvironment
import persona.core.contracts.HubScope
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.data.DraftStore
import persona.core.designsystem.*
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/** Exercises the production Room DraftStore and editor against delayed reads and completed sends. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class AssistantDraftScopeTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private lateinit var database: Any
    private lateinit var drafts: DraftRepository
    private val a = DraftKey.Assistant()
    private val b = DraftKey.Assistant("main@work")
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    @Before fun setup() {
        // Room is an implementation dependency of core:data; reflect only its factory, then use its real public store API.
        val databaseClass = Class.forName("persona.core.data.PersistentDatabase")
        val builder = Class.forName("androidx.room.Room").getMethod("inMemoryDatabaseBuilder", Context::class.java, Class::class.java)
            .invoke(null, RuntimeEnvironment.getApplication(), databaseClass)
        database = builder.javaClass.getMethod("build").invoke(builder)
        drafts = DraftStore::class.java.constructors.single().newInstance(database, HubScope("hub", "draft-test")) as DraftRepository
        AssistantManagerChoice.choose("hub", null, "assistant")
    }
    @After fun cleanup() {
        rule.runOnIdle { scope.cancel() }
        if (::database.isInitialized) database.javaClass.getMethod("close").invoke(database)
        AssistantManagerChoice.reset()
    }

    private class Hub : AssistantRepository, MessageSender, MainConversationSender {
        override val assistant = MutableStateFlow(AssistantSnapshot())
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        val sent = mutableListOf<String>()
        val ids = mutableListOf<String>()
        val targets = mutableListOf<SessionKey>()
        var failNext = false
        val gate = CompletableDeferred<Unit>()
        override suspend fun refresh() = Unit
        override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) {
            sent += text
            ids += clientMessageId
            if (failNext) { failNext = false; throw IOException("durable commit failed") }
            gate.await()
        }
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) {
            sent += text
            ids += clientMessageId
            targets += session
            if (failNext) { failNext = false; throw IOException("durable commit failed") }
            gate.await()
        }
    }
    private class Management(private val manager: String? = null, val reads: MutableList<String> = mutableListOf()) : AssistantManagement {
        override fun forManager(manager: String?) = Management(manager, reads)
        override suspend fun readiness(probe: Boolean): AssistantReadiness {
            reads += manager ?: "assistant"
            return AssistantReadiness()
        }
        override suspend fun warm() = Unit
        override suspend fun endConversation(session: SessionKey) = Unit
        override suspend fun tasks() = emptyList<ScheduledTask>()
        override suspend fun saveTask(task: ScheduledTask) = task
        override suspend fun deleteTask(id: String) = Unit
        override suspend fun runTask(id: String) = SessionKey("mac", id)
        override suspend fun freshTask(id: String) = ScheduledTask(id)
        override suspend fun runs() = emptyList<Run>()
        override suspend fun config() = AssistantConfig(machineId = "mac", launcher = "default", timezone = java.util.TimeZone.getDefault().id)
        override suspend fun launchers(machineId: String) = listOf(AgentLauncher("codex-test", agentType = "codex"))
        override suspend fun notes() = AssistantNotes(machineId = "mac")
        override suspend fun setNotes(text: String) = Unit
        override suspend fun memory() = emptyList<MemoryFile>()
        override suspend fun saveMemory(name: String, content: String) = Unit
        override suspend fun deleteMemory(name: String) = Unit
    }
    private fun mount(repository: DraftRepository = drafts, management: Management? = null,
        hub: Hub = Hub(), style: AppStyle = AppStyle.Default, startWith: (suspend (String) -> SessionKey)? = null): Hub {
        rule.setContent {
            PersonaTheme(reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalManagerRoster provides ManagerRoster(listOf(
                    HubManager("assistant", "助手", online = true, isDefault = true), HubManager("work", "工作", online = true),
                ), "assistant"), LocalHubFeatures provides setOf(AssistantReadinessFeature)) {
                    AssistantScreen(navigator, hub, hub, { SessionKey("mac", "new") }, management = management,
                        draftRepository = repository, draftScope = scope, composerService = null, mainConversationSender = hub, startConversationWith = startWith)
                }
            }
        }
        rule.waitForIdle()
        return hub
    }
    // The page draws no manager chips any more: the sidebar's switcher calls AssistantManagerChoice.pick(id), so this does the same.
    private fun pick(name: String) {
        AssistantManagerChoice.pick(mapOf("助手" to "assistant", "工作" to "work").getValue(name))
        rule.waitForIdle()
    }
    private fun text(text: String) { rule.onNode(hasSetTextAction()).performTextReplacement(text); rule.waitForIdle() }
    private fun persisted(key: DraftKey) = runBlocking { drafts.read(key).text }
    private fun awaitDraft(key: DraftKey, expected: String) = rule.waitUntil(10_000) { persisted(key) == expected }
    private fun input(expected: String) = rule.onNode(hasSetTextAction()).assert(SemanticsMatcher.expectValue(SemanticsProperties.EditableText, AnnotatedString(expected)))

    @Test fun delayedWorkReadCannotDisplayOrPersistAssistantDraft() {
        runBlocking { drafts.save(b, "工作原稿", 0) }
        val gate = CompletableDeferred<Unit>()
        val delayed = object : DraftRepository by drafts {
            override suspend fun read(key: DraftKey): DraftSnapshot {
                if (key == b) gate.await()
                return drafts.read(key)
            }
        }
        mount(delayed)
        rule.waitUntil(10_000) { rule.onAllNodes(hasSetTextAction()).fetchSemanticsNodes().isNotEmpty() }
        text("助手原稿")
        awaitDraft(a, "助手原稿")
        pick("工作")
        rule.onNodeWithText("助手原稿").assertDoesNotExist()
        rule.onNodeWithContentDescription("发送").assertIsNotEnabled()
        rule.runOnIdle { gate.complete(Unit) }
        rule.waitUntil(10_000) { rule.onAllNodes(hasText("工作原稿")).fetchSemanticsNodes().isNotEmpty() }
        input("工作原稿")
        assertEquals("工作原稿", persisted(b))
        pick("助手")
        input("助手原稿")
    }

    @Test fun sendCompletesWhileWorkShownClearsOnlyAssistantDraft() {
        val hub = mount()
        text("助手提交稿")
        rule.onNodeWithContentDescription("发送").performClick()
        rule.waitForIdle()
        pick("工作")
        text("工作保留稿")
        awaitDraft(b, "工作保留稿")
        rule.runOnIdle { hub.gate.complete(Unit) }
        awaitDraft(a, "")
        input("工作保留稿")
        pick("助手")
        input("")
        assertEquals(listOf("助手提交稿"), hub.sent)
    }

    @Test fun returningDuringSendUsesSameEditorAndDoesNotEraseNewEdits() {
        val hub = mount()
        text("助手提交稿")
        rule.onNodeWithContentDescription("发送").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("发送中").assertExists()
        pick("工作")
        rule.onNodeWithText("发送中").assertDoesNotExist()
        text("工作保留稿")
        pick("助手")
        rule.onNodeWithText("发送中").assertExists()
        assertEquals("助手提交稿", persisted(a))
        rule.onNodeWithContentDescription("发送").assertIsNotEnabled()
        rule.runOnIdle { hub.gate.complete(Unit) }
        awaitDraft(a, "")
        input("")
        text("助手新稿")
        awaitDraft(a, "助手新稿")
        pick("工作")
        input("工作保留稿")
        pick("助手")
        input("助手新稿")
    }

    @Test fun failedSendKeepsClientIdAcrossAssistantSwitchAndRetriesOnlyOnce() {
        val hub = mount(hub = Hub().apply { failNext = true })
        text("失败后重试的助手稿")
        rule.onNodeWithContentDescription("发送").performClick()
        rule.waitForIdle()
        assertEquals(1, hub.ids.size)
        val firstId = hub.ids.single()
        pick("工作")
        text("工作稿不受重试影响")
        pick("助手")
        input("失败后重试的助手稿")
        rule.onNodeWithContentDescription("发送").performClick()
        rule.waitForIdle()
        assertEquals(listOf(firstId, firstId), hub.ids)
        rule.runOnIdle { hub.gate.complete(Unit) }
        awaitDraft(a, "")
        pick("工作")
        input("工作稿不受重试影响")
    }

    @Test fun failedLauncherSendRetainsCreatedTargetAndClientIdAfterSwitchingBack() {
        val created = mutableListOf<SessionKey>()
        // The launcher chip remains on Brief; Glass uses the model panel covered by ModelPillTest.
        val hub = mount(management = Management(), hub = Hub().apply { failNext = true }, style = AppStyle.Brief, startWith = {
            SessionKey("mac", "branch-${created.size}").also { created += it }
        })
        rule.onNodeWithText("默认：default").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("codex-test").performClick()
        text("独立分支重试稿")
        rule.onNodeWithContentDescription("发送").performClick()
        rule.waitForIdle()
        assertEquals(1, created.size)
        val firstId = hub.ids.single()
        val firstTarget = hub.targets.single()
        pick("工作")
        text("工作稿")
        pick("助手")
        rule.onNodeWithText("codex-test").assertExists()
        // The failed first message waits as a bubble on its assistant's page (#4339 C18): Resend sends it again.
        rule.onNodeWithText("重发").performClick()
        rule.waitForIdle()
        assertEquals(1, created.size)
        assertEquals(listOf(firstTarget, firstTarget), hub.targets)
        assertEquals(listOf(firstId, firstId), hub.ids)
        rule.runOnIdle { hub.gate.complete(Unit) }
        awaitDraft(a, "")
        pick("工作")
        input("工作稿")
    }

    @Test fun switchingLoadsEachManagersReadinessWithoutMachineChanges() {
        val management = Management()
        mount(management = management)
        assertTrue(management.reads.contains("assistant"))
        pick("工作")
        assertTrue(management.reads.contains("work"))
        pick("助手")
        assertTrue(management.reads.count { it == "assistant" } >= 2)
    }
}
