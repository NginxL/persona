package persona.core.domain.lines

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.EngineLeg
import persona.core.contracts.Item
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.domain.parseContexts

class EngineChangesTest {
    private val handoff = """<persona-context kind="engine-switch" from="claude" to="codex" model="gpt-5.5" resumed="1">Review &amp; continue</persona-context>"""
    private fun user(id: String, text: String) = ChatLine.Row(ChatRow("user", LineItem(Item(id, kind = "text", role = "user", text = text))))
    private fun reply(id: String, model: String) = ChatLine.Row(ChatRow("reply", LineItem(Item(id, kind = "text", role = "assistant", model = model))))

    @Test fun aValidHandoffMarksTheExchangeAndReplacesItsModelChange() {
        val lines = listOf(user("old", "Keep the worker"), reply("first", "claude-sonnet-4-5"),
            user("switch", "$handoff\n\nContinue from here"), reply("second", "gpt-5.5"),
            user("regular", "Check once more"), reply("third", "gpt-5.4"))
        val marked = EngineChanges.mark(ModelChanges.mark(lines))
        val divider = marked.filterIsInstance<ChatLine.EngineChanged>().single()
        assertEquals("engine-switch", divider.id)
        assertEquals(EngineChange("claude", "codex", "gpt-5.5", true, "Review & continue"), divider.change)
        assertEquals("switch", marked[marked.indexOf(divider) + 1].id)
        assertEquals("gpt-5.4", marked.filterIsInstance<ChatLine.ModelChange>().single().to)
        assertEquals("Continue from here", parseContexts(lines[2].let { (it as ChatLine.Row).row.item!!.text!! }).second)
    }

    @Test fun onlyDifferentRecognizedEnginesAndUserRowsMakeADivider() {
        val reply = ChatLine.Row(ChatRow("reply", LineItem(Item("a", text = handoff))))
        val invalid = listOf(reply, user("same", handoff.replace("to=\"codex\"", "to=\"claude\"")),
            user("unknown", handoff.replace("from=\"claude\"", "from=\"other\"")))
        assertSame(invalid, EngineChanges.mark(invalid))
    }

    @Test fun anOldLinkFollowsAUniqueSuccessorOnlyOnItsOwnComputer() {
        val old = SessionKey("m", "old")
        val new = Session("m", "new", engineHistory = listOf(EngineLeg("old", "claude")))
        assertEquals(new, EngineChanges.home(listOf(new.copy(machineId = "other"), new), old))
        assertNull(EngineChanges.home(listOf(new, Session("m", "old")), old))
        assertNull(EngineChanges.home(listOf(new, new.copy(sessionId = "different")), old))
        assertNull(EngineChanges.home(listOf(new.copy(assistant = true)), old))
        assertNull(EngineChanges.home(listOf(new.copy(archived = true)), old))
    }
}
