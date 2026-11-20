package persona.feature.settings

import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.*

class ExtensionCandidatesStateTest {
    private val providers = listOf(ModelProvider("launcher.one"), ModelProvider("launcher.two"))
    private val purpose = ModelPurpose("extension.worker", revision = 9, providerIds = providers.map { it.id },
        candidates = ModelCandidates("extension", modes = listOf("extension", "off", "custom"), most = 2, directProviderIds = providers.map { it.id }))
    private val detail = ModelPurposeDetail(purpose, providers)
    private fun state(vararg rows: CandidateRow): ModelPurposeState = ModelPurposeState(detail = detail,
        draft = ModelConfigurationDraft.from(purpose).copy(candidates = CandidateDraft("custom", rows.toList())))

    @Test fun onlyValidOrderedProviderAndModelPairsCanBeSaved() {
        val first = CandidateRow(3, "launcher.one", "one")
        val second = CandidateRow(7, "launcher.two", "one")
        assertNull(state(first, second).candidateProblem)
        assertNull(state(first.copy(model = ""), second.copy(model = "~two:latest")).candidateProblem)
        assertNotNull(state().candidateProblem)
        assertNotNull(state(first, second, first.copy(key = 8, model = "third")).candidateProblem)
        assertNotNull(state(first, first.copy(key = 7, model = " one ")).candidateProblem)
        assertNotNull(state(first.copy(provider = "launcher.no")).candidateProblem)
        assertNotNull(state(first.copy(model = "invalid model")).candidateProblem)
        assertNotNull(state(first.copy(model = "a".repeat(257))).candidateProblem)
        val none = state(first).copy(detail = detail.copy(purpose = purpose.copy(candidates = purpose.candidates?.copy(directProviderIds = emptyList()))))
        assertNotNull(none.candidateProblem)
        val stored = purpose.copy(candidates = purpose.candidates?.copy(mode = "custom", directProviderIds = emptyList(),
            custom = listOf(ModelCandidate(model = "one", provider = "launcher.one"))))
        val old = none.copy(detail = detail.copy(purpose = stored))
        assertFalse(old.candidateChanged)
        assertNotNull(old.candidateProblem)
        assertNull(state(first).copy(draft = ModelConfigurationDraft(candidates = CandidateDraft("off", emptyList()))).candidateProblem)
    }

    @Test fun refreshingProvidersDoesNotTurnSomeoneElsesCandidatePolicyIntoALocalEdit() = runBlocking {
        var remote = detail
        val api = object : ModelConfiguration {
            override suspend fun catalog() = ModelCatalog()
            override suspend fun purpose(id: String) = remote
            override suspend fun providerModels(provider: String, refresh: Boolean, machine: String?) = null
            override suspend fun save(purpose: ModelPurpose, draft: ModelConfigurationDraft) = error("Refreshing may only read")
        }
        val editor = ModelPurposeModel(api, purpose.id)
        editor.load()
        val off = purpose.copy(revision = 10, candidates = purpose.candidates?.copy(mode = "off"))
        remote = detail.copy(purpose = off)
        editor.providerAdded("launcher.two")
        assertEquals("off", editor.state.value.draft.candidates?.mode)
        assertFalse(editor.state.value.draft.patch(off).containsKey("candidates"))
        val edited = CandidateDraft("custom", listOf(CandidateRow(0, "launcher.one", "saved-locally")))
        editor.edit(editor.state.value.draft.copy(candidates = edited))
        remote = detail.copy(purpose = purpose.copy(revision = 11))
        editor.providerAdded("launcher.two")
        assertTrue(editor.state.value.conflict)
        assertEquals(edited, editor.state.value.draft.candidates)
        editor.providerAdded("launcher.two")
        assertNotNull(editor.state.value.error)
        assertNull(editor.save())
    }

    @Test fun newerRevisionIsOnlyLoadedAfterTheUserElectsToDiscardTheConflictingDraft() = runBlocking {
        var server = detail
        val sent = mutableListOf<ModelConfigurationDraft>()
        val api = object : ModelConfiguration {
            override suspend fun catalog() = ModelCatalog()
            override suspend fun purpose(id: String) = server
            override suspend fun providerModels(provider: String, refresh: Boolean, machine: String?) = null
            override suspend fun save(purpose: ModelPurpose, draft: ModelConfigurationDraft): ModelPurposeSave {
                sent += draft
                assertEquals(9L, purpose.revision)
                return ModelPurposeSave.Conflict
            }
        }
        val editor = ModelPurposeModel(api, purpose.id)
        editor.load()
        val invalid = state(CandidateRow(1, "launcher.one", "non valid")).draft
        editor.edit(invalid)
        editor.save()
        assertTrue(sent.isEmpty())
        val draft = state(CandidateRow(1, "launcher.two", "b"), CandidateRow(2, "launcher.one", "")).draft
        editor.edit(draft)
        editor.save(restore = true)
        assertTrue(sent.isEmpty())
        server = detail.copy(purpose = purpose.copy(revision = 10, override = ModelBinding(model = "someone-else")))
        editor.save()
        assertTrue(editor.state.value.conflict)
        assertEquals(draft, editor.state.value.draft)
        assertEquals(9L, editor.state.value.purpose?.revision)
        editor.save()
        assertEquals(listOf(draft), sent)
        editor.load()
        assertFalse(editor.state.value.conflict)
        assertEquals(10L, editor.state.value.purpose?.revision)
        assertEquals(ModelConfigurationDraft.from(server.purpose), editor.state.value.draft)
    }
}
