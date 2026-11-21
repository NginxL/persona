package persona.feature.settings

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.launch
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import persona.core.contracts.api.HubLauncher
import persona.core.contracts.api.ImportedProvider
import persona.core.contracts.api.LauncherCheck
import persona.core.contracts.api.LauncherImportResult
import persona.core.contracts.api.ProviderSetup
import persona.core.contracts.isTraeCliId
import persona.core.designsystem.UiText
import persona.core.designsystem.failureReasonText
import persona.core.hub.HubFailure

/** The three groups of Settings › Model providers (iOS `ModelLaunchersView`): kept by Persona, from ~/.zshrc, hidden. */
internal data class ProviderGroups(val managed: List<HubLauncher>, val zshrc: List<HubLauncher>, val hidden: List<HubLauncher>) {
    val isEmpty: Boolean get() = managed.isEmpty() && zshrc.isEmpty() && hidden.isEmpty()

    companion object {
        /** [query] matches the name, the models, the gateway or a machine's name ([machineName]). */
        fun of(list: List<HubLauncher>, query: String = "", machineName: (String) -> String = { it }): ProviderGroups {
            val q = query.trim()
            val shown = if (q.isEmpty()) list else list.filter { l ->
                (listOfNotNull(l.name, l.settingsTitle, l.model, l.smallModel, l.baseUrl) + l.machines.map(machineName)).any { it.contains(q, ignoreCase = true) }
            }
            return ProviderGroups(shown.filter { it.managed }, shown.filter { !it.managed && !it.hidden }, shown.filter { !it.managed && it.hidden })
        }
    }
}

internal val HubLauncher.settingsTitle: String
    get() = if (isTraeCliId(name) || isTraeCliId(harness)) title else name

/** A gateway's host, for the row (the whole address is on the provider's page). */
internal fun providerHost(baseUrl: String?): String? =
    baseUrl?.takeIf { it.isNotBlank() }?.let { runCatching { java.net.URI(it).host }.getOrNull() }?.takeIf { it.isNotBlank() }

internal data class ProvidersUiState(
    val launchers: List<HubLauncher>? = null,
    val loading: Boolean = false,
    val error: ResText? = null,
    /** A hide, restore or delete that failed: said once, the row back where it was. */
    val notice: ResText? = null,
)

/** Settings › Model providers: the hub's providers; hide, restore and delete move the row at once and put it back on failure. */
internal class ProvidersModel(private val api: ProviderSetup) {
    private val mutable = MutableStateFlow(ProvidersUiState())
    val state = mutable.asStateFlow()

    suspend fun load() {
        mutable.update { it.copy(loading = true) }
        try {
            val list = api.launchers()
            mutable.update { it.copy(launchers = list, error = null) }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) { mutable.update { it.copy(error = ResText.of(R.string.settings_providers_load_failed, failureReasonText(error))) } }
        finally { mutable.update { it.copy(loading = false) } }
    }

    fun clearNotice() = mutable.update { it.copy(notice = null) }

    /** Hides a ~/.zshrc one or lists it again; a Persona one is not hidden (it is deleted). */
    suspend fun setHidden(launcher: HubLauncher, hide: Boolean): Boolean {
        if (launcher.managed) return false
        return change(launcher, { list -> list.map { if (it.name == launcher.name) it.copy(hidden = hide) else it } }) {
            if (hide) api.remove(launcher.name) else api.restore(launcher.name)
        }
    }

    /** Deletes a Persona one with its key. */
    suspend fun delete(launcher: HubLauncher): Boolean {
        if (!launcher.managed) return false
        return change(launcher, { list -> list.filterNot { it.name == launcher.name } }) { api.remove(launcher.name) }
    }

    private suspend fun change(launcher: HubLauncher, edit: (List<HubLauncher>) -> List<HubLauncher>, call: suspend () -> Unit): Boolean {
        val before = state.value.launchers ?: return false
        mutable.update { it.copy(launchers = edit(before), notice = null) }
        return try { call(); true }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            mutable.update { it.copy(launchers = before, notice = ResText.of(R.string.settings_providers_change_failed, launcher.settingsTitle, failureReasonText(error))) }
            false
        }
    }
}

internal data class ProviderDetailUiState(
    val launcher: HubLauncher? = null,
    val error: ResText? = null,
    val busy: Boolean = false,
    /** "Test availability" by machine id ("" for a direct API, the hub's machine when none was picked): null while it runs. */
    val checks: Map<String, LauncherCheck?> = emptyMap(),
    /** Why a machine's check did not come back, in the page's language (keyed like [checks]). */
    val checkFailures: Map<String, UiText> = emptyMap(),
    val notice: ResText? = null,
    /** Deleted (or hidden): the page closes. */
    val gone: Boolean = false,
)

