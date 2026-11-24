package persona.core.domain.lines

import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText

/**
 * A model API error in a chat (iOS `APIErrorInfo`), read from the text Claude Code writes when it gives up
 * ("API Error: 502 …", an answer) or from the hub's notice for a retry (its Chinese "API error: Overloaded (retrying)"): why, in
 * words, the status, and whether it is likely to pass by itself; the ids and the raw text fold under it.
 */
data class ApiErrorInfo(
    val status: Int?,
    /** One line, in words. */
    val reason: CoreText,
    /**
     * Likely to pass if tried again (5xx, rate limits, timeouts); false when someone has to act (login, a bad
     * request); null when the text does not say.
     */
    val transient: Boolean?,
    val requestId: String?,
    val session: String?,
    val raw: String,
    /** An older hub has no `turn_error_code`; the same raw 400 recognition as the web keeps its tool-search remedy. */
    val toolSearchUnsupported: Boolean = false,
) {
    /** Two errors are "the same" for folding when these match. */
    val signature: String get() = "${status ?: 0}|${reason.key}"

    /** Someone has to act (a login, a bad request): sending "continue" again would not help. */
    val needsAction: Boolean get() = transient == false

    companion object {
        fun unrecoverable(text: String?): Boolean {
            val s = text.orEmpty().lowercase()
            return "compacted natively" in s && "compaction is sealed" in s && "to that account" in s
        }

        private val statusPattern = Regex("""^\s*(\d{3})\b""")
        private val badRequestPattern = Regex("""^\s*400\b""")
        private val toolSearchMarkers = listOf("toolsearch", "tool_search", "tool search", "defer_loading", "tool_reference")
        /** One short sentence, then the error ("Failed to authenticate. API Error:"), not prose that mentions one. */
        private val lead = Regex("""^[A-Z][A-Za-z ]{2,40}\.\s+API Error:""")
        private val requestPattern = Regex("""(?i)\b(?:req(?:uest)?(?:[ _-]?id)?)[:=\s"]+([A-Za-z0-9_-]{8,})""")
        private val sessionPattern = Regex("""(?i)\bsession(?:[ _-]?id)?[:=\s"]+([A-Za-z0-9_:-]{6,})""")

        /** The error in [text], or null when it is not one (an answer that only mentions an API error is not). */
        fun parse(text: String?): ApiErrorInfo? {
            val trimmed = text?.trim() ?: return null
            val body = when {
                trimmed.startsWith("API Error:") -> trimmed.removePrefix("API Error:")
                trimmed.startsWith("API 错误：") -> trimmed.removePrefix("API 错误：") // i18n-ignore: the hub's retry notice it reads
                unrecoverable(trimmed) && trimmed.lowercase().startsWith("this conversation was compacted natively by ") -> trimmed
                // Claude Code leads with a sentence for some: "Failed to authenticate. API Error: 401 …".
                else -> lead.find(trimmed)?.let { trimmed.substring(it.range.last + 1) } ?: return null
            }
            val status = statusPattern.find(body)?.groupValues?.get(1)?.toIntOrNull()
            val lower = body.lowercase()
            val (reason, transient) = explain(lower, status)
            val toolSearchUnsupported = (badRequestPattern.containsMatchIn(lower) || "not representable" in lower) &&
                toolSearchMarkers.any { it in lower }
            return ApiErrorInfo(status, reason, transient,
                requestPattern.find(body)?.groupValues?.get(1), sessionPattern.find(body)?.groupValues?.get(1), trimmed,
                toolSearchUnsupported)
        }

        /** What went wrong, as a person would say it, and whether it is likely to pass (the iOS wording). */
        private fun explain(s: String, status: Int?): Pair<CoreText, Boolean?> = when {
            unrecoverable(s) -> CoreText.of(Key.ApiConversationInvalid) to false
            "no available gpu" in s -> CoreText.of(Key.ApiNoGpu) to true
            "overloaded" in s || status == 529 -> CoreText.of(Key.ApiOverloaded) to true
            status == 401 || status == 403 || "authentication" in s || "/login" in s || "login expire" in s ->
                CoreText.of(Key.ApiLogin) to false
            status == 429 || "rate limit" in s || "rate_limit" in s -> CoreText.of(Key.ApiRateLimited) to true
            "timed out" in s || "timeout" in s -> CoreText.of(Key.ApiTimeout) to true
            "connection" in s -> CoreText.of(Key.ApiConnection) to true
            status == 400 -> CoreText.of(Key.ApiBadRequest) to false
            status != null && status in 500..599 -> CoreText.of(Key.ApiUnavailable) to true
            else -> CoreText.of(Key.ApiFailed) to (if ("temporary" in s || "try again" in s) true else null)
        }
    }
}

private val retryWords = setOf("继续", "重试", "再试一次", "continue", "retry", "go on") // i18n-ignore: what a user types to retry

/** A bare "continue" between two of the same error: the user retrying, folded into the error's count. */
internal fun isRetry(line: ChatLine): Boolean {
    val row = (line as? ChatLine.Row)?.row ?: return false
    return row.type == "user" && row.item?.text.orEmpty().trim().lowercase() in retryWords
}

/**
 * Answers and notices that are API errors become [ChatLine.ApiError] cards; the same error again right after, with
 * nothing but a retry ("continue") between, folds into the first with a count (iOS `ChatLines.foldAPIErrors`). Only
 * top-level lines: a notice inside a work line stays a step of it.
 */
fun foldApiErrors(lines: List<ChatLine>): List<ChatLine> {
    val out = ArrayList<ChatLine>(lines.size)
    for (line in lines) {
        val row = (line as? ChatLine.Row)?.row
        val item = if (row != null && (row.type == "reply" || row.type == "notice")) row.item else null
        val info = item?.let { ApiErrorInfo.parse(it.text) }
        if (item == null || info == null) {
            out += line
            continue
        }
        val last = out.indexOfLast { it is ChatLine.ApiError }
        val previous = out.getOrNull(last) as? ChatLine.ApiError
        if (previous != null && previous.info.signature == info.signature && out.subList(last + 1, out.size).all(::isRetry)) {
            out.subList(last + 1, out.size).clear()
            out[last] = previous.copy(info = info, count = previous.count + 1)
            continue
        }
        out += ChatLine.ApiError(item, info, 1)
    }
    return out
}

/**
 * Claude Code writes a recap each time an idle session is looked at again: several in a row (nothing else between)
 * are one line (iOS `ChatLines.foldRecaps`), keeping the first recap's id so the line stays put as more arrive.
 */
fun foldRecaps(lines: List<ChatLine>): List<ChatLine> {
    val out = ArrayList<ChatLine>(lines.size)
    val run = ArrayList<ChatLine.Row>()
    fun flush() {
        when (run.size) {
            0 -> Unit
            1 -> out += run[0]
            else -> out += ChatLine.Recaps(run.mapNotNull { it.row.item })
        }
        run.clear()
    }
    for (line in lines) {
        if (line is ChatLine.Row && line.row.type == "recap" && line.row.item != null) {
            run += line
            continue
        }
        flush()
        out += line
    }
    flush()
    return out
}
