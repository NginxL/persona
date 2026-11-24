package persona.core.designsystem

import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.unit.Density

/** The uncapped density chosen by the system, retained through nested themes for reading content. */
internal val LocalReadingDensity = compositionLocalOf<Density?> { null }

/** Densities captured before a window boundary; a dialog's Android view can otherwise replace them with its configuration. */
data class TextDensities(val interfaceDensity: Density, val readingDensity: Density?)

/** Capture the caller's scales before a raw `Dialog` / `Popup`; shared Persona overlays already do this themselves. */
@Composable
fun currentTextDensities() = TextDensities(LocalDensity.current, LocalReadingDensity.current)

/** Restore densities captured by [currentTextDensities] inside the raw window's content. */
@Composable
fun RetainedTextDensity(densities: TextDensities, content: @Composable () -> Unit) {
    val chosenScale = densities.readingDensity?.fontScale ?: densities.interfaceDensity.fontScale
    if (chosenScale <= InterfaceFontScaleCap) {
        // A legacy window used its own density here. Preserve those pixels and do not restore a retained reading scale either.
        CompositionLocalProvider(LocalReadingDensity provides null, content = content)
    } else {
        CompositionLocalProvider(
            LocalDensity provides densities.interfaceDensity,
            LocalReadingDensity provides densities.readingDensity,
            content = content,
        )
    }
}

/** Preserve the original density object whenever its text is already at or below the interface cap. */
internal fun Density.atInterfaceCap(): Density =
    if (fontScale > InterfaceFontScaleCap) Density(density, InterfaceFontScaleCap) else this

/**
 * Reading words keep the system's chosen text size after the interface stops at [InterfaceFontScaleCap]. Apply to a
 * message, document or editor's words, never a whole screen. Embedded controls use [InterfaceContent] to stay capped.
 */
@Composable
fun ReadingContent(content: @Composable () -> Unit) {
    val density = LocalReadingDensity.current ?: LocalDensity.current
    CompositionLocalProvider(LocalDensity provides density, content = content)
}

/** Interface controls embedded in [ReadingContent] stay at [InterfaceFontScaleCap]. Other callers need no wrapper. */
@Composable
fun InterfaceContent(content: @Composable () -> Unit) {
    CompositionLocalProvider(LocalDensity provides LocalDensity.current.atInterfaceCap(), content = content)
}
