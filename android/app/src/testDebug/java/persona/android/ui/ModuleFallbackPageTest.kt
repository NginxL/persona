package persona.android.ui

import android.app.Application
import androidx.activity.ComponentActivity
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/**
 * The page the shell shows for a route nobody registered (a module page left open when its module turned inactive,
 * ModuleNavigationSafetyTest): it says so and logs. Debug unit tests only: the release variant has no test activity.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en", application = Application::class)
class ModuleFallbackPageTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private object Orphan : NavKey

    @Test fun anUnregisteredRouteShowsTheUnavailablePage() {
        val logged = mutableListOf<String>()
        val navigator = object : Navigator {
            override fun navigate(key: NavKey) = Unit
            override fun back() = Unit
            override fun selectTab(tab: AppTab) = Unit
        }
        val entry = shellFallbackEntry(Orphan, navigator, logged::add)
        rule.setContent { entry.Content() }
        rule.onNodeWithText("This page isn't available right now").assertExists()
        assertEquals(1, logged.size)
    }
}
