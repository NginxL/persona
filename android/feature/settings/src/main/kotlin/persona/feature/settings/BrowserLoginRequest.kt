package persona.feature.settings

import java.net.URI
import java.net.URISyntaxException
import java.net.URLDecoder

internal data class BrowserLoginRequest(val id: String, val hub: String)

/** Parse a scanner/deep-link boundary. No address from the code is used for networking. */
internal fun browserLoginRequest(link: String): BrowserLoginRequest? {
    val uri = try { URI(link) } catch (_: URISyntaxException) { return null }
    if (uri.scheme != "persona" || uri.host != "browser-login" ||
        uri.rawUserInfo != null || uri.port != -1 || !uri.rawPath.isNullOrEmpty() || uri.rawFragment != null) return null
    val fields = try {
        uri.rawQuery.orEmpty().split('&').map { field ->
            val parts = field.split('=', limit = 2)
            URLDecoder.decode(parts[0], "UTF-8") to URLDecoder.decode(parts.getOrElse(1) { "" }, "UTF-8")
        }
    } catch (_: IllegalArgumentException) { return null }
    if (fields.size != 2 || fields.map { it.first }.toSet() != setOf("hub", "request")) return null
    val values = fields.toMap()
    val id = values.getValue("request")
    if (!id.matches(Regex("[A-Za-z0-9_-]{43}")) || values.getValue("hub").isBlank() || values.getValue("hub").length > 80) return null
    return BrowserLoginRequest(id, values.getValue("hub"))
}
