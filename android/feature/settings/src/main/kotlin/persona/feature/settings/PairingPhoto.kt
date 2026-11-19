package persona.feature.settings

import android.content.ContentResolver
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.net.Uri
import com.google.zxing.BinaryBitmap
import com.google.zxing.DecodeHintType
import com.google.zxing.NotFoundException
import com.google.zxing.RGBLuminanceSource
import com.google.zxing.ReaderException
import com.google.zxing.common.HybridBinarizer
import com.google.zxing.multi.qrcode.QRCodeMultiReader

internal sealed interface PairingPhotoResult {
    data class Code(val payload: String) : PairingPhotoResult
    data class Failure(val error: PairingPhotoError) : PairingPhotoResult
}

internal enum class PairingPhotoError { Unreadable, NoQR, MultipleQR }

/** Reads a picked image with the same ZXing QR engine as the camera, without a photo-library permission. */
internal fun readPairingPhoto(resolver: ContentResolver, uri: Uri): PairingPhotoResult {
    val bitmap: Bitmap = try {
        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        val boundsStream = resolver.openInputStream(uri) ?: return PairingPhotoResult.Failure(PairingPhotoError.Unreadable)
        boundsStream.use { BitmapFactory.decodeStream(it, null, bounds) }
        if (bounds.outWidth <= 0 || bounds.outHeight <= 0) return PairingPhotoResult.Failure(PairingPhotoError.Unreadable)
        val options = BitmapFactory.Options().apply {
            var sample = 1
            while (bounds.outWidth / sample > PhotoMaxDimension || bounds.outHeight / sample > PhotoMaxDimension) sample *= 2
            inSampleSize = sample
            inPreferredConfig = Bitmap.Config.ARGB_8888
        }
        val decoded = resolver.openInputStream(uri)?.use { BitmapFactory.decodeStream(it, null, options) }
        decoded ?: return PairingPhotoResult.Failure(PairingPhotoError.Unreadable)
    } catch (_: Exception) {
        return PairingPhotoResult.Failure(PairingPhotoError.Unreadable)
    }
    try {
        val pixels = IntArray(bitmap.width * bitmap.height)
        bitmap.getPixels(pixels, 0, bitmap.width, 0, 0, bitmap.width, bitmap.height)
        val image = BinaryBitmap(HybridBinarizer(RGBLuminanceSource(bitmap.width, bitmap.height, pixels)))
        val results = QRCodeMultiReader().decodeMultiple(image, mapOf(DecodeHintType.TRY_HARDER to true))
        return when (results.size) {
            0 -> PairingPhotoResult.Failure(PairingPhotoError.NoQR)
            1 -> PairingPhotoResult.Code(results.single().text)
            else -> PairingPhotoResult.Failure(PairingPhotoError.MultipleQR)
        }
    } catch (_: NotFoundException) {
        return PairingPhotoResult.Failure(PairingPhotoError.NoQR)
    } catch (_: ReaderException) {
        return PairingPhotoResult.Failure(PairingPhotoError.Unreadable)
    } catch (_: Exception) {
        return PairingPhotoResult.Failure(PairingPhotoError.Unreadable)
    } finally {
        bitmap.recycle()
    }
}

private const val PhotoMaxDimension = 4096
