package persona.feature.settings

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.designsystem.AppStyle
import persona.core.designsystem.AppStyleStore

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
@OptIn(ExperimentalCoroutinesApi::class)
class LarkHomePreferenceTest {
    private lateinit var context: Context

    @Before fun setup() {
        context = ApplicationProvider.getApplicationContext()
        context.getSharedPreferences("persona-appearance", Context.MODE_PRIVATE).edit().clear().commit()
    }

    @Test fun messagesAreOffByDefaultWithoutChangingTheInterfaceStyle() {
        context.getSharedPreferences("persona-appearance", Context.MODE_PRIVATE).edit().putString("style", AppStyle.Brief.key).commit()
        val store = AppStyleStore(context)
        assertFalse(store.larkHomeMessages.value)
        assertEquals(AppStyle.Brief, store.style.value)
    }

    @Test fun changesPublishImmediately() = runTest {
        val store = AppStyleStore(context)
        val values = mutableListOf<Boolean>()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { store.larkHomeMessages.collect { values += it } }

        store.setLarkHomeMessages(true)
        assertTrue(store.larkHomeMessages.value)
        store.setLarkHomeMessages(false)
        assertFalse(store.larkHomeMessages.value)
        assertEquals(listOf(false, true, false), values)
    }

    @Test fun recreationRestoresTheChoiceUsingTheIosDeviceKey() {
        val store = AppStyleStore(context)
        store.setStyle(AppStyle.Board)
        store.setLarkHomeMessages(true)

        val restored = AppStyleStore(context)
        assertTrue(restored.larkHomeMessages.value)
        assertEquals(AppStyle.Board, restored.style.value)
        assertTrue(context.getSharedPreferences("persona-appearance", Context.MODE_PRIVATE)
            .getBoolean("integrations.lark.enabled", false))

        restored.setLarkHomeMessages(false)
        assertFalse(AppStyleStore(context).larkHomeMessages.value)
        assertEquals(AppStyle.Board, AppStyleStore(context).style.value)
    }
}
