package persona.feature.settings

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.yield
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.GroupKeys
import persona.core.contracts.Machine
import persona.core.contracts.api.HubLauncher
import persona.core.contracts.api.NewProvider
import persona.core.contracts.api.PresetModel
import persona.core.contracts.api.ProviderPasteDraft
import persona.core.contracts.api.ProviderPasteMatch
import persona.core.contracts.api.ProviderPasteResult
import persona.core.contracts.api.ProviderPreset
import persona.core.contracts.api.ProviderSetup
import persona.core.contracts.api.ProviderUpdate
import persona.core.contracts.api.PastedProvider
import persona.core.hub.HubError
import persona.core.hub.HubFailure

/** #5220: only rc-file aliases that can still be imported appear under "Found on your computers". */
class ProviderPasteStateTest {
    @Test fun foundAliasesExcludeHiddenAndImportedOnAnyComputerAndPickAnOnlineOne() {
        val machines = listOf(Machine("mac", "Mac", online = false), Machine("linux", "Linux", online = true), Machine("old", "Old"))
        val list = listOf(
            HubLauncher("cc_work", source = "zshrc", baseUrl = "https://work.example.com", machines = listOf("mac", "linux")),
            HubLauncher("cc_offline", source = "zshrc", baseUrl = "https://offline.example.com", machines = listOf("old")),
            HubLauncher("cc_imported", source = "zshrc", baseUrl = "https://imported.example.com", machines = listOf("mac", "linux")),
            HubLauncher("cc_hidden", source = "zshrc", baseUrl = "https://hidden.example.com", machines = listOf("mac"), hidden = true),
            HubLauncher("cc_no_gateway", source = "zshrc", machines = listOf("mac")),
            HubLauncher("cc_no_machine", source = "zshrc", baseUrl = "https://none.example.com"),
            HubLauncher("cc_path", source = "path", baseUrl = "https://path.example.com", machines = listOf("mac")),
            HubLauncher("managed", source = "persona", origin = "zshrc:cc_imported@linux"),
            HubLauncher("managed_other", source = "persona", origin = "preset:team"),
        )
        assertEquals(listOf(
            FoundAlias("cc_work", "https://work.example.com", "linux", "Linux", true),
            FoundAlias("cc_offline", "https://offline.example.com", "old", "Old", false),
        ), foundAliases(list, machines))
        assertEquals("an import on another of the alias's computers still hides it", emptyList<FoundAlias>(),
            foundAliases(listOf(list[2], list[7].copy(origin = "zshrc:cc_imported@mac")), machines))
    }

    @Test fun sameRawMachineIdInAnotherHubCannotSupplyOnlineStatus() {
        val machines = listOf(
            Machine("mac", "Other hub Mac", online = true),
            Machine(GroupKeys.machine("mac", "g12345678"), "Mac", online = false),
            Machine(GroupKeys.machine("linux", "g12345678"), "Linux", online = true),
        )
        val aliases = listOf(HubLauncher("cc_work", source = "zshrc", baseUrl = "https://work.example.com", machines = listOf("mac", "linux")))
        val ownsSecondHub = { id: String -> GroupKeys.namespaceOf(id) == "g12345678" }
        assertEquals(listOf(FoundAlias("cc_work", "https://work.example.com", "linux", "Linux", true)),
            foundAliases(aliases, machines, ownsSecondHub))
    }

    private val pasted = ProviderPasteDraft(
        label = "cc_work",
        provider = PastedProvider(name = "cc_work", displayName = "Work gateway", baseUrl = "https://work.example.com", apiKey = "eg-secret",
            model = "gw-pro", models = listOf(PresetModel("gw-mid", contextWindow = 200000)), smallModel = "gw-small", sonnetModel = "gw-mid",
            args = listOf("--verbose", "--allowedTools", "Bash(git:*)"), headers = "X-Team: demo", env = "MAX_OUTPUT=8000",
            proxy = "http://proxy.example.com:8080", noProxy = "localhost", timeoutMs = 600000, compactWindow = 180000,
            maxContext = 1000000, effort = "high", customModel = "custom", customModelName = "Custom", customModelDescription = "Synthetic"),
        matches = listOf(ProviderPasteMatch("work", "Work", sameKey = true), ProviderPasteMatch("other", sameKey = false)),
    )

