package persona.feature.chat

import androidx.compose.runtime.compositionLocalOf
import persona.core.contracts.api.SavedPrompt

/** The shown assistant's prompts, also used in Agent chats and chats with no built-in slash commands. */
val LocalSavedPrompts = compositionLocalOf<List<SavedPrompt>> { emptyList() }

internal fun matchingSavedPrompts(prompts: List<SavedPrompt>, draft: String): List<SavedPrompt> {
    if (!draft.startsWith('/') || draft.any(Char::isWhitespace)) return emptyList()
    val query = draft.drop(1)
    return prompts.filter { it.name.startsWith(query, ignoreCase = true) }
}
