package persona.feature.settings

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.yield
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Machine
import persona.core.contracts.api.*

/** Main and per-computer key references: explicit storage, safe probes, and exact replacement/removal maps. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en")
class ProviderKeyReferenceStateTest {
    private val reference = SecretReference("example-vault", "Example Vault", "op://Dev/Work/token", "Work token")
    private val stored = HubLauncher("work", source = "persona", baseUrl = "https://gateway.example.com", model = "m",
        keyReference = reference, share = Share.Selected, scopeMachines = listOf("mac"), keyMachines = emptyList(),
        machineKeyReferences = mapOf("mac" to reference.copy(reference = "op://Dev/Work/laptop", title = "Laptop token")))
    private val machines = listOf(Machine("mac", "Work Mac", online = true), Machine("box", "Build box", online = true))

    private class Hub(available: Boolean = true) : ProviderSetup {
        val referencesAvailable = MutableStateFlow(available)
        override val secretReferencesAvailable = referencesAvailable
        val added = mutableListOf<NewProvider>()
        val updated = mutableListOf<Pair<String, ProviderUpdate>>()
        var probes = 0
        var discoveries = 0
        override suspend fun presets(): List<ProviderPreset> = emptyList()
        override suspend fun add(provider: NewProvider) { added += provider }
        override suspend fun update(name: String, changes: ProviderUpdate) { updated += name to changes }
        override suspend fun test(probe: ProviderProbe): ProviderTest { probes++; return ProviderTest(ok = true) }
        override suspend fun discover(probe: ProviderProbe): ProviderDiscovery { discoveries++; return ProviderDiscovery() }
    }

    private fun delayedGate(backend: Hub, gate: CompletableDeferred<Boolean>) = object : ProviderSetup by backend {
        override val secretReferencesAvailable = flow { emit(gate.await()) }
    }

    @Test fun aStoredReferenceOpensInReferenceModeWithoutAResolvedValue() {
        val draft = providerDraftOf(stored)
        assertTrue(draft.apiKeyByReference)
        assertEquals("", draft.apiKey)
        val same = providerUpdate(draft, stored)
        assertNull(same.apiKey)
        assertNull(same.apiKeyReference)
        assertEquals("op://Dev/Work/new", providerUpdate(draft.copy(apiKey = " op://Dev/Work/new "), stored).apiKeyReference)
        assertNull(providerUpdate(draft.copy(apiKey = "op://Dev/Work/new"), stored).apiKey)

        val ordinary = providerUpdate(draft.copy(apiKeyByReference = false, apiKey = "op://literal/value"), stored)
        assertEquals("op://literal/value", ordinary.apiKey)
        assertNull(ordinary.apiKeyReference)
        assertEquals(setOf("mac"), scopeFrom(stored)?.keyRefs)
    }

    @Test fun aTypedReferenceCannotReachConnectionTestsOrModelDiscovery() = runBlocking {
        val hub = Hub()
        val model = NewProviderModel(hub)
        model.prepare(stored, true, machines)
        assertEquals("work", model.state.value.formProbe?.name)
        assertNull(model.state.value.formProbe?.apiKey)
        model.edit(model.state.value.draft.copy(apiKey = "op://Dev/Work/new"))
        assertTrue(model.state.value.referencePending)
        assertNull(model.state.value.formProbe)
        model.probeForm()
        model.discoverForm()
        assertEquals(0, hub.probes)
        assertEquals(0, hub.discoveries)

        model.edit(model.state.value.draft.copy(apiKeyByReference = false, apiKey = "op://literal/key"))
        assertFalse(model.state.value.referencePending)
        assertEquals("op://literal/key", model.state.value.formProbe?.apiKey)
    }

    @Test fun createAndPresetBodiesCarryExactlyOneKindOfMainKey() {
        val hub = Hub()
        val model = NewProviderModel(hub)
        model.prepare(null, true, machines)
        model.edit(model.state.value.draft.copy(name = "work", baseUrl = "https://gateway.example.com", model = "m",
            apiKey = "op://Dev/Work/token", apiKeyByReference = true))
        val request = model.state.value.request!!
        assertNull(request.apiKey)
        assertEquals("op://Dev/Work/token", request.apiKeyReference)

        val preset = ProviderPreset("example/preset", name = "Example")
        val presetState = NewProviderState(presets = listOf(preset), draft = NewProviderDraft(
            fromPreset = true, preset = preset.id, name = "jv_preset", apiKey = "op://Dev/Preset/token", apiKeyByReference = true))
        val presetRequest = presetState.request!!
        assertNull(presetRequest.apiKey)
        assertEquals("op://Dev/Preset/token", presetRequest.apiKeyReference)
    }

    @Test fun perComputerReferencesPartitionAndRemovalDoesNotOverrideAReplacement() {
        val scope = requireNotNull(scopeFrom(stored)).toggle("box")
            .typeKey("mac", " op://Dev/Work/new-laptop ")
            .typeKey("box", "ordinary-key")
        val fields = scopeFields(scope, stored, machines)
        assertEquals(mapOf("mac" to "op://Dev/Work/new-laptop"), fields.machineKeyReferences)
        assertEquals(mapOf("box" to "ordinary-key"), fields.machineKeys)

        val removed = scopeFields(scope.removeKey("mac").typeKey("mac", " op://Dev/Work/new-laptop "), stored, machines)
        assertEquals(mapOf("mac" to "op://Dev/Work/new-laptop"), removed.machineKeyReferences)
        assertFalse(removed.machineKeys?.containsKey("mac") == true)
        val actuallyRemoved = scopeFields(scopeFrom(stored)!!.removeKey("mac"), stored, machines)
        assertEquals(mapOf("mac" to ""), actuallyRemoved.machineKeys)
        assertNull(actuallyRemoved.machineKeyReferences)

        val ordinary = scopeFields(scope.referenceKey("mac", false).typeKey("mac", "op://literal/key"), stored, machines)
        assertEquals(mapOf("box" to "ordinary-key", "mac" to "op://literal/key"), ordinary.machineKeys)
        assertNull(ordinary.machineKeyReferences)
    }

    @Test fun aSlowCreateGateFreezesTheRequestBeforeChecking() = runBlocking {
        val backend = Hub()
        val gate = CompletableDeferred<Boolean>()
        val model = NewProviderModel(delayedGate(backend, gate))
        model.prepare(null, true, machines)
        model.edit(model.state.value.draft.copy(name = "work", baseUrl = "https://gateway.example.com", model = "m",
            apiKey = "op://Dev/Work/first", apiKeyByReference = true))

        val saving = async { model.save() }
        yield()
        model.edit(model.state.value.draft.copy(apiKey = "op://Dev/Work/second"))
        gate.complete(true)

        assertEquals("launcher.work", saving.await())
        assertEquals("op://Dev/Work/first", backend.added.single().apiKeyReference)
        assertEquals("op://Dev/Work/first", model.state.value.draft.apiKey)
    }

    @Test fun aSlowEditGateCannotMarkANewerUnsentDraftAsSaved() = runBlocking {
        val backend = Hub()
        val gate = CompletableDeferred<Boolean>()
        val model = NewProviderModel(delayedGate(backend, gate))
        model.prepare(stored, true, machines)
        model.edit(model.state.value.draft.copy(apiKey = "op://Dev/Work/first"))

        val saving = async { model.saveEdit() }
        yield()
        model.edit(model.state.value.draft.copy(apiKey = "op://Dev/Work/second"))
        gate.complete(true)

        assertTrue(saving.await())
        assertEquals("op://Dev/Work/first", backend.updated.single().second.apiKeyReference)
        assertEquals("op://Dev/Work/first", model.state.value.draft.apiKey)
        assertEquals("op://Dev/Work/first", model.state.value.opened?.first?.apiKey)
    }

    @Test fun aLiveGateCloseRefusesCreateAndClearsOnlyUnsavedReferenceDrafts() = runBlocking {
        val hub = Hub(available = true)
        val model = NewProviderModel(hub)
        model.prepare(null, true, machines)
        model.edit(model.state.value.draft.copy(name = "work", baseUrl = "https://gateway.example.com", model = "m",
            apiKey = "op://Dev/Work/new", apiKeyByReference = true))
        model.editScope(ScopeForm().referenceKey("mac", true).typeKey("mac", "op://Dev/Work/laptop-new")
            .typeKey("box", "ordinary-key"))
        assertNotNull(model.state.value.request)

        // The switch was available when the form was drawn, but its live capability closed before Save.
        hub.referencesAvailable.value = false
        assertNull(model.save())
        assertTrue(hub.added.isEmpty())
        val state = model.state.value
        assertFalse(state.draft.apiKeyByReference)
        assertEquals("", state.draft.apiKey)
        assertEquals(emptySet<String>(), state.scope?.keyRefs)
        assertEquals("", state.scope?.keys?.get("mac"))
        assertEquals("ordinary-key", state.scope?.keys?.get("box"))
        assertEquals(R.string.settings_provider_reference_unavailable, state.error?.id)
    }

    @Test fun aLiveGateCloseRefusesAnEditWithoutReplacingStoredReferences() = runBlocking {
        val hub = Hub(available = true)
        val model = NewProviderModel(hub)
        model.prepare(stored, true, machines)
        model.edit(model.state.value.draft.copy(apiKey = "op://Dev/Work/replacement"))
        assertTrue(model.state.value.canSaveEdit)
        assertEquals("op://Dev/Work/replacement", model.state.value.update?.apiKeyReference)

        hub.referencesAvailable.value = false
        assertFalse(model.saveEdit())
        assertTrue(hub.updated.isEmpty())
        val state = model.state.value
        assertFalse(state.draft.apiKeyByReference)
        assertEquals("", state.draft.apiKey)
        assertEquals(emptySet<String>(), state.scope?.keyRefs)
        assertNull(state.update?.apiKey)
        assertNull(state.update?.apiKeyReference)
        assertNull(state.update?.machineKeyReferences)
        assertEquals(R.string.settings_provider_reference_unavailable, state.error?.id)
    }

    @Test fun providerGoneDisplayFallsBackToTitleThenReference() {
        val res = ApplicationProvider.getApplicationContext<Context>().resources
        assertEquals("From Example Vault · Work token", providerReferenceState(res, reference))
        assertEquals("From example-vault · Work token", providerReferenceState(res, reference.copy(providerName = "")))
        assertEquals("Work token", providerReferenceState(res, reference.copy(provider = "", providerName = "")))
        assertEquals("op://Dev/Work/token", providerReferenceState(res, reference.copy(provider = "", providerName = "", title = null)))
    }
}
