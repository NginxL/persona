package persona.android.ui

import androidx.activity.compose.BackHandler
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavEntry
import androidx.navigation3.runtime.NavKey
import androidx.navigation3.scene.Scene
import androidx.navigation3.scene.SceneStrategy
import androidx.navigation3.scene.SceneStrategyScope
import persona.android.navigation.PaneSplits
import persona.android.navigation.AppNavigator
import persona.android.navigation.isDetailDestination
import persona.core.designsystem.JSidebarColumn
import persona.core.designsystem.LocalSplitDetail
import persona.core.designsystem.SplitDetailContext
import persona.core.designsystem.DetailPlaceholder
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JPage
import persona.core.designsystem.icons.PersonaIcons
import persona.android.R
import persona.core.navigation.AgentsRoute
import persona.core.navigation.AppTab

/** Detail picks replace the overlaid root detail; other actions push over it and retain its Back navigation. */
internal fun pickAgentsList(navigator: AppNavigator, stack: MutableList<NavKey>, shown: MutableState<Boolean>, key: NavKey) {
    val replacing = shown.value
    shown.value = false
    navigator.navigate(key, replacingDetailIn = AppTab.Agents.takeIf { replacing && isDetailDestination(key) && stack.lastOrNull() != key })
}

/** A narrow regular window uses the same decorated list and detail entries, with the list over the full-width page. */
@Composable
internal fun rememberAgentsOverlayStrategy(
    enabled: Boolean,
    shown: MutableState<Boolean>,
    emptyDetail: @Composable () -> Unit = { AgentsEmptyPage { shown.value = true } },
): SceneStrategy<NavKey> {
    val currentEmpty = rememberUpdatedState(emptyDetail)
    return remember(enabled, shown) { AgentsOverlayStrategy(enabled, shown, { currentEmpty.value() }) }
}

private class AgentsOverlayStrategy(private val enabled: Boolean, private val shown: MutableState<Boolean>,
    private val emptyDetail: @Composable () -> Unit) : SceneStrategy<NavKey> {
    private val listKey = NavEntry<NavKey>(AgentsRoute) {}.contentKey
    override fun SceneStrategyScope<NavKey>.calculateScene(entries: List<NavEntry<NavKey>>): Scene<NavKey>? {
        if (!enabled || entries.isEmpty() || entries.first().contentKey != listKey) return null
        return AgentsOverlayScene(entries, shown, emptyDetail)
    }
}

private class AgentsOverlayScene(
    override val entries: List<NavEntry<NavKey>>,
    private val shown: MutableState<Boolean>,
    private val emptyDetail: @Composable () -> Unit,
) : Scene<NavKey> {
    override val key: Any = AgentsRoute
    override val previousEntries: List<NavEntry<NavKey>> = entries.dropLast(1)
    override val content: @Composable () -> Unit = {
        val detail = entries.last().takeIf { entries.size > 1 }
        // A row pick, including a new detail on the same list, closes only the overlaid column.
        LaunchedEffect(detail?.contentKey) { shown.value = false }
        // The Agents list drags like iOS when `listKey != nil` (`SplitNavigation.swift:237`). A pushed detail keeps
        // the edge (`SplitColumnControl.swift:160-169`); `entries.size <= 2` is the list plus its root detail.
        JSidebarColumn(sidebar = { entries.first().Content() }, width = PaneSplits.DefaultLeft.dp,
            pageStart = PaneSplits.DefaultLeft.dp, shown = shown.value, overlay = true,
            onDismiss = { shown.value = false }, insetSidebar = false, drags = true,
            detailAtRoot = entries.size <= 2, onDragSettle = { shown.value = it }) {
            CompositionLocalProvider(
                LocalSplitDetail provides SplitDetailContext(
                    isFirstPage = entries.size == 2,
                    listHidden = true,
                    onShowList = { shown.value = true },
                )
            ) {
                // iOS SplitNavigation.swift:125-146, 178-208: a sidebar value replaces the detail stack immediately;
                // only the overlaid list slides away with Theme.Motion.layout. Keep this scene's key fixed.
                if (detail != null) detail.Content() else emptyDetail()
            }
        }
        BackHandler(shown.value) { shown.value = false }
    }
}

/** The unselected page also stays full width under a narrow regular window's floating list, as on iOS. */
@Composable
internal fun AgentsEmptyPage(onShowList: (() -> Unit)?) {
    JPage(stringResource(R.string.app_tab_agents), actions = {
        if (onShowList != null) JIconButton(PersonaIcons.sidebar, stringResource(R.string.app_show_list), onShowList)
    }) { padding ->
        Box(Modifier.fillMaxSize().padding(padding)) {
            DetailPlaceholder(stringResource(R.string.app_pane_agents_title), stringResource(R.string.app_pane_agents_detail), PersonaIcons.agentChat)
        }
    }
}
