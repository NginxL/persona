package persona.core.domain.lines

import persona.core.contracts.api.modelName

/**
 * Where the chat marks that the replies' model changed (#5055, the web's `lib/model-change.ts` from #5034; docs/web/web.md
 * "Model pill and model panel"): between two visible exchanges whose reply models are both known and differ. Only what
 * the transcript says wrote a reply counts (`Item.model`), never the model picker; an exchange whose model is unknown is
 * skipped, and nothing goes inside an exchange. The divider sits after the message that started the exchange (Cindy's
 * place), or at its start when no message of the user's began it.
 */
object ModelChanges {
    /** A model id as the chat compares and names it: the same model written another way has the same [canonical]. */
    data class Identity(val canonical: String, val name: String)

    /**
     * A model id without what only says where it runs or which snapshot it is, its case kept: a gateway's vendor path
     * ("vendor/glm-5.1"), Bedrock's region and vendor prefix and version suffix ("us.anthropic.…-v1:0"), Vertex's
     * "@<date>", a trailing "-YYYYMMDD". A context-size suffix ("[1m]") stays: [identity] names it as another model.
     */
    fun bareModel(id: String): String {
        val trimmed = id.trim()
        var s = trimmed.substringAfterLast('/')
        s = s.replace(bedrock, "")
        s = s.replace(vertex, "")
        s = s.replace(bedrockVersion, "")
        s = s.replace(snapshot, "")
        return s.ifEmpty { trimmed }
    }

    /** Claude's id as its family and version ("claude-sonnet-4-5" is "sonnet 4.5", "[1m]" "… 1M"); any other id as it is. */
    fun claudeShort(id: String): String {
        val m = claudeId.matchEntire(id.trim()) ?: return id.trim()
        val version = m.groupValues[2].split('-').filter(String::isNotEmpty).joinToString(".")
        return listOf(m.groupValues[1].lowercase(), version, if (m.groupValues[3].isNotEmpty()) "1M" else "")
            .filter(String::isNotEmpty).joinToString(" ")
    }

    private val identities = object : LinkedHashMap<String, Identity>(64, 0.75f, true) {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, Identity>?) = size > MAX_IDENTITIES
    }

    /** [Identity] of [id]: Claude's "claude-sonnet-4-5-20250929" names "Sonnet 4.5", a GPT id is made readable, any other as it is. */
    fun identity(id: String): Identity = synchronized(identities) {
        identities.getOrPut(id) {
            val bare = bareModel(id)
            val short = claudeShort(bare)
            val name = if (short.isNotEmpty() && short != bare) short.replaceFirstChar { it.uppercaseChar() } else modelName(bare)
            Identity(bare.lowercase(), name)
        }
    }

    /** How a reply's model is named in the chat (the divider's words). */
    fun replyModelName(id: String): String = identity(id).name

    /**
     * [lines] with a [ChatLine.ModelChange] before each exchange whose reply's model differs from the previous visible
     * exchange's. An exchange starts at a message of the user's (its photos before it go with it); its reply's model is
     * that of its latest assistant item naming one, its answer and every step alike (a tool call included). A folded
     * round (a reopened turn's drawer, a background round) is no visible reply, and an exchange whose model is unknown
     * neither gets a divider nor ends the run. The same model written another way is no change, nor are two ids the
     * chat names alike.
     */
    fun mark(lines: List<ChatLine>): List<ChatLine> {
        val exchanges = exchanges(lines)
        if (exchanges.size < 2) return lines
        val inserts = HashMap<Int, ChatLine.ModelChange>()
        var previous: Pair<String, Identity>? = null
        for (exchange in exchanges) {
            val model = replyModel(lines.subList(exchange.start, exchange.end)) ?: continue
            val identity = identity(model)
            val from = previous
            previous = model to identity
            if (from == null || from.second.canonical == identity.canonical || from.second.name == identity.name ||
                exchange.user?.let { EngineChanges.of(lines[it]) } != null) continue
            val at = exchange.user?.let { it + 1 } ?: exchange.start
            inserts[at] = ChatLine.ModelChange("mc-" + lines[exchange.user ?: exchange.start].id, from.first, model)
        }
        if (inserts.isEmpty()) return lines
        return buildList(lines.size + inserts.size) {
            lines.forEachIndexed { i, line ->
                inserts[i]?.let(::add)
                add(line)
            }
            inserts[lines.size]?.let(::add)
        }
    }

    /** One exchange's lines, [start] to [end] (exclusive), and where its user message is. */
    private data class Exchange(val start: Int, val end: Int, val user: Int?)

    private fun exchanges(lines: List<ChatLine>): List<Exchange> {
        val starts = mutableListOf<Pair<Int, Int?>>()
        lines.forEachIndexed { i, line ->
            if (line is ChatLine.Row && line.row.type == "user") {
                // its photos come right before it and belong to it
                val first = if (i > 0 && lines[i - 1] is ChatLine.UserImages) i - 1 else i
                starts += first to i
            }
        }
        if (starts.isEmpty() || starts.first().first > 0) starts.add(0, 0 to null)
        return starts.mapIndexed { k, (start, user) -> Exchange(start, starts.getOrNull(k + 1)?.first ?: lines.size, user) }
    }

    /** The model that wrote an exchange's reply: that of its latest assistant item naming one. */
    private fun replyModel(lines: List<ChatLine>): String? {
        var latest: LineItem? = null
        fun see(item: LineItem?) {
            if (item == null || item.item.role != "assistant" || item.item.model.isNullOrBlank()) return
            val now = latest
            if (now == null || positionCompare(item.seq, item.part, now.seq, now.part) > 0) latest = item
        }
        fun see(step: WorkItem) = when (step) {
            is WorkItem.Tool -> see(step.call.use)
            is WorkItem.Thought -> see(step.item)
            is WorkItem.Folded -> see(step.item)
            is WorkItem.Narration -> see(step.item)
        }
        for (line in lines) when (line) {
            is ChatLine.Row -> { see(line.row.item); line.row.steps.forEach { see(it) } }
            is ChatLine.Work -> line.steps.forEach { see(it) }
            is ChatLine.Step -> see(line.step)
            else -> Unit
        }
        return latest?.item?.model?.trim()
    }

    private const val MAX_IDENTITIES = 256
    private val bedrock = Regex("""^(?:[a-z]+\.)?anthropic\.""", RegexOption.IGNORE_CASE)
    private val vertex = Regex("""@[^@]*$""")
    // only with the colon: "deepseek-v3" is a model
    private val bedrockVersion = Regex("""-v\d+:\d+(?=(?:\[[^\]]*])?$)""", RegexOption.IGNORE_CASE)
    private val snapshot = Regex("""-\d{8}(?=(?:\[[^\]]*])?$)""")
    private val claudeId = Regex("""^claude-([a-z]+)((?:-\d{1,2})*)(?:-\d{8})?(\[1m])?$""", RegexOption.IGNORE_CASE)
}
