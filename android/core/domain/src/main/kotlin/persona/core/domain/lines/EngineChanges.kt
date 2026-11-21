package persona.core.domain.lines

import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.domain.MessageContext
import persona.core.domain.parseContexts

/** One handoff recorded in the first message a new engine receives. */
data class EngineChange(val from: String, val to: String, val model: String?, val resumed: Boolean, val handoff: String)

object EngineChanges {
    fun fromContext(context: MessageContext): EngineChange? {
        if (context.kind != "engine-switch") return null
        val from = context.attributes["from"]?.trim().orEmpty()
        val to = context.attributes["to"]?.trim().orEmpty()
        if (from == to || from !in engines || to !in engines) return null
        return EngineChange(from, to, context.attributes["model"]?.trim()?.takeIf(String::isNotEmpty),
            context.attributes["resumed"] == "1", context.body)
    }

    fun of(line: ChatLine): EngineChange? {
        val row = (line as? ChatLine.Row)?.row?.takeIf { it.type == "user" } ?: return null
        val text = row.item?.text?.takeIf { "engine-switch" in it } ?: return null
        return parseContexts(text).first.firstNotNullOfOrNull(::fromContext)
    }

    /** Keep the divider with the exchange, including a photo row immediately before the text. */
    fun mark(lines: List<ChatLine>): List<ChatLine> {
        val marks = lines.mapIndexedNotNull { index, line ->
            of(line)?.let { change ->
                val at = if (lines.getOrNull(index - 1) is ChatLine.UserImages) index - 1 else index
                at to ChatLine.EngineChanged("engine-" + line.id, change)
            }
        }.toMap()
        if (marks.isEmpty()) return lines
        return buildList(lines.size + marks.size) {
            lines.forEachIndexed { index, line ->
                marks[index]?.let(::add)
                add(line)
            }
        }
    }

    /** A listed session keeps its own link. Only a unique, active successor on its computer may replace it. */
    fun home(rows: List<Session>, previous: SessionKey): Session? {
        if (rows.any { it.key == previous && !it.archived }) return null
        return rows.filter { row ->
            row.machineId == previous.machineId && !row.assistant && !row.archived &&
                row.sessionId != previous.sessionId && row.engineHistory.any { it.sessionId == previous.sessionId }
        }.singleOrNull()
    }

    private val engines = setOf("claude", "codex")
}
