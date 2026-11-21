package persona.core.contracts.api

import kotlinx.coroutines.flow.flowOn
import kotlinx.coroutines.withContext

/** Same request ownership used by connector pages and model configuration. */
internal class ManagerProviderSetup(private val source: ProviderSetup, manager: String) : ProviderSetup {
    init { require(manager.isNotBlank()) }
    private val owner = RequestManager(manager)
    override val builtinPresets get() = source.builtinPresets
    override val launcherScope get() = source.launcherScope
    override val launcherImportParse get() = source.launcherImportParse
    // Provider discovery performs manager-owned extension requests; collect that upstream work under the same owner.
    override val secretReferencesAvailable get() = source.secretReferencesAvailable.flowOn(owner)
    override fun forManager(manager: String) = source.forManager(manager)
    override fun ownsMachine(id: String) = source.ownsMachine(id)
    override suspend fun presets() = withContext(owner) { source.presets() }
    override suspend fun add(provider: NewProvider) = withContext(owner) { source.add(provider) }
    override suspend fun create(provider: NewProvider) = withContext(owner) { source.create(provider) }
    override suspend fun test(probe: ProviderProbe) = withContext(owner) { source.test(probe) }
    override suspend fun discover(probe: ProviderProbe) = withContext(owner) { source.discover(probe) }
    override suspend fun update(name: String, changes: ProviderUpdate) = withContext(owner) { source.update(name, changes) }
    override suspend fun launchers() = withContext(owner) { source.launchers() }
    override suspend fun launcher(name: String) = withContext(owner) { source.launcher(name) }
    override suspend fun remove(name: String) = withContext(owner) { source.remove(name) }
    override suspend fun restore(name: String) = withContext(owner) { source.restore(name) }
    override suspend fun check(name: String, machine: String?) = withContext(owner) { source.check(name, machine) }
    override suspend fun importPreview(machine: String, alias: String?) = withContext(owner) { source.importPreview(machine, alias) }
    override suspend fun import(machine: String, providers: List<String>, alias: String?) = withContext(owner) { source.import(machine, providers, alias) }
    override suspend fun parse(text: String) = withContext(owner) { source.parse(text) }
}
