package persona.feature.inbox

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import persona.core.inbox.*

/** Owns only the visible Messages page's requests; the pairing-scoped repository owns their state and serialization. */
class LarkMessagesViewModel(private val repository: LarkMessagesRepository) : ViewModel() {
    val state: StateFlow<LarkMessagesState> = repository.state

    init {
        refresh()
    }

    fun refresh() {
        viewModelScope.launch { repository.refresh() }
    }

    fun loadMore() {
        viewModelScope.launch { repository.loadMore() }
    }

    fun avatar(ref: String?): AuthenticatedLarkImage? = repository.avatar(ref)
}
