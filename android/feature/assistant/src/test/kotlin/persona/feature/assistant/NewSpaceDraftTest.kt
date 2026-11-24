package persona.feature.assistant

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.ConversationCategories
import persona.core.contracts.api.TrackKind
import persona.core.contracts.api.Tracks

/** The new space form's rules (#13500) and the categories chip's words, apart from the screen. */
class NewSpaceDraftTest {
    @Test fun theAddressFollowsTheNameUntilTypedAndTakenOnesAreRefused() {
        val draft = NewSpaceDraft { listOf("qa-plain") }
        assertFalse(draft.ready)
        draft.title = "Family notes"
        assertEquals("family-notes", draft.slug)
        assertTrue(draft.ready)
        draft.typeSlug("QA-Plain")
        assertEquals("qa-plain", draft.slug)
        assertTrue(draft.slugTaken)
        assertFalse(draft.ready)
        draft.typeSlug("new")
        assertFalse("the form's own word is never an address", draft.slugOk)
    }

    @Test fun aChineseNameGetsASpaceAddressAndNoCategoriesByDefault() {
        val draft = NewSpaceDraft { emptyList() }
        draft.title = "家人"
        assertEquals("space", draft.slug)
        val create = draft.create()
        assertEquals(emptyList<String>(), create.defaultCategories)
        assertEquals(Tracks.DEFAULT_KIND, create.kind)
        assertEquals("layers", create.icon)
        draft.toggle("finance")
        draft.toggle("health")
        draft.toggle("finance")
        assertEquals(listOf("health"), draft.create().defaultCategories)
    }

    @Test fun theKindFollowsWhatTheAssistantOffers() {
        val draft = NewSpaceDraft { emptyList() }
        draft.kindsLoaded(listOf(TrackKind("orchestration"), TrackKind("journal")))
        assertEquals(listOf("journal"), draft.kinds.map { it.slug })
        assertEquals("journal", draft.kind)
        val other = NewSpaceDraft { emptyList() }
        other.kindsLoaded(listOf(TrackKind("journal"), TrackKind(Tracks.DEFAULT_KIND)))
        assertEquals(Tracks.DEFAULT_KIND, other.kind)
    }

    @Test fun theChipSaysTheFirstCategoryAndHowManyMore() {
        assertEquals(null, categoriesChipText(ConversationCategories()) { it })
        assertEquals("finance", categoriesChipText(ConversationCategories(listOf("finance"))) { it })
        assertEquals("finance +2", categoriesChipText(ConversationCategories(listOf("finance", "health", "emotion"))) { it })
    }
}
