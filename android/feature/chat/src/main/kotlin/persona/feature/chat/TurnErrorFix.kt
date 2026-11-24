package persona.feature.chat

import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.res.stringResource
import persona.core.designsystem.ErrorCard
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.JTextButton
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.ReadinessActionId
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons

/**
 * A turn of Persona's conversation that ended on a model error the user has to fix, as the hub names it (the session's
 * `turn_error_code`, docs/assistant/onboarding.md 4.4): the card's words and where Fix It goes (iOS `TurnErrorFix`).
 */
internal enum class TurnErrorFix(val code: String, @StringRes val title: Int, @StringRes val detail: Int, val action: ReadinessActionId?) {
    NotLoggedIn("not_logged_in", R.string.chat_turn_fix_not_logged_in, R.string.chat_turn_fix_not_logged_in_detail, ReadinessActionId.ConnectModel),
    AuthFailed("auth_failed", R.string.chat_turn_fix_auth_failed, R.string.chat_turn_fix_auth_failed_detail, ReadinessActionId.EditProvider),
    ModelRoute("model_route", R.string.chat_turn_fix_model_route, R.string.chat_turn_fix_model_route_detail, null),
    ToolSearchUnsupported("tool_search_unsupported", R.string.chat_turn_fix_tool_search_unsupported,
        R.string.chat_turn_fix_tool_search_unsupported_detail, null);

    companion object {
        fun of(code: String?): TurnErrorFix? = entries.firstOrNull { it.code == code }
    }

    /** The same title with the affected launcher's ID, when this remedy names one. */
    @get:StringRes val launcherTitle: Int? get() = when (this) {
        NotLoggedIn -> R.string.chat_turn_fix_not_logged_in_launcher
        AuthFailed -> R.string.chat_turn_fix_auth_failed_launcher
        ModelRoute -> R.string.chat_turn_fix_model_route_launcher
        ToolSearchUnsupported -> null
    }
}

/** The chat's own model picker and, when it keeps a pick, the way back to its default. */
internal data class TurnErrorModelActions(
    val openPicker: () -> Unit,
    val clearPick: (() -> Unit)? = null,
    @StringRes val clearLabel: Int = R.string.chat_modelpill_follow_default,
)

/** Where a turn error's Fix It goes for account and provider failures; null where the app cannot open it. */
val LocalTurnErrorFixAction = staticCompositionLocalOf<((ReadinessActionId) -> Unit)?> { null }

/** The model remedy stays in this chat instead of opening the general model-purpose page. */
internal val LocalTurnErrorModelActions = staticCompositionLocalOf<TurnErrorModelActions?> { null }

/**
 * The card in the place of the turn's error line (an answer, a notice or an API error), as iOS's "Model Not Connected":
 * the error card's frame with what is wrong, what to do and Fix It; [raw], the agent's own words, folded under it.
 */
@Composable
internal fun TurnErrorFixCard(fix: TurnErrorFix, raw: String?, launcher: String? = null, onRetry: (() -> Unit)? = null) {
    val onFix = LocalTurnErrorFixAction.current
    val modelActions = LocalTurnErrorModelActions.current
    val palette = PersonaTheme.palette
    var open by rememberSaveable(fix) { mutableStateOf(false) }
    val launcherTitle = fix.launcherTitle
    val title = if (launcher.isNullOrBlank() || launcherTitle == null) stringResource(fix.title) else stringResource(launcherTitle, launcher)
    ErrorCard(title, emptyList(), open, { open = !open }, action = {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Text(stringResource(fix.detail), style = PersonaTheme.type.callout, color = palette.ink2)
            when (fix) {
                TurnErrorFix.ToolSearchUnsupported -> onRetry?.let {
                    JButton(stringResource(R.string.chat_api_error_retry), it, kind = JButtonKind.Secondary,
                        size = JButtonSize.Medium, icon = PersonaIcons.retry)
                }
                TurnErrorFix.ModelRoute -> modelActions?.let { actions ->
                    JButton(stringResource(R.string.chat_turn_fix_action), actions.openPicker, size = JButtonSize.Medium)
                    actions.clearPick?.let { clear -> JTextButton(stringResource(actions.clearLabel), clear) }
                }
                else -> onFix?.let { callback -> fix.action?.let { action ->
                    JButton(stringResource(R.string.chat_turn_fix_action), { callback(action) }, size = JButtonSize.Medium)
                } }
            }
        }
    }) {
        raw?.takeIf(String::isNotBlank)?.let {
            SelectionContainer {
                Text(it, style = PersonaTheme.type.code, color = palette.ink,
                    modifier = Modifier.fillMaxWidth().clip(PersonaTheme.shapes.controlShape).background(palette.sunken).padding(Tokens.Space.m))
            }
        }
    }
}
