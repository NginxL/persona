package persona.core.designsystem

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.core.MutableTransitionState
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.focus.FocusDirection
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.focusRestorer
import androidx.compose.ui.focus.focusTarget
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties

/** Focus state shared with the outer sheet's explicit blank-tap clear signal. */
internal class SettingsFocusRestoration {
    var restoreChildFocus by mutableStateOf(false)
    var hasFocus by mutableStateOf(false)
    fun clearFocus() { restoreChildFocus = false }
    fun shouldRestoreFocus(): Boolean = restoreChildFocus && !hasFocus
}

/** A centered page above the shell, with the same opaque surface and dismiss behavior as a settings sheet. */
@Composable
fun JPageSizedSettingsSheet(onDismiss: () -> Unit, content: @Composable () -> Unit) {
    val restoration = remember { SettingsFocusRestoration() }
    val densities = currentTextDensities()
    Dialog(onDismissRequest = onDismiss, properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false)) {
        RetainedTextDensity(densities) {
            HardwareShortcutHost(Modifier.fillMaxSize(), requestFocusWhenLarge = false) {
                Box(Modifier.fillMaxSize().hardwareShortcutDismiss(onDismiss)
                    .dismissKeyboardOnBlankTap { restoration.clearFocus() }) {
                    JSettingsPresentationFrame(SettingsPresentation.PageSized, onDismiss, restoration, content)
                }
            }
        }
    }
}

/**
 * Present Settings without changing the content's composition parent. The production settings sheet keeps one JSheet
 * instance and changes only this frame's geometry, so non-saveable state and running effects survive window changes.
 */
@Composable
internal fun JSettingsPresentationFrame(
    presentation: SettingsPresentation,
    onDismiss: () -> Unit,
    restoration: SettingsFocusRestoration,
    content: @Composable () -> Unit,
) {
    val palette = PersonaTheme.palette
    val container = if (PersonaTheme.style == AppStyle.Brief) palette.background else palette.surfaceOpaque
    val close = stringResource(R.string.designsystem_close)
    val pageSized = presentation == SettingsPresentation.PageSized
    val duration = if (PersonaTheme.reduceMotion) Tokens.Motion.reduced else PersonaTheme.motion.standard
    val focus = remember { FocusRequester() }
    val focusManager = LocalFocusManager.current
    LaunchedEffect(pageSized) {
        if (restoration.shouldRestoreFocus()) {
            val _ = runCatching { focus.requestFocus() }
            focusManager.moveFocus(FocusDirection.Enter)
        }
    }
    BoxWithConstraints(
        Modifier.fillMaxSize().then(if (pageSized) Modifier.safeDrawingPadding() else Modifier),
        contentAlignment = Alignment.Center,
    ) {
        val bounds = if (pageSized) settingsPageBounds(maxWidth, maxHeight) else null
        Box(
            Modifier.fillMaxSize().then(
                if (pageSized) Modifier.background(palette.scrim)
                    .clickable(role = Role.Button, onClick = onDismiss).semantics { contentDescription = close }
                else Modifier,
            ),
        )
        val presented = androidx.compose.runtime.remember {
            MutableTransitionState(!pageSized).apply { targetState = true }
        }
        AnimatedVisibility(visibleState = presented, enter = fadeIn(tween(duration)), exit = fadeOut(tween(duration))) {
            Box(
                (if (bounds != null) Modifier.size(bounds.width, bounds.height) else Modifier.fillMaxSize())
                    .then(
                        if (pageSized) Modifier.testTag("settings-page-card")
                            .clip(PersonaTheme.shapes.panelShape)
                            .background(container)
                        else Modifier,
                    )
                    .onFocusChanged {
                        restoration.hasFocus = it.hasFocus
                        if (it.hasFocus && !it.isFocused) restoration.restoreChildFocus = true
                    }
                    .focusRestorer()
                    .focusRequester(focus)
                    .focusTarget(),
            ) {
                CompositionLocalProvider(LocalReadingInset provides Tokens.Space.flush, LocalGlassSource provides null) {
                    content()
                }
            }
        }
    }
}
