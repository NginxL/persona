package persona.feature.inbox

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class LarkMessagesNavigationMetadataTest {
    private val listed = listOf(
        LarkChatSummary(id = "chat", name = "Listed name", readPosition = 41, mentionPosition = 52),
    )

    @Test fun onlyMessagesRoutesReuseTheListedSummary() {
        assertEquals(
            listed.single(),
            larkMessagesSummary(LarkRoute("chat", fromMessages = true), listed),
        )
        assertNull(larkMessagesSummary(LarkRoute("chat"), listed))
        assertNull(larkMessagesSummary(LarkRoute("chat", itemId = "lark:chat"), listed))
        assertNull(larkMessagesSummary(LarkRoute("chat", startPosition = 7), listed))
        assertNull(larkMessagesSummary(LarkRoute("other", fromMessages = true), listed))
    }
}
