package persona.core.moduleapi

import persona.core.contracts.api.AssistantConversations
import persona.core.contracts.api.KnowledgeConnection
import persona.core.contracts.api.MainConversationSender
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.SessionsRepository

/**
 * What a module's pages may use of the core for the paired hub ([ModuleNavContext.shell]): the shell's own repositories,
 * so a module does not build a second copy of what the core keeps (#13934). Null in demo mode.
 */
interface ModuleShell {
    /** The assistant's memory: the knowledge base's files and search, which stay core (#13430 section 2). */
    val knowledge: KnowledgeConnection
    /** The knowledge base's namespace on a hub group, null on the default one. */
    val knowledgeNamespace: String?
    val assistant: AssistantConversations
    val sender: MessageSender
    /** The main conversation's sender, the app's one for every pairing. */
    fun mainConversationSender(): MainConversationSender
    val sessions: SessionsRepository

    /** Opens [url] in the app's reader, never an outside app. */
    fun openLink(url: String)
}