    @Test fun aPastedDraftPrefillsTheWholeNewProviderForm() {
        val draft = pasted.newProviderDraft()
        assertEquals("cc_work", draft.name)
        assertEquals("Work gateway", draft.displayName)
        assertEquals("eg-secret", draft.apiKey)
        assertEquals("https://work.example.com", draft.baseUrl)
        assertEquals("gw-pro", draft.model)
        assertEquals(listOf("gw-pro", "gw-mid"), draft.modelsOut().map { it.id })
        assertEquals("gw-small", draft.smallModel)
        assertEquals("gw-mid", draft.sonnetModel)
        assertEquals("X-Team: demo", draft.headers)
        assertEquals("MAX_OUTPUT=8000", draft.env)
        assertEquals(ProxyMode.Manual, draft.proxyMode)
        assertEquals("600000", draft.timeoutMs)
        assertEquals("180000", draft.compactWindow)
        assertEquals("1000000", draft.maxContext)
        assertEquals("high", draft.effort)
        assertEquals("custom", draft.customModel)
        assertEquals("Custom", draft.customModelName)
        assertEquals("Synthetic", draft.customModelDescription)
        assertTrue(draft.args.contains("--verbose"))
        assertTrue(draft.args.contains("Bash(git:*)"))
        val request = NewProviderState(draft = draft).request!!
        assertEquals("Work gateway", request.displayName)
        assertEquals(listOf("--verbose", "--allowedTools", "Bash(git:*)"), request.args)
        assertEquals(listOf("gw-pro", "gw-mid"), request.models?.map { it.id })
    }

    @Test fun sameKeyMergesOnlyModelsAndAnotherKeyReplacesOnlyTheKey() {
        assertEquals(listOf(ProviderPasteAction(PasteActionKind.Merge, "work"), ProviderPasteAction(PasteActionKind.Key, "other"),
            ProviderPasteAction(PasteActionKind.New)), pasted.actions())
        assertEquals(ProviderPasteAction(PasteActionKind.Merge, "work"), pasted.defaultAction())
        val keyless = pasted.copy(provider = pasted.provider.copy(apiKey = null), matches = pasted.matches.map { it.copy(sameKey = false) })
        assertEquals(listOf(PasteActionKind.Merge, PasteActionKind.Merge, PasteActionKind.New), keyless.actions().map { it.kind })
        assertEquals(ProviderPasteAction(PasteActionKind.New), keyless.defaultAction())
        val existing = HubLauncher("work", source = "persona", baseUrl = "https://old.example.com", model = "old-pro",
            models = listOf(PresetModel("old-pro")), hasKey = true, auth = "bearer", sonnetModel = "old-mid", args = listOf("--old"),
            opusModel = "old-max", effort = "low", timeoutMs = 1000)
        val merged = pasted.existingDraft(existing, ProviderPasteAction(PasteActionKind.Merge, "work"))
        assertEquals("old-pro", merged.model)
        assertEquals("https://old.example.com", merged.baseUrl)
        assertEquals("bearer", merged.auth)
        assertEquals("old-mid", merged.sonnetModel)
        assertEquals("old-max", merged.opusModel)
        assertEquals("low", merged.effort)
        assertEquals("1000", merged.timeoutMs)
        assertEquals("", merged.apiKey)
        assertEquals(listOf("old-pro", "gw-pro", "gw-mid"), merged.models.map { it.id })
        val keyed = pasted.existingDraft(existing, ProviderPasteAction(PasteActionKind.Key, "work"))
        assertEquals("eg-secret", keyed.apiKey)
        assertEquals(providerDraftOf(existing).copy(apiKey = "eg-secret"), keyed)
        assertFalse("no key means no key replacement choice", keyless.actions().any { it.kind == PasteActionKind.Key })
    }

