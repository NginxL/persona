package persona.android.platform

import persona.android.R
import android.app.Application
import android.content.Context
import android.net.Uri
import android.os.Process
import android.provider.OpenableColumns
import androidx.work.*
import dagger.hilt.EntryPoint
import dagger.hilt.InstallIn
import dagger.hilt.android.EntryPointAccessors
import dagger.hilt.components.SingletonComponent
import persona.android.formatSharedContent
import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.MessageVia
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.*
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.Request
import okhttp3.RequestBody.Companion.asRequestBody
import java.io.File
import java.io.IOException
import java.util.UUID
import javax.inject.Inject
import javax.inject.Singleton

@Serializable
internal data class SharedAttachment(val file: String, val name: String, val path: String? = null)
@Serializable
internal data class QueuedShare(
    val id: String, val title: String, val text: String,
    val attachments: List<SharedAttachment> = emptyList(),
    val machine: String? = null, val session: String? = null,
    val note: String = "", val asContext: Boolean = true,
    val queued: Boolean = false, val finalText: String? = null,
)

/** Private, pairing-owned staging persists before the sending UI can be dismissed. */
@Singleton
class ShareQueue @Inject constructor(
    private val application: Application,
    private val sessions: PlatformSessions,
    private val runtime: persona.android.di.ConnectionRuntime,
) {
    /** One per data scope: a group's long upload never holds another group's share sheet (#4818). */
    private val locks = ShareLocks()
    /** Each open group's observer, by data scope: every group drains its own folder (#4818). */
    private val wake = java.util.concurrent.ConcurrentHashMap<String, Channel<Unit>>()

    internal suspend fun stage(id: String, title: String, text: String, uris: List<Uri>): Boolean {
        val owner = sessions.requireSession()
        return owner.io { locks[owner.data.scope.path].withLock {
            val directory = directory(owner, id)
            val entry = File(directory, "entry.json")
            if (entry.exists()) return@withLock read(entry).queued
            check(directory.isDirectory || directory.mkdirs())
            try {
                val attachments = uris.map { uri ->
                    require(uri.scheme == "content")
                    val provider = application.packageManager.resolveContentProvider(requireNotNull(uri.authority), 0)
                    require(provider?.applicationInfo?.uid != Process.myUid()) { application.getString(R.string.app_share_private_file) }
                    var name = application.getString(R.string.app_share_attachment)
                    application.contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)?.use { cursor ->
                        if (cursor.moveToFirst()) {
                            val column = cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                            if (column >= 0) name = cursor.getString(column)?.substringAfterLast('/')?.substringAfterLast('\\')?.takeIf { it.isNotBlank() } ?: name
                            val size = cursor.getColumnIndex(OpenableColumns.SIZE)
                            if (size >= 0 && !cursor.isNull(size)) require(cursor.getLong(size) <= MAX_FILE_BYTES)
                        }
                    }
                    val file = File(directory, UUID.randomUUID().toString() + ".bin")
                    application.contentResolver.openInputStream(uri)?.use { input ->
                        file.outputStream().use { output ->
                            val buffer = ByteArray(DEFAULT_BUFFER_SIZE)
                            var copied = 0L
                            while (true) {
                                currentCoroutineContext().ensureActive()
                                val count = input.read(buffer)
                                if (count < 0) break
                                copied += count
                                require(copied <= MAX_FILE_BYTES) { application.getString(R.string.app_share_too_large) }
                                output.write(buffer, 0, count)
                            }
                            output.fd.sync()
                        }
                    } ?: throw IOException(application.getString(R.string.app_share_unreadable))
                    SharedAttachment(file.name, name)
                }
                write(entry, QueuedShare(id, title, text, attachments))
                false
            } catch (error: Exception) {
                directory.deleteRecursively()
                throw error
            }
        } }
    }

    internal suspend fun submit(id: String, target: SessionKey?, note: String, asContext: Boolean) {
        val owner = sessions.requireSession()
        owner.io { locks[owner.data.scope.path].withLock {
            val file = File(directory(owner, id), "entry.json")
            val draft = read(file)
            if (!draft.queued) write(file, draft.copy(machine = target?.machineId, session = target?.sessionId,
                note = note, asContext = asContext, queued = true))
            // The durable queue remains recoverable even if the scheduler is temporarily unavailable.
            try { schedule(owner) } catch (_: Exception) { }
            wake[owner.data.scope.path]?.trySend(Unit)
        } }
    }

    internal suspend fun discardDraft(id: String) {
        val owner = sessions.requireSession()
        owner.io { locks[owner.data.scope.path].withLock {
            val directory = directory(owner, id)
            val file = File(directory, "entry.json")
            if (file.exists() && !read(file).queued) directory.deleteRecursively()
        } }
    }

    internal suspend fun observe(owner: PlatformSession) = coroutineScope {
        val pending = Channel<Unit>(Channel.CONFLATED)
        val key = owner.data.scope.path
        wake[key] = pending
        pending.trySend(Unit)
        launch { owner.hub.connection.state.collect { if (it == ConnectionState.Online) pending.trySend(Unit) } }
        try {
            for (ignored in pending) {
                val remaining = drain(owner)
                if (remaining && owner.hub.connection.state.value == ConnectionState.Online) {
                    delay(15_000)
                    pending.trySend(Unit)
                }
            }
        } finally {
            wake.remove(key, pending)
            pending.close()
        }
    }

    internal suspend fun drain(owner: PlatformSession): Boolean = owner.io { locks[owner.data.scope.path].withLock {
        val entries = File(owner.directory, "shares").listFiles().orEmpty()
            .map { File(it, "entry.json") }.filter { it.exists() }
        drainQueuedShares(entries, owner::active) { file ->
            deliverQueuedShare(file, owner.hub.connection.state.value == ConnectionState.Online,
                untitled = application.getString(R.string.app_share_content),
                prepareMain = { via -> owner.data.messages.prepareMainConversation(via) },
                upload = { machine, attachment, name, main -> upload(owner, machine, main, attachment, name) },
                main = { body, id, via -> owner.data.messages.sendToMain(body, id, null, via) },
                // A session of another hub group goes to that group (docs/android/android.md "Multiple hubs"); one group: the same outbox.
                agent = { target, body, id, via ->
                    if (persona.core.contracts.GroupKeys.namespaceOf(target.machineId) == null && runtime.groupOf(target.machineId)?.data === owner.data)
                        owner.data.messages.send(target, body, id, null, null, null, via)
                    else runtime.messages.send(target, body, id, null, null, null, via)
                })
        }
    } }

    private suspend fun upload(owner: PlatformSession, machine: String, main: Boolean, file: File, name: String): String {
        owner.active()
        val api = shareUploadApi(main, machine, owner.hub.api) { runtime.groupOf(it)?.hub?.api }
        val url = api.url("/api/v1/machines").newBuilder().addPathSegment(persona.core.contracts.GroupKeys.raw(machine)).addPathSegment("files")
            .addQueryParameter("name", name).build()
        val request = Request.Builder().url(url).header("Authorization", "Bearer ${api.token}")
            .post(file.asRequestBody("application/octet-stream".toMediaType())).build()
        val call = api.http.newCall(request)
        val response = suspendCancellableCoroutine<okhttp3.Response> { continuation ->
            continuation.invokeOnCancellation { call.cancel() }
            call.enqueue(object : okhttp3.Callback {
                override fun onFailure(call: okhttp3.Call, e: IOException) { continuation.resumeWith(Result.failure(e)) }
                override fun onResponse(call: okhttp3.Call, response: okhttp3.Response) {
                    continuation.resume(response, onCancellation = { _, value, _ -> value.close() })
                }
            })
        }
        return response.use {
            check(it.isSuccessful) { application.getString(R.string.app_share_upload_failed) }
            owner.active()
            JsonCodec.parseToJsonElement(it.body?.string().orEmpty()).jsonObject["path"]?.jsonPrimitive?.contentOrNull
                ?.takeIf(String::isNotBlank) ?: throw IOException(application.getString(R.string.app_share_no_path))
        }
    }

    private fun schedule(owner: PlatformSession) {
        val request = OneTimeWorkRequestBuilder<ShareRetryWorker>()
            .setInputData(workDataOf("scope" to owner.data.scope.path))
            .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build()).build()
        WorkManager.getInstance(application).enqueueUniqueWork("shares-${owner.data.scope.path}", ExistingWorkPolicy.APPEND_OR_REPLACE, request)
            .result.get()
    }
    private fun directory(owner: PlatformSession, id: String): File {
        require(id.matches(Regex("[A-Za-z0-9_-]+")))
        return File(owner.directory, "shares/$id")
    }
    private fun read(file: File): QueuedShare = JsonCodec.decodeFromString(file.readText())
    private fun write(file: File, entry: QueuedShare) = writePlatformFile(file, JsonCodec.encodeToString(QueuedShare.serializer(), entry))

    companion object {
        // Protocol upload limit; this is not a measured resource budget.
        private const val MAX_FILE_BYTES = 200L * 1024 * 1024
    }
}

