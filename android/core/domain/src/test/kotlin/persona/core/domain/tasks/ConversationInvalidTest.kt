package persona.core.domain.tasks

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.CoreText
import persona.core.contracts.Session

class ConversationInvalidTest {
    @Test fun codedPermanentErrorsHaveNoContinueEvenWhenTheTextIsUnrecognized() {
        val session = Session(machineId = "box", sessionId = "old", state = "unread", turnError = "Context unavailable",
            turnErrorKind = "unrecoverable", turnErrorCode = "compaction_sealed")
        val cut = Interruption.of(session)!!
        assertTrue(cut.invalid)
        assertFalse(cut.canContinue)
        assertEquals(CoreText.of(CoreText.Key.ApiConversationInvalid), cut.reason)
    }
}
