package persona.feature.assistant

import android.content.res.Resources
import androidx.annotation.StringRes
import persona.core.contracts.SessionCompaction
import persona.core.designsystem.LocalizedFormats
import java.util.Locale

/**
 * Hub features behind "Compact context" and the main conversation's "New main chat" (docs/assistant/assistant.md, #1727)
 * and "Clear context" (#5095, the apps' side #5100).
 */
internal const val CompactFeature = "assistant_compact"
internal const val NewMainFeature = "assistant_main_new"
internal const val ClearMainFeature = "assistant_main_clear"

/** The main conversation's menu: which of its actions the hub offers. */
internal data class MainActions(val compact: Boolean, val newMain: Boolean, val clear: Boolean = false) {
    companion object {
        fun of(features: Set<String>) = MainActions(CompactFeature in features, NewMainFeature in features, ClearMainFeature in features)
    }
}

/** What the line over the selected Assistant conversation says about a compaction. */
internal sealed interface CompactionLine {
    data object Running : CompactionLine
    data object Done : CompactionLine
    data class Failed(val error: String) : CompactionLine
}

/**
 * The compaction line of the conversation on screen, from the session's `compaction` as updates come in. A compaction
 * is known by when it started. Its end is announced ("Compacted", "Compaction failed") only when this screen saw it run or asked for it
 * here: one that ended before the screen opened is old news, however often the screen opens again.
 */
internal data class CompactionStatus(
    /** Compactions seen running here, whose end is still to announce. */
    private val watched: Set<String> = emptySet(),
    /** Asked for here and not ended yet: the compaction that was there when asking ("" for none) is not it. */
    private val askedOver: String? = null,
    val line: CompactionLine? = null,
) {
    fun observe(compaction: SessionCompaction?): CompactionStatus {
        if (compaction == null) return if (line == CompactionLine.Running && askedOver == null) copy(line = null) else this
        val key = compaction.startedAt.orEmpty()
        return when {
            compaction.running -> copy(watched = watched + key, askedOver = null, line = CompactionLine.Running)
            key in watched || (askedOver != null && key != askedOver) -> copy(watched = watched - key, askedOver = null,
                line = if (compaction.failed) CompactionLine.Failed(compaction.error.orEmpty()) else CompactionLine.Done)
            askedOver != null -> this // the one asked for has not shown up yet
            // An end already announced or from before: nothing new; a line still saying "Compacting" has nothing behind it.
            line == CompactionLine.Running -> copy(line = null)
            else -> this
        }
    }

    /** The user asked for a compaction; [current] is the session's compaction at that moment. */
    fun asked(current: SessionCompaction?): CompactionStatus =
        copy(askedOver = current?.startedAt.orEmpty(), line = CompactionLine.Running)

    /** Asking failed: back to what the session says. */
    fun refused(current: SessionCompaction?): CompactionStatus =
        copy(askedOver = null, line = null).observe(current)

    /** "Compacted" goes by itself, "Compaction failed" when the user closes it; "Compacting" stays while it runs. */
    fun dismissed(): CompactionStatus = if (line == CompactionLine.Running) this else copy(line = null)
}

/** How long "Compacted" stays over the conversation. */
internal const val CompactionDoneMillis = 3_000L

/** "New main chat"'s confirmation text: the archived name carries [today] as a month and day in [locale]. */
internal fun newMainText(resources: Resources, today: java.time.LocalDate, locale: Locale): String =
    resources.getString(R.string.assistant_new_main_text,
        LocalizedFormats.formatter(LocalizedFormats.MONTH_DAY, locale).format(today))

/** What to tell the user after "New main chat" went through. */
@StringRes
internal fun newMainNotice(carried: Boolean, asked: Boolean): Int = when {
    !asked -> R.string.assistant_new_main_done
    carried -> R.string.assistant_new_main_done_carried
    else -> R.string.assistant_new_main_done_empty
}