// Attachments go to the hub group of the machine they are for, with its plain id. The main target comes from
// the share owner's own data with an unsuffixed id, which groupOf would map to the primary group (#4816).
internal fun <A> shareUploadApi(main: Boolean, machine: String, owner: A, groupApi: (String) -> A?): A =
    if (main) owner else groupApi(machine) ?: owner

private const val DRAFT_MAX_AGE_MILLIS = 24L * 60 * 60 * 1000

internal class ShareLocks {
    private val locks = java.util.concurrent.ConcurrentHashMap<String, Mutex>()
    operator fun get(scope: String): Mutex = locks.computeIfAbsent(scope) { Mutex() }
}

/** Every entry is tried; one that fails stays queued and the result reports it as remaining. */
internal suspend fun drainQueuedShares(entries: List<File>, active: () -> Unit, deliver: suspend (File) -> Boolean): Boolean {
    var remaining = false
    for (file in entries) {
        try {
            active()
            remaining = deliver(file) || remaining
        } catch (error: CancellationException) {
            // An unselected group's main resolver refuses with "Pairing changed" (PlatformRuntime): the entry waits.
            currentCoroutineContext().ensureActive()
            active()
            remaining = true
        }
        catch (_: Exception) { remaining = true }
    }
    return remaining
}

/** Returns true while the staged entry still needs a connection or a main target. */
internal suspend fun deliverQueuedShare(
    file: File, connected: Boolean,
    untitled: String,
    prepareMain: suspend (String) -> SessionKey?,
    upload: suspend (String, File, String, Boolean) -> String,
    main: suspend (String, String, String) -> Unit,
    agent: suspend (SessionKey, String, String, String) -> Unit,
): Boolean {
    var entry = JsonCodec.decodeFromString<QueuedShare>(file.readText())
    fun save() = writePlatformFile(file, JsonCodec.encodeToString(QueuedShare.serializer(), entry))
    if (!entry.queued) {
        // A sheet that closed without sending (or died first) leaves its copy; drop it after a day.
        if (System.currentTimeMillis() - file.lastModified() > DRAFT_MAX_AGE_MILLIS) file.parentFile!!.deleteRecursively()
        return false
    }
    if (entry.attachments.isNotEmpty() && entry.finalText == null) {
        if (!connected) return true
        val target = entry.machine?.let { SessionKey(it, requireNotNull(entry.session)) } ?: prepareMain(MessageVia.Share) ?: return true
        for (index in entry.attachments.indices) {
            val attachment = entry.attachments[index]
            if (attachment.path != null) continue
            val path = upload(target.machineId, File(file.parentFile, attachment.file), attachment.name, entry.machine == null)
            entry = entry.copy(attachments = entry.attachments.mapIndexed { i, value ->
                if (i == index) value.copy(path = path) else value
            })
            save()
        }
    }
    val text = entry.finalText ?: formatSharedContent(entry.title, entry.text,
        entry.attachments.map { requireNotNull(it.path) }, entry.note, entry.asContext, untitled)
    if (entry.finalText == null) { entry = entry.copy(finalText = text); save() }
    check(text.isNotBlank())
    if (entry.machine == null) main(text, entry.id, MessageVia.Share)
    else agent(SessionKey(requireNotNull(entry.machine), requireNotNull(entry.session)), text, entry.id, MessageVia.Share)
    // Core outbox has committed the complete message, including all uploaded paths.
    check(file.parentFile!!.deleteRecursively())
    return false
}

