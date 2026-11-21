package persona.feature.files

import android.content.res.Resources
import persona.core.designsystem.LocalizedFormats
import java.math.RoundingMode
import java.text.NumberFormat
import java.time.Duration
import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneId
import java.time.temporal.ChronoUnit
import java.util.Locale

// Pure helpers of the file browser and the previews: paths on the session's machine, sizes and times.

/** [parent] + [child] without doubling or losing a slash ("/" + "a" = "/a", "~" + "a" = "~/a"). */
fun joinRemotePath(parent: String, child: String): String {
    val name = child.trim('/')
    if (name.isEmpty()) return parent
    return if (parent.endsWith("/")) parent + name else "$parent/$name"
}

internal object FilesPaths {
    /** "~" and "~/…" are the machine's home: only the hub knows where that is, so they travel as they are. */
    fun isHome(path: String) = path == "~" || path.startsWith("~/")

    private val windowsRoot = Regex("^[A-Za-z]:[\\\\/]|^\\\\\\\\")

    /**
     * Where a path written in a conversation points: absolute, "~" and Windows drive paths as they are, anything else in [cwd].
     * "." and ".." are folded in absolute paths, a trailing slash dropped (except the root's).
     */
    fun resolve(path: String, cwd: String): String {
        val trimmed = path.trim()
        if (windowsRoot.containsMatchIn(trimmed)) return trimmed
        val joined = when {
            trimmed.isEmpty() -> cwd
            trimmed.startsWith("/") || isHome(trimmed) -> trimmed
            else -> joinRemotePath(cwd, trimmed)
        }
        return normalize(joined)
    }

    /** "/a/./b/../c/" is "/a/c"; "~/x/.." is "~". Relative leftovers that climb out of the top stay at the top. */
    fun normalize(path: String): String {
        val home = isHome(path)
        val absolute = path.startsWith("/")
        if (!home && !absolute) return path.trimEnd('/').ifEmpty { path }
        val parts = ArrayDeque<String>()
        for (part in (if (home) path.removePrefix("~") else path).split('/')) {
            when (part) {
                "", "." -> Unit
                ".." -> parts.removeLastOrNull()
                else -> parts.addLast(part)
            }
        }
        val body = parts.joinToString("/")
        return if (home) (if (body.isEmpty()) "~" else "~/$body") else "/$body"
    }

    /** Whoever opened the path said it is a folder (a trailing slash, the home itself). */
    fun namesDirectory(path: String): Boolean = path.trim().let { it.endsWith("/") || it == "~" }

    /** The folder above [path]; null at the top ("/", "~"). */
    fun parent(path: String): String? {
        val p = path.trimEnd('/')
        if (p.isEmpty() || p == "~") return null
        val i = p.lastIndexOf('/')
        return when {
            i < 0 -> null
            i == 0 -> "/"
            else -> p.substring(0, i)
        }
    }

    /** The last component: "/" for the root, "~" for the home. */
    fun name(path: String): String = path.trimEnd('/').substringAfterLast('/').ifEmpty { "/" }

    /** Whether [path] is [folder] or inside it. */
    fun isWithin(path: String, folder: String): Boolean {
        val f = folder.trimEnd('/')
        val p = path.trimEnd('/')
        if (f.isEmpty()) return p.startsWith("/") || p.isEmpty()
        return p == f || p.startsWith("$f/")
    }

    /** The levels from [root] down to [here] as (name, path), for the breadcrumb; just [here] when it is not under [root]. */
    fun crumbs(root: String, here: String): List<Pair<String, String>> {
        val r = root.trimEnd('/').ifEmpty { "/" }
        val h = here.trimEnd('/').ifEmpty { "/" }
        if (h == r) return listOf(name(r) to r)
        if (!isWithin(h, r)) return listOf(name(h) to h)
        val out = mutableListOf(name(r) to r)
        var at = r
        for (part in h.removePrefix(if (r == "/") "/" else "$r/").split('/').filter { it.isNotEmpty() }) {
            at = joinRemotePath(at, part)
            out += part to at
        }
        return out
    }

