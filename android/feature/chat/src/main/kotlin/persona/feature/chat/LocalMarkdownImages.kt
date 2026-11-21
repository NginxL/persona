package persona.feature.chat

import android.content.res.Resources
import android.graphics.Bitmap
import android.util.LruCache
import androidx.compose.runtime.mutableStateMapOf
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import persona.core.contracts.api.SessionFiles
import persona.core.contracts.api.hubCodedFailure
import persona.core.designsystem.decodePicture
import java.io.File
import java.security.MessageDigest
import java.util.Base64

internal data class LocalImageFailure(val attempt: Int, val reason: String)

/**
 * Loads and caches local-path Markdown images from the session's machine (#12288, aligned with iOS PR #12102).
 *
 * Uses `SessionFiles.download` (with fallback to `SessionFiles.file` for transports without streaming download),
 * backed by bounded memory and disk caches keyed by `(hubScope, machineId, cwd, resolvedPath)`. All I/O and
 * bitmap decoding run off the main thread. Failure and retry state is shared across tile recompositions so
 * scrolling away and back never automatically refetches a failed image until the user explicitly taps Retry.
 */
internal class LocalMarkdownImages(
    private val files: SessionFiles,
    private val hubScope: String,
    private val machineId: String,
    cwd: String,
    private val cacheDir: File? = null,
    private val decode: (ByteArray, Int) -> Bitmap = { bytes, size -> decodePicture(bytes, size) },
) {
    val effectiveCwd: String = cwd.ifBlank { "~" }

    private val attempts = mutableStateMapOf<String, Int>()
    private val failures = mutableStateMapOf<String, LocalImageFailure>()

    fun resolvePath(rawPath: String): String = LocalMarkdownPath.resolve(rawPath, effectiveCwd)

    fun cacheKey(rawPath: String): String =
        "$hubScope\u0000$machineId\u0000$effectiveCwd\u0000${resolvePath(rawPath)}"

    fun attemptOf(rawPath: String): Int = attempts[cacheKey(rawPath)] ?: 0

    fun failureOf(rawPath: String): LocalImageFailure? {
        val key = cacheKey(rawPath)
        val recorded = failures[key] ?: return null
        return recorded.takeIf { it.attempt == (attempts[key] ?: 0) }
    }

    fun recordFailure(rawPath: String, attempt: Int, reason: String) {
        val key = cacheKey(rawPath)
        if ((attempts[key] ?: 0) == attempt) {
            failures[key] = LocalImageFailure(attempt, reason)
        }
    }

    fun clearFailure(rawPath: String) {
        failures.remove(cacheKey(rawPath))
    }

    fun retry(rawPath: String): Int {
        val key = cacheKey(rawPath)
        val next = (attempts[key] ?: 0) + 1
        failures.remove(key)
        attempts[key] = next
        evictMemory(key)
        return next
    }

    fun cachedThumbnail(rawPath: String, size: Int): Bitmap? =
        thumbnailCache.get("${cacheKey(rawPath)}\u0000$size")

    suspend fun bytes(rawPath: String, forceRefresh: Boolean = false): ByteArray {
        val resolved = resolvePath(rawPath)
        val key = "$hubScope\u0000$machineId\u0000$effectiveCwd\u0000$resolved"
        if (!forceRefresh) {
            bytesCache.get(key)?.let { return it }
            readDiskCache(key)?.let { disk ->
                bytesCache.put(key, disk)
                return disk
            }
        }
        val fetched = withContext(Dispatchers.IO) { fetchBytes(resolved, key) }
        check(fetched.isNotEmpty()) { "empty image" }
        bytesCache.put(key, fetched)
        return fetched
    }

    suspend fun thumbnail(rawPath: String, size: Int, attempt: Int): Bitmap {
        val key = cacheKey(rawPath)
        val thumbKey = "$key\u0000$size"
        if (attempt == 0) {
            thumbnailCache.get(thumbKey)?.let { return it }
        }
        val raw = bytes(rawPath, forceRefresh = attempt > 0)
        val bitmap = withContext(Dispatchers.Default) {
            try {
                decode(raw, size)
            } catch (e: CancellationException) {
                throw e
            } catch (t: Throwable) {
                throw LocalImageDecodeException(t)
            }
        }
        thumbnailCache.put(thumbKey, bitmap)
        return bitmap
    }

    private suspend fun fetchBytes(resolved: String, key: String): ByteArray {
        val dir = cacheDir?.let { File(it, DISK_DIR).apply { mkdirs() } }
        val target = dir?.let { File(it, sha256(key)) }
        val temp = if (dir != null) File(dir, "${sha256(key)}.part-${System.nanoTime()}")
        else File.createTempFile("persona-local-img-", ".tmp")
        try {
            return try {
                files.download(machineId, effectiveCwd, resolved, temp)
                val loaded = temp.readBytes()
                check(loaded.isNotEmpty()) { "empty image" }
                if (target != null) {
                    if (!temp.renameTo(target)) target.writeBytes(loaded)
                    target.setLastModified(System.currentTimeMillis())
                    trimDiskCache(dir, target)
                }
                loaded
            } catch (_: UnsupportedOperationException) {
                val file = files.file(machineId, effectiveCwd, resolved)
                val data = file.data
                if (file.isDir || file.truncated || data.isNullOrEmpty() || !file.mime.orEmpty().startsWith("image/")) {
                    throw LocalImageDecodeException()
                }
                val decoded = withContext(Dispatchers.Default) { Base64.getMimeDecoder().decode(data) }
                if (target != null && decoded.isNotEmpty()) {
                    runCatching {
                        target.writeBytes(decoded)
                        target.setLastModified(System.currentTimeMillis())
                        trimDiskCache(dir, target)
                    }
                }
                decoded
            }
        } finally {
            if (temp.exists()) temp.delete()
        }
    }

    private fun readDiskCache(key: String): ByteArray? {
        val dir = cacheDir?.let { File(it, DISK_DIR) } ?: return null
        val file = File(dir, sha256(key))
        if (!file.isFile || file.length() <= 0L) return null
        return runCatching {
            file.setLastModified(System.currentTimeMillis())
            file.readBytes()
        }.getOrNull()
    }

    private fun trimDiskCache(dir: File, keep: File) {
        val files = dir.listFiles()?.filter { it.isFile && it != keep && !it.name.contains(".part-") } ?: return
        if (files.size < MAX_DISK_FILES) return
        files.sortedByDescending { it.lastModified() }.drop(MAX_DISK_FILES - 1).forEach { it.delete() }
    }

    private fun evictMemory(key: String) {
        bytesCache.remove(key)
        val prefix = "$key\u0000"
        for (k in thumbnailCache.snapshot().keys) {
            if (k.startsWith(prefix)) thumbnailCache.remove(k)
        }
        cacheDir?.let { File(File(it, DISK_DIR), sha256(key)).delete() }
    }

    companion object {
        private const val DISK_DIR = "local-md-images"
        private const val MAX_DISK_FILES = 32
        private const val MAX_THUMBNAIL_BYTES = 16 * 1024 * 1024
        private const val MAX_RAW_BYTES = 8 * 1024 * 1024

        private val thumbnailCache = object : LruCache<String, Bitmap>(MAX_THUMBNAIL_BYTES) {
            override fun sizeOf(key: String, value: Bitmap): Int = value.allocationByteCount.coerceAtLeast(1)
        }

        private val bytesCache = object : LruCache<String, ByteArray>(MAX_RAW_BYTES) {
            override fun sizeOf(key: String, value: ByteArray): Int = value.size.coerceAtLeast(1)
        }

        fun clearCaches() {
            thumbnailCache.evictAll()
            bytesCache.evictAll()
        }

        fun failureReason(resources: Resources, error: Throwable): String {
            if (error is LocalImageDecodeException) return resources.getString(R.string.chat_local_image_unreadable)
            val failure = hubCodedFailure(error)
            return when {
                failure?.status == 404 -> resources.getString(R.string.chat_local_image_not_found)
                failure?.status == 403 -> resources.getString(R.string.chat_local_image_denied)
                !error.message.isNullOrBlank() && error !is IllegalStateException -> error.message!!
                else -> resources.getString(R.string.chat_local_image_load_failed)
            }
        }

        private fun sha256(value: String): String =
            MessageDigest.getInstance("SHA-256").digest(value.toByteArray()).joinToString("") { "%02x".format(it) }
    }
}

internal class LocalImageDecodeException(cause: Throwable? = null) : RuntimeException("unreadable image", cause)
