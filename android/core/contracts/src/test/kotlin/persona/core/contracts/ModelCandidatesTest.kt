package persona.core.contracts

import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.*

class ModelCandidatesTest {
    private val all = listOf(
        ModelProvider("launcher.api", kind = "api", api = "responses", hasKey = true),
        ModelProvider("launcher.messages", api = "messages", hasKey = true),
        ModelProvider("launcher.legacy", hasKey = true),
        ModelProvider("launcher.responses", api = "responses", hasKey = true),
        ModelProvider("launcher.keyless", kind = "api"),
        ModelProvider("launcher.hidden", kind = "api", hasKey = true, hidden = true),
        ModelProvider("external.any", kind = "api", hasKey = true),
        ModelProvider("launcher.plugin", kind = "extension", hasKey = true),
    )
    private val base = ModelPurpose("extension.notes", revision = 4, fields = listOf("provider", "model"), providerIds = all.map { it.id })
    private fun ids(p: ModelPurpose) = p.candidateProviders(all).map { it.id }

    @Test fun olderHubsOnlyOfferDirectManagedProvidersAmongThisPurposesBindings() {
        val p = base.copy(candidates = ModelCandidates())
        assertEquals(listOf("launcher.api", "launcher.messages", "launcher.legacy"), ids(p))
        assertEquals(listOf("launcher.messages"), ids(p.copy(providerIds = listOf("launcher.messages", "external.any"))))
        assertTrue(ids(p.copy(providerIds = null)).isEmpty())
        assertTrue(ids(p.copy(id = "inbox.triage")).isEmpty())
        assertEquals(p.providersFrom(all), base.providersFrom(all))
    }

    @Test fun directAllowlistIsAuthoritativeIncludingAnExplicitEmptyList() {
        val s = ModelCandidates(directProviderIds = listOf("launcher.keyless", "launcher.responses", "launcher.hidden"))
        assertEquals(listOf("launcher.responses", "launcher.keyless"), ids(base.copy(candidates = s, providerIds = all.map { it.id } - "launcher.hidden")))
        assertTrue(ids(base.copy(candidates = s.copy(directProviderIds = emptyList()))).isEmpty())
    }

    @Test fun onlyEditedPoliciesSendCandidatesAndDefaultModelsKeepFollowingTheirProvider() {
        val p = base.copy(candidates = ModelCandidates(mode = "custom", custom = listOf(
            ModelCandidate(model = "effective-now", provider = "launcher.api", configuredModel = ""),
            ModelCandidate(model = "older-hub-model", provider = "launcher.messages"),
        )))
        val first = ModelConfigurationDraft.from(p)
        val policy = requireNotNull(first.candidates)
        assertEquals(listOf("", "older-hub-model"), policy.custom.map { it.model })
        assertFalse(first.patch(p).containsKey("candidates"))
        val choosing = first.choosingProvider("launcher.messages")
        assertEquals(policy, choosing.candidates)
        assertFalse(choosing.patch(p).containsKey("candidates"))
        val moved = first.copy(candidates = policy.move(0, 1))
        val patch = moved.patch(p)
        assertEquals(setOf("revision", "candidates"), patch.keys)
        assertEquals(JsonCodec.parseToJsonElement("""{"mode":"custom","custom":[{"provider":"launcher.messages","model":"older-hub-model"},{"provider":"launcher.api","model":""}]}"""), patch["candidates"])
        assertEquals(setOf("revision","provider","model","candidates"), moved.copy(model = "another").patch(p).keys)
        assertEquals(JsonCodec.parseToJsonElement("""{"mode":"off"}"""), first.copy(candidates = policy.copy(mode = "off")).patch(p)["candidates"])
        assertFalse(ModelConfigurationDraft.Restore.patch(p).containsKey("candidates"))
    }

    @Test fun legacyDetailsAndOtherPurposesRetainTheirUnchangedBindingContract() {
        val old = JsonCodec.decodeFromString<ModelPurpose>("""{"id":"extension.note","revision":9}""")
        assertNull(ModelConfigurationDraft.from(old).candidates)
        val other = old.copy(id = "assistant", candidates = ModelCandidates())
        assertNull(ModelConfigurationDraft.from(other).candidates)
        val unoffered = ModelConfigurationDraft(candidates = CandidateDraft("off", emptyList())).patch(other)
        assertEquals(JsonCodec.parseToJsonElement("""{"revision":9,"provider":null,"model":null}"""), unoffered)
    }

    @Test fun editingRowsWhileAnotherModeIsSelectedDoesNotTransmitAnInactiveList() {
        val p = base.copy(candidates = ModelCandidates(mode = "extension"))
        val draft = ModelConfigurationDraft.from(p)
        val temp = requireNotNull(draft.candidates).add(all).replace(CandidateRow(0, "launcher.api", "one"))
        assertFalse(draft.copy(candidates = temp).patch(p).containsKey("candidates"))
        val custom = draft.copy(candidates = temp.copy(mode = "custom", custom = listOf(CandidateRow(4, "launcher.api", "  model-id  "))))
        assertEquals("model-id", custom.patch(p).getValue("candidates").jsonObject.getValue("custom").jsonArray.single().jsonObject.getValue("model").jsonPrimitive.content)
    }
}
