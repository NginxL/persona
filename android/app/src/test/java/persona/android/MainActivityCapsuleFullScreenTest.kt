package persona.android

import android.content.Intent
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import persona.android.platform.LockScreenTests
import persona.core.navigation.NotificationRoute

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class MainActivityCapsuleFullScreenTest {
    @Test fun notificationAndDeepLinkIntentsLeaveCapsuleFullScreenOnColdAndWarmLaunch() {
        val notification = Intent(Intent.ACTION_VIEW, NotificationRoute.conversation("machine", "sched-1", assistant = true).uri())
        assertTrue(leavesCapsuleFullScreen(notification))

        val sessionExtra = Intent().putExtra(MainActivity.EXTRA_SESSION, "machine/sched-1")
        assertTrue(leavesCapsuleFullScreen(sessionExtra))

        val capsuleUrl = Intent().putExtra(MainActivity.EXTRA_CAPSULE_URL, "https://example.com/docs")
        assertTrue(leavesCapsuleFullScreen(capsuleUrl))

        val lockTest = Intent().putExtra(LockScreenTests.EXTRA_LOCK_TEST, true)
        assertTrue(leavesCapsuleFullScreen(lockTest))

        val debugTab = Intent().putExtra("debug.openTab", "Persona")
        assertTrue(leavesCapsuleFullScreen(debugTab))

        val debugAdapter = Intent()
        assertTrue(leavesCapsuleFullScreen(debugAdapter, hasLaunchInput = true))
    }

    @Test fun plainExpandInAppAndLauncherIntentsDoNotClearCapsuleFullScreen() {
        assertFalse(leavesCapsuleFullScreen(null))
        assertFalse(leavesCapsuleFullScreen(Intent()))
        assertFalse(leavesCapsuleFullScreen(Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER)))
    }
}
