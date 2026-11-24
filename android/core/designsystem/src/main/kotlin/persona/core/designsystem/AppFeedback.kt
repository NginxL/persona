package persona.core.designsystem

import androidx.compose.runtime.staticCompositionLocalOf

/** A button on a notice ("Undo"): its label, kept to a word or two, and what it does. */
class FeedbackAction(val label: String, val run: () -> Unit)

/**
 * A notice as the window's toast shows it ([JToastHost]): the words, its [style], up to two buttons, and the [Haptic] it
 * plays when shown (the style's when null).
 */
class FeedbackNotice(
    val message: String, val action: FeedbackAction? = null, val haptic: Haptic? = null,
    val style: FeedbackStyle = FeedbackStyle.Info, val secondary: FeedbackAction? = null,
)

/** Return true when the shell owns presentation, including deliberately silent refresh failures. */
interface AppFeedback {
    fun failure(error: Throwable, operation: Boolean = true): Boolean
    fun notice(message: String): Boolean
    /** A notice in a [FeedbackStyle] ("Copied" as a success); the plain [notice] is info. */
    fun notice(message: String, style: FeedbackStyle): Boolean = notice(message)
    /** A notice with a button ("Undo" after a bulk action), shown for a few seconds; false when the shell cannot show one. */
    fun notice(message: String, action: FeedbackAction): Boolean = false
    /** A styled notice with a button (e.g. Error with Retry); falls back to [notice] with [action]. */
    fun notice(message: String, action: FeedbackAction, style: FeedbackStyle): Boolean = notice(message, action)
}
val LocalAppFeedback = staticCompositionLocalOf<AppFeedback?> { null }
