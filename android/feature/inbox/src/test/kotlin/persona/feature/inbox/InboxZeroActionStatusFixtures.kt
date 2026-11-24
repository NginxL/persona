package persona.feature.inbox

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.key
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.junit4.ComposeContentTestRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import java.io.File
import java.time.Instant
import java.time.ZoneOffset
import java.util.Locale
import persona.core.contracts.InboxEventFields
import persona.core.contracts.InboxFollow
import persona.core.contracts.InboxSuggestion
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.Navigator
import persona.core.inbox.*

internal enum class ZeroActionContentLength(val id: String) { Typical("typical"), Maximum("maximum") }

internal enum class ZeroActionFixtureKind(val id: String) {
    DirectWaiting("direct-waiting"),
    DirectCheckDue("direct-check-due"),
    DirectFailedStress("direct-failed-stress"),
    DirectDoneStress("direct-done-stress"),
    DetailCheckDue("detail-check-due"),
    DetailDoneFallback("detail-done-fallback"),
    DetailSuggestionFallback("detail-suggestion-fallback"),
}

internal data class ZeroActionCase(
    val language: String,
    val kind: ZeroActionFixtureKind,
    val length: ZeroActionContentLength,
    val widthDp: Int,
    val fontScale: Float,
) {
    val id: String = "${kind.id}-$language-${length.id}-${widthDp}dp-${fontScale}x"
    val direct: Boolean get() = kind.name.startsWith("Direct")
    val locale: Locale = when (language) {
        "en" -> Locale.ENGLISH
        "zh-Hans" -> Locale.SIMPLIFIED_CHINESE
        "ja" -> Locale.JAPANESE
        else -> error("Unsupported test language $language")
    }
    val clock: Instant = when (length) {
        ZeroActionContentLength.Typical -> Instant.parse("2026-01-01T12:00:00Z")
        ZeroActionContentLength.Maximum -> Instant.parse("2026-12-30T12:00:00Z")
    }
    val toneKey: String = when (kind) {
        ZeroActionFixtureKind.DirectWaiting -> "waiting"
        ZeroActionFixtureKind.DirectCheckDue, ZeroActionFixtureKind.DetailCheckDue -> "check_due"
        ZeroActionFixtureKind.DirectFailedStress -> "failed"
        ZeroActionFixtureKind.DirectDoneStress -> "done_user"
        ZeroActionFixtureKind.DetailDoneFallback, ZeroActionFixtureKind.DetailSuggestionFallback -> "future_unknown"
    }
    val compositionKey: String = when (kind) {
        ZeroActionFixtureKind.DirectCheckDue, ZeroActionFixtureKind.DetailCheckDue -> "check_due"
        ZeroActionFixtureKind.DetailDoneFallback, ZeroActionFixtureKind.DetailSuggestionFallback -> "future_unknown"
        else -> "waiting"
    }
}

private val directTones = listOf(
    ZeroActionFixtureKind.DirectWaiting,
    ZeroActionFixtureKind.DirectCheckDue,
    ZeroActionFixtureKind.DirectFailedStress,
    ZeroActionFixtureKind.DirectDoneStress,
)

internal fun zeroActionDirectCases(language: String): List<ZeroActionCase> = buildList {
    for (length in ZeroActionContentLength.entries) for (width in listOf(360, 320)) for (scale in listOf(1f, 2f)) {
        for (kind in directTones) add(ZeroActionCase(language, kind, length, width, scale))
    }
}

internal fun zeroActionDetailCases(language: String): List<ZeroActionCase> = listOf(
    ZeroActionCase(language, ZeroActionFixtureKind.DetailCheckDue, ZeroActionContentLength.Typical, 360, 1f),
    ZeroActionCase(language, ZeroActionFixtureKind.DetailCheckDue, ZeroActionContentLength.Maximum, 320, 2f),
    ZeroActionCase(language, ZeroActionFixtureKind.DetailDoneFallback, ZeroActionContentLength.Typical, 320, 2f),
    ZeroActionCase(language, ZeroActionFixtureKind.DetailSuggestionFallback, ZeroActionContentLength.Typical, 320, 2f),
)

