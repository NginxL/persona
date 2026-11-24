package persona.feature.assistant.settings

import androidx.activity.ComponentActivity
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.*

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1800dp")
class AssistantSettingsManagerTest {
    @get:Rule val rule = createComposeRule()
    @org.junit.After fun reset() = AssistantManagerChoice.reset()

    internal class Hub(
        private val values: MutableMap<String, AssistantConfig>,
        private val owner: String = "assistant",
        private val managers: MutableList<HubManager> = mutableListOf(),
        val renamed: MutableList<Pair<String, String>> = mutableListOf(),
        private val onRename: (HubManager) -> Unit = {},
    ) : AssistantManagement, HubManagers {
        // Null names the default assistant (AssistantManagement.forManager), as the domain spaces ask for (#13500).
        override fun forManager(manager: String?) =
            Hub(values, manager ?: managers.firstOrNull { it.isDefault }?.id ?: "assistant", managers, renamed, onRename)
        override suspend fun roster() = ManagerRoster(managers.toList(), managers.firstOrNull { it.isDefault }?.id.orEmpty())
        override suspend fun create(name: String): ManagerCode = error("not used")
        override suspend fun newCode(id: String): ManagerCode = error("not used")
        override suspend fun rename(id: String, name: String): HubManager {
            renamed += id to name
            val confirmed = managers.first { it.id == id }.copy(name = name)
            managers.replaceAll { if (it.id == id) confirmed else it }
            onRename(confirmed)
            return confirmed
        }
        override suspend fun makeDefault(id: String): HubManager = error("not used")
        override suspend fun setHide(id: String, hide: Boolean) = error("not used")
        override suspend fun delete(id: String) = error("not used")
        override suspend fun config() = values.getValue(owner)
        override suspend fun updateConfig(update: AssistantConfigUpdate): AssistantConfig {
            val previous = values.getValue(owner)
            return previous.copy(defaultPermissionMode = update.defaultPermissionMode ?: previous.defaultPermissionMode,
                timezone = update.timezone ?: previous.timezone, recentTaskChats = update.recentTaskChats ?: previous.recentTaskChats).also { values[owner] = it }
        }
        override suspend fun memory() = emptyList<MemoryFile>()
        override suspend fun warm() {}
        override suspend fun endConversation(session: SessionKey) {}
        override suspend fun tasks() = listOf(ScheduledTask(id = "daily", title = "Daily review", prompt = "Review",
            repeats = ScheduledTask.DAILY, time = "09:00", enabled = true))
        override suspend fun saveTask(task: ScheduledTask) = task
        override suspend fun deleteTask(id: String) {}
        override suspend fun runTask(id: String) = SessionKey("computer", id)
        override suspend fun freshTask(id: String) = tasks().first()
        override suspend fun runs() = emptyList<Run>()
        override suspend fun notes() = AssistantNotes()
        override suspend fun setNotes(text: String) {}
        override suspend fun saveMemory(name: String, content: String) {}
        override suspend fun deleteMemory(name: String) {}
    }

