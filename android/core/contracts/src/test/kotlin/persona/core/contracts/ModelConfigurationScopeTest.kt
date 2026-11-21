package persona.core.contracts

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.*

class ModelConfigurationScopeTest {
    @Test fun capturedOwnersSurviveSelectionChangesAndLateReplies() = runBlocking {
        val entered = CompletableDeferred<Unit>()
        val release = CompletableDeferred<Unit>()
        val calls = mutableListOf<Pair<String, String?>>()
        val item = ModelPurpose("agent.new")
        val source = object : ModelConfiguration {
            private suspend fun record(operation: String) {
                calls += operation to currentCoroutineContext()[RequestManager]?.id
            }
            override suspend fun catalog(): ModelCatalog {
                record("catalog")
                if (currentCoroutineContext()[RequestManager]?.id == "assistant") {
                    entered.complete(Unit); release.await()
                }
                return ModelCatalog(listOf(item))
            }
            override suspend fun purpose(id: String): ModelPurposeDetail {
                record("purpose"); return ModelPurposeDetail(item)
            }
            override suspend fun save(purpose: ModelPurpose, draft: ModelConfigurationDraft): ModelPurposeSave {
                record("save"); return ModelPurposeSave.Saved(ModelPurposeDetail(item))
            }
            override suspend fun providerModels(provider: String, refresh: Boolean, machine: String?): ProviderModels? {
                record("provider"); return null
            }
        }
        val first = source.forManager("assistant")
        val second = first.forManager("work")
        val pending = async { first.catalog() }
        entered.await()
        second.catalog(); second.purpose("agent.new")
        second.save(item, ModelConfigurationDraft()); second.providerModels("demo", false, null)
        release.complete(Unit); pending.await()
        first.save(item, ModelConfigurationDraft())
        assertEquals(listOf("catalog" to "assistant", "catalog" to "work", "purpose" to "work",
            "save" to "work", "provider" to "work", "save" to "assistant"), calls)
        assertNull(currentCoroutineContext()[RequestManager])
        assertTrue(runCatching { source.forManager("") }.exceptionOrNull() is IllegalArgumentException)
    }

    @Test fun forManagerForwardsModelProfileOperations() = runBlocking {
        val calls = mutableListOf<Pair<String, String?>>()
        val emptyView = ModelProfilesView(revision = 2, active = "performance")
        val source = object : ModelConfiguration {
            private suspend fun record(op: String) {
                calls += op to currentCoroutineContext()[RequestManager]?.id
            }
            override suspend fun catalog() = ModelCatalog()
            override suspend fun purpose(id: String) = throw ModelPurposeNotFound(id)
            override suspend fun save(purpose: ModelPurpose, draft: ModelConfigurationDraft) = ModelPurposeSave.Conflict
            override suspend fun providerModels(provider: String, refresh: Boolean, machine: String?): ProviderModels? = null
            override suspend fun profiles(): ModelProfilesView { record("profiles"); return emptyView }
            override suspend fun applyProfile(id: String): ModelProfilesView { record("apply:$id"); return emptyView }
            override suspend fun rollbackProfile(): ModelProfilesView { record("rollback"); return emptyView }
            override suspend fun saveProfile(id: String, input: ModelProfileSaveInput): ModelProfileSave {
                record("save:$id"); return ModelProfileSave.Saved(emptyView)
            }
        }
        val scoped = source.forManager("work")
        scoped.profiles()
        scoped.applyProfile("economy")
        scoped.rollbackProfile()
        scoped.saveProfile("performance", ModelProfileSaveInput(revision = 2, name = "Performance"))
        assertEquals(
            listOf("profiles" to "work", "apply:economy" to "work", "rollback" to "work", "save:performance" to "work"),
            calls,
        )
    }

    @Test fun profileSaveInputAndDraftPermissionModeSerializeCleanly() {
        val input = ModelProfileSaveInput(
            revision = 3,
            name = "Economy",
            description = "Lower cost",
            purposes = mapOf(
                "assistant" to ModelBinding(provider = "launcher.claude", model = "sonnet", effort = "medium", permissionMode = "plan"),
            ),
            launchers = mapOf(
                "gateway" to ModelProfileLauncherModels(model = "glm-5", smallModel = "", effort = null),
            ),
            tiers = mapOf(
                "fast" to ModelProfileTier(launcher = "claude", model = "haiku", effort = "low", speed = "fast", permissionMode = "acceptEdits", description = "Quick edits"),
            ),
        )
        val json = input.toJson().toString()
        assertTrue(json.contains("\"revision\":3"))
        assertTrue(json.contains("\"permission_mode\":\"plan\""))
        assertTrue(json.contains("\"small_model\":\"\""))
        assertFalse(json.contains("\"opus_model\""))
        assertFalse(json.contains("config_dir"))

        val launcherPurpose = ModelPurpose(
            id = "agent.new",
            kind = "launcher",
            revision = 5,
            fields = listOf("provider", "model", "effort", "permission_mode"),
        )
        val draft = ModelConfigurationDraft(provider = "launcher.claude", model = "opus", effort = "high", permissionMode = "auto")
        val patch = draft.patch(launcherPurpose)
        assertEquals("auto", patch["permission_mode"]?.toString()?.trim('"'))
        assertEquals("", draft.choosingProvider("launcher.codex").permissionMode)
    }
}