private fun maximumField(character: String): String {
    val value = character.repeat(80) + "…"
    check(value.length == 81) // All fixtures are BMP: one UTF-16 code unit is one Go rune here.
    return value
}

private fun fields(case: ZeroActionCase): Pair<String, String> {
    if (case.length == ZeroActionContentLength.Maximum) {
        return when (case.language) {
            "en" -> maximumField("W") to maximumField("N")
            "zh-Hans" -> maximumField("采") to maximumField("办") // i18n-ignore: synthetic hub boundary data
            "ja" -> maximumField("担") to maximumField("続") // i18n-ignore: synthetic hub boundary data
            else -> error("Unsupported test language ${case.language}")
        }
    }
    return when (case.language) {
        "en" ->
            "the procurement team to confirm the revised delivery date and pricing" to
                "ask whether Friday works for everyone before confirming the revised quote"
        "zh-Hans" ->
            "采购团队确认修改后的交付日期、最终报价、付款条件和各项交付责任" to // i18n-ignore: synthetic hub data
                "等对方回复后，确认周五的新安排是否适合所有参与人，核对修订后的报价与付款条件，再把最终结论和后续安排整理给大家" // i18n-ignore
        "ja" ->
            "調達チームによる変更後の納品日、最終見積もり、支払条件、納品責任の確認" to // i18n-ignore: synthetic hub data
                "返信を受けてから、金曜日の新しい日程が関係者全員に適するか確認し、修正版の見積もりと支払条件を供給業者に確約してもらう" // i18n-ignore
        else -> error("Unsupported test language ${case.language}")
    }
}

internal fun zeroActionEvent(case: ZeroActionCase): InboxEventFields {
    val (who, next) = fields(case)
    val checkAt = when {
        case.compositionKey != "waiting" -> null
        case.length == ZeroActionContentLength.Typical -> "2026-01-05T09:00:00Z"
        else -> "2027-01-12T09:00:00Z"
    }
    val suggestion = when (case.kind) {
        ZeroActionFixtureKind.DetailCheckDue -> InboxSuggestion(version = 1, kind = "conclusion", text = "Synthetic conclusion")
        ZeroActionFixtureKind.DetailSuggestionFallback -> InboxSuggestion(version = 1, kind = "reply", text = "Synthetic reply")
        else -> null
    }
    return InboxEventFields(
        status = when (case.kind) {
            ZeroActionFixtureKind.DetailCheckDue -> "fyi"
            ZeroActionFixtureKind.DetailSuggestionFallback -> "action"
            else -> null
        },
        suggestion = suggestion,
        doneBy = if (case.kind == ZeroActionFixtureKind.DetailDoneFallback) "manager" else null,
        follow = InboxFollow(who = who, next = next, checkAt = checkAt),
        now = case.compositionKey,
    )
}

internal fun zeroActionRecord(case: ZeroActionCase): InboxRecord {
    val done = case.kind == ZeroActionFixtureKind.DetailDoneFallback
    val status = when (case.kind) {
        ZeroActionFixtureKind.DetailCheckDue -> "fyi"
        ZeroActionFixtureKind.DetailSuggestionFallback -> "action"
        else -> null
    }
    return InboxRecord(
        InboxItem(
            id = case.id,
            kind = "chat",
            title = "Inbox status boundary",
            preview = "Synthetic item",
            tier = status,
            updatedAt = "2026-01-01T12:00:00Z",
            doneAt = if (done) "2026-01-01T12:00:00Z" else null,
            status = status,
        ),
        source = "assistant",
        event = zeroActionEvent(case),
    )
}