    fun extension(name: String): String = name.substringAfterLast('.', "").lowercase(Locale.ROOT)

    fun isMarkdown(name: String) = extension(name) in setOf("md", "markdown", "mdx")

    /** [s] cut from the start to at most [max] characters ("…/b/c"): a path keeps its informative end. */
    fun keepEnd(s: String, max: Int = 48): String = if (s.length <= max) s else "…" + s.takeLast(max - 1)
}

/** 1023 B, 1.5 KB, 12 MB (binary units, one decimal under 10), the number in [locale]'s digits and decimal mark. */
internal fun fileSize(size: Long, locale: Locale): String {
    if (size < 1024) return "${LocalizedFormats.number(size, locale)} B"
    val units = listOf("KB", "MB", "GB", "TB")
    var value = size / 1024.0
    var unit = 0
    while (value >= 1024 && unit < units.lastIndex) { value /= 1024; unit++ }
    val number = NumberFormat.getInstance(locale).apply {
        maximumFractionDigits = if (value < 10) 1 else 0
        // Under 10 rounded to one decimal, above that the whole units only.
        roundingMode = if (value < 10) RoundingMode.HALF_UP else RoundingMode.DOWN
    }
    return "${number.format(value)} ${units[unit]}"
}

/**
 * A time a node sent: RFC 3339 with the machine's own offset ("2026-10-31T13:58:40.205893083+08:00"). Android 12
 * and 13's `Instant.parse` only takes "Z", so it goes through OffsetDateTime. Go's zero time ("0001-01-01…", a file
 * or commit without one) and anything unreadable are null.
 */
internal fun fileTime(value: String?): Instant? {
    if (value.isNullOrBlank() || value.startsWith("0001-")) return null
    return runCatching { OffsetDateTime.parse(value.trim()).toInstant() }.getOrNull()
}

/**
 * "Just now" / "5 min ago" / "3 hr ago" / "2 days ago", then the date (the year only when it is not this one), in [resources]' language.
 * Days count the midnights in [zone], not 24-hour spans, and from seven it is the date, as the web's `ago` (#9889).
 */
internal fun relativeTime(resources: Resources, time: Instant, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String {
    val seconds = Duration.between(time, now).seconds
    // At least 1: a 25-hour day can leave a whole day's gap on one date.
    val days = ChronoUnit.DAYS.between(time.atZone(zone).toLocalDate(), now.atZone(zone).toLocalDate()).coerceAtLeast(1).toInt()
    return when {
        seconds < 60 -> resources.getString(R.string.files_time_just_now)
        seconds < 3600 -> (seconds / 60).toInt().let { resources.getQuantityString(R.plurals.files_time_minutes_ago, it, it) }
        seconds < 86_400 -> (seconds / 3600).toInt().let { resources.getQuantityString(R.plurals.files_time_hours_ago, it, it) }
        days < 7 -> resources.getQuantityString(R.plurals.files_time_days_ago, days, days)
        else -> {
            val thisYear = time.atZone(zone).year == now.atZone(zone).year
            LocalizedFormats.format(time, if (thisYear) LocalizedFormats.MONTH_DAY else LocalizedFormats.DATE, resources.configuration.locales[0], zone)
        }
    }
}

/** A modification or commit time for a list row; empty when the node sent none (or one this cannot read). */
internal fun fileDate(resources: Resources, value: String?, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String? =
    fileTime(value)?.let { relativeTime(resources, it, now, zone) }

/** The whole date of a commit: "Oct 31, 2026, 1:58 PM" in English, the same in each language's own form. */
internal fun fullDate(value: String?, locale: Locale, zone: ZoneId = ZoneId.systemDefault()): String? =
    fileTime(value)?.let { LocalizedFormats.format(it, LocalizedFormats.DATE_TIME, locale, zone) }
