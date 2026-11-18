package persona.core.domain.tasks

import persona.core.contracts.Item
import persona.core.domain.sourceMetadata
import persona.core.domain.sourceOf
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.WorkItem

/** Display-only partition: the repository and timeline keep every original record and fold range. */
data class AssistantConversation(val foreground: List<ChatLine>, val background: List<ChatLine>) {
    companion object {
        /**
         * [folding]: the main and leader conversations fold their background rounds (#4314, #6595, [BackgroundFolding]); null
         * leaves every round as it was. [inline] (Glass, #4339 C13, iOS `ChatLines.lines`): a quiet round stays in the
         * conversation rather than going to the background record: a supervision round folds into one "Handled in the
         * background" line (with [folding]), and the scheduled and follow-up rounds read as they are unless the background
         * rounds fold. [supervisingConversation]: true for the main and leader conversations; a standalone scheduled or
         * follow-up conversation owns its scheduled/follow-up exchanges and never strips them into the background record.
         */
        fun project(
            lines: List<ChatLine>,
            items: List<Item>,
            folding: BackgroundFolding? = null,
            inline: Boolean = false,
            supervisingConversation: Boolean = true,
        ): AssistantConversation {
            val quiet = mutableSetOf<String>()
            val hints = mutableSetOf<String>()
            val quietRoundOf = mutableMapOf<String, String>()
            val quietRounds = mutableMapOf<String, MutableList<Item>>()
            var quietRound: String? = null
            // The folded round each item belongs to (its first input's id), and each such round's source and items.
            val roundOf = mutableMapOf<String, String>()
            val rounds = mutableMapOf<String, Pair<persona.core.contracts.MessageSource, MutableList<Item>>>()
            var round: String? = null
            items.sortedBy { it.seq }.forEach { item ->
                if (item.kind == "text" && item.role == "user") {
                    val injected = item.injected
                    val metadata = sourceMetadata(item.text.orEmpty())
                    // Only structured provenance can move a whole exchange out of the conversation.
                    val source = if (item.source != null || injected != null || metadata.found) sourceOf(item) else null
                    val systemHint = injected?.kind == "system" && injected.state != "supervise"
                    if (systemHint) hints += item.id
                    else {
                        val isSupervision = source?.kind == "supervision" || injected?.kind == "supervision"
                        val isScheduledOrFollowup = injected?.kind != "limit_continue" &&
                            injected?.state !in setOf("limit_continue", "wake") &&
                            (source?.kind in setOf("patrol", "routine", "schedule") ||
                                (source?.kind == "followup" && !source.conversationId.isNullOrBlank()) ||
                                (injected?.kind == "followup" && injected.state in setOf("once", "daily", "interval")) ||
                                injected?.kind in setOf("routine", "patrol", "schedule"))
                        val quietCandidate = !inline && (isSupervision || (supervisingConversation && isScheduledOrFollowup))
                        // A round lasts until the next genuine input, a queued one included; a folded one is drawn, not moved.
                        round = folding?.let { f ->
                            BackgroundFolding.supervision.takeIf { (inline || !supervisingConversation) && isSupervision }
                                ?: BackgroundFolding.sourceOf(item)?.takeIf { f.rounds }
                        }?.let { folded ->
                            rounds[item.id] = folded to mutableListOf()
                            item.id
                        }
                        quietRound = if (round == null && quietCandidate) {
                            quietRounds[item.id] = mutableListOf()
                            item.id
                        } else null
                    }
                } else if (item.kind == "image" && item.role == "user" && item.toolUseId == null) {
                    quietRound = null
                    round = null
                }
                quietRound?.let { id -> quietRoundOf[item.id] = id; quietRounds.getValue(id) += item }
                round?.let { id -> roundOf[item.id] = id; rounds.getValue(id).second += item }
                // An upstream refusal (#2528) is the user's to see in Persona's chats too, as on the web.
                if ((item.role == "system" || item.kind == "notice" || item.kind == "recap") && item.modelRefusal == null) hints += item.id
            }
            if (quietRounds.isNotEmpty()) {
                val results = items.filter { it.kind == "tool_result" && it.toolUseId != null }.associateBy { it.toolUseId!! }
                for ((id, ordered) in quietRounds) {
                    val live = lines.any { line ->
                        line is ChatLine.Work && line.live && (
                            (line.id == persona.core.domain.lines.LiveWorkId && quietRound == id) ||
                                quietRoundOf[line.id.removePrefix("w-")] == id ||
                                line.steps.any { quietRoundOf[it.originalId()] == id }
                            )
                    }
                    if (!supervisionVisible(ordered, results, live)) ordered.mapTo(quiet) { it.id }
                }
            }
            val quietGroups = lines.filterIsInstance<ChatLine.Work>().filter { line ->
                line.id.removePrefix("w-") in quiet || line.steps.any { it.originalId() in quiet }
            }.mapTo(mutableSetOf()) { it.id }
            if (quietRound != null && quietRound in quiet) quietGroups += persona.core.domain.lines.LiveWorkId
            val record = mutableListOf<ChatLine>()
            val visible = lines.mapNotNull { line ->
                val hidden = when (line) {
                    is ChatLine.Row -> line.id in quiet || line.id in hints
                    is ChatLine.UserImages -> line.images.all { it.id in quiet }
                    is ChatLine.Work -> line.id in quietGroups
                    is ChatLine.Step -> line.groupId in quietGroups || line.step.originalId() in hints
                    is ChatLine.End -> line.groupId in quietGroups
                    // What a quiet run's work looked at goes with its work line.
                    is ChatLine.Viewed -> "w-" + line.id.removePrefix("vi-") in quietGroups
                    is ChatLine.ApiError -> line.id in quiet
                    is ChatLine.Recaps, is ChatLine.Wakes -> true
                    is ChatLine.Extra -> (line.payload as? AssistantExtra.Feed)?.item?.let(AssistantTaskCards::isBackgroundEvent) == true
                    else -> false
                }
                if (hidden) { record += line; null }
                else if (line is ChatLine.Work) {
                    val clean = line.steps.filterNot { it.originalId() in hints }
                    if (clean != line.steps) record += line
                    if (clean.isEmpty() && !line.live) null else line.copy(steps = clean)
                } else line
            }
            if (folding == null || rounds.isEmpty()) return AssistantConversation(visible, record)
            return AssistantConversation(fold(visible, items, folding, roundOf, rounds, liveRound = round), record)
        }

        /**
         * Each folded round's lines, which are consecutive, become one [BackgroundRound] line followed by them while it is
         * open; a round that must stay shown ([supervisionVisible]) keeps its lines as they are.
         */
        private fun fold(
            lines: List<ChatLine>, items: List<Item>, folding: BackgroundFolding, roundOf: Map<String, String>,
            rounds: Map<String, Pair<persona.core.contracts.MessageSource, List<Item>>>, liveRound: String?,
        ): List<ChatLine> {
            val workRound = mutableMapOf<String, String>()
            fun of(line: ChatLine): String? = when (line) {
                is ChatLine.Row -> line.row.item?.id?.let(roundOf::get) ?: line.row.steps.firstNotNullOfOrNull { roundOf[it.originalId()] }
                is ChatLine.UserImages -> line.images.firstNotNullOfOrNull { roundOf[it.id] }
                // Working with nothing of this run yet: the run belongs to the round the last input started.
                is ChatLine.Work -> (if (line.id == persona.core.domain.lines.LiveWorkId) liveRound
                    else roundOf[line.id.removePrefix("w-")] ?: line.steps.firstNotNullOfOrNull { roundOf[it.originalId()] })
                    ?.also { workRound[line.id] = it }
                is ChatLine.Step -> workRound[line.groupId]
                is ChatLine.End -> workRound[line.groupId]
                is ChatLine.Viewed -> workRound["w-" + line.id.removePrefix("vi-")]
                is ChatLine.ApiError -> roundOf[line.item.id]
                else -> null
            }
            val owners = lines.map(::of)
            val results = items.filter { it.kind == "tool_result" && it.toolUseId != null }.associateBy { it.toolUseId!! }
            val out = ArrayList<ChatLine>(lines.size)
            var i = 0
            while (i < lines.size) {
                val id = owners[i]
                if (id == null) { out += lines[i]; i++; continue }
                // Up to the round's last line, so a round is one fold whatever is drawn between its parts.
                val end = owners.lastIndexOf(id)
                val members = lines.subList(i, end + 1)
                i = end + 1
                val live = members.any { it is ChatLine.Work && it.live }
                val (source, ordered) = rounds.getValue(id)
                if (supervisionVisible(ordered, results, live)) { out += members; continue }
                val lineId = BackgroundFolding.lineId(id)
                val open = lineId in folding.open || ordered.any { it.id == folding.reveal }
                out += ChatLine.Extra(lineId, BackgroundRound(lineId, source, live, open, members.toList()))
                if (open) out += members
            }
            return out
        }
    }
}

private fun WorkItem.originalId(): String = when (this) {
    is WorkItem.Tool -> call.use.id
    is WorkItem.Thought -> item.id
    is WorkItem.Folded -> item.id
    is WorkItem.Narration -> item.id
}
