@file:OptIn(androidx.compose.material3.adaptive.ExperimentalMaterial3AdaptiveApi::class)

package persona.android.ui

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.adaptive.HingeInfo
import androidx.compose.material3.adaptive.layout.PaneScaffoldDirective
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.layout.positionInWindow
import androidx.compose.ui.platform.LocalDensity
import androidx.lifecycle.viewmodel.navigation3.rememberViewModelStoreNavEntryDecorator
import androidx.navigation3.runtime.NavBackStack
import androidx.navigation3.runtime.NavEntry
import androidx.navigation3.runtime.NavKey
import androidx.navigation3.runtime.rememberSaveableStateHolderNavEntryDecorator
import androidx.navigation3.ui.NavDisplay
import persona.android.navigation.PaneHinge
import persona.android.navigation.detailOf
import persona.core.navigation.LocalDetailKey
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.glassPageChrome
import persona.core.designsystem.rememberPageTransitions

/**
 * Whether a window has been regular (an open foldable, a tablet) at any point since it was first composed. It only ever
 * goes from false to true, so reading it twice for the same history gives the same answer, and a phone, which is never
 * regular, always reads false.
 */
internal class RegularWindowLatch(regular: Boolean) {
    private var seen = regular

    fun seen(regular: Boolean): Boolean {
        if (regular) seen = true
        return seen
    }
}

/**
 * The Settings sheet's pages: their list beside the page it opens in a [regular] window, one page elsewhere.
 *
 * A window that has been regular keeps one list-detail scene whatever its width, so narrowing it to one pane only
 * changes the scaffold's geometry. Swapping the list-detail scene for the single-page one moves every page's content
 * between the two scenes with movable content; when the window crosses the width boundary twice within a few frames
 * (dragging a free window's edge, a display size and density change) that move crashed Compose's runtime while this
 * sheet's dialog window was open. A window that was never regular (every phone) still uses the single-page scene, so
 * its pages are drawn and animated as before.
 *
 * [directive] is the one-pane directive when the window is not [regular]; [hinges] are the vertical folds across it.
 */
@Composable
internal fun SettingsSheetPanes(
    stack: NavBackStack<NavKey>,
    onBack: () -> Unit,
    regular: Boolean,
    directive: PaneScaffoldDirective,
    hinges: List<HingeInfo>,
    entries: (NavKey) -> NavEntry<NavKey>,
    paneMetadata: (NavKey) -> Map<String, Any>,
) {
    var sheetLeft by remember { mutableFloatStateOf(0f) }
    val density = LocalDensity.current.density
    val latch = remember { RegularWindowLatch(regular) }
    val pageTransitions = rememberPageTransitions()
    BoxWithConstraints(Modifier.fillMaxSize().onGloballyPositioned { sheetLeft = it.positionInWindow().x }) {
        val sheetHinges = hinges.map { hinge ->
            PaneHinge((hinge.bounds.left - sheetLeft) / density, (hinge.bounds.right - sheetLeft) / density, hinge.isSeparating)
        }
        val detail = detailOf(stack)
        val listDetail = rememberShellListDetail(null, directive, maxWidth, sheetHinges, detailOpen = detail != null,
            store = null, collapsible = false, keepScaffoldWhenSingle = latch.seen(regular))
        CompositionLocalProvider(LocalDetailKey provides detail) {
            NavDisplay(backStack = stack, onBack = onBack,
                entryDecorators = listOf(rememberSaveableStateHolderNavEntryDecorator(), rememberViewModelStoreNavEntryDecorator()),
                sceneStrategies = listOf(listDetail.strategy),
                transitionSpec = { pageTransitions.push },
                popTransitionSpec = { pageTransitions.pop },
                predictivePopTransitionSpec = { pageTransitions.predictivePop },
                entryProvider = { key ->
                    val entry = entries(key)
                    NavEntry(key, entry.contentKey, entry.metadata + paneMetadata(key)) {
                        // The Box stays when style or Reduce Motion changes. The chrome modifier is identity outside Glass.
                        Box(Modifier.fillMaxSize().glassPageChrome()) { entry.Content() }
                    }
                })
        }
    }
}