@EntryPoint
@InstallIn(SingletonComponent::class)
interface ShareRetryDependencies {
    fun platformSessions(): PlatformSessions
    fun shares(): ShareQueue
}

open class ShareRetryWorker(context: Context, parameters: WorkerParameters) : CoroutineWorker(context, parameters) {
    override suspend fun doWork(): Result = try {
        val dependencies = EntryPointAccessors.fromApplication(applicationContext, ShareRetryDependencies::class.java)
        val sessions = dependencies.platformSessions()
        val owner = sessions.requireSession()
        shareRetryResult(inputData.getString("scope"), owner, sessions.open(), { it.data.scope.path }) { dependencies.shares().drain(it) }
    } catch (error: CancellationException) { throw error }
    catch (_: Exception) { Result.retry() }
}

/** The work's own group drains, selected or not (#4818); with no open group of that scope (deleted, closed) it ends. */
internal suspend fun <S> shareRetryResult(scope: String?, selected: S, open: List<S>, scopeOf: (S) -> String,
    drain: suspend (S) -> Boolean): ListenableWorker.Result {
    val owner = if (scopeOf(selected) == scope) selected
        else open.firstOrNull { scopeOf(it) == scope } ?: return ListenableWorker.Result.success()
    return if (drain(owner)) ListenableWorker.Result.retry() else ListenableWorker.Result.success()
}
