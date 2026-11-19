package persona.core.contracts

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** `work.just_finished` (#9751, #9786): optional, and absent means what older hubs did (Seen). */
class JustFinishedContractTest {
    private fun session(work: String) = JsonCodec.decodeFromString<Session>(
        """{"machine_id":"m1","session_id":"s1","status":"done","state":"idle","work":$work}""")

    @Test fun aSessionsWorkSaysItJustFinished() {
        assertTrue(session("""{"phase":"done","line":"Merged","just_finished":true}""").work!!.justFinished)
        // An older hub, or a turn somebody saw: no flag.
        assertFalse(session("""{"phase":"done","line":"Merged"}""").work!!.justFinished)
        assertFalse(WorkState().justFinished)
    }
}