/** One provider: its settings, "Test availability" on each machine, and hide / restore / delete. */
internal class ProviderDetailModel(private val api: ProviderSetup, private val name: String) {
    private val mutable = MutableStateFlow(ProviderDetailUiState())
    val state = mutable.asStateFlow()

    suspend fun load() {
        try { mutable.update { it.copy(launcher = api.launcher(name), error = null) } }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) { mutable.update { it.copy(error = ResText.of(R.string.settings_providers_load_failed, failureReasonText(error))) } }
    }

    /**
     * Runs a check on each of [machines] (a direct API: once, on the hub) at the same time, as iOS's task group does;
     * each row fills in when its own answer comes.
     */
    suspend fun check(machines: List<String?>) {
        val targets = machines.ifEmpty { listOf(null) }
        mutable.update { s -> s.copy(checks = s.checks + targets.associate { (it ?: "") to null }, checkFailures = s.checkFailures - targets.map { it ?: "" }.toSet()) }
        coroutineScope {
            targets.forEach { machine ->
                launch {
                    var failure: UiText? = null
                    val result = try { api.check(name, machine).check }
                    catch (cancelled: CancellationException) { throw cancelled }
                    catch (error: Exception) { failure = failureReasonText(error); LauncherCheck(ok = false) }
                    val key = machine ?: ""
                    mutable.update { s -> s.copy(checks = s.checks + (key to result), checkFailures = failure?.let { s.checkFailures + (key to it) } ?: s.checkFailures) }
                }
            }
        }
    }

    suspend fun remove() = perform { api.remove(name); mutable.update { it.copy(gone = true) } }

    suspend fun restore() = perform {
        api.restore(name)
        mutable.update { s -> s.copy(launcher = s.launcher?.copy(hidden = false)) }
    }

    private suspend fun perform(block: suspend () -> Unit) {
        if (state.value.busy) return
        mutable.update { it.copy(busy = true, notice = null) }
        try { block() }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) { mutable.update { it.copy(notice = ResText.of(R.string.settings_providers_change_failed, name, failureReasonText(error))) } }
        finally { mutable.update { it.copy(busy = false) } }
    }
}

/** "Import from ~/.zshrc" (iOS `LauncherImportModel`): pick a machine, tick what its aliases come to, import. */
internal enum class ImportPhase { Machine, Loading, Preview, Importing, Done }

internal data class ProviderImportUiState(
    val phase: ImportPhase = ImportPhase.Machine,
    val machine: String = "",
    val alias: String? = null,
    val providers: List<ImportedProvider> = emptyList(),
    val picked: Set<String> = emptySet(),
    val result: LauncherImportResult? = null,
    val error: ResText? = null,
) {
    val canImport: Boolean get() = phase == ImportPhase.Preview && picked.isNotEmpty()
}

internal class ProviderImportModel(private val api: ProviderSetup) {
    private val mutable = MutableStateFlow(ProviderImportUiState())
    val state = mutable.asStateFlow()

    fun choose(machine: String, alias: String? = null) = mutable.update { it.copy(machine = machine, alias = alias) }

    fun toggle(name: String) = mutable.update { s -> s.copy(picked = if (name in s.picked) s.picked - name else s.picked + name) }

    /** Reads the machine's aliases; everything found starts ticked. */
    suspend fun load() {
        val machine = state.value.machine.takeIf { it.isNotBlank() } ?: return
        mutable.update { it.copy(phase = ImportPhase.Loading, error = null) }
        try {
            val found = api.importPreview(machine, state.value.alias)
            mutable.update { it.copy(phase = ImportPhase.Preview, providers = found, picked = found.map { p -> p.name }.toSet()) }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            // The machine's personad is too old to read ~/.zshrc for this (iOS `outdated`): it needs deploying again.
            val failure = HubFailure.from(error)
            val message = if (failure.status == 409 && failure.code == "node_outdated") ResText.of(R.string.settings_providers_import_outdated)
                else ResText.of(R.string.settings_providers_import_read_failed, failureReasonText(error))
            mutable.update { it.copy(phase = ImportPhase.Machine, error = message) }
        }
    }

    suspend fun runImport() {
        val s = state.value
        if (!s.canImport) return
        mutable.update { it.copy(phase = ImportPhase.Importing, error = null) }
        try {
            // In the order the hub listed them.
            val result = api.import(s.machine, s.providers.map { it.name }.filter { it in s.picked }, s.alias)
            mutable.update { it.copy(phase = ImportPhase.Done, result = result) }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            mutable.update { it.copy(phase = ImportPhase.Preview, error = ResText.of(R.string.settings_providers_import_failed, failureReasonText(error))) }
        }
    }

    fun restart() = mutable.update { ProviderImportUiState(machine = it.machine, alias = it.alias) }
}
