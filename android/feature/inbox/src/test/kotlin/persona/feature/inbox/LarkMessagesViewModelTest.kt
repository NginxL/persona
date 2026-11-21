package persona.feature.inbox

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class LarkMessagesViewModelTest {
    private class FakeRepository : LarkMessagesRepository {
        private val mutable = MutableStateFlow(LarkMessagesState(loaded = true, loading = false))
        override val scope = "pairing/a"
        override val state: StateFlow<LarkMessagesState> = mutable
        var refreshes = 0
        var more = 0
        override suspend fun refresh() { refreshes += 1 }
        override suspend fun loadMore() { more += 1 }
        override fun avatar(ref: String?): AuthenticatedLarkImage? = null
    }

    @Test fun exposesRepositoryStateAndStartsOneInitialRead() = runTest {
        Dispatchers.setMain(UnconfinedTestDispatcher(testScheduler))
        try {
            val repository = FakeRepository()
            val model = LarkMessagesViewModel(repository)
            assertSame(repository.state, model.state)
            assertEquals(1, repository.refreshes)
            assertEquals(0, repository.more)

            model.refresh()
            model.loadMore()
            assertEquals(2, repository.refreshes)
            assertEquals(1, repository.more)
        } finally {
            Dispatchers.resetMain()
        }
    }
}
