package persona.feature.settings

import android.content.res.Resources
import androidx.annotation.PluralsRes
import androidx.annotation.StringRes
import androidx.compose.runtime.Composable
import androidx.compose.runtime.ReadOnlyComposable
import androidx.compose.ui.platform.LocalResources
import java.time.Duration
import java.time.Instant
import java.time.ZoneId
import java.time.temporal.ChronoUnit
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.UiText

/**
 * Words a model hands its page (a failure, a notice): a string resource and its arguments, put into words in the
 * language the page is drawn in, so models stay free of `Resources` (docs/protocol/i18n.md). [count] picks the form of a
 * `<plurals>` [id]. An argument that is itself a [ResText] or a [UiText] is put into words first; a list of them is one per line.
 */
internal data class ResText(val id: Int, val args: List<Any> = emptyList(), val count: Int? = null) {
    fun resolve(res: Resources): String {
        val words = args.map { arg -> words(res, arg) }.toTypedArray()
        return if (count != null) res.getQuantityString(id, count, *words) else res.getString(id, *words)
    }

    companion object {
        fun of(@StringRes id: Int, vararg args: Any) = ResText(id, args.toList())
        fun plural(@PluralsRes id: Int, count: Int, vararg args: Any) = ResText(id, args.toList(), count)

        private fun words(res: Resources, arg: Any): Any = when (arg) {
            is ResText -> arg.resolve(res)
            is UiText -> arg.resolve(res)
            is List<*> -> arg.joinToString("\n") { it?.let { a -> words(res, a).toString() }.orEmpty() }
            else -> arg
        }
    }
}

/** [ResText] in the page's language. */
@Composable
@ReadOnlyComposable
internal fun ResText.text(): String = resolve(LocalResources.current)

/**
 * "Just now", "3 minutes ago", "5 hours ago", then the day (Nov 2) or, with [days], "2 days ago" - how settings say when
 * something last happened. Days count the midnights in [zone], not 24-hour spans, as [SentText.ago] and the web's `ago` (#9889).
 * [showSeconds] keeps a sub-minute age as a plural number of seconds instead of "Just now".
 */
internal fun agoText(
    res: Resources,
    at: Instant,
    now: Instant = Instant.now(),
    zone: ZoneId = ZoneId.systemDefault(),
    days: Boolean = false,
    showSeconds: Boolean = false,
): String {
    val gap = Duration.between(at, now)
    return when {
        gap.isNegative || gap < Duration.ofSeconds(1) -> res.getString(R.string.settings_src_just_now)
        gap < Duration.ofMinutes(1) && showSeconds -> gap.seconds.toInt()
            .let { res.getQuantityString(R.plurals.settings_src_seconds_ago, it, it) }
        gap < Duration.ofMinutes(1) -> res.getString(R.string.settings_src_just_now)
        gap < Duration.ofHours(1) -> gap.toMinutes().toInt().let { res.getQuantityString(R.plurals.settings_src_minutes_ago, it, it) }
        gap < Duration.ofDays(1) -> gap.toHours().toInt().let { res.getQuantityString(R.plurals.settings_src_hours_ago, it, it) }
        // At least 1: a 25-hour day can leave a whole day's gap on one date.
        days -> ChronoUnit.DAYS.between(at.atZone(zone).toLocalDate(), now.atZone(zone).toLocalDate()).coerceAtLeast(1).toInt()
            .let { res.getQuantityString(R.plurals.settings_src_days_ago, it, it) }
        else -> LocalizedFormats.format(at, LocalizedFormats.MONTH_DAY, res.configuration.locales[0], zone)
    }
}
