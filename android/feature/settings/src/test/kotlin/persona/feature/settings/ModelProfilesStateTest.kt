package persona.feature.settings

import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.*
import persona.core.hub.HubError
import persona.core.hub.HubFailure

class ModelProfilesStateTest {
    private val sampleProfile = ModelProfile(
        id = "performance",
        name = "Performance",
        description = "Use strong models",
        purposes = mapOf(
            "assistant" to ModelBinding(provider = "launcher.claude", model = "opus", effort = "high", permissionMode = "auto"),
            "kb" to ModelBinding(provider = "api.fast", model = "gpt-5-mini"),
        ),
        launchers = mapOf(
            "gateway" to ModelProfileLauncherModels(model = "glm-5", smallModel = ""),
        ),
        tiers = mapOf(
            "strong" to ModelProfileTier(launcher = "claude", model = "opus", effort = "high", speed = "fast", permissionMode = "acceptEdits", description = "Hard tasks"),
            "standard" to ModelProfileTier(launcher = "claude", model = "sonnet", effort = "medium", description = "Everyday tasks"),
        ),
        active = true,
        changes = emptyList(),
    )

    private val sampleView = ModelProfilesView(
        revision = 3,
        active = "performance",
        rollback = ModelProfileUndo(to = "economy", changes = listOf("assistant")),
        profiles = listOf(
            sampleProfile,
            ModelProfile(id = "economy", name = "Economy", active = false, changes = listOf("assistant")),
        ),
    )

    private fun fakeApi(
        onProfiles: suspend () -> ModelProfilesView? = { sampleView },
        onApply: suspend (String) -> ModelProfilesView = { sampleView.copy(active = it) },
        onRollback: suspend () -> ModelProfilesView = { sampleView.copy(active = "economy", rollback = null) },
        onSave: suspend (String, ModelProfileSaveInput) -> ModelProfileSave = { _, input ->
            ModelProfileSave.Saved(sampleView.copy(revision = input.revision + 1))
        },
    ) = object : ModelConfiguration {
        override suspend fun catalog() = ModelCatalog()
        override suspend fun purpose(id: String) = throw ModelPurposeNotFound(id)
        override suspend fun save(purpose: ModelPurpose, draft: ModelConfigurationDraft) = ModelPurposeSave.Conflict
        override suspend fun providerModels(provider: String, refresh: Boolean, machine: String?): ProviderModels? = null
        override suspend fun profiles() = onProfiles()
        override suspend fun applyProfile(id: String) = onApply(id)
        override suspend fun rollbackProfile() = onRollback()
        override suspend fun saveProfile(id: String, input: ModelProfileSaveInput) = onSave(id, input)
    }

    @Test fun modelProfilesLoadApplyRollbackSaveAndHandleErrors() = runBlocking {
        // Load success
        val model = ModelProfilesModel(fakeApi())
        assertNotNull(model.load())
        assertEquals(3L, model.state.value.view?.revision)
        assertFalse(model.state.value.unsupported)
        assertNull(model.state.value.error)

        // Apply profile
        assertTrue(model.apply("economy"))
        assertEquals("economy", model.state.value.view?.active)
        assertNull(model.state.value.actionError)

        // Rollback
        assertTrue(model.rollback())
        assertNull(model.state.value.view?.rollback)

        // Save success
        val input = ModelProfileSaveInput(revision = 3, name = "Performance")
        assertTrue(model.save("performance", input))
        assertEquals(4L, model.state.value.view?.revision)
        assertFalse(model.state.value.conflict)

        // Older hub (404 -> null) marks unsupported cleanly without error
        val oldHub = ModelProfilesModel(fakeApi(onProfiles = { null }))
        assertNull(oldHub.load())
        assertTrue(oldHub.state.value.unsupported)
        assertNull(oldHub.state.value.error)

        // 409 Conflict sets conflict flag and message
        val conflictModel = ModelProfilesModel(fakeApi(onSave = { _, _ -> ModelProfileSave.Conflict }))
        conflictModel.load()
        assertFalse(conflictModel.save("performance", input))
        assertTrue(conflictModel.state.value.conflict)
        assertNotNull(conflictModel.state.value.actionError)

        // Network/server errors surface cleanly
        val broken = ModelProfilesModel(
            fakeApi(
                onProfiles = { throw HubFailure(HubError.NoNetwork, "offline") },
                onApply = { throw HubFailure(HubError.Server, "failed", 500) },
                onRollback = { throw HubFailure(HubError.Server, "no rollback", 409) },
                onSave = { _, _ -> throw HubFailure(HubError.Server, "bad request", 400) },
            )
        )
        assertNull(broken.load())
        assertNotNull(broken.state.value.error)
        assertFalse(broken.apply("economy"))
        assertNotNull(broken.state.value.actionError)
        broken.clearErrors()
        assertNull(broken.state.value.error)
        assertNull(broken.state.value.actionError)
        assertFalse(broken.rollback())
        assertNotNull(broken.state.value.actionError)
        assertFalse(broken.save("performance", input))
        assertNotNull(broken.state.value.actionError)
    }

