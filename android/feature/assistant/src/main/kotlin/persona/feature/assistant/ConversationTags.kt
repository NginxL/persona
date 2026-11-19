package persona.feature.assistant

import persona.core.contracts.Session

/** The hub's write limits (issue #9719), mirrored so the editor refuses what the API would. */
internal const val CONVERSATION_TAG_MAX_CODE_POINTS = 32
internal const val CONVERSATION_TAGS_MAX = 10

internal enum class ConversationTagProblem { TooLong, TooMany }

/** Trim, drop empties and collapse exact, case-sensitive duplicates to their first occurrence. */
internal fun normalizeConversationTags(input: List<String>): List<String> =
    input.asSequence().map { it.trim() }.filter { it.isNotEmpty() }.distinct().toList()

/** Null when the hub would accept [input], after the same trim and length checks in the same order. */
internal fun conversationTagsProblem(input: List<String>): ConversationTagProblem? {
    val distinct = LinkedHashSet<String>()
    for (raw in input) {
        val tag = raw.trim()
        if (tag.isEmpty()) continue
        if (tag.codePointCount(0, tag.length) > CONVERSATION_TAG_MAX_CODE_POINTS) return ConversationTagProblem.TooLong
        distinct += tag
    }
    return if (distinct.size > CONVERSATION_TAGS_MAX) ConversationTagProblem.TooMany else null
}

/** Every label in use, once, for the filter strip. Null labels from older hubs contribute nothing. */
internal fun inUseConversationTags(sessions: List<Session>): List<String> =
    sessions.flatMap { it.tags.orEmpty() }.distinct().sorted()

/** Only rows carrying this exact, case-sensitive tag; no selection returns the original list and structure. */
internal fun List<Session>.withConversationTag(tag: String?): List<Session> =
    if (tag.isNullOrEmpty()) this else filter { it.tags?.contains(tag) == true }
