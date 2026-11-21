package persona.core.hub

import okhttp3.Response
import persona.core.contracts.api.SessionLogTail

internal const val LOG_TAIL_BYTES = 65_536
private val ContentRange = Regex("""bytes (\d+)-(\d+)/(\d+)""")

/** Require the exact suffix from a ranged server, even when a body lacks Content-Length. */
internal fun readSessionLogTail(response: Response): SessionLogTail {
    fun invalid(): Nothing = throw HubFailure(HubError.Protocol, retryable = false)
    val body = response.body ?: invalid()
    val source = body.source()
    if (response.code == 200 || response.code == 206 && response.header("Content-Range") == "bytes 0--1/0") {
        // Go ServeContent can answer a suffix on an empty file with this exact empty range.
        // A server ignoring Range must not stream a larger whole-file body here.
        if (body.contentLength() > 0 || !source.exhausted()) invalid()
        return SessionLogTail("", suffix = false)
    }
    if (response.code != 206) invalid()
    val match = ContentRange.matchEntire(response.header("Content-Range").orEmpty()) ?: invalid()
    val start = match.groupValues[1].toLongOrNull() ?: invalid()
    val end = match.groupValues[2].toLongOrNull() ?: invalid()
    val total = match.groupValues[3].toLongOrNull() ?: invalid()
    if (total <= 0 || end != total - 1L || start != (total - LOG_TAIL_BYTES).coerceAtLeast(0)) invalid()

    val length = total - start
    if (body.contentLength() >= 0 && body.contentLength() != length) invalid()
    // Check one byte past the declared range without consuming an unbounded body.
    if (!source.request(length) || source.request(length + 1L)) invalid()
    val bytes = source.readByteArray(length)
    if (bytes.any { it == 0.toByte() }) invalid()

    val suffix = start > 0
    var first = 0
    // A byte suffix can start inside a UTF-8 character or line.
    if (suffix) while (first < bytes.size && (bytes[first].toInt() and 0xc0) == 0x80) first++
    val decoded = bytes.decodeToString(startIndex = first)
    val newline = decoded.indexOf('\n')
    val shown = if (suffix && newline in 0 until decoded.lastIndex) decoded.substring(newline + 1) else decoded
    return SessionLogTail(shown, suffix)
}
