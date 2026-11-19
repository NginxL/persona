package persona.feature.settings

import android.content.Context
import persona.core.contracts.Machine
import persona.core.contracts.api.ClaudeSetting
import persona.core.contracts.api.MachineManagement
import persona.core.designsystem.failureReasonText
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.serialization.json.JsonElement

/**
 * Which agent's settings the page edits (iOS `AgentSettingsKind`): Claude Code's `~/.claude/settings.json`, Codex's
 * `~/.codex/config.toml`, or Antigravity's `~/.gemini/antigravity-cli/settings.json`. The node keeps a whitelist of each
 * (claudeconfig.go / codexconfig.go / agyompconfig.go) in the same shape.
 */
internal enum class AgentSettingsKind(val title: Int, val demo: Int, val caption: Int) {
    Claude(R.string.settings_src_claude_title, R.string.settings_src_claude_demo, R.string.settings_src_claude_caption),
    Codex(R.string.settings_src_codex_title, R.string.settings_src_codex_demo, R.string.settings_src_codex_caption),
    Agy(R.string.settings_src_agy_title, R.string.settings_src_agy_demo, R.string.settings_src_agy_caption);

    suspend fun read(api: MachineManagement, machineId: String): List<ClaudeSetting> = when (this) {
        Claude -> api.claudeSettings(machineId)
        Codex -> api.codexSettings(machineId)
        Agy -> api.agySettings(machineId)
    }

    suspend fun write(api: MachineManagement, machineId: String, key: String, value: JsonElement): List<ClaudeSetting> = when (this) {
        Claude -> api.setClaudeSetting(machineId, key, value)
        Codex -> api.setCodexSetting(machineId, key, value)
        Agy -> api.setAgySetting(machineId, key, value)
    }
}

/** The machine last looked at and whether changes go to every online machine; kept on this phone, shared by both kinds like iOS. */
internal interface ClaudeSettingsChoices {
    var machine: String
    var applyAll: Boolean
}

internal class StoredClaudeSettingsChoices(context: Context) : ClaudeSettingsChoices {
    private val preferences = context.applicationContext.getSharedPreferences("claude-settings", Context.MODE_PRIVATE)
    override var machine: String
        get() = preferences.getString("machine", "").orEmpty()
        set(value) { preferences.edit().putString("machine", value).apply() }
    override var applyAll: Boolean
        get() = preferences.getBoolean("apply-all", true)
        set(value) { preferences.edit().putBoolean("apply-all", value).apply() }
}

internal data class ClaudeSettingsState(
    val machineId: String = "",
    val applyAll: Boolean = true,
    val settings: List<ClaudeSetting> = emptyList(),
    val loading: Boolean = false,
    val error: ResText? = null,
)

/** First visit: the hub's own machine; a remembered one that went offline falls back to the first online machine. */
internal fun claudeSettingsMachine(remembered: String, online: List<Machine>): String? =
    remembered.takeIf { id -> online.any { it.id == id } } ?: (online.firstOrNull { it.kind == "local" } ?: online.firstOrNull())?.id

/**
 * Settings › Claude Code settings / Codex settings (iOS `ClaudeSettingsView`): the whitelisted options of one machine's [kind],
 * written to every online machine at once unless that is switched off.
 */
internal class ClaudeSettingsModel(
    private val api: MachineManagement,
    private val choices: ClaudeSettingsChoices,
    private val kind: AgentSettingsKind = AgentSettingsKind.Claude,
) {
    private val mutable = MutableStateFlow(ClaudeSettingsState(applyAll = choices.applyAll))
    val state = mutable.asStateFlow()

    /** Keeps the shown machine among the [online] ones (see [claudeSettingsMachine]); none while nothing is online. */
    fun follow(online: List<Machine>) {
        val pick = claudeSettingsMachine(state.value.machineId.ifBlank { choices.machine }, online)
        if (pick == null) mutable.update { it.copy(machineId = "", settings = emptyList(), error = null) } else choose(pick)
    }

    fun choose(machineId: String) {
        choices.machine = machineId
        mutable.update { if (it.machineId == machineId) it else it.copy(machineId = machineId, settings = emptyList(), error = null) }
    }

    fun applyAll(value: Boolean) {
        choices.applyAll = value
        mutable.update { it.copy(applyAll = value) }
    }

    /** Reads the shown machine's settings; [keepError] leaves a write's failure on screen. */
    suspend fun load(keepError: Boolean = false) {
        val machine = state.value.machineId.takeIf { it.isNotBlank() } ?: return
        mutable.update { it.copy(loading = true) }
        try {
            val settings = kind.read(api, machine)
            mutable.update { if (it.machineId == machine) it.copy(settings = settings, error = it.error.takeIf { keepError }) else it }
        } catch (cancelled: CancellationException) { throw cancelled }
        // A write's failure stays: "not saved" says more than "could not read" about the control the owner just moved.
        catch (error: Exception) { mutable.update { if (it.machineId == machine && !(keepError && it.error != null)) it.copy(error = ResText.of(R.string.settings_src_claude_read_failed, failureReasonText(error))) else it } }
        finally { mutable.update { it.copy(loading = false) } }
    }

    /**
     * Writes [key] (JsonNull: back to the default) to this machine, or to every [online] machine; the control moves
     * at once and the hub's answer corrects it. When the shown machine's write fails the control goes back, then the
     * table is read again (which may fail too while offline). Returns the notice to show when it worked.
     */
    suspend fun set(key: String, value: JsonElement, online: List<Machine>): ResText? {
        val current = state.value
        val before = current.settings.firstOrNull { it.key == key }
        val targets = if (current.applyAll) online.map { it.id }.ifEmpty { listOf(current.machineId) } else listOf(current.machineId)
        mutable.update { s -> s.copy(settings = s.settings.map { if (it.key == key) it.copy(value = value) else it }) }
        val failed = mutableListOf<ResText>()
        for (id in targets) {
            try {
                val list = kind.write(api, id, key, value)
                if (id == state.value.machineId) mutable.update { it.copy(settings = list) }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                failed += ResText.of(R.string.settings_src_name_reason, online.firstOrNull { it.id == id }?.displayName ?: id, failureReasonText(error))
                if (id == current.machineId && state.value.machineId == id && before != null) mutable.update { s -> s.copy(settings = s.settings.map { if (it.key == key) before else it }) }
            }
        }
        if (failed.isEmpty()) {
            mutable.update { it.copy(error = null) }
            return if (targets.size > 1) ResText.plural(R.plurals.settings_src_claude_applied, targets.size, targets.size) else ResText.of(R.string.settings_src_saved)
        }
        mutable.update { it.copy(error = ResText.of(R.string.settings_src_claude_not_all_saved, failed.toList())) }
        load(keepError = true)
        return null
    }
}
