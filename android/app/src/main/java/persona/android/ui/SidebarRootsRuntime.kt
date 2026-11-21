package persona.android.ui

import androidx.activity.compose.BackHandler
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.key
import androidx.navigation3.runtime.NavKey
import androidx.navigationevent.compose.LocalNavigationEventDispatcherOwner
import androidx.navigationevent.compose.rememberNavigationEventDispatcherOwner
import persona.android.navigation.SidebarPageRoots
import persona.android.navigation.SidebarPlaceWindow
import persona.android.navigation.SidebarRootHosts
import persona.core.designsystem.ReleaseTextInputOnChange
import persona.core.navigation.AssistantRoute

/**
 * Clear focus when the sidebar host in front changes.
 *
 * iOS gives the root page a new identity (`AssistantView.swift:666` `.id(value)`), so the field
 * left behind is not first responder. Android keeps that page composed, so the same host id is the
 * key of [ReleaseTextInputOnChange] (#894): a drill on the same host does not clear the field.
 */
@Composable
internal fun ReleaseFocusOnSidebarHost(hostId: String) {
    ReleaseTextInputOnChange(hostId)
}

/**
 * The place-root system Back, registered before [content] (the host's `NavDisplay`).
 *
 * The last enabled callback wins. A page handler composed inside the display — Inbox selection
 * (`InboxScreen`'s `BackHandler(selecting)`), search, a sheet — is registered later and runs first.
 * The overlay column's handler still runs first because this one is disabled while the overlay is
 * open. iOS has no system Back; Android returns to the conversation from a place root.
 */
@Composable
internal fun PlaceRootBack(enabled: Boolean, onBack: () -> Unit, content: @Composable () -> Unit) {
    BackHandler(enabled, onBack)
    content()
}

/**
 * Width changes and one `NavDisplay` per visited host. [PersonaShell] calls this; tests call the
 * same function. The place-root Back handler is registered before [hostContent].
 */
@Composable
internal fun SidebarRootsRuntime(
    window: SidebarPlaceWindow,
    hosts: SidebarRootHosts,
    pageRoots: SidebarPageRoots,
    assistantStack: MutableList<NavKey>,
    regular: Boolean,
    overlayOpen: Boolean,
    shown: Boolean,
    showHost: (NavKey, List<NavKey>) -> Unit,
    assignPersona: (MutableList<NavKey>) -> Unit,
    onBack: () -> Unit,
    merged: Boolean = false,
    hostContent: @Composable (hostId: String, stack: MutableList<NavKey>, hostShown: Boolean) -> Unit,
) {
    LaunchedEffect(regular, merged) {
        if (regular || !merged) window.onWidth(regular, hosts, assistantStack, pageRoots, showHost, assignPersona)
    }
    // A navigate or restore pushes onto the host list without activate. Trim before the next frame
    // so that route is not composed on two hosts (docs/design/sidebar-navigation.md).
    SideEffect { hosts.trimComposedClashes() }
    // Every visited host, at both widths. Folding a fixed place keeps it on its own list, so a
    // compact frame must keep composing it. Folding a to-do drops only that host.
    hosts.visited.forEach { hostId ->
        val hostStack = hosts.stack(hostId) ?: return@forEach
        key(hostId) {
            val hostShown = shown && hosts.current === hostStack
            val back = rememberNavigationEventDispatcherOwner(enabled = hostShown)
            val root = hostStack.firstOrNull()
            ShellTabLifecycle(hostShown) {
                CompositionLocalProvider(LocalNavigationEventDispatcherOwner provides back) {
                    PlaceRootBack(
                        enabled = hostShown && !overlayOpen && hostStack.size <= 1 &&
                            root != null && root != AssistantRoute,
                        onBack = onBack,
                    ) {
                        hostContent(hostId, hostStack, hostShown)
                    }
                }
            }
        }
    }
}