@Composable
internal fun ZeroActionStatusFixture(
    case: ZeroActionCase,
    repository: FakeInboxRepository,
    navigator: Navigator,
    style: AppStyle,
    dark: Boolean,
    recreation: Int,
    onText: (String) -> Unit,
) {
    CompositionLocalProvider(
        LocalDensity provides Density(LocalDensity.current.density, case.fontScale),
        LocalInboxClock provides { case.clock },
        LocalInboxZone provides ZoneOffset.UTC,
    ) {
        PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
            key(recreation) {
                val resources = LocalResources.current
                val text = when (case.kind) {
                    ZeroActionFixtureKind.DetailDoneFallback -> resources.getString(R.string.inbox_done_by_manager)
                    ZeroActionFixtureKind.DetailSuggestionFallback -> resources.getString(R.string.inbox_bay_needs_you)
                    else -> inboxNowText(zeroActionEvent(case), resources, case.locale, case.clock, ZoneOffset.UTC)!!
                }
                onText(text)
                Box(
                    Modifier.testTag("zero-action-status-shot").width(case.widthDp.dp)
                        .then(if (case.direct) Modifier else Modifier.height(1800.dp))
                        .background(PersonaTheme.palette.background),
                ) {
                    if (case.direct) {
                        EventStateLine(EventMark.Now(text, inboxNowStatus(case.toneKey), key = case.toneKey), action = null)
                    } else {
                        InboxEventScreen(repository, InboxEventRoute(case.id), navigator)
                    }
                }
            }
        }
    }
}

internal data class ZeroActionMeasurement(
    val caseId: String,
    val textRunes: Int,
    val lineCount: Int,
    val visualOverflow: Boolean,
    val ellipsizedLines: List<Int>,
    val lastGlyphInside: Boolean,
) {
    val complete: Boolean = lineCount > 0 && !visualOverflow && ellipsizedLines.isEmpty() && lastGlyphInside
    val tsv: String = listOf(
        caseId, textRunes.toString(), lineCount.toString(), visualOverflow.toString(),
        ellipsizedLines.joinToString(",", "[", "]"), lastGlyphInside.toString(), complete.toString(),
    ).joinToString("\t")
    val failure: String? = if (complete) null else
        "$caseId: runes=$textRunes lines=$lineCount overflow=$visualOverflow ellipsized=$ellipsizedLines lastGlyphInside=$lastGlyphInside"
}

internal fun measureZeroActionText(rule: ComposeContentTestRule, case: ZeroActionCase, text: String): ZeroActionMeasurement {
    val node = rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode()
    val layouts = mutableListOf<TextLayoutResult>()
    val action = node.config[SemanticsActions.GetTextLayoutResult].action
        ?: error("${case.id}: GetTextLayoutResult has no action")
    check(action.invoke(layouts)) { "${case.id}: GetTextLayoutResult failed" }
    val layout = layouts.single()
    val ellipsized = (0 until layout.lineCount).filter(layout::isLineEllipsized)
    val lastGlyph = layout.getBoundingBox(text.lastIndex)
    val lastGlyphInside = lastGlyph.width > 0f && lastGlyph.left >= 0f && lastGlyph.right <= layout.size.width + 1f &&
        lastGlyph.top >= 0f && lastGlyph.bottom <= layout.size.height + 1f
    return ZeroActionMeasurement(case.id, text.length, layout.lineCount, layout.hasVisualOverflow, ellipsized, lastGlyphInside)
}

internal fun writeZeroActionMeasurements(label: String, measurements: List<ZeroActionMeasurement>): File {
    val directory = File("build/inbox-zero-action-status-layout").apply { mkdirs() }
    return File(directory, "$label.tsv").apply {
        writeText((listOf("case\ttextRunes\tlineCount\thasVisualOverflow\tellipsizedLines\tlastGlyphInside\tcomplete") +
            measurements.map(ZeroActionMeasurement::tsv)).joinToString("\n", postfix = "\n"))
    }
}
