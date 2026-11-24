package persona.feature.chat

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.contracts.api.StarredConversation
import persona.core.contracts.api.StarredMessagesApi
import persona.core.contracts.api.StarredSnapshot
import persona.core.contracts.api.StarredSource
import persona.core.contracts.api.hubCodedFailure
import persona.core.designsystem.JIcon
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons

enum class StarredLoadState { Idle, Loading, Ready, Unsupported, Failed }

enum class StarredMark { Saved, Saving }

enum class StarredOriginal { Verified, Unavailable, Failed, Retired }

data class StarredMessagesState(
    val loadState: StarredLoadState = StarredLoadState.Idle,
    val count: Int? = null,
    val canWrite: Boolean = false,
    val snapshots: List<StarredSnapshot> = emptyList(),
    val refs: Map<StarredSource, String> = emptyMap(),
    val pending: Set<StarredSource> = emptySet(),
    val refreshing: Boolean = false,
) {
    fun mark(source: StarredSource): StarredMark? = when {
        source in pending -> StarredMark.Saving
        source in refs -> StarredMark.Saved
        else -> null
    }

    fun isPending(source: StarredSource): Boolean = source in pending

    fun referenceId(source: StarredSource): String? = refs[source]
}

/**
 * One assistant's starred messages on one hub (matching iOS `StarredStore.swift`).
 * Call from the UI dispatcher and replace the store when the hub, assistant, or feature availability changes.
 */
class StarredMessagesStore(
    private val api: StarredMessagesApi?,
    val offered: Boolean = api != null,
) {
    private val mutable = MutableStateFlow(
        StarredMessagesState(loadState = if (offered && api != null) StarredLoadState.Idle else StarredLoadState.Unsupported),
    )
    val state = mutable.asStateFlow()

    private var indexRead = 0L
    private var pageRead = 0L
    private var readers = 0

    suspend fun refresh() {
        if (!offered || api == null) {
            clearUnavailable()
            return
        }
        loadIndex()
        if (readers > 0) loadPage()
    }

    suspend fun startReading() {
        readers++
        loadPage()
    }

    fun stopReading() {
        readers = (readers - 1).coerceAtLeast(0)
        if (readers > 0) return
        pageRead++
        mutable.value = state.value.copy(
            snapshots = emptyList(),
            loadState = if (offered && api != null) StarredLoadState.Idle else StarredLoadState.Unsupported,
            refreshing = false,
        )
    }

    suspend fun loadIndex() {
        if (!offered || api == null) {
            clearUnavailable()
            return
        }
        val read = ++indexRead
        try {
            val result = api.index()
            currentCoroutineContext().ensureActive()
            if (read != indexRead) return
            if (result == null) {
                clearUnavailable()
                return
            }
            val verifiedRefs = buildMap {
                for (ref in result.refs) {
                    if (ref.source.isVerified) put(ref.source, ref.id)
                }
            }
            mutable.value = state.value.copy(
                count = result.refs.size,
                canWrite = result.canWrite,
                refs = verifiedRefs,
            )
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            if (read != indexRead) return
            if (isOldHub(error)) clearUnavailable()
        }
    }

    suspend fun loadPage() {
        if (!offered || api == null) {
            clearUnavailable()
            return
        }
        val read = ++pageRead
        val before = state.value
        mutable.value = before.copy(
            loadState = if (before.snapshots.isEmpty()) StarredLoadState.Loading else before.loadState,
            refreshing = true,
        )
        try {
            val result = api.list()
            currentCoroutineContext().ensureActive()
            if (read != pageRead) return
            if (result == null) {
                clearUnavailable()
                return
            }
            val valid = result.stars.filter { !it.id.isNullOrBlank() }
            val verifiedRefs = buildMap {
                for (star in valid) {
                    val id = star.id ?: continue
                    if (star.source.isVerified) put(star.source, id)
                }
            }
            mutable.value = state.value.copy(
                loadState = StarredLoadState.Ready,
                count = valid.size,
                canWrite = result.canWrite,
                snapshots = valid,
                refs = if (state.value.refs.isEmpty()) verifiedRefs else state.value.refs,
                refreshing = false,
            )
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            if (read != pageRead) return
            if (isOldHub(error)) {
                clearUnavailable()
            } else {
                mutable.value = state.value.copy(loadState = StarredLoadState.Failed, refreshing = false)
            }
        }
    }

    suspend fun add(
        draft: StarredSnapshot,
        loadText: (suspend () -> String)? = null,
    ): Result<StarredSnapshot> {
        val client = api ?: return Result.failure(IllegalStateException("Starred messages unavailable"))
        val source = draft.source
        val current = state.value
        if (!offered || !current.canWrite || (!source.isVerified && draft.id == null) || source in current.pending) {
            return Result.failure(IllegalStateException("Cannot star message right now"))
        }
        mutable.value = current.copy(pending = current.pending + source)
        return try {
            val input = if (loadText != null) draft.copy(text = loadText()) else draft
            val saved = client.add(input)
            currentCoroutineContext().ensureActive()
            val savedId = saved.id ?: return Result.failure(IllegalStateException("Missing star id"))
            indexRead++
            pageRead++
            val now = state.value
            val existed = saved.source in now.refs
            val nextRefs = if (saved.source.isVerified) now.refs + (saved.source to savedId) else now.refs
            val nextCount = if (saved.source.isVerified && !existed) now.count?.plus(1) ?: nextRefs.size else now.count
            val nextSnapshots = if (readers > 0 && now.snapshots.none { it.id == savedId }) {
                listOf(saved) + now.snapshots
            } else {
                now.snapshots
            }
            mutable.value = now.copy(
                refs = nextRefs,
                count = nextCount,
                snapshots = nextSnapshots,
                pending = now.pending - source,
            )
            Result.success(saved)
        } catch (cancelled: CancellationException) {
            mutable.value = state.value.copy(pending = state.value.pending - source)
            throw cancelled
        } catch (error: Exception) {
            mutable.value = state.value.copy(pending = state.value.pending - source)
            Result.failure(error)
        }
    }

    suspend fun remove(source: StarredSource, id: String? = null): Result<StarredSnapshot?> {
        val client = api ?: return Result.failure(IllegalStateException("Starred messages unavailable"))
        val current = state.value
        val targetId = id ?: current.refs[source]
            ?: return Result.failure(IllegalStateException("Message is not starred"))
        if (!offered || !current.canWrite || source in current.pending) {
            return Result.failure(IllegalStateException("Cannot unstar message right now"))
        }
        mutable.value = current.copy(pending = current.pending + source)
        return try {
            val removed = client.remove(targetId)
            currentCoroutineContext().ensureActive()
            indexRead++
            pageRead++
            val now = state.value
            val removedRef = now.refs[source] == targetId
            val nextRefs = if (removedRef) now.refs - source else now.refs
            val nextCount = if (removedRef) now.count?.minus(1)?.coerceAtLeast(0) else now.count
            val nextSnapshots = now.snapshots.filterNot { it.id == targetId }
            mutable.value = now.copy(
                refs = nextRefs,
                count = nextCount,
                snapshots = nextSnapshots,
                pending = now.pending - source,
            )
            Result.success(removed)
        } catch (cancelled: CancellationException) {
            mutable.value = state.value.copy(pending = state.value.pending - source)
            throw cancelled
        } catch (error: Exception) {
            mutable.value = state.value.copy(pending = state.value.pending - source)
            Result.failure(error)
        }
    }

    suspend fun verifyOriginal(source: StarredSource): StarredOriginal {
        val client = api ?: return StarredOriginal.Retired
        if (!source.isVerified) return StarredOriginal.Unavailable
        return try {
            val item = client.fullItem(source.sessionKey, source.itemId)
            currentCoroutineContext().ensureActive()
            if (source.matches(item)) StarredOriginal.Verified else StarredOriginal.Unavailable
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            val coded = hubCodedFailure(error)
            if (coded != null && (coded.status == 404 || (coded.status == 410 && coded.code == "conversation_deleted"))) {
                StarredOriginal.Unavailable
            } else {
                StarredOriginal.Failed
            }
        }
    }

    private fun clearUnavailable() {
        indexRead++
        pageRead++
        mutable.value = StarredMessagesState(loadState = StarredLoadState.Unsupported)
    }

    private fun isOldHub(error: Exception): Boolean = hubCodedFailure(error)?.status == 404
}

