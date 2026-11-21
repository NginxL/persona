package persona.feature.settings

import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.yield
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.*

/**
 * "New model provider" on a hub with built-in presets (docs/assistant/onboarding.md 4.3 B / 5.4): pick, paste the key (tested by itself),
 * assign that follows the readiness, what step (3) says; and "Test connection" / "Fetch models" of a gateway typed in.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ProviderWizardTest {
    private val deepseek = ProviderPreset("builtin/deepseek", ProviderPreset.BUILTIN, "DeepSeek", description = "国内直接开通",
        baseUrl = "https://api.deepseek.example", model = "deepseek-chat", auth = "bearer", signupUrl = "https://platform.deepseek.example/keys",
        displayName = "深度求索")
    private val responses = ProviderPreset("example/api", "example", "示例 API", kind = "api", api = "responses", baseUrl = "https://api.example.com/v1")

    private class Hub(val presets: List<ProviderPreset>) : ProviderSetup {
        override val builtinPresets = flowOf(true)
        val referencesAvailable = MutableStateFlow(true)
        override val secretReferencesAvailable = referencesAvailable
        val owners = mutableListOf<String?>()
        private suspend fun noteOwner() { owners += kotlinx.coroutines.currentCoroutineContext()[RequestManager]?.id }
        val created = mutableListOf<NewProvider>()
        val tests = mutableListOf<ProviderProbe>()
        val discovers = mutableListOf<ProviderProbe>()
        var answer = ProviderAdded("deepseek", "DeepSeek", assigned = listOf("assistant"), kept = listOf(KeptPurpose("agent.new", "customized")))
        var testResult = ProviderTest(ok = true, ms = 300, model = "deepseek-chat")
        var gate: CompletableDeferred<Unit>? = null
        override suspend fun presets() = presets.also { noteOwner() }
        override suspend fun add(provider: NewProvider) { created += provider }
        override suspend fun create(provider: NewProvider): ProviderAdded { noteOwner(); created += provider; return answer }
        override suspend fun test(probe: ProviderProbe): ProviderTest { noteOwner(); tests += probe; gate?.await(); return testResult }
        override suspend fun discover(probe: ProviderProbe): ProviderDiscovery { noteOwner(); discovers += probe; return ProviderDiscovery(listOf(PresetModel("m1"), PresetModel("m2"))) }
    }

    private fun readiness(works: Boolean, noClaude: Boolean = false) = AssistantReadiness(mode = "setup", ready = works,
        machine = ReadinessMachine("mbp", "工作电脑"), steps = listOf(
            ReadinessStep("computer", "ok"),
            if (noClaude) ReadinessStep("claude", "problem", code = "claude_missing") else ReadinessStep("claude", "ok"),
            ReadinessStep("model", if (works) "ok" else "unknown")))

    @Test fun aProviderWizardKeepsTheModelPagesOwnerThroughCreateAndFollowUp() = runBlocking {
        val hub = Hub(listOf(deepseek))
        val scoped = hub.forManager("assistant").forManager("work")
        val model = NewProviderModel(scoped)
        model.load(hubNames = true)
        model.choose("builtin/deepseek")
        model.editKey(model.state.value.draft.copy(apiKey = "sk-example-key"))
        model.testKey()
        assertTrue(model.saveKey())
        assertTrue(hub.owners.size >= 3)
        assertTrue(hub.owners.all { it == "work" })
        assertEquals("launcher.deepseek", providerId(model.state.value.added!!.result.name))
    }

    @Test fun aPresetKeyIsTestedAndSavedWithAssignWhenNothingAnswersYet() = runBlocking {
        val hub = Hub(listOf(deepseek, responses))
        val model = NewProviderModel(hub) { readiness(works = false) }
        model.loadReadiness()
        model.load(hubNames = true)
        assertEquals(WizardStep.Pick, model.state.value.step)
        assertEquals("深度求索", deepseek.title)
        assertEquals("https://platform.deepseek.example/keys", deepseek.signupLink)

        model.choose("builtin/deepseek")
        var s = model.state.value
        assertEquals(WizardStep.Key, s.step)
        assertEquals("deepseek-chat", s.draft.presetModel)
        assertTrue("assign follows the readiness: nothing answers, so on", s.assign)
        assertFalse(s.canSaveKey)

        // Too short to test by itself; then a pasted key is tested with the preset (its auth, no model: the default).
        model.editKey(s.draft.copy(apiKey = "sk-1"))
        model.testKey()
        assertTrue(hub.tests.isEmpty())
        model.editKey(model.state.value.draft.copy(apiKey = " sk-example-key "))
        model.testKey()
        assertEquals(ProviderProbe(preset = "builtin/deepseek", apiKey = "sk-example-key", auth = "bearer", model = null), hub.tests.single())
        assertEquals(true, model.state.value.keyTest?.result?.ok)

        // The name is optional: the hub names it. Saved with assign.
        assertTrue(model.state.value.canSaveKey)
        assertTrue(model.saveKey())
        assertEquals(NewProvider("", "sk-example-key", preset = "builtin/deepseek", assign = AssignPurposes), hub.created.single())
        s = model.state.value
        assertEquals(WizardStep.Done, s.step)
        val summary = assignSummary(s.added!!.result, s.added!!.asked)
        assertEquals(R.string.settings_onb_assigned, summary.assigned?.id)
        assertEquals(1, summary.kept.size)
        assertEquals("launcher.deepseek", providerId(s.added!!.result.name))
    }

    @Test fun aClaudeCodeThatAlreadyAnswersIsNotTakenOverUnlessAsked() = runBlocking {
        val hub = Hub(listOf(deepseek))
        val model = NewProviderModel(hub) { readiness(works = true) }
        model.loadReadiness()
        model.load(hubNames = true)
        model.choose("builtin/deepseek")
        assertTrue(model.state.value.works)
        assertFalse(model.state.value.assign)
        model.editKey(model.state.value.draft.copy(apiKey = "sk-example-key"))
        model.saveKey()
        assertNull(hub.created.single().assign)
        assertNull(assignSummary(model.state.value.added!!.result, false).assigned)

        // Turned on by hand, it is sent.
        val again = NewProviderModel(hub) { readiness(works = true) }
        again.loadReadiness(); again.load(hubNames = true); again.choose("builtin/deepseek")
        again.setAssign(true)
        again.editKey(again.state.value.draft.copy(apiKey = "sk-example-key"))
        again.saveKey()
        assertEquals(AssignPurposes, hub.created.last().assign)
    }

    @Test fun savingWaitsForTheReadinessAndNotesAMissingClaudeCode() = runBlocking {
        val hub = Hub(listOf(deepseek))
        val answer = CompletableDeferred<AssistantReadiness>()
        val model = NewProviderModel(hub) { answer.await() }
        val asking = async { model.loadReadiness() }
        yield()
        model.load(hubNames = true)
        model.choose("builtin/deepseek")
        model.editKey(model.state.value.draft.copy(apiKey = "sk-example-key"))
        assertTrue(model.state.value.readinessPending)
        val saving = async { model.saveKey() }
        yield()
        assertTrue("nothing is sent before the readiness says whether Claude Code answers", hub.created.isEmpty())
        answer.complete(readiness(works = false, noClaude = true))
        asking.await(); saving.await()
        val added = model.state.value.added!!
        assertTrue(added.noClaudeBefore)
        assertTrue(stillNoClaude(added, model.state.value.readiness))
        assertFalse(stillNoClaude(added.copy(noClaudeBefore = false), readiness(works = true)))
    }

    @Test fun aTestOvertakenByAnotherKeyLandsNowhere() = runBlocking {
        val hub = Hub(listOf(deepseek)).apply { gate = CompletableDeferred() }
        val model = NewProviderModel(hub)
        model.load(hubNames = true)
        model.choose("builtin/deepseek")
        model.editKey(model.state.value.draft.copy(apiKey = "sk-first-key"))
        val first = async { model.testKey() }
        yield()
        assertEquals(true, model.state.value.keyTest?.running)
        assertFalse("not while its test runs", model.state.value.canSaveKey)
        model.editKey(model.state.value.draft.copy(apiKey = "sk-second-key"))
        hub.gate!!.complete(Unit)
        first.await()
        assertNull(model.state.value.keyTest)
    }

    @Test fun aPresetReferenceSkipsTheKeyTestAndCreatesOnlyAReference() = runBlocking {
        val hub = Hub(listOf(deepseek))
        val model = NewProviderModel(hub)
        model.load(hubNames = true)
        model.choose(deepseek.id)
        model.editKey(model.state.value.draft.copy(apiKey = "op://Dev/DeepSeek/key", apiKeyByReference = true))
        model.testKey()
        assertTrue("an unresolved reference is never sent to the model gateway", hub.tests.isEmpty())
        assertTrue(model.saveKey())
        val created = hub.created.single()
        assertNull(created.apiKey)
        assertEquals("op://Dev/DeepSeek/key", created.apiKeyReference)
        assertEquals(deepseek.id, created.preset)
    }

    @Test fun changingPresetsCannotReclassifyAReferenceAsAnOrdinaryKey() = runBlocking {
        val hub = Hub(listOf(deepseek, responses))
        val model = NewProviderModel(hub)
        model.load(hubNames = true)
        model.choose(deepseek.id)
        model.editKey(model.state.value.draft.copy(apiKey = "op://Dev/DeepSeek/key", apiKeyByReference = true))
        model.choose(responses.id)
        assertFalse(model.state.value.draft.apiKeyByReference)
        assertEquals("", model.state.value.draft.apiKey)
        model.testKey()
        assertTrue(hub.tests.isEmpty())

        model.editKey(model.state.value.draft.copy(apiKey = "sk-example-key"))
        model.testKey()
        assertEquals("sk-example-key", hub.tests.single().apiKey)
        assertTrue(model.saveKey())
        assertEquals("sk-example-key", hub.created.single().apiKey)
        assertNull(hub.created.single().apiKeyReference)
    }

    @Test fun aPresetReferenceSaveRechecksTheLiveGate() = runBlocking {
        val hub = Hub(listOf(deepseek))
        val model = NewProviderModel(hub)
        model.load(hubNames = true)
        model.choose(deepseek.id)
        model.editKey(model.state.value.draft.copy(apiKey = "op://Dev/DeepSeek/key", apiKeyByReference = true))
        hub.referencesAvailable.value = false
        assertFalse(model.saveKey())
        assertTrue(hub.created.isEmpty())
        assertFalse(model.state.value.draft.apiKeyByReference)
        assertEquals("", model.state.value.draft.apiKey)
        assertEquals(R.string.settings_provider_reference_unavailable, model.state.value.error?.id)
    }

    @Test fun directApiProtocolsMatchTheHubKeyTestAndClaudeCompatibility() {
        val presets = listOf(null, "", "messages", "responses", "chat", "future").map {
            ProviderPreset("example/$it", "example", kind = "api", api = it)
        }
        assertEquals(listOf(true, true, true, true, true, false), presets.map { it.testable })
        assertEquals(listOf(true, true, true, false, false, false), presets.map { it.runsClaude })
    }

    @Test fun aDirectApiDoesNotWaitForReadinessOrAssign() = runBlocking {
        val hub = Hub(listOf(responses))
        val readinessAnswer = CompletableDeferred<AssistantReadiness>()
        val model = NewProviderModel(hub) { readinessAnswer.await() }
        val asking = async { model.loadReadiness() }
        yield()
        model.load(hubNames = true)
        model.choose("example/api")
        model.setAssign(true) // Even an explicit choice is meaningless for a direct API.
        model.editKey(model.state.value.draft.copy(apiKey = "sk-example-key"))
        withTimeout(1_000) { assertTrue("a direct API must not wait for readiness", model.saveKey()) }
        assertNull(hub.created.single().assign)
        assertFalse(model.state.value.added!!.asked)
        assertFalse(model.state.value.added!!.noClaudeBefore)
        asking.cancel()
    }

    @Test fun aResponsesPresetIsTestedAndTheGatewayGoesToTheForm() = runBlocking {
        val hub = Hub(listOf(responses))
        val model = NewProviderModel(hub)
        model.load(hubNames = true)
        model.choose("example/api")
        assertTrue(responses.testable)
        model.editKey(model.state.value.draft.copy(apiKey = "sk-example-key"))
        model.testKey()
        assertEquals(ProviderProbe(preset = "example/api", apiKey = "sk-example-key", auth = "api_key"), hub.tests.single())
        model.backToPick()
        assertEquals(WizardStep.Pick, model.state.value.step)
        model.choose(null)
        assertEquals(WizardStep.Form, model.state.value.step)
        assertFalse(model.state.value.usesPreset)
    }

    @Test fun theFormTestsTheGatewayAndFillsTheModelFromItsList() = runBlocking {
        val hub = Hub(emptyList())
        val model = NewProviderModel(hub)
        model.load(hubNames = true)
        model.choose(null)
        assertNull(model.state.value.formProbe)
        model.edit(model.state.value.draft.copy(name = "jv_gw", baseUrl = " https://gateway.example.com ", apiKey = "k", auth = "bearer"))
        model.discoverForm()
        assertEquals(ProviderProbe(baseUrl = "https://gateway.example.com", apiKey = "k", auth = "bearer"), hub.discovers.single())
        assertEquals("m1", model.state.value.draft.model)
        model.probeForm()
        assertEquals("m1", hub.tests.single().model)
        assertEquals(true, model.state.value.probe?.ok)
        assertEquals("bearer", model.state.value.request?.auth)
    }

    @Test fun anOlderHubKeepsItsFormAndNeedsTheName() = runBlocking {
        val hub = object : ProviderSetup {
            val added = mutableListOf<NewProvider>()
            override suspend fun presets() = listOf(deepseek)
            override suspend fun add(provider: NewProvider) { added += provider }
        }
        val model = NewProviderModel(hub)
        model.load(preferPreset = true, hubNames = false)
        assertEquals(WizardStep.Form, model.state.value.step)
        assertTrue(model.state.value.usesPreset)
        model.edit(model.state.value.draft.copy(name = "jv_ds", apiKey = "k"))
        assertEquals("launcher.jv_ds", model.save())
        assertNull(hub.added.single().assign)
    }
}
