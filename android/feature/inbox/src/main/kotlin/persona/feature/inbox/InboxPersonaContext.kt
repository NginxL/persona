package persona.feature.inbox

import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import persona.core.inbox.*

internal suspend fun sendInboxToPersona(repository: InboxRepository, request: InboxPersonaRequest, navigator: Navigator?) {
    repository.askPersona(request)
    navigator?.selectTab(AppTab.Persona)
}