    @Test fun aChatCompletionsDraftKeepsItsProtocolAndDropsLauncherOnlyFields() {
        val chat = pasted.copy(provider = pasted.provider.copy(kind = "api", api = "chat", headers = null, env = null))
        val request = NewProviderState(draft = chat.newProviderDraft()).request!!
        assertEquals("api", request.kind)
        assertEquals("chat", request.api)
        assertEquals(null, request.args)
        assertEquals(null, request.headers)
        assertEquals(null, request.env)
        assertEquals(listOf("gw-pro", "gw-mid"), request.models?.map { it.id })
    }

    private class PasteHub : ProviderSetup {
        val gates = mutableMapOf<String, CompletableDeferred<ProviderPasteResult>>()
        val parsed = mutableListOf<String>()
        val updated = mutableListOf<ProviderUpdate>()
        var launchersCalls = 0
        var unsupported = false
        var nonLink = false
        val existing = HubLauncher("work", source = "persona", baseUrl = "https://work.example.com", model = "old",
            models = listOf(PresetModel("old")), hasKey = true, sonnetModel = "old-mid")
        val aliases = listOf(existing, HubLauncher("cc_work", source = "zshrc", baseUrl = "https://work.example.com", machines = listOf("mac")))
        override suspend fun presets() = emptyList<ProviderPreset>()
        override suspend fun add(provider: NewProvider) {}
        override suspend fun launchers(): List<HubLauncher> { launchersCalls++; return aliases }
        override suspend fun launcher(name: String) = existing
        override suspend fun update(name: String, changes: ProviderUpdate) { updated += changes }
        override suspend fun parse(text: String): ProviderPasteResult {
            parsed += text
            if (unsupported) throw HubFailure(HubError.Server, "HTTP 404", status = 404)
            return gates[text]?.await() ?: ProviderPasteResult(format = if (text.startsWith("persona://provider/import") && !nonLink) "link" else "shell", drafts = listOf(ProviderPasteDraft(label = text,
                provider = PastedProvider(name = "new_$text", baseUrl = "https://$text.example.com", apiKey = "secret", model = "new-model"),
                matches = listOf(ProviderPasteMatch("work", sameKey = true)))))
        }
    }

    @Test fun featureGateLoadsFoundAliasesAndAnOlderHubDoesNotAsk() = runBlocking {
        val hub = PasteHub()
        val model = NewProviderModel(hub)
        model.prepare(null, false, listOf(Machine("mac", "Mac", online = true)))
        model.load(importParse = false)
        assertEquals(0, hub.launchersCalls)
        model.load(importParse = true)
        assertEquals(1, hub.launchersCalls)
        assertEquals(listOf("cc_work"), model.state.value.foundAliases.map { it.name })
        model.openPaste()
        assertEquals(WizardStep.Paste, model.state.value.step)
    }

    @Test fun anImportLinkIsReadThroughTheExistingPasteFlow() = runBlocking {
        val hub = PasteHub()
        val model = NewProviderModel(hub)
        model.load(importParse = true)
        val link = "persona://provider/import?v=1&data=fixture"
        model.openImportLink(link)
        assertEquals(WizardStep.Paste, model.state.value.step)
        assertEquals(link, model.state.value.paste.text)
        assertTrue(model.state.value.paste.fromLink)
        assertEquals(listOf(link), hub.parsed)
        assertTrue("nothing is added while the draft is reviewed", hub.updated.isEmpty())
        model.pasteSomethingElse()
        assertEquals("", model.state.value.paste.text)
        assertFalse(model.state.value.paste.fromLink)
    }

