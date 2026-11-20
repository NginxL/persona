package persona.android.di

import dagger.Module
import dagger.Provides
import dagger.hilt.InstallIn
import dagger.hilt.components.SingletonComponent
import javax.inject.Singleton
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.flatMapLatest
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.ExtensionManagement
import persona.core.contracts.api.RequestManager
import persona.core.contracts.api.unpinned
import persona.feature.assistant.OpenerIcons

/**
 * The marks of conversations an extension opened draw that extension's own icon (#10358, feature/assistant
 * ConversationOpener.kt). The selected group's installed extensions and their icons: forgotten for another hub, read again
 * after an install, an upgrade or a removal and when the hub's features change (a hello with other ones).
 */
@Module
@InstallIn(SingletonComponent::class)
object OpenerIconsModule {
    @OptIn(ExperimentalCoroutinesApi::class)
    @Provides @Singleton
    fun openerIcons(stacks: GroupStacks, extensions: ExtensionManagement): OpenerIcons {
        // The store is used on the main thread: the marks ask it from composition, and these follow it there. It lives as
        // long as the app, as the page's marks do.
        val main = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
        val source = object : OpenerIcons.Source {
            override val listsExtensions: Boolean
                get() = stacks.selected.value?.connection?.serverFeatures?.value.orEmpty().let { "extensions" in it || "plugins" in it }

            // Every manager's list: asked of none, and without the stamps a list noted for the path.
            override suspend fun extensions() = withContext(RequestManager("")) { extensions.unpinned.extensions() }

            override fun iconPath(url: String) = extensions.iconPath(url)

            // The icon from the extension's own manager; the default's is asked of none.
            override suspend fun icon(path: String, manager: String?): ByteArray {
                val named = manager?.takeIf { it.isNotBlank() && it != AssistantManagerChoice.current.value.defaultId }.orEmpty()
                return withContext(RequestManager(named)) { extensions.unpinned.icon(path) }
            }
        }
        val store = OpenerIcons(main, source)
        main.launch {
            stacks.selected.map { stack -> stack?.let { it.id + "|" + it.data.scope.path } }.distinctUntilChanged().collect { store.reset() }
        }
        main.launch {
            stacks.selected.flatMapLatest { stack -> stack?.connection?.serverFeatures ?: flowOf(emptySet()) }
                .distinctUntilChanged().collect { store.stale() }
        }
        main.launch { extensions.changes.collect { store.stale() } }
        return store
    }
}
