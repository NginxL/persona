package persona.feature.settings

import android.Manifest
import android.app.Activity
import android.app.Application
import android.content.ContentProvider
import android.content.ContentValues
import android.content.Intent
import android.content.pm.PackageManager
import android.database.Cursor
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.net.Uri
import android.os.Bundle
import android.os.Looper
import android.os.ParcelFileDescriptor
import android.provider.MediaStore
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.activity.ComponentActivity
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import com.google.zxing.BarcodeFormat
import com.google.zxing.EncodeHintType
import com.google.zxing.qrcode.QRCodeWriter
import com.journeyapps.barcodescanner.DecoratedBarcodeView
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.Shadows.shadowOf
import org.robolectric.android.controller.ActivityController
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.PairingProgress
import persona.core.contracts.api.PairingRequest
import persona.core.contracts.api.PairingService
import persona.core.contracts.api.PairingStage
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.PersonaTheme
import java.io.ByteArrayOutputStream
import java.io.File
import java.io.FileNotFoundException
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicInteger

/**
 * Choosing a pairing photo is another input to the existing scanner result, not a second pairing flow. The picked
 * images below are real PNGs with generated QR codes, served from a test content provider through the system-picker
 * activity result. No production decoder, resource id or photo-result method is replaced by these tests.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1600dp-xhdpi")
class PairingScanActivityTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private class FakePairing : PairingService {
        override val isPaired = MutableStateFlow(false)
        val requests = mutableListOf<PairingRequest>()
        override suspend fun pair(request: PairingRequest) { requests += request }
        override suspend fun pair(request: PairingRequest, progress: (PairingProgress) -> Unit) {
            requests += request
            progress(PairingProgress(PairingStage.ExchangeCredentials))
        }
        override suspend fun unpair() = Unit
    }

    private val sessions = object : SessionsRepository {
        override val machines = MutableStateFlow(emptyList<Machine>())
        override val sessions = MutableStateFlow(emptyList<Session>())
        override suspend fun refresh() = Unit
    }

    private data class OpenScanner(
        val controller: ActivityController<PairingScanActivity>,
        val parentRequestCode: Int? = null,
    ) {
        val activity: PairingScanActivity get() = controller.get()
    }

    private val controllers = mutableListOf<ActivityController<PairingScanActivity>>()

    @Before fun setUpPhotos() {
        Robolectric.setupContentProvider(PhotoProvider::class.java, PhotoProvider.AUTHORITY)
    }

    @After fun destroyScanners() {
        controllers.asReversed().forEach { it.destroy() }
        controllers.clear()
        PhotoProvider.clear()
    }

    private fun text(id: Int) = rule.activity.getString(id)

    /** Launched by the same Compose ScanContract as a camera scan; camera permission remains that caller's concern. */
    private fun openScanner(pairing: FakePairing): OpenScanner {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                PairingScreen(pairing, sessions, onReady = {}, onDiagnostics = {}, skipWelcome = true)
            }
        }
        rule.onNodeWithText(text(R.string.settings_password_login_title)).assertExists()
        rule.onNode(hasText(text(R.string.settings_password_login_pairing)) and hasClickAction()).performClick()
        rule.waitForIdle()
        shadowOf(ApplicationProvider.getApplicationContext<Application>()).grantPermissions(Manifest.permission.CAMERA)
        rule.onNode(hasText(text(R.string.settings_pairing_scan)) and hasClickAction()).performClick()
        rule.waitForIdle()
        val launched = shadowOf(rule.activity).nextStartedActivityForResult
        assertNotNull("Scan to pair must open PairingScanActivity for a result", launched)
        assertEquals(PairingScanActivity::class.java.name, launched.intent.component?.className)
        return openScanner(launched.intent, launched.requestCode)
    }

    private fun openScanner(): OpenScanner {
        val intent = ScanOptions().setCaptureActivity(PairingScanActivity::class.java)
            .setOrientationLocked(false).setDesiredBarcodeFormats(ScanOptions.QR_CODE)
            .setPrompt("").setBeepEnabled(false).createScanIntent(rule.activity)
        return openScanner(intent)
    }

    private fun openScanner(intent: Intent, parentRequestCode: Int? = null): OpenScanner {
        // visible() lays out the real views without onResume(), so Robolectric never opens camera hardware.
        val controller = Robolectric.buildActivity(PairingScanActivity::class.java, intent).create().start().visible()
        controllers += controller
        return OpenScanner(controller, parentRequestCode)
    }

    private fun allViews(root: View): List<View> = buildList {
        fun visit(view: View) {
            add(view)
            if (view is ViewGroup) for (index in 0 until view.childCount) visit(view.getChildAt(index))
        }
        visit(root)
    }

    private fun views(scanner: OpenScanner) = allViews(scanner.activity.window.decorView)

    private fun chooseButton(scanner: OpenScanner): TextView {
        val existingControls = setOf(
            R.id.pairing_scan_close,
            R.id.pairing_scan_close_space,
            R.id.pairing_scan_manual,
        )
        val candidates = views(scanner).filterIsInstance<TextView>().filter {
            it.isShown && it.hasOnClickListeners() && it.text.isNotBlank() && it.id !in existingControls
        }
        assertEquals("The scan screen must offer one Choose from Photos action", 1, candidates.size)
        return candidates.single()
    }

    private fun choosePhoto(scanner: OpenScanner): ShadowActivityIntent {
        chooseButton(scanner).performClick()
        shadowOf(Looper.getMainLooper()).idle()
        val launched = shadowOf(scanner.activity).nextStartedActivityForResult
        assertNotNull("Choose from Photos must launch a picker for a result", launched)
        assertTrue(
            "The action must use Android's photo picker, not request the whole photo library: ${launched.intent.action}",
            launched.intent.action in setOf(MediaStore.ACTION_PICK_IMAGES, SYSTEM_FALLBACK_PICK_IMAGES),
        )
        assertEquals("image/*", launched.intent.type)
        return ShadowActivityIntent(launched.intent)
    }

    /** A tiny type keeps the helper independent of Robolectric's IntentForResult implementation details. */
    private data class ShadowActivityIntent(val intent: Intent)

    private fun pick(scanner: OpenScanner, picker: ShadowActivityIntent, uri: Uri?) {
        val result = uri?.let { Intent().setData(it) }
        rule.runOnUiThread {
            shadowOf(scanner.activity).receiveResult(picker.intent, if (uri == null) Activity.RESULT_CANCELED else Activity.RESULT_OK, result)
        }
        shadowOf(Looper.getMainLooper()).idle()
    }

    private fun visibleTexts(scanner: OpenScanner) = views(scanner).filterIsInstance<TextView>()
        .filter { it.isShown }.map { it.text.toString() }.toSet()

    private fun assertRejectedPhoto(scanner: OpenScanner, pairing: FakePairing, image: Uri, errorCopy: Int) {
        pick(scanner, choosePhoto(scanner), image)
        val expected = text(errorCopy)
        rule.waitUntil(5_000) {
            shadowOf(Looper.getMainLooper()).idle()
            expected in visibleTexts(scanner) && chooseButton(scanner).isEnabled
        }
        assertTrue("The specific photo error must replace the progress copy: ${visibleTexts(scanner)}",
            expected in visibleTexts(scanner))

        // No ScanContract result means PairingScreen.accept cannot create confirmation, failure or pairing state.
        assertFalse("An unusable photo must leave the user on the scan screen", scanner.activity.isFinishing)
        assertNull("An unusable photo must not produce a scan result", shadowOf(scanner.activity).resultIntent)
        assertTrue("No unusable photo may invoke pairing", pairing.requests.isEmpty())
        rule.onAllNodesWithText(text(R.string.settings_pairing_confirm)).assertCountEquals(0)
        rule.onAllNodesWithText(text(R.string.settings_pairing_failed_title)).assertCountEquals(0)
        rule.onNodeWithText(text(R.string.settings_pairing_scan)).assertExists()

        // The existing camera remains in place and the photo action can immediately be tried again.
        assertNotNull(scanner.activity.findViewById<DecoratedBarcodeView>(R.id.zxing_barcode_scanner))
        choosePhoto(scanner)
    }

    @Test fun validSingleQrPhotoUsesTheExistingConfirmationAndPairingPathExactlyOnce() {
        val payload = "persona://pair?url=https%3A%2F%2Fhub.example.com%3A18760" +
            "&token=photo%2Btoken-123&hub=photo_hub"
        val pairing = FakePairing()
        val scanner = openScanner(pairing)
        pick(scanner, choosePhoto(scanner), PhotoProvider.put(qrPhoto(payload)))

        rule.waitUntil(5_000) {
            shadowOf(Looper.getMainLooper()).idle()
            scanner.activity.isFinishing
        }
        val shadow = shadowOf(scanner.activity)
        val result = ScanContract().parseResult(shadow.resultCode, shadow.resultIntent)
        assertEquals("The picked QR payload must reach ScanContract unchanged", payload, result.contents)
        assertEquals(BarcodeFormat.QR_CODE.name, result.formatName)

        rule.runOnUiThread {
            rule.activity.activityResultRegistry.dispatchResult(
                requireNotNull(scanner.parentRequestCode), shadow.resultCode, shadow.resultIntent,
            )
        }
        rule.waitForIdle()
        rule.onAllNodesWithText(text(R.string.settings_pairing_confirm)).onFirst().assertExists()
        rule.onAllNodesWithText("https://hub.example.com:18760").onFirst().assertExists()
        assertTrue("Decoding is not consent: confirmation must still be required", pairing.requests.isEmpty())

        rule.onNode(hasText(text(R.string.settings_pairing_confirm)) and hasClickAction()).performClick()
        rule.waitUntil(5_000) {
            shadowOf(Looper.getMainLooper()).idle()
            pairing.requests.isNotEmpty()
        }
        rule.waitForIdle()
        assertEquals("Exactly one photo result may produce exactly one pairing request", 1, pairing.requests.size)
        assertEquals(PairingInput.parse(payload), pairing.requests.single())
    }

    @Test fun photoWithoutAQrShowsAnErrorAndKeepsBothScanningChoicesAvailable() {
        val pairing = FakePairing()
        val scanner = openScanner(pairing)
        val blank = Bitmap.createBitmap(640, 640, Bitmap.Config.ARGB_8888).apply { eraseColor(Color.LTGRAY) }
        assertRejectedPhoto(scanner, pairing, PhotoProvider.put(blank.png()), R.string.settings_pairing_scan_photo_none)
    }

    @Test fun photoWithMultipleQrsIsNotAnAmbiguousPairingAndKeepsScanningAvailable() {
        val pairing = FakePairing()
        val scanner = openScanner(pairing)
        val first = qrBitmap("first-photo-qr", 480)
        val second = qrBitmap("second-photo-qr", 480)
        val multiple = Bitmap.createBitmap(1040, 480, Bitmap.Config.ARGB_8888)
        Canvas(multiple).apply {
            drawColor(Color.WHITE)
            drawBitmap(first, 0f, 0f, null)
            drawBitmap(second, 560f, 0f, null)
        }
        assertRejectedPhoto(scanner, pairing, PhotoProvider.put(multiple.png()), R.string.settings_pairing_scan_photo_multiple)
    }

    @Test fun unreadablePhotoShowsAnErrorWithoutPairingOrClosingTheScanner() {
        val pairing = FakePairing()
        val scanner = openScanner(pairing)
        val unreadable = PhotoProvider.put("this is not an encoded image".encodeToByteArray())
        assertRejectedPhoto(scanner, pairing, unreadable, R.string.settings_pairing_scan_photo_unreadable)
    }

    @Test fun photoPickerNeedsNeitherPhotoLibraryNorCameraPermission() {
        val app = ApplicationProvider.getApplicationContext<Application>()
        shadowOf(app).denyPermissions(
            Manifest.permission.CAMERA,
            Manifest.permission.READ_EXTERNAL_STORAGE,
            Manifest.permission.READ_MEDIA_IMAGES,
        )
        val scanner = openScanner()
        assertEquals(PackageManager.PERMISSION_DENIED, scanner.activity.checkSelfPermission(Manifest.permission.READ_MEDIA_IMAGES))
        choosePhoto(scanner)
        assertNull("The system photo picker must not trigger a runtime permission request",
            shadowOf(scanner.activity).lastRequestedPermission)
        assertFalse(scanner.activity.isFinishing)
    }

    @Test fun existingCameraScannerAndManualAndCloseResultsStayInPlace() {
        val manual = openScanner()
        assertNotNull(manual.activity.findViewById<DecoratedBarcodeView>(R.id.zxing_barcode_scanner))
        manual.activity.findViewById<View>(R.id.pairing_scan_manual).performClick()
        assertTrue(manual.activity.isFinishing)
        assertEquals(Activity.RESULT_CANCELED, shadowOf(manual.activity).resultCode)
        assertTrue(shadowOf(manual.activity).resultIntent.getBooleanExtra(PairingScanActivity.ManualInput, false))

        val closed = openScanner()
        closed.activity.findViewById<View>(R.id.pairing_scan_close).performClick()
        assertTrue(closed.activity.isFinishing)
        assertNull(shadowOf(closed.activity).resultIntent)
    }

    @Test fun cameraDenialKeepsPhotoSelectionAvailable() {
        val scanner = openScanner()
        scanner.activity.onRequestPermissionsResult(
            com.journeyapps.barcodescanner.CaptureManager.getCameraPermissionReqCode(),
            arrayOf(Manifest.permission.CAMERA), intArrayOf(PackageManager.PERMISSION_DENIED),
        )
        assertFalse("Pairing must still offer a photo after camera denial", scanner.activity.isFinishing)
        choosePhoto(scanner)
    }

    @Test fun cameraFailureKeepsPhotoSelectionAvailable() {
        val app = ApplicationProvider.getApplicationContext<Application>()
        shadowOf(app).grantPermissions(Manifest.permission.CAMERA)
        val scanner = openScanner()
        scanner.controller.resume()
        shadowOf(Looper.getMainLooper()).idle()
        assertFalse("An unusable camera must not close pairing's photo path", scanner.activity.isFinishing)
        assertTrue("The camera failure must leave a usable explanation: ${visibleTexts(scanner)}",
            visibleTexts(scanner).any { it.contains("Camera is unavailable") })
        choosePhoto(scanner)
    }

    @Test fun browserSignInKeepsItsCameraOnlyScanner() {
        val intent = ScanOptions().setCaptureActivity(PairingScanActivity::class.java)
            .addExtra(PairingScanActivity.DisablePhoto, true)
            .setOrientationLocked(false).setDesiredBarcodeFormats(ScanOptions.QR_CODE)
            .setPrompt("").setBeepEnabled(false).createScanIntent(rule.activity)
        val scanner = openScanner(intent)
        assertEquals(View.GONE, scanner.activity.findViewById<View>(R.id.pairing_scan_photo).visibility)
        assertNotNull(scanner.activity.findViewById<DecoratedBarcodeView>(R.id.zxing_barcode_scanner))
        scanner.activity.onRequestPermissionsResult(
            com.journeyapps.barcodescanner.CaptureManager.getCameraPermissionReqCode(),
            arrayOf(Manifest.permission.CAMERA), intArrayOf(PackageManager.PERMISSION_DENIED),
        )
        assertEquals(Activity.RESULT_CANCELED, shadowOf(scanner.activity).resultCode)
        assertTrue("Browser sign-in keeps the library's original denial result",
            shadowOf(scanner.activity).resultIntent.getBooleanExtra(com.google.zxing.client.android.Intents.Scan.MISSING_CAMERA_PERMISSION, false))
    }

    @Config(qualifiers = "zh-rCN-w400dp-h1600dp-xhdpi")
    @Test fun chooseFromPhotosUsesSimplifiedChineseCopyAndOpensThePicker() {
        val scanner = openScanner()
        assertEquals("zh", scanner.activity.resources.configuration.locales[0].language)
        val copy = chooseButton(scanner).text.toString()
        assertFalse("Simplified Chinese must not fall back to English", copy == ENGLISH_CHOOSE_PHOTOS)
        assertTrue(copy.isNotBlank())
        choosePhoto(scanner)
    }

    @Config(qualifiers = "ja-w400dp-h1600dp-xhdpi")
    @Test fun chooseFromPhotosUsesJapaneseCopyAndOpensThePicker() {
        val scanner = openScanner()
        assertEquals("ja", scanner.activity.resources.configuration.locales[0].language)
        val copy = chooseButton(scanner).text.toString()
        assertFalse("Japanese must not fall back to English", copy == ENGLISH_CHOOSE_PHOTOS)
        assertTrue(copy.isNotBlank())
        choosePhoto(scanner)
    }

    @Test fun chooseFromPhotosHasTheExpectedEnglishCopy() {
        val scanner = openScanner()
        assertEquals(ENGLISH_CHOOSE_PHOTOS, chooseButton(scanner).text.toString())
        choosePhoto(scanner)
    }

    private fun qrPhoto(payload: String) = qrBitmap(payload, 640).png()

    private fun qrBitmap(payload: String, size: Int): Bitmap {
        val matrix = QRCodeWriter().encode(payload, BarcodeFormat.QR_CODE, size, size,
            mapOf(EncodeHintType.MARGIN to 4))
        return Bitmap.createBitmap(size, size, Bitmap.Config.ARGB_8888).apply {
            for (y in 0 until size) for (x in 0 until size) {
                setPixel(x, y, if (matrix[x, y]) Color.BLACK else Color.WHITE)
            }
        }
    }

    private fun Bitmap.png(): ByteArray = ByteArrayOutputStream().also { output ->
        assertTrue(compress(Bitmap.CompressFormat.PNG, 100, output))
    }.toByteArray()

    /** Content URIs behave like a real picker result; no file-storage or photo-library permission is involved. */
    class PhotoProvider : ContentProvider() {
        override fun onCreate() = true
        override fun getType(uri: Uri) = "image/png"
        override fun query(uri: Uri, projection: Array<out String>?, selection: String?, selectionArgs: Array<out String>?, sortOrder: String?): Cursor? = null
        override fun insert(uri: Uri, values: ContentValues?): Uri? = null
        override fun delete(uri: Uri, selection: String?, selectionArgs: Array<out String>?) = 0
        override fun update(uri: Uri, values: ContentValues?, selection: String?, selectionArgs: Array<out String>?) = 0

        override fun openFile(uri: Uri, mode: String): ParcelFileDescriptor {
            val bytes = photos[uri.lastPathSegment] ?: throw FileNotFoundException(uri.toString())
            val file = File.createTempFile("pairing-photo-", ".png", context!!.cacheDir)
            file.writeBytes(bytes)
            files += file
            return ParcelFileDescriptor.open(file, ParcelFileDescriptor.MODE_READ_ONLY)
        }

        companion object {
            const val AUTHORITY = "persona.feature.settings.test.photos"
            private val next = AtomicInteger()
            private val photos = ConcurrentHashMap<String, ByteArray>()
            private val files = mutableListOf<File>()

            fun put(bytes: ByteArray): Uri {
                val id = next.incrementAndGet().toString()
                photos[id] = bytes
                return Uri.parse("content://$AUTHORITY/$id")
            }

            fun clear() {
                photos.clear()
                files.forEach(File::delete)
                files.clear()
            }
        }
    }

    companion object {
        private const val ENGLISH_CHOOSE_PHOTOS = "Choose from Photos"
        private const val SYSTEM_FALLBACK_PICK_IMAGES = "android.provider.action.SYSTEM_FALLBACK_PICK_IMAGES"
    }
}