    @Test fun unknownPermissionSupportKeepsTaskModelNavigationScoped() {
        val values = mutableMapOf(
            "assistant" to AssistantConfig(manager = "assistant", defaultPermissionMode = "plan", main = "personal-history"),
            "work" to AssistantConfig(manager = "work", defaultPermissionMode = "default", main = "work-history"),
        )
        val hub = Hub(values)
        val roster = ManagerRoster(listOf(HubManager("assistant", "Personal", online = true, isDefault = true),
            HubManager("work", "Work", online = true)), "assistant")
        var opened: Pair<String, String>? = null
        rule.setContent {
            CompositionLocalProvider(LocalManagerRoster provides roster) {
                PersonaTheme(reduceMotion = true) {
                    AssistantSettingsScreen(hub, emptyList(), onBack = {}, onModelConfiguration = { purpose, owner -> opened = purpose to owner })
                }
            }
        }
        rule.waitForIdle()
        // The page is the current assistant's (picked in the sidebar's switcher): Work's once it is current.
        rule.runOnIdle { AssistantManagerChoice.choose("hub", "work", "assistant") }
        rule.waitForIdle()
        rule.onAllNodesWithText("New conversation permissions").onLast().assertIsNotEnabled()
        rule.onNodeWithText("Unavailable until this assistant can confirm support for permission defaults. Other settings remain available.").assertExists()
        assertEquals("default", values.getValue("work").defaultPermissionMode)
        assertEquals("plan", values.getValue("assistant").defaultPermissionMode)
        assertEquals("work-history", values.getValue("work").main)
        rule.onNodeWithText("Scheduled tasks, follow-ups and runs").performScrollTo().performClick()
        rule.waitForIdle()
        rule.onNodeWithText("Daily review").performScrollTo().performClick()
        rule.waitForIdle()
        rule.onNodeWithText("Task model").performScrollTo().performClick()
        rule.waitForIdle()
        assertEquals("task.daily" to "work", opened)
    }
    @Test fun theCurrentAssistantsProfileSavesToItAndFollowsASwitch() {
        val values = mutableMapOf(
            "assistant" to AssistantConfig(manager = "assistant", defaultPermissionMode = "default", main = "personal-history",
                permissionDefaults = AssistantPermissionDefaults(1, "assistant", AssistantPermissionDefaults.MODES)),
            "work" to AssistantConfig(manager = "work", defaultPermissionMode = "plan", main = "work-history",
                permissionDefaults = AssistantPermissionDefaults(1, "work", AssistantPermissionDefaults.MODES)),
        )
        val roster = ManagerRoster(listOf(HubManager("assistant", "Personal", online = true, isDefault = true),
            HubManager("work", "Work", online = true)), "assistant")
        rule.setContent {
            CompositionLocalProvider(LocalManagerRoster provides roster) {
                PersonaTheme(reduceMotion = true) {
                    AssistantSettingsScreen(Hub(values), emptyList(), onBack = {})
                }
            }
        }
        AssistantManagerChoice.choose("hub", null, "assistant")
        rule.waitForIdle()
        // The current assistant is named here and can be renamed; the sidebar's switcher still picks which one this is.
        rule.onAllNodesWithText("Personal").assertCountEquals(1)
        rule.onAllNodesWithText("Work").assertCountEquals(0)
        rule.onNodeWithText("Rename").assertExists()
        rule.onAllNodesWithText("New conversation permissions").onLast().assertIsEnabled().performClick()
        rule.onNodeWithText("Auto accept edits").performClick()
        rule.waitForIdle()
        assertEquals("acceptEdits", values.getValue("assistant").defaultPermissionMode)
        // Another assistant current: the page reads and writes that one's.
        rule.runOnIdle { AssistantManagerChoice.pick("work") }
        rule.waitForIdle()
        rule.onAllNodesWithText("Work").assertCountEquals(1)
        rule.onAllNodesWithText("Personal").assertCountEquals(0)
        rule.onAllNodesWithText("New conversation permissions").onLast().assertIsEnabled().performClick()
        rule.onNodeWithText("Full access").performClick()
        rule.waitForIdle()
        assertEquals("bypassPermissions", values.getValue("work").defaultPermissionMode)
        assertEquals("acceptEdits", values.getValue("assistant").defaultPermissionMode)
        rule.runOnIdle { AssistantManagerChoice.pick(null) }
        rule.waitForIdle()
        rule.onNodeWithText("Auto accept edits").assertExists()
        assertEquals("personal-history", values.getValue("assistant").main)
        assertEquals("work-history", values.getValue("work").main)
    }

    @Test fun withoutARosterThePersonaNameIsVisibleAndReadOnly() {
        val hub = Hub(mutableMapOf("assistant" to AssistantConfig(manager = "assistant")))
        AssistantManagerChoice.choose("hub", null, "assistant")
        rule.setContent {
            CompositionLocalProvider(LocalManagerRoster provides null) {
                PersonaTheme(reduceMotion = true) { AssistantSettingsScreen(hub, emptyList(), onBack = {}) }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("Persona").assertExists()
        rule.onAllNodesWithText("Rename").assertCountEquals(0)
        assertTrue(hub.renamed.isEmpty())
    }

}

/** #6401: the existing profile drives the existing manager rename API for the assistant selected in the switcher. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-xhdpi")
class AssistantProfileRenameTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    @org.junit.After fun reset() = AssistantManagerChoice.reset()

    @Test fun renameRequestsTheCurrentManagerAndShowsTheConfirmedName() {
        val values = mutableMapOf(
            "assistant" to AssistantConfig(manager = "assistant"),
            "work" to AssistantConfig(manager = "work"),
        )
        val initial = ManagerRoster(listOf(
            HubManager("assistant", "Personal", online = true, isDefault = true),
            HubManager("work", "Work", online = true),
        ), "assistant")
        val roster = androidx.compose.runtime.mutableStateOf(initial)
        val hub = AssistantSettingsManagerTest.Hub(values, managers = initial.managers.toMutableList(), onRename = { confirmed ->
            roster.value = roster.value.copy(managers = roster.value.managers.map { if (it.id == confirmed.id) confirmed else it })
        })
        AssistantManagerChoice.choose("hub", "work", "assistant")
        rule.setContent {
            CompositionLocalProvider(LocalManagerRoster provides roster.value) {
                PersonaTheme(reduceMotion = true) { AssistantSettingsScreen(hub, emptyList(), onBack = {}) }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("Work").assertExists()
        rule.onAllNodesWithText("Personal").assertCountEquals(0)
        rule.onNodeWithText("Rename").performClick()
        rule.onNode(hasSetTextAction()).performTextReplacement("  Studio  ")
        rule.onNodeWithText("Save").assertIsEnabled().performClick()
        rule.waitForIdle()
        assertEquals(listOf("work" to "Studio"), hub.renamed)
        rule.onNodeWithText("Studio").assertExists()
        rule.onAllNodesWithText("Work").assertCountEquals(0)
        assertEquals("Personal", roster.value.manager("assistant")?.name)
    }
}
