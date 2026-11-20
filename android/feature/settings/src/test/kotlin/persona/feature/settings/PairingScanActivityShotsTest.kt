package persona.feature.settings

import android.content.pm.PackageManager
import android.content.res.Configuration
import android.graphics.Bitmap
import android.graphics.Canvas
import android.view.View
import android.widget.TextView
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RuntimeEnvironment
import org.robolectric.android.controller.ActivityController
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.AppStyle
import java.io.File

/** Native scanner content, whose fixed Material theme intentionally does not follow the three Compose styles. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1600dp-xhdpi")
class PairingScanActivityShotsTest {
    private val controllers = mutableListOf<ActivityController<PairingScanActivity>>()

    @After fun destroyScanners() {
        controllers.asReversed().forEach { it.destroy() }
        controllers.clear()
        RuntimeEnvironment.setFontScale(1f)
        RuntimeEnvironment.application.resources.configuration.uiMode = Configuration.UI_MODE_NIGHT_NO
    }

    @Test fun everyStyleInLight() = shots("light")

    @Test fun everyStyleInDark() = shots("dark")

    @Test fun everyStyleWithLargeText() {
        RuntimeEnvironment.setFontScale(1.3f)
        shots("large")
    }

    private fun shots(appearance: String) {
        val configuration = RuntimeEnvironment.application.resources.configuration
        configuration.uiMode = (configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
            if (appearance == "dark") Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
        AppStyle.entries.forEach { style ->
            val activity = Robolectric.buildActivity(PairingScanActivity::class.java).create().start().visible()
            controllers += activity
            val info = activity.get().packageManager.getActivityInfo(activity.get().componentName, PackageManager.GET_ACTIVITIES)
            assertEquals("The native scanner must not change with $style", android.R.style.Theme_Material_NoActionBar, info.theme)
            capture(activity.get(), "pairing-scan-${style.name.lowercase()}-$appearance.png", error = false)
            capture(activity.get(), "pairing-scan-${style.name.lowercase()}-$appearance-error.png", error = true)
        }
    }

    private fun capture(activity: PairingScanActivity, name: String, error: Boolean) {
        activity.findViewById<TextView>(R.id.pairing_scan_photo_error).apply {
            if (error) {
                setText(R.string.settings_pairing_scan_photo_multiple)
                visibility = View.VISIBLE
            }
        }
        val root = activity.window.decorView
        root.measure(View.MeasureSpec.makeMeasureSpec(800, View.MeasureSpec.EXACTLY),
            View.MeasureSpec.makeMeasureSpec(1600, View.MeasureSpec.EXACTLY))
        root.layout(0, 0, 800, 1600)
        val bitmap = Bitmap.createBitmap(800, 1600, Bitmap.Config.ARGB_8888)
        root.draw(Canvas(bitmap))
        val directory = File(System.getProperty("persona.shots") ?: "build/pairing-scan-shots").also { it.mkdirs() }
        File(directory, name).outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
    }
}