    @Test fun anImportLinkOnAnOlderHubSaysSoWithoutSavingIt() = runBlocking {
        val hub = PasteHub().apply { unsupported = true }
        val model = NewProviderModel(hub)
        model.load(importParse = true)
        model.openImportLink("persona://provider/import?v=1&data=fixture")
        assertTrue(model.state.value.paste.error != null)
        assertTrue(model.state.value.paste.fromLink)
        assertTrue(model.state.value.step == WizardStep.Paste)
        assertTrue(hub.updated.isEmpty())
    }

    @Test fun anImportLinkRejectsASuccessfulAnswerWithAnotherFormat() = runBlocking {
        val hub = PasteHub().apply { nonLink = true }
        val model = NewProviderModel(hub)
        model.load(importParse = true)
        model.openImportLink("persona://provider/import?v=1&data=fixture")
        assertTrue(model.state.value.paste.fromLink)
        assertTrue("a non-link answer is an unsupported hub, not a draft", model.state.value.paste.error != null)
        assertEquals(null, model.state.value.paste.answer)
        assertTrue(hub.updated.isEmpty())
    }

    @Test fun aLateAnswerForOlderTextCannotBeContinued() = runBlocking {
        val hub = PasteHub()
        val model = NewProviderModel(hub)
        model.load(importParse = true)
        model.openPaste()
        hub.gates["first"] = CompletableDeferred()
        hub.gates["second"] = CompletableDeferred()
        model.editPaste("first")
        val first = async { model.parsePaste() }
        yield()
        model.editPaste("second")
        val second = async { model.parsePaste() }
        yield()
        hub.gates.getValue("second").complete(ProviderPasteResult(format = "second", drafts = listOf(ProviderPasteDraft(
            provider = PastedProvider(name = "second", baseUrl = "https://second.example.com", model = "m")))))
        second.await()
        hub.gates.getValue("first").complete(ProviderPasteResult(format = "first", drafts = listOf(ProviderPasteDraft(
            provider = PastedProvider(name = "first", baseUrl = "https://first.example.com", model = "m")))))
        first.await()
        val paste = model.state.value.paste
        assertEquals("second", paste.answerText)
        assertEquals("second", paste.answer?.format)
        assertEquals(0, paste.draft)
        assertEquals("second", paste.choice?.draft?.provider?.name)
        model.editPaste("second changed")
        assertEquals("an older answer is not a choice after an edit", null, model.state.value.paste.choice)
    }

    @Test fun continuePrefillsNewThenSameKeyEditAndAnotherKeyEdit() = runBlocking {
        val hub = PasteHub()
        val model = NewProviderModel(hub)
        model.load(importParse = true)
        model.openPaste()
        model.editPaste("draft")
        model.parsePaste()
        assertEquals(ProviderPasteAction(PasteActionKind.Merge, "work"), model.state.value.paste.choice?.action)
        model.choosePasteAction(ProviderPasteAction(PasteActionKind.New))
        assertTrue(model.continuePaste())
        assertEquals(WizardStep.Form, model.state.value.step)
        assertEquals("new_draft", model.state.value.draft.name)
        assertEquals("new-model", model.state.value.draft.model)
        assertFalse(model.state.value.editing)
        model.backToPaste()
        assertEquals(WizardStep.Paste, model.state.value.step)
        model.choosePasteAction(ProviderPasteAction(PasteActionKind.Merge, "work"))
        assertTrue(model.continuePaste())
        assertTrue(model.state.value.editing)
        assertTrue(model.state.value.dirty)
        assertEquals(listOf("old", "new-model"), model.state.value.draft.models.map { it.id })
        assertEquals("", model.state.value.draft.apiKey)
        model.backToPaste()
        model.choosePasteAction(ProviderPasteAction(PasteActionKind.Key, "work"))
        assertTrue(model.continuePaste())
        assertEquals("secret", model.state.value.draft.apiKey)
        assertEquals(listOf("old"), model.state.value.draft.models.map { it.id })
        assertEquals("old-mid", model.state.value.draft.sonnetModel)
    }
}
