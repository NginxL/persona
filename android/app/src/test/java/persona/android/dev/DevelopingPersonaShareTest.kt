package persona.android.dev

import android.app.Application
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.Uri
import androidx.test.core.app.ApplicationProvider
import java.io.File
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf

@RunWith(RobolectricTestRunner::class)
class DevelopingPersonaShareTest {
    private val context: Context = ApplicationProvider.getApplicationContext()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private lateinit var dev: DevelopingPersona
    private lateinit var archive: File

    @Before fun setUp() {
        dev = DevelopingPersona(context, scope, observeSystem = false)
        archive = File(File(context.cacheDir, "dev-exports").apply { mkdirs() }, "persona-diagnostics-android-20261117-1200.zip")
        archive.writeBytes(byteArrayOf(1, 2, 3))
    }

    @After fun tearDown() {
        dev.close()
        archive.delete()
    }

    @Suppress("DEPRECATION")
    @Test fun systemShareGrantsOnlyTheZipUriForReading() {
        dev.share(context, archive)
        val chooser = requireNotNull(shadowOf(context as Application).nextStartedActivity)
        assertEquals(Intent.ACTION_CHOOSER, chooser.action)
        val send = requireNotNull(chooser.getParcelableExtra<Intent>(Intent.EXTRA_INTENT))
        assertEquals(Intent.ACTION_SEND, send.action)
        assertEquals("application/zip", send.type)
        assertTrue(send.flags and Intent.FLAG_GRANT_READ_URI_PERMISSION != 0)
        assertFalse(send.flags and Intent.FLAG_GRANT_WRITE_URI_PERMISSION != 0)
        val uri = requireNotNull(send.getParcelableExtra<Uri>(Intent.EXTRA_STREAM))
        assertEquals("content", uri.scheme)
        assertEquals("${context.packageName}.dev.files", uri.authority)
        assertEquals(uri, send.clipData?.getItemAt(0)?.uri)
        assertEquals(archive.name, send.getStringExtra(Intent.EXTRA_SUBJECT))
    }

    @Test fun agentHandoffOpensOnlyTheInternalPickerWithTheArchivePath() {
        dev.sendToAgent(context, archive)
        val opened = shadowOf(context as Application).nextStartedActivity
        assertEquals(ComponentName(context, DiagnosticsShareActivity::class.java), opened.component)
        assertEquals(archive.absolutePath, opened.getStringExtra(DiagnosticsShareActivity.EXTRA_FILE))
    }

    @Test fun filesOutsideTheExportDirectoryCannotBeShared() {
        val outside = File.createTempFile("not-a-diagnostics-export", ".zip").apply { writeBytes(byteArrayOf(4)) }
        try {
            assertThrows(IllegalArgumentException::class.java) { dev.share(context, outside) }
            assertThrows(IllegalArgumentException::class.java) { dev.sendToAgent(context, outside) }
        } finally {
            outside.delete()
        }
    }

    @Test fun theProviderAndAgentPickerAreNotExported() {
        val manager = context.packageManager
        val provider = manager.getProviderInfo(ComponentName(context, DevShareProvider::class.java), 0)
        val activity = manager.getActivityInfo(ComponentName(context, DiagnosticsShareActivity::class.java), 0)
        assertFalse(provider.exported)
        assertTrue(provider.grantUriPermissions)
        assertFalse(activity.exported)
    }
}
