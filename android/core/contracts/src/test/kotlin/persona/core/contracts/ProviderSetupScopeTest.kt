package persona.core.contracts

import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.flow
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.*

class ProviderSetupScopeTest {
    @Test fun everyWizardOperationKeepsTheChosenOwner() = runBlocking {
        val calls = mutableListOf<Pair<String, String?>>()
        val source = object : ProviderSetup {
            private suspend fun record(name: String) { calls += name to currentCoroutineContext()[RequestManager]?.id }
            override val secretReferencesAvailable = flow {
                record("references")
                emit(true)
            }
            override suspend fun presets(): List<ProviderPreset> { record("presets"); return emptyList() }
            override suspend fun add(provider: NewProvider) { record("add") }
            override suspend fun create(provider: NewProvider): ProviderAdded { record("create"); return ProviderAdded(provider.name) }
            override suspend fun test(probe: ProviderProbe): ProviderTest { record("test"); return ProviderTest(ok = true) }
            override suspend fun discover(probe: ProviderProbe): ProviderDiscovery { record("discover"); return ProviderDiscovery() }
            override suspend fun update(name: String, changes: ProviderUpdate) { record("update") }
            override suspend fun launchers(): List<HubLauncher> { record("list"); return emptyList() }
            override suspend fun launcher(name: String): HubLauncher { record("detail"); return HubLauncher(name) }
            override suspend fun remove(name: String) { record("remove") }
            override suspend fun restore(name: String) { record("restore") }
            override suspend fun check(name: String, machine: String?): LauncherCheckAnswer { record("check"); return LauncherCheckAnswer(null, LauncherCheck()) }
            override suspend fun importPreview(machine: String, alias: String?): List<ImportedProvider> { record("preview"); return emptyList() }
            override suspend fun import(machine: String, providers: List<String>, alias: String?): LauncherImportResult { record("import"); return LauncherImportResult() }
            override suspend fun parse(text: String): ProviderPasteResult { record("parse"); return ProviderPasteResult() }
            override fun ownsMachine(id: String) = id == "mine"
        }
        val scoped = source.forManager("assistant").forManager("work")
        scoped.presets(); scoped.add(NewProvider("demo", "")); scoped.create(NewProvider("demo", ""))
        scoped.test(ProviderProbe()); scoped.discover(ProviderProbe()); scoped.launchers(); scoped.launcher("demo")
        scoped.remove("demo"); scoped.restore("demo"); scoped.check("demo", null)
        scoped.update("demo", ProviderUpdate("demo", "launcher", "messages", "", "m", "", "", "", "", "", emptyList()))
        scoped.importPreview("computer"); scoped.import("computer", listOf("demo")); scoped.parse("alias demo='claude'")
        assertTrue(scoped.secretReferencesAvailable.first())
        assertTrue(scoped.ownsMachine("mine"))
        assertFalse(scoped.ownsMachine("other"))
        assertEquals(15, calls.size)
        assertTrue(calls.all { it.second == "work" })
        assertNull(currentCoroutineContext()[RequestManager])
    }
}
