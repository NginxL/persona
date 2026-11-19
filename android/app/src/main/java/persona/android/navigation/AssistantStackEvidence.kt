package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import persona.core.navigation.AddComputerRoute
import persona.core.navigation.AppTab
import persona.core.navigation.AgentsRoute
import persona.core.navigation.AssistantRoute
import persona.core.navigation.ChatRoute
import persona.core.navigation.EngineRoute
import persona.core.navigation.ExtensionUiRoute
import persona.core.navigation.InboxItemRoute
import persona.core.navigation.InboxItemUiRoute
import persona.core.navigation.InboxRoute
import persona.core.navigation.InteractionRoute
import persona.core.navigation.LarkChatRoute
import persona.core.navigation.LauncherRoute
import persona.core.navigation.MachineDetailRoute
import persona.core.navigation.MachinesRoute
import persona.core.navigation.SettingsPageRoute
import persona.core.navigation.SettingsRoute
import persona.core.navigation.TodoDetailRoute
import persona.core.navigation.TodosRoute
import persona.feature.inbox.ConnectorThreadRoute
import persona.feature.inbox.InboxEventRoute
import persona.feature.inbox.InboxItemDetailRoute
import persona.feature.inbox.LarkMomentRoute
import persona.feature.inbox.LarkRoute
import persona.feature.inbox.MailRoute
import persona.feature.knowledge.KnowledgeDetailRoute
import persona.feature.knowledge.KnowledgeKindRoute
import persona.feature.knowledge.KnowledgeRoute

internal data class AssistantStackEvidence(val route: String, val selectedKey: String?)

/** The visible route and its stable content key, used only by DEBUG adaptive evidence. */
internal fun assistantStackEvidence(key: NavKey?, tab: AppTab = AppTab.Persona): AssistantStackEvidence = when (key) {
    null -> AssistantStackEvidence("none", null)
    AssistantRoute -> AssistantStackEvidence("assistant", null)
    AgentsRoute -> AssistantStackEvidence("agents", null)
    InboxRoute -> AssistantStackEvidence("inbox", null)
    SettingsRoute -> AssistantStackEvidence("settings", null)
    MachinesRoute -> AssistantStackEvidence("machines", null)
    KnowledgeRoute -> AssistantStackEvidence("knowledge", null)
    is KnowledgeKindRoute -> AssistantStackEvidence("knowledge/${key.kind.wire}", null)
    TodosRoute -> AssistantStackEvidence("todos", null)
    is ChatRoute -> AssistantStackEvidence("${tab.name}/chat", "${key.machineId}/${key.sessionId}")
    is InteractionRoute -> AssistantStackEvidence("${tab.name}/chat", "${key.chat.machineId}/${key.chat.sessionId}")
    is LarkRoute -> AssistantStackEvidence("inbox/lark", key.itemId ?: key.feedId)
    is MailRoute -> AssistantStackEvidence("inbox/mail", key.itemId)
    is InboxEventRoute -> AssistantStackEvidence("inbox/event", key.itemId)
    is InboxItemRoute -> AssistantStackEvidence("inbox/item", key.itemId)
    is InboxItemUiRoute -> AssistantStackEvidence("inbox/item-ui", key.itemId)
    is LarkChatRoute -> AssistantStackEvidence("inbox/lark-chat", key.chatId)
    is LarkMomentRoute -> AssistantStackEvidence("inbox/moment", key.itemId ?: key.postId)
    is InboxItemDetailRoute -> AssistantStackEvidence("inbox/detail", key.itemId)
    is ConnectorThreadRoute -> AssistantStackEvidence("inbox/connector", key.itemId)
    is MachineDetailRoute -> AssistantStackEvidence("machines/detail", key.machineId)
    is AddComputerRoute -> AssistantStackEvidence("machines/add", null)
    is EngineRoute -> AssistantStackEvidence("machines/engine", key.machineId)
    is LauncherRoute -> AssistantStackEvidence("machines/launcher", "${key.machineId}/${key.name}")
    is KnowledgeDetailRoute -> AssistantStackEvidence("knowledge/${key.kind.wire}", key.id)
    is TodoDetailRoute -> AssistantStackEvidence("todos/detail", key.todoId)
    is ExtensionUiRoute -> AssistantStackEvidence("extension/${key.extensionId}/${key.uiId}", "${key.extensionId}/${key.uiId}")
    is SettingsPageRoute -> AssistantStackEvidence("settings/${key.page.name}", key.page.name)
    else -> AssistantStackEvidence(key::class.simpleName ?: "unknown", null)
}