val LocalStarredMessagesStore = staticCompositionLocalOf<StarredMessagesStore?> { null }

/** The transcript identity of the currently open chat, for starring messages and drawing starred badges. */
@Immutable
internal data class StarredChatContext(
    val key: SessionKey,
    val conversation: StarredConversation,
    val title: String,
    val speaker: String,
    val manager: String? = null,
) {
    fun source(item: Item?): StarredSource? {
        if (item == null) return null
        if (item.kind != "text" || item.role !in setOf("user", "assistant")) return null
        if (item.injected != null || item.wake == true || item.narration == true) return null
        val source = StarredSource(
            machineId = key.machineId,
            sessionId = key.sessionId,
            itemId = item.id,
            conversation = conversation,
            sourceFingerprint = item.sourceFingerprint,
        )
        return source.takeIf { it.isVerified }
    }

    fun draft(item: Item, target: MessageTarget): StarredSnapshot? {
        val source = source(item) ?: return null
        if (item.fullSize == null && target.text.isBlank()) return null
        val resolvedSpeaker = target.from?.takeIf { it.isNotBlank() } ?: if (target.user) "" else speaker
        return StarredSnapshot(
            machineId = source.machineId,
            sessionId = source.sessionId,
            itemId = source.itemId,
            conversation = source.conversation,
            sourceFingerprint = source.sourceFingerprint,
            role = item.role,
            title = title,
            speaker = resolvedSpeaker,
            sourceManager = manager,
            writtenAt = item.ts,
            text = target.text,
        )
    }
}

internal val LocalStarredChatContext = compositionLocalOf<StarredChatContext?> { null }

/** Small star mark drawn beside a starred chat message (matching iOS `StarredChatMark`). */
@Composable
internal fun StarredChatMark(item: Item?, modifier: Modifier = Modifier) {
    val store = LocalStarredMessagesStore.current ?: return
    val chat = LocalStarredChatContext.current ?: return
    val source = chat.source(item) ?: return
    val state by store.state.collectAsState()
    val mark = state.mark(source) ?: return
    val palette = PersonaTheme.palette
    val description = stringResource(
        if (mark == StarredMark.Saving) R.string.chat_starred_saving else R.string.chat_starred_saved,
    )
    Box(
        modifier
            .size(Tokens.Size.iconXS)
            .padding(Tokens.Space.xxs)
            .testTag("chat-starred-mark-${source.itemId}"),
        contentAlignment = Alignment.Center,
    ) {
        JIcon(
            icon = PersonaIcons.pluginStar,
            contentDescription = description,
            size = Tokens.Size.markS,
            tint = if (mark == StarredMark.Saving) palette.ink3 else palette.accent,
        )
    }
}

/** Renders Markdown for a saved Starred snapshot outside `feature:chat`. */
@Composable
fun StarredSnapshotMarkdown(text: String, id: String, modifier: Modifier = Modifier) {
    Box(modifier) {
        ChatMarkdown(text = text, id = id, selectable = false)
    }
}
