package persona.feature.assistant

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Test
import persona.core.contracts.Session
import persona.core.contracts.api.AssistantSearchAnswer
import persona.core.contracts.api.AssistantSearchResult
import persona.core.contracts.api.AssistantSnapshot

class ConversationTagsTest {
    private val words = ConversationWords("Chat", "Hand off", "Hand off: %1\$s", "Background")

    @Test fun normalizationTrimsDropsEmptiesAndKeepsTheFirstExactOccurrence() {
        assertEquals(
            listOf("planning", "Planning", "旅行"),
            normalizeConversationTags(listOf("  planning ", "", "planning", " Planning ", "\t", "旅行")),
        )
        assertEquals(emptyList<String>(), normalizeConversationTags(emptyList()))
        assertEquals(emptyList<String>(), normalizeConversationTags(listOf(" ", "\n")))
    }

    @Test fun validationCountsUnicodeCodePointsAndNormalizedDistinctTags() {
        val book = "📚"
        assertNull(conversationTagsProblem(listOf(book.repeat(32))))
        assertEquals(ConversationTagProblem.TooLong, conversationTagsProblem(listOf(book.repeat(33))))
        assertEquals(ConversationTagProblem.TooLong, conversationTagsProblem(listOf("好".repeat(33))))
        assertNull(conversationTagsProblem((1..10).map { "tag-$it" }))
        assertEquals(ConversationTagProblem.TooMany, conversationTagsProblem((1..11).map { "tag-$it" }))
        assertNull(conversationTagsProblem((1..10).map { "tag-$it" } + listOf(" tag-1 ", "")))
        assertNull(conversationTagsProblem(emptyList()))
    }

    @Test fun chipsUseEveryExactTagOnceAndNoTagsLeaveTheListUntouched() {
        val main = Session(machineId = "mac", sessionId = "main", tags = listOf("trip", "Planning"))
        val chat = Session(machineId = "mac", sessionId = "chat", tags = listOf("trip", "旅行"))
        val duplicate = chat.copy(tags = listOf("Planning"))
        val untagged = Session(machineId = "mac", sessionId = "plain")
        val chats = listOf(main, chat, duplicate, untagged)

        assertEquals(listOf("Planning", "trip", "旅行"), inUseConversationTags(chats))
        assertEquals(listOf(main, chat), chats.withConversationTag("trip"))
        assertEquals(emptyList<Session>(), chats.withConversationTag("Trip"))
        assertSame(chats, chats.withConversationTag(null))
        assertSame(chats, chats.withConversationTag(""))
        assertEquals(emptyList<String>(), inUseConversationTags(listOf(untagged, untagged.copy(tags = null))))
    }

    @Test fun tagAndTextSearchIntersectAcrossMainRecentAndAllChats() {
        val main = Session(machineId = "mac", sessionId = "main", title = "Main", tags = listOf("work"),
            updatedAt = "2026-11-17T09:00:00Z")
        val release = Session(machineId = "mac", sessionId = "release", title = "Release plan", lastText = "Alpha notes",
            tags = listOf("work"), updatedAt = "2026-11-17T08:00:00Z")
        val personal = Session(machineId = "mac", sessionId = "personal", title = "Personal", lastText = "Alpha notes",
            tags = listOf("personal"), updatedAt = "2026-11-17T07:00:00Z")
        val otherWork = Session(machineId = "mac", sessionId = "other", title = "Other work", tags = listOf("work"),
            updatedAt = "2026-11-17T06:00:00Z")
        val archived = Session(machineId = "mac", sessionId = "archived", title = "Alpha archived", archived = true,
            tags = listOf("work"))
        val snapshot = AssistantSnapshot(main.key, listOf(main, release, personal, otherWork, archived))

        val selected = conversationGroups(snapshot, "", words, activeTag = "work")
        assertEquals(main, selected.main)
        assertEquals(listOf(release, otherWork), selected.others)
        assertEquals(listOf(release, otherWork), selected.recent)
        val intersected = conversationGroups(snapshot, "alpha", words, activeTag = "work")
        assertEquals(listOf(release), intersected.others)
        assertEquals(emptyList<Session>(), intersected.recent)
        val withoutMain = conversationGroups(snapshot, "", words, activeTag = "personal")
        assertNull(withoutMain.main)
        assertEquals(listOf(personal), withoutMain.others)
        assertEquals(conversationGroups(snapshot, "alpha", words),
            conversationGroups(snapshot, "alpha", words, activeTag = null))
        assertEquals(emptyList<Session>(), conversationGroups(snapshot, "", words, activeTag = "missing").others)
    }

    @Test fun fullWindowSearchAlsoIntersectsTheTagAndKeepsArchivedRowsOut() {
        val work = Session(machineId = "mac", sessionId = "work", title = "Alpha work", tags = listOf("work"))
        val personal = Session(machineId = "mac", sessionId = "personal", title = "Alpha personal", tags = listOf("personal"))
        val archived = Session(machineId = "mac", sessionId = "archived", title = "Alpha archived", archived = true, tags = listOf("work"))
        val chats = listOf(work, personal, archived)
        val answer = AssistantSearchAnswer(results = listOf(
            AssistantSearchResult(sessionId = personal.sessionId, machineId = personal.machineId, title = personal.title),
            AssistantSearchResult(sessionId = work.sessionId, machineId = work.machineId, title = work.title),
        ))

        assertEquals(listOf(work), filterChatsForFullSearch(chats, "alpha", words, activeTag = "work"))
        val rows = fullSearchRows("alpha", chats, chats, enabled = true, answer = answer, words = words, activeTag = "work")
        assertEquals(listOf(work.key), rows.map { it.session.key })
        assertEquals(listOf(work), filterChatsForFullSearch(chats, "", words, activeTag = "work"))

        // The compact drawer's hub merge can synthesize a row unknown to the filtered local roster; it still needs the tag.
        val drawerRows = shownSearchRows("alpha", enabled = true, local = emptyList(), known = listOf(work),
            answer = answer, activeTag = "work")
        assertEquals(listOf(work.key), drawerRows?.map { it.session.key })
    }
}
