package persona.android.di

import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.SessionKey
import persona.core.contracts.StandingAuthorization
import persona.core.contracts.StandingAuthorizationList
import persona.core.contracts.api.*

class AssistantManagementGroupScopeTest {
    private class Management(
        private val calls: MutableList<String>,
        private val manager: String = "unbound",
    ) : AssistantManagement {
        override fun forManager(manager: String?) = Management(calls, manager ?: "default")
        override suspend fun warm() { calls += "$manager warm" }
        override suspend fun config() = AssistantConfig(machineId = "pro").also { calls += "$manager config" }
        override suspend fun updateConfig(update: AssistantConfigUpdate) =
            AssistantConfig(machineId = requireNotNull(update.machineId)).also { calls += "$manager update ${update.machineId}" }
        override suspend fun mainAssistantLauncherList(machineId: String, refresh: Boolean) =
            AssistantLaunchers(default = LauncherDefault(provider = "main-engine", model = "main-model", effort = "high")).also {
                calls += "$manager main $machineId $refresh"
            }
        override suspend fun endConversation(session: SessionKey) { calls += "$manager close ${session.machineId}/${session.sessionId}" }
        override suspend fun runTask(id: String) = SessionKey("pro", id).also { calls += "$manager run $id" }
        override suspend fun tasks() = emptyList<ScheduledTask>()
        override suspend fun saveTask(task: ScheduledTask) = task
        override suspend fun deleteTask(id: String) = error("unused")
        override suspend fun freshTask(id: String) = error("unused")
        override suspend fun runs() = emptyList<Run>()
        override suspend fun notes() = AssistantNotes()
        override suspend fun setNotes(text: String) = error("unused")
        override suspend fun memory() = emptyList<MemoryFile>()
        override suspend fun saveMemory(name: String, content: String) = error("unused")
        override suspend fun deleteMemory(name: String) = error("unused")
        override suspend fun clearMainConversation(expect: String?) = "archived-$expect".also { calls += "$manager clear $expect" }
        override suspend fun standingAuthorizations(): StandingAuthorizationList {
            calls += "$manager list"
            return StandingAuthorizationList(1, listOf(StandingAuthorization(id = "sa", machineId = "pro"), StandingAuthorization(id = "blank")))
        }
        override suspend fun revokeStandingAuthorization(id: String) { calls += "$manager revoke $id" }
    }

    @Test fun bindingManagerPreservesGroupInputAndOutputTranslationAndLeavesOriginalUnbound() = runBlocking {
        val calls = mutableListOf<String>()
        val grouped = Management(calls).inGroup(GroupNames("g0a1b2c3d"))
        val work = grouped.forManager("work")
        assertEquals("pro~g0a1b2c3d", work.config().machineId)
        assertEquals("pro~g0a1b2c3d", work.updateConfig(AssistantConfigUpdate(machineId = "pro~g0a1b2c3d")).machineId)
        work.endConversation(SessionKey("pro~g0a1b2c3d", "main"))
        assertEquals(SessionKey("pro~g0a1b2c3d", "task"), work.runTask("task"))
        grouped.warm()
        assertEquals(listOf("work config", "work update pro", "work close pro/main", "work run task", "unbound warm"), calls)
    }

    @Test fun mainCatalogPreservesManagerMachineAndRefreshAcrossGroupRouting() = runBlocking {
        val calls = mutableListOf<String>()
        val work = Management(calls).inGroup(GroupNames("g0a1b2c3d")).forManager("work")
        val catalog = work.mainAssistantLauncherList("pro~g0a1b2c3d", refresh = true)
        assertEquals(LauncherDefault("main-engine", "main-model", "high"), catalog.default)
        assertEquals(listOf("work main pro true"), calls)
    }

    @Test fun theRoutedManagementForwardsTheIndependentMainCatalog() {
        val routed = Class.forName("persona.android.di.RoutedAssistantManagement")
        assertTrue(routed.declaredMethods.any { it.name == "mainAssistantLauncherList" })
    }

    @Test fun rebindingDefaultPreservesGroupAndPrimaryGroupRemainsIdentity() = runBlocking {
        val calls = mutableListOf<String>()
        val source = Management(calls)
        assertSame(source, source.inGroup(GroupNames(null)))
        val grouped = source.inGroup(GroupNames("g0a1b2c3d"))
        val work = grouped.forManager("work")
        val default = work.forManager(null)
        assertEquals("pro~g0a1b2c3d", default.config().machineId)
        work.warm()
        default.warm()
        assertEquals(listOf("default config", "work warm", "default warm"), calls)
    }

    // Clear context (#5100): the group's wrapper hands it to its hub for the bound assistant, `expect` as it is (a session id).
    @Test fun clearingTheMainConversationGoesThroughTheGroupForTheBoundManager() = runBlocking {
        val calls = mutableListOf<String>()
        val work = Management(calls).inGroup(GroupNames("g0a1b2c3d")).forManager("work")
        assertEquals("archived-main-1", work.clearMainConversation("main-1"))
        assertEquals(listOf("work clear main-1"), calls)
    }

    // The routed wrapper the app pairs with (ChatWiringModule) forwards it too: without its own override the app would get
    // the interface's "unavailable" default.
    @Test fun theRoutedManagementForwardsClearingTheMainConversation() {
        val routed = Class.forName("persona.android.di.RoutedAssistantManagement")
        assertTrue(routed.declaredMethods.any { it.name == "clearMainConversation" })
    }

    @Test fun standingAuthorizationsTranslateTheMachineAndRevokeTheIdAsGiven() = runBlocking {
        val calls = mutableListOf<String>()
        val work = Management(calls).inGroup(GroupNames("g0a1b2c3d")).forManager("work")
        val list = work.standingAuthorizations()
        assertEquals(listOf("pro~g0a1b2c3d", ""), list.authorizations.map { it.machineId })
        work.revokeStandingAuthorization("a/b")
        assertEquals(listOf("work list", "work revoke a/b"), calls)
    }

    @Test fun theRoutedManagementForwardsStandingAuthorizations() {
        val routed = Class.forName("persona.android.di.RoutedAssistantManagement")
        assertTrue(routed.declaredMethods.any { it.name == "standingAuthorizations" })
        assertTrue(routed.declaredMethods.any { it.name == "revokeStandingAuthorization" })
    }
}
