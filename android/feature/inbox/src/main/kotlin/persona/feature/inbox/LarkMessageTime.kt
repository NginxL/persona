package persona.feature.inbox

import persona.core.contracts.api.InboxAssistantTransport
import persona.core.designsystem.LocalizedFormats
import kotlinx.serialization.json.*
import java.net.URLEncoder
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId
import java.util.Locale
import persona.core.inbox.*

internal fun larkDate(time: String): LocalDate? = runCatching {
    Instant.parse(time).atZone(ZoneId.systemDefault()).toLocalDate()
}.getOrNull()

/** A message's time in the language the app is in: 14:30 · 2:30 PM. */
internal fun larkTime(time: String, locale: Locale, zone: ZoneId = ZoneId.systemDefault()): String = runCatching {
    LocalizedFormats.format(Instant.parse(time), LocalizedFormats.TIME, locale, zone)
}.getOrDefault(time)

/** Account-scoped retained recall and edit-history preferences. */
internal fun larkMessageDisplay(data: JsonObject): LarkMessageDisplay =
    LarkMessageDisplay(accountId = data.text("account_id"),
        antiRecall = data.flag("anti_recall"), showEditHistory = data.flag("show_edit_history"))
