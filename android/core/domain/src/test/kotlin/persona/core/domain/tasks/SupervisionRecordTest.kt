package persona.core.domain.tasks

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.domain.lines.*

/**
 * #4339 C13: on Glass a quiet supervision round stays in Persona's conversation as one "Handled in the background" line (iOS
 * `SupervisionRecordLine`), and scheduled and follow-up rounds read as they are unless the background rounds fold; Brief
 * and Board keep moving them to the background record.
 */
class SupervisionRecordTest {
    private fun user(n: Int, text: String, source: MessageSource? = null, injected: Injected? = null) =
        Item("u$n", n, ts = "2026-11-12T10:00:${n.toString().padStart(2, '0')}Z", role = "user", kind = "text",
            text = text, source = source, injected = injected)
    private fun reply(n: Int, text: String) = Item("a$n", n, role = "assistant", kind = "text", text = text)
    private fun use(n: Int, tool: String) = Item("use$n", n, role = "assistant", kind = "tool_use", toolUseId = "call$n", toolName = tool)
    private fun result(n: Int, call: Int) = Item("res$n", n, role = "user", kind = "tool_result", toolUseId = "call$call", text = "ok")

    private val supervise = Injected("system", "supervise", "Supervision")
    private val digest = listOf(user(1, "My question"), reply(2, "My answer"),
        user(3, "Supervision: 2 tasks running, nothing for the owner", injected = supervise), use(4, "Bash"), result(5, 4),
        reply(6, "Checking the tasks").copy(narration = true),
        user(7, "Next question"), reply(8, "Next answer"))

    private fun project(items: List<Item>, folding: BackgroundFolding? = BackgroundFolding(rounds = false), inline: Boolean = true,
        live: Boolean = false) =
        AssistantConversation.project(chatLines(items.map(::LineItem), LineOptions(live = live)).lines, items, folding, inline)
    private fun folds(lines: List<ChatLine>) = lines.mapNotNull { (it as? ChatLine.Extra)?.payload as? BackgroundRound }

    @Test fun aSupervisionRoundIsOneFoldedLineOnGlass() {
        val projection = project(digest)
        val round = folds(projection.foreground).single()
        assertTrue(round.supervision)
        assertFalse(round.open)
        assertEquals(listOf("u1", "a2", "bg-u3", "u7", "a8"), projection.foreground.filter { it !is ChatLine.Work }.map { it.id })
        assertTrue(round.lines.map { it.id }.containsAll(listOf("u3", "a6")))
        assertTrue("Folded, not moved into the background record", projection.background.none { it.id in setOf("u3", "a6") })
        // opened by hand: the round follows its line
        val opened = project(digest, BackgroundFolding(open = setOf("bg-u3"), rounds = false)).foreground
        assertTrue(folds(opened).single().open)
        assertTrue(opened.any { it.id == "u3" } && opened.any { it.id == "a6" })
    }

    @Test fun aRunningSupervisionRoundSaysSo() {
        val running = digest.take(5)
        assertTrue(folds(project(running, live = true).foreground).single().live)
    }

    @Test fun aRoundThatToldTheUserStaysShown() {
        val told = listOf(user(1, "Supervision: the deploy failed", injected = supervise),
            use(2, "mcp__persona__notify_user"), result(3, 2))
        val foreground = project(told).foreground
        assertTrue(folds(foreground).isEmpty())
        assertTrue(foreground.any { it.id == "u1" })
    }

    @Test fun completedSupervisionReplyStaysVisibleAcrossStylesUnlessTheFinishWasSilent() {
        val input = user(1, "Check", injected = supervise)
        val finish = use(2, "mcp__persona__supervision_finish")
        val silent = result(3, 2).copy(text = """{"visibility":"silent"}""")
        val reply = reply(4, "The example build is ready.")
        for (inline in listOf(false, true)) {
            val shown = project(listOf(input, reply), BackgroundFolding(), inline)
            assertTrue(folds(shown.foreground).isEmpty())
            assertTrue(shown.foreground.any { it.id == reply.id })
            val quiet = project(listOf(input, finish, silent, reply), BackgroundFolding(), inline)
            assertFalse(quiet.foreground.any { it.id == reply.id })
            if (inline) assertTrue(folds(quiet.foreground).single().lines.any { it.id == reply.id })
            else assertTrue(quiet.background.any { it.id == reply.id })
        }
    }

    @Test fun scheduledRoundsReadAsTheyAreUnlessBackgroundRoundsFold() {
        val items = listOf(user(1, "Daily: summarize the inbox",
            injected = Injected("followup", "daily", "Inbox digest", line = "Inbox digest")), reply(2, "Digest done").copy(narration = true))
        val shown = project(items)
        assertTrue(folds(shown.foreground).isEmpty())
        assertTrue(shown.foreground.any { it.id == "a2" })
        assertTrue(shown.background.none { it.id == "a2" })
        // the main conversation with the hub's switch on folds it as a background round, as before
        assertEquals(listOf("patrol"), folds(project(items, BackgroundFolding()).foreground).map { it.source.kind })
    }

    @Test fun briefAndBoardMoveQuietRoundsToTheBackgroundRecord() {
        val projection = project(digest, folding = null, inline = false)
        assertTrue(folds(projection.foreground).isEmpty())
        assertTrue(projection.foreground.none { it.id == "a6" })
        assertTrue(projection.background.any { it.id == "a6" })
    }
}
