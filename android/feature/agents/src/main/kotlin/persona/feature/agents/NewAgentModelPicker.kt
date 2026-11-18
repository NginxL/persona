package persona.feature.agents

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import persona.core.contracts.PaseoAgent
import persona.core.contracts.api.AgentModel
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons

/**
 * "Model": what the launcher that will run starts with, picked from the list its agent shows in /model (the sheet).
 * Nothing when that launcher has no list (a compatible CLI, a runtime's, older hubs); starting never waits for it.
 */
internal fun LazyListScope.newAgentModelItems(state: NewAgentState, editable: Boolean, onOpen: () -> Unit, onRetry: () -> Unit) {
    val models = state.models
    if (state.modelLauncher.isEmpty() || models.machineId != state.machineId || models.launcher != state.modelLauncher) return
    val list = state.modelList
    if (list == null && !models.loading && models.error == null) return
    item("model") {
        JSectionHeader(stringResource(R.string.agents_model))
        JGroupItem(GroupPosition.Single) {
            when {
                list?.locked != null -> JRow(stringResource(R.string.agents_mode_follow), enabled = false, chevron = false,
                    trailing = { JIcon(PhIcons.LockSimple, stringResource(R.string.agents_model_locked), tint = PersonaTheme.palette.ink3) })
                list != null -> JRow(state.pickedModel?.label ?: stringResource(R.string.agents_model_default),
                    subtitle = state.pickedModel.let { picked -> if (picked != null) modelDetail(picked) else defaultModelDetail(agentResources(), state) },
                    enabled = editable, onClick = onOpen, trailing = if (models.loading) ({ JSpinner() }) else null)
                models.loading -> JRow(stringResource(R.string.agents_model_loading), subtitle = stringResource(R.string.agents_model_loading_detail, state.modelLauncherTitle), chevron = false, trailing = { JSpinner() })
                else -> JRow(stringResource(R.string.agents_model_failed), subtitle = stringResource(R.string.agents_model_failed_detail, models.error?.text().orEmpty()), subtitleLines = 4, chevron = false,
                    trailing = { JButton(stringResource(R.string.agents_model_retry), onRetry, enabled = editable, kind = JButtonKind.Secondary, size = JButtonSize.Small) })
            }
        }
        list?.locked?.let {
            Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2,
                modifier = Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))
        }
    }
}

/** The list behind the "Model" row (the design system's [JModelSheet]): only for this start. */
@Composable
internal fun NewAgentModelSheet(
    state: NewAgentState,
    onPick: (String) -> Unit,
    onRefresh: () -> Unit,
    onDismiss: () -> Unit,
    initialQuery: String = "",
) {
    val list = state.modelList
    if (list == null || list.locked != null) {
        LaunchedEffect(Unit) { onDismiss() }
        return
    }
    // A Paseo provider has no terminal and its own name: its note names only it, by its label (#8261), never the engine (#8143).
    val agent = list.agent.ifBlank { state.agentType }
    JModelSheet(
        note = if (agent == PaseoAgent) stringResource(R.string.agents_model_sheet_note_provider, state.modelLauncherTitle)
            else stringResource(R.string.agents_model_sheet_note, state.modelLauncherTitle, agent.agentLabel()),
        owner = state.launchers.firstOrNull { it.name == state.modelLauncher }?.title ?: state.modelLauncher,
        models = list.models, selected = state.model, defaultDetail = defaultModelDetail(agentResources(), state),
        loading = state.models.loading, error = state.models.error?.text(),
        onPick = onPick, onRefresh = onRefresh, onDismiss = onDismiss, initialQuery = initialQuery,
    )
}

/** A model's second line (the design system's [detailLine]). */
internal fun modelDetail(model: AgentModel): String? = model.detailLine()

/** What runs with no pick: "Model configuration"'s model, else the one the agent defaults to. */
internal fun defaultModelDetail(res: android.content.res.Resources, state: NewAgentState): String? {
    if (state.configuredModel.isNotEmpty()) return res.getString(R.string.agents_model_configured, state.configuredModel)
    val default = state.modelList?.models?.firstOrNull { it.isDefault } ?: return null
    return default.resolved?.takeIf { it.isNotBlank() } ?: default.label
}

/** The thinking efforts offered: "Follow launcher", then the levels the model that will run takes (all when not known). */
internal fun effortChoices(efforts: List<String>?): List<Pair<String, Int>> =
    effortLevels.filter { (id, _) -> id.isEmpty() || efforts == null || id in efforts }

/** For captures (debug builds): steps the "New session" page takes once loaded, see `NewAgentPreview.steps`. */
object NewAgentPreview {
    /**
     * Comma separated: `launcher:<name>` picks a launcher, `effort:<level>` an effort, `model:<id>` a model, and
     * `models` or `models:<query>` opens the model list (searching for the query).
     */
    @Volatile var steps: String? = null
}
