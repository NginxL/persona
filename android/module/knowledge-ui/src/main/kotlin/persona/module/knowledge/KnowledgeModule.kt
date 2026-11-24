package persona.module.knowledge

import persona.core.navigation.KnowledgeDetailRoute
import persona.core.navigation.KnowledgeKindRoute
import persona.core.navigation.KnowledgeMapRoute
import persona.core.navigation.KnowledgeRoute
import androidx.compose.ui.res.stringResource
import androidx.navigation3.runtime.EntryProviderScope
import androidx.navigation3.runtime.NavKey
import persona.core.moduleapi.ModuleNavContext
import persona.core.moduleapi.ModuleRegistry
import persona.core.moduleapi.PersonaModule
import persona.core.navigation.NewAgentRoute

/**
 * The knowledge pages as a module (#13934, #13430 section 2 `knowledge-ui`): the knowledge list, an entry, the knowledge
 * map and meeting minutes. The assistant's memory itself (the knowledge base's files and search) stays core and reaches
 * the pages through [ModuleNavContext.shell]; the shell links to the pages by their routes in `:core:navigation`.
 */
object KnowledgeModule : PersonaModule {
    override val id = "knowledge-ui"
    override val feature = ModuleRegistry.moduleFeature(id)
    override val places = setOf(KnowledgeRoute::class, KnowledgeKindRoute::class, KnowledgeMapRoute::class, KnowledgeDetailRoute::class)

    override fun navEntries(entries: EntryProviderScope<NavKey>, context: ModuleNavContext) {
        val shell = context.shell
        if (shell == null) {
            // Demo mode: the pages are there, saying why they cannot work.
            entries.entry<KnowledgeRoute> { context.unavailable(stringResource(R.string.knowledge_ui_demo_unavailable)) }
            entries.entry<KnowledgeKindRoute> { context.unavailable(stringResource(R.string.knowledge_ui_demo_unavailable)) }
            entries.entry<KnowledgeMapRoute> { context.unavailable(stringResource(R.string.knowledge_ui_demo_unavailable)) }
            entries.entry<KnowledgeDetailRoute> { context.unavailable(stringResource(R.string.knowledge_ui_demo_unavailable)) }
        } else {
            entries.knowledgeEntries(context.navigator, shell.knowledge, shell.assistant, shell.sender, shell::mainConversationSender, shell.sessions,
                shell.knowledgeNamespace, shell::openLink,
                onNewAgent = { start -> context.navigator.navigate(NewAgentRoute(start.machineId, start.cwd, start.prompt)) })
        }
    }

    override fun debugPages(): Map<String, NavKey> = mapOf("knowledge" to KnowledgeRoute)
}
