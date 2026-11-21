package persona.core.designsystem

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class SidebarLayoutStoreTest {
    private val context = ApplicationProvider.getApplicationContext<Context>()

    @Test fun freshAndUnknownValuesDefaultToSeparate() {
        val prefs = context.getSharedPreferences("persona-appearance", Context.MODE_PRIVATE)
        assertEquals(SidebarLayout.Separate, AppStyleStore(context).sidebar.value)
        prefs.edit().putString("sidebar-layout", "unknown").commit()
        assertEquals(SidebarLayout.Separate, AppStyleStore(context).sidebar.value)
    }

    @Test fun bothChoicesPublishImmediatelyAndRestoreInANewStore() {
        val store = AppStyleStore(context)
        store.setSidebar(SidebarLayout.Merged)
        assertEquals(SidebarLayout.Merged, store.sidebar.value)
        val reopened = AppStyleStore(context)
        assertEquals(SidebarLayout.Merged, reopened.sidebar.value)
        reopened.setSidebar(SidebarLayout.Separate)
        assertEquals(SidebarLayout.Separate, reopened.sidebar.value)
        assertEquals(SidebarLayout.Separate, AppStyleStore(context).sidebar.value)
    }
}
