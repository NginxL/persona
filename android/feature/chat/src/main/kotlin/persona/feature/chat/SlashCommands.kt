package persona.feature.chat

import android.content.res.Resources
import androidx.compose.runtime.Composable
import androidx.compose.ui.platform.LocalResources

/** How long the chat waits after /clear for the new session in the same pane before pointing at the list (iOS: 20 s). */
internal const val CLEAR_WAIT_MILLIS = 20_000L

/**
 * Persona's quick messages under short "/" words (iOS SlashCommand, the same five): the "+" sheet sends one at once; a
 * draft that is a single "/" word lists them above the field, and picking one puts its message in the box to send.
 */
internal data class SlashCommand(val title: String, val word: String, val detail: String, val text: String) {
    val clear: Boolean get() = word == CLEAR
    /** /model: picking it opens the model choices (the "Mode" sheet) instead of typing anything (web `isModelCommand`). */
    val model: Boolean get() = word == MODEL
    /**
     * /permissions (#6513, the web's #6476): Persona's own command, picking it opens the permission mode menu with every
     * mode instead of sending anything to the agent.
     */
    val permissions: Boolean get() = word == PERMISSIONS
    /** /loop: picking it types `/loop ` to go on writing, a line under it saying what it will do (web `loopHint`). */
    val loop: Boolean get() = word == LOOP
    val workflows: Boolean get() = word == WORKFLOWS

    companion object {
        /** Claude Code's own commands keep their words in every language; the others are Persona's, in the app's language. */
        fun all(resources: Resources): List<SlashCommand> = listOf(
            SlashCommand(resources.getString(R.string.chat_slash_continue_title), resources.getString(R.string.chat_slash_continue_word),
                resources.getString(R.string.chat_slash_continue_detail), resources.getString(R.string.chat_slash_continue_text)),
            SlashCommand(resources.getString(R.string.chat_slash_compact_title), COMPACT, resources.getString(R.string.chat_slash_compact_detail), COMPACT),
            SlashCommand(resources.getString(R.string.chat_slash_context_title), CONTEXT, resources.getString(R.string.chat_slash_context_detail), CONTEXT),
            SlashCommand(resources.getString(R.string.chat_slash_commit_title), resources.getString(R.string.chat_slash_commit_word),
                resources.getString(R.string.chat_slash_commit_detail), resources.getString(R.string.chat_slash_commit_text)),
            SlashCommand(resources.getString(R.string.chat_slash_report_title), resources.getString(R.string.chat_slash_report_word),
                resources.getString(R.string.chat_slash_report_detail), resources.getString(R.string.chat_slash_report_text)),
        )

        /** A new Claude session in the same pane, the conversation kept in history: asks first, never a quick message. */
        const val CLEAR = "/clear"
        private const val COMPACT = "/compact"
        private const val CONTEXT = "/context"
        fun clearCommand(resources: Resources) =
            SlashCommand(resources.getString(R.string.chat_slash_clear_title), CLEAR, resources.getString(R.string.chat_slash_clear_detail), CLEAR)

        /** The main conversation clears its own context in place, rather than starting an Agent session (web mainCommands). */
        fun mainClearCommand(resources: Resources) =
            SlashCommand(resources.getString(R.string.chat_slash_clear_title), CLEAR, resources.getString(R.string.chat_slash_clear_main_detail), CLEAR)

        /** Claude Code's /model and /loop (web AGENT_COMMANDS): only in the "/" list, never sent from the "+" sheet. */
        const val MODEL = "/model"
        const val LOOP = "/loop"
        const val PERMISSIONS = "/permissions"
        const val WORKFLOWS = "/workflows"
        /** What picking /loop puts in the box: the word, the interval and the task to follow. */
        const val LOOP_TEXT = "$LOOP "
        fun modelCommand(resources: Resources) = resources.getString(R.string.chat_slash_model_detail).let { SlashCommand(it, MODEL, it, MODEL) }
        fun permissionsCommand(resources: Resources) =
            resources.getString(R.string.chat_slash_permissions_detail).let { SlashCommand(it, PERMISSIONS, it, PERMISSIONS) }
        /** `/permissions` alone in the box (typed out and sent): it opens the menu too (the web's `isPermissionsCommand`). */
        fun isPermissions(draft: String): Boolean = draft.trim().equals(PERMISSIONS, ignoreCase = true)
        fun isWorkflows(draft: String): Boolean = draft.trim().equals(WORKFLOWS, ignoreCase = true)
        fun workflowsCommand(resources: Resources) = resources.getString(R.string.chat_background_title).let {
            SlashCommand(it, WORKFLOWS, resources.getString(R.string.chat_background_slash_detail), WORKFLOWS)
        }
        fun loopCommand(resources: Resources) = resources.getString(R.string.chat_slash_loop_detail).let { SlashCommand(it, LOOP, it, LOOP_TEXT) }

        /**
         * What the "/" list offers for [draft]: everything for a bare "/", else the words starting with it (any case).
         * [model] and [loop] add Claude Code's /model and /loop, [clear] its /clear, for a session that takes them;
         * [permissions] Persona's /permissions where the chat has a permission mode picker.
         */
        fun matching(draft: String, clear: Boolean, resources: Resources, model: Boolean = false, loop: Boolean = false,
                     permissions: Boolean = false, workflows: Boolean = false): List<SlashCommand> {
            if (!draft.startsWith("/") || draft.any { it == ' ' || it == '\n' }) return emptyList()
            val list = all(resources) + listOfNotNull(modelCommand(resources).takeIf { model }, permissionsCommand(resources).takeIf { permissions },
                loopCommand(resources).takeIf { loop }, clearCommand(resources).takeIf { clear },
                workflowsCommand(resources).takeIf { workflows })
            return list.filter { it.word.startsWith(draft, ignoreCase = true) }
        }

        /**
         * The list the composer shows: [matching], except once the draft is exactly the one quick message it matches
         * (picked, or typed out): there is nothing left to choose. /clear stays, so picking it can still ask first.
         * [keepExact] keeps the built-in row when a saved prompt also matches the draft.
         */
        fun suggestions(draft: String, clear: Boolean, resources: Resources, model: Boolean = false, loop: Boolean = false,
                        permissions: Boolean = false, keepExact: Boolean = false, workflows: Boolean = false): List<SlashCommand> =
            matching(draft, clear, resources, model, loop, permissions, workflows).takeUnless {
                !keepExact && it.singleOrNull()?.let { only ->
                    !only.clear && !only.model && !only.permissions && !only.workflows && only.word.equals(draft, ignoreCase = true)
                } == true
            }.orEmpty()

        /** The main conversation has its own list (web mainCommands), never the Agent quick messages or terminal commands. */
        fun mainSuggestions(draft: String, clear: Boolean, resources: Resources, permissions: Boolean = false): List<SlashCommand> {
            if (!draft.startsWith("/") || draft.any { it == ' ' || it == '\n' }) return emptyList()
            // /permissions where the conversation has a permission mode picker (the web offers it in Persona's too, #6513).
            return listOfNotNull(permissionsCommand(resources).takeIf { permissions }, mainClearCommand(resources).takeIf { clear })
                .filter { it.word.startsWith(draft, ignoreCase = true) }
        }

        @Composable
        fun suggestions(draft: String, clear: Boolean, model: Boolean = false, loop: Boolean = false, permissions: Boolean = false): List<SlashCommand> =
            suggestions(draft, clear, LocalResources.current, model, loop, permissions)
    }
}
