package persona.android.capsule

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.compose.ui.platform.LocalWindowInfo
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.snapshots.SnapshotStateMap
import androidx.compose.ui.Modifier
import persona.platform.capsule.CapsuleSurface

internal data class CapsulePresentation(
    val source: CapsuleConversationsSource,
    val drafts: SnapshotStateMap<Pair<String?, String?>, String>,
)

/** The expanded activity and overlay read the same source and draft ownership. */
@Composable
internal fun CapsuleFullScreen(runtime: CapsuleRuntime, onReturn: () -> Unit) {
    val presentation by runtime.presentation.collectAsState()
    val current = presentation ?: run {
        androidx.compose.runtime.LaunchedEffect(runtime) { runtime.leaveFullScreen() }
        return
    }
    val content by current.source.content.collectAsState()
    val lifecycle by LocalLifecycleOwner.current.lifecycle.currentStateFlow.collectAsState()
    val active = lifecycle.isAtLeast(Lifecycle.State.RESUMED) && LocalWindowInfo.current.isWindowFocused
    DisposableEffect(current.source, active) {
        current.source.fullScreen(active)
        onDispose { current.source.fullScreen(false) }
    }
    BackHandler(onBack = onReturn)
    Box(Modifier.fillMaxSize().safeDrawingPadding().imePadding()) {
        CapsuleSurface(content, expanded = true, onExpand = {}, onCollapse = onReturn,
            onSelect = current.source::select, onSelectSection = current.source::selectSection,
            onOpen = { id ->
                if (content.entries.firstOrNull { it.id == id }?.detailOnly == true) current.source.open(id)
                else onReturn()
            }, onSend = current.source::send, onAction = current.source::perform,
            onViewed = current.source::viewed, onDraftChange = current.source::updateDraft,
            windowFocused = active, drafts = current.drafts, fullScreen = true)
    }
}
