package persona.core.navigation

import androidx.navigation3.runtime.NavKey
import persona.core.contracts.api.KnowledgeKind
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** The knowledge pages' places (#13934): the shell links to them; :module:knowledge-ui registers their pages. */
@Serializable @SerialName("persona.feature.knowledge.KnowledgeRoute") data object KnowledgeRoute : NavKey
/** The knowledge list opened at one kind by a deep link; the user can still change the filter in the page. */
@Serializable @SerialName("persona.feature.knowledge.KnowledgeKindRoute")
data class KnowledgeKindRoute(val kind: KnowledgeKind) : NavKey

/**
 * The knowledge map: topics and their points, or projects and their parts, as an outline (iOS `KBTopicsMap`, the web's Knowledge map).
 * Of [manager]'s knowledge base, the assistant current when it was opened (null from a page saved before it was kept: the one
 * current when it opens again); switching assistants while it is open leaves it that one's.
 */
@Serializable @SerialName("persona.feature.knowledge.KnowledgeMapRoute")
data class KnowledgeMapRoute(val manager: String? = null) : NavKey

/**
 * An entry of [manager]'s knowledge base (the assistant current when it was opened; null from a page saved before it was
 * kept: the one current when it opens again). Its page stays that assistant's (docs/assistant/managers.md "Clients").
 */
@Serializable @SerialName("persona.feature.knowledge.KnowledgeDetailRoute")
data class KnowledgeDetailRoute(val kind: KnowledgeKind, val id: String, val manager: String? = null) : NavKey
