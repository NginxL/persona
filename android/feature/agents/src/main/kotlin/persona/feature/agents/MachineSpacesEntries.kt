package persona.feature.agents

import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalContext
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.repeatOnLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.navigation3.runtime.EntryProviderScope
import androidx.navigation3.runtime.NavKey
import dagger.hilt.android.EntryPointAccessors
import kotlinx.coroutines.delay
import persona.core.navigation.ChatRoute
import persona.core.navigation.MachineDetailRoute
import persona.core.navigation.MachineSpacesRoute
import persona.core.navigation.Navigator
import persona.core.navigation.SpaceRoute
import java.time.Instant
import persona.core.contracts.Session
import persona.core.domain.sort.AgentSession
import persona.core.domain.sort.AgentSpace

/** Keep every project member, with the held markers captured by the main projection, rather than its idle-folded rows. */
internal fun machineSpaceRows(sessions: List<Session>, spaces: List<AgentSpace>, machineId: String,
    preferences: AgentsPreferencesSnapshot): List<AgentSession> = spaces.filter { it.machineId == machineId }.flatMap { space ->
    sessions.filter { it.machineId == space.machineId && it.spaceId == space.members.first().session.spaceId }
        .map { member ->
            val own = space.members.filter { it.session.key == member.key }
            member.listSession(preferences).copy(heldInList = own.any { it.heldInList })
                .let { row -> if (own.any { it.heldFinished }) row.heldAsFinished() else row }
        }
}

/**
 * A computer's projects as a destination of its own ([MachineSpacesRoute]): in a wide window it opens beside the Agents list
 * (the shell gives it the detail pane), on a phone it is pushed over the space page's computer. A space or an agent opened
 * from it goes on top, and back returns to it. [dependenciesOverride] is the same one the shell gives [agentsEntries].
 */
fun EntryProviderScope<NavKey>.machineSpacesEntries(navigator: Navigator, dependenciesOverride: AgentsDependencies? = null) {
    entry<MachineSpacesRoute> { route -> MachineSpacesDestination(route, navigator, dependenciesOverride) }
}

/** The pairing's services for the page, set up as the space page's destination sets them up. */
@Composable
private fun MachineSpacesDestination(route: MachineSpacesRoute, navigator: Navigator, dependenciesOverride: AgentsDependencies?) {
    val context = LocalContext.current.applicationContext
    val dependencies = remember(context, dependenciesOverride) {
        dependenciesOverride ?: EntryPointAccessors.fromApplication(context, AgentsDependencies::class.java)
    }
    val wiring by dependencies.wiring().state.collectAsStateWithLifecycle()
    val renamer = remember(context) {
        if (context is dagger.hilt.internal.GeneratedComponentManager<*>)
            EntryPointAccessors.fromApplication(context, AgentsRenameDependencies::class.java).renamer()
        else null
    }
    CompositionLocalProvider(LocalAgentsOperations provides (wiring.operations ?: LocalAgentsOperations.current)) {
        val operations = LocalAgentsOperations.current
        val scope = wiring.scope.ifEmpty { operations?.operations?.scope.orEmpty() }
        val model = viewModel(key = "machine-spaces:${wiring.scope}:${operations?.operations?.scope.orEmpty()}") {
            AgentsViewModel(dependencies.sessions(), dependencies.interactions(), dependencies.connection(), dependencies.sender(),
                dependencies.preferences(), operations, wiring.mainSender, scope, renamer, hubGroups = dependencies.groups(),
                listContext = kotlinx.coroutines.Dispatchers.Default, listAdapter = dependencies.listAdapter(),
                visibleChat = dependencies.visibleChat(), heldChat = dependencies.heldChat(), finishedChat = dependencies.finishedChat())
        }
        MachineSpacesPage(route, model, navigator)
    }
}

/** The page itself: the computer's agents from the Agents list's model, its projects drawn by [MachineSpacesScreen]. */
@Composable
private fun MachineSpacesPage(route: MachineSpacesRoute, model: AgentsViewModel, navigator: Navigator) {
    val page by model.page.collectAsStateWithLifecycle()
    // Times read "5 min ago": once in a while is often enough.
    var now by remember { mutableStateOf(Instant.now()) }
    val lifecycle = LocalLifecycleOwner.current
    LaunchedEffect(lifecycle) {
        lifecycle.lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            while (true) { now = Instant.now(); delay(30_000) }
        }
    }
    val rows = machineSpaceRows(page.sessions, page.spaces, route.machineId, page.preferences)
    MachineSpacesScreen(
        machineId = route.machineId, machine = page.machines.firstOrNull { it.id == route.machineId }, rows = rows,
        preferences = page.preferences, now = now, loaded = page.loaded,
        onBack = navigator::back, onDetails = { navigator.navigate(MachineDetailRoute(route.machineId)) },
        onOpenSpace = { navigator.navigate(SpaceRoute(it.id, it.name)) },
        onOpenAgent = { row ->
            model.opened(row)
            navigator.navigate(ChatRoute(row.session.machineId, row.session.sessionId))
        },
    )
}
