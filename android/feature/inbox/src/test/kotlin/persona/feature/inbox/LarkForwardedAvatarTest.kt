package persona.feature.inbox

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.util.concurrent.atomic.AtomicInteger
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.designsystem.PersonaTheme
import persona.core.inbox.*

/** #5696: a forwarded bundle's senders show their pictures once the hub is connected, not only their initials. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class LarkForwardedAvatarTest {
    @get:Rule val rule = createComposeRule()

    @Test fun anOnlineForwardedSheetReadsTheSendersPicture() {
        val reads = AtomicInteger()
        val avatar = AuthenticatedLarkImage("hub/a:/api/v1/lark/media/forwarded-5696?size=144") { reads.incrementAndGet(); byteArrayOf(1) }
        val part = LarkPart.MergeForward("Chat history", listOf(LarkForwardedMessage("m1", "Alice", avatar, text = "hi")))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                LarkForwardedSheet(part, onlineSince = System.currentTimeMillis(), onDismiss = {})
            }
        }
        rule.waitUntil(5_000) { reads.get() > 0 }
    }
}
