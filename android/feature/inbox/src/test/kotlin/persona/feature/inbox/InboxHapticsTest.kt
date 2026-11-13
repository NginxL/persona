package persona.feature.inbox

import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.designsystem.Haptic

class InboxHapticsTest {
    // As iOS InboxStore.setDone(_ item:): one item filed or put back is a light tap, whichever way (a swipe, the Lark chat's button).
    @Test fun oneItemTapsBothWays() {
        assertEquals(Haptic.Tap, doneHaptic(true, single = true))
        assertEquals(Haptic.Tap, doneHaptic(false, single = true))
    }

    // As iOS InboxStore.run: many at once (Mark all as seen, Clear all) buzz success when filed and tap when put back.
    @Test fun manyAtOnceBuzzSuccessWhenFiledAndTapWhenPutBack() {
        assertEquals(Haptic.Success, doneHaptic(true))
        assertEquals(Haptic.Tap, doneHaptic(false))
    }
}