    @Test fun draftRoundTripAndDirtyDetection() {
        val draft = profileToDraft(sampleProfile, listOf("assistant", "agent.new", "kb", "inbox.triage"))
        assertEquals("Performance", draft.name)
        assertTrue(draft.purposes.getValue("assistant").included)
        assertFalse(draft.purposes.getValue("agent.new").included)
        assertEquals(2, draft.tiers.size)
        assertEquals(listOf("standard", "strong"), draft.tiers.map { it.name })
        assertFalse(isProfileDraftDirty(draft, draft))

        val edited = draft.copy(description = "Updated description")
        assertTrue(isProfileDraftDirty(edited, draft))
        val payload = draftToProfileInput(edited, revision = 3)
        assertEquals(3L, payload.revision)
        assertEquals("Updated description", payload.description)
        assertEquals(setOf("assistant", "kb"), payload.purposes?.keys)
        assertEquals("", payload.launchers?.get("gateway")?.smallModel)
        assertEquals(setOf("standard", "strong"), payload.tiers?.keys)
    }

    @Test fun draftValidationCatchesInvalidFieldsAndAllowsLegacyTierModel() {
        val validDraft = profileToDraft(sampleProfile)
        assertTrue(profileDraftProblems(validDraft).isEmpty())

        // Empty name & too-long description
        val badIdentity = validDraft.copy(name = "   ", description = "a".repeat(301))
        assertEquals(2, profileDraftProblems(badIdentity).size)

        // Invalid tier name, duplicate tier name, missing launcher, bad speed, bad model ID
        val badTiers = validDraft.copy(
            tiers = listOf(
                ModelProfileTierDraft(key = "1", name = "BadName", launcher = "", model = "bad model", effort = "ultra", speed = "warp", permissionMode = "invalid", description = "line1\nline2"),
                ModelProfileTierDraft(key = "2", name = "ok_tier", launcher = "claude"),
                ModelProfileTierDraft(key = "3", name = "ok_tier", launcher = "claude"),
            )
        )
        val problems = profileDraftProblems(badTiers)
        assertTrue("expected multiple validation problems, got $problems", problems.size >= 7)

        // Unchanged legacy model ID on existing tier is preserved without blocking save
        val legacyTier = ModelProfileTierDraft(
            key = "legacy",
            originalName = "legacy",
            originalModel = "legacy model id with spaces?",
            name = "legacy",
            launcher = "claude",
            model = "legacy/custom:1",
        )
        val legacyDraft = validDraft.copy(tiers = listOf(legacyTier.copy(originalModel = "legacy/custom:1")))
        assertTrue(profileDraftProblems(legacyDraft).isEmpty())
    }

    @Test fun showsEffortSupportsChatEffortMarkedProviders() {
        val triage = ModelPurpose(id = "inbox.triage", kind = "api", fields = listOf("provider", "model", "effort"))
        val plainChat = ModelProvider(id = "api.chat", kind = "api", api = "chat", chatEffort = false)
        val markedChat = plainChat.copy(chatEffort = true)
        assertFalse(showsEffort(triage, plainChat))
        assertTrue(showsEffort(triage, markedChat))
    }
}
